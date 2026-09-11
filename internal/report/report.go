// Package report renders the reconciliation workbook (Summary + Consolidated
// Data) with github.com/xuri/excelize/v2.
package report

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"
)

// --- Summary sheet layout (mirrors amazon_sample_output_report.xlsx) ---

type sumLine struct {
	label      string
	slug       string // "" => always-zero line or subtotal
	isSubtotal bool
	section    string // subtotal lines aggregate their section's slug lines
	blankAfter bool
}

// the sheet, top to bottom
var summarySheet = []sumLine{
	{label: "Sales", isSubtotal: true, section: "Sales"},
	{label: "Product Charges", slug: "sales_product_charges"},
	{label: "Tax", slug: "sales_tax"},
	{label: "Shipping", slug: "sales_shipping"},
	{label: "Amazon fees", slug: "sales_amazon_fees"},
	{label: "Inventory Reimbursements", slug: "sales_inventory_reimbursements"},
	{label: "Cross-account Debt Adjustment"},
	{label: "Other", slug: "sales_other"},
	{label: "FBA Fees"},
	{label: "Micro Deposit (Failed)", blankAfter: true},

	{label: "Refunds", isSubtotal: true, section: "Refunds"},
	{label: "Refund expenses", slug: "refunded_expenses"},
	{label: "Refunded sales", slug: "refunded_sales", blankAfter: true},

	{label: "Expenses", isSubtotal: true, section: "Expenses"},
	{label: "Promo rebates", slug: "expenses_promotional_rebates"},
	{label: "FBA fees", slug: "expenses_fba_fees"},
	{label: "Cost of Advertising", slug: "expenses_cost_of_advertising"},
	{label: "Shipping Charges"},
	{label: "Amazon fees", slug: "expenses_amazon_fees"},
	{label: "Reversed Reimbursements", slug: "expenses_reversed_reimbursements"},
	{label: "Cross-account Debt Adjustment"},
	{label: "Other", slug: "expenses_other"},
	{label: "Micro Deposit", blankAfter: true},

	{label: "Paid To Amazon", slug: "paid_to_amazon"},
}

// sectionSlugs lists which slugs roll into each subtotal.
var sectionSlugs = map[string][]string{
	"Sales":   {"sales_product_charges", "sales_tax", "sales_shipping", "sales_amazon_fees", "sales_inventory_reimbursements", "sales_other"},
	"Refunds": {"refunded_expenses", "refunded_sales"},
	"Expenses": {"expenses_promotional_rebates", "expenses_fba_fees", "expenses_cost_of_advertising",
		"expenses_amazon_fees", "expenses_reversed_reimbursements", "expenses_other"},
}

// Generate writes the workbook to path.
func Generate(ctx context.Context, pool *pgxpool.Pool, path string) error {
	totals, err := loadSummaryTotals(ctx, pool)
	if err != nil {
		return err
	}
	recs, buckets, err := loadRecon(ctx, pool)
	if err != nil {
		return err
	}

	f := excelize.NewFile()
	defer f.Close()

	if err := writeSummary(f, totals); err != nil {
		return err
	}
	if err := writeConsolidated(f, recs, buckets); err != nil {
		return err
	}
	f.DeleteSheet("Sheet1")
	return f.SaveAs(path)
}

func round2(v float64) float64 {
	if v < 0 {
		return -round2(-v)
	}
	return float64(int64(v*100+0.5)) / 100
}

func writeSummary(f *excelize.File, totals map[[2]string]float64) error {
	const s = "Summary"
	f.NewSheet(s)
	f.SetColWidth(s, "B", "B", 34)
	f.SetColWidth(s, "C", "E", 20)
	f.SetCellValue(s, "C1", "Payments")
	f.SetCellValue(s, "D1", "Settlements")
	f.SetCellValue(s, "E1", "Payments - Settlements")

	pay := func(slug string) float64 { return totals[[2]string{"payments", slug}] }
	set := func(slug string) float64 { return totals[[2]string{"settlements", slug}] }

	row := 3
	for _, ln := range summarySheet {
		f.SetCellValue(s, cell("B", row), ln.label)
		var p, d float64
		switch {
		case ln.isSubtotal:
			for _, slug := range sectionSlugs[ln.section] {
				p += pay(slug)
				d += set(slug)
			}
		case ln.slug != "":
			p, d = pay(ln.slug), set(ln.slug)
		}
		f.SetCellValue(s, cell("C", row), round2(p))
		f.SetCellValue(s, cell("D", row), round2(d))
		f.SetCellValue(s, cell("E", row), round2(p-d))
		row++
		if ln.blankAfter {
			row++
		}
	}
	return nil
}

func cell(col string, row int) string { return fmt.Sprintf("%s%d", col, row) }

// --- Consolidated Data sheet ---

type reconRow struct {
	recordRef    string
	status       string
	txnType      string
	description  string
	sku          string
	date         string
	settlementID string
	payBuckets   map[string]float64
	setBuckets   map[string]float64
	payRawTotal  float64 // all amount_entry rows for this record, summarised or not
	setRawTotal  float64
	payRowIDs    string
	setRowIDs    string
}

func writeConsolidated(f *excelize.File, recs []reconRow, buckets []string) error {
	const s = "Consolidated Data"
	f.NewSheet(s)

	head := []string{"record_ref", "reconciliation status", "transaction type", "description / amount type",
		"sku", "date", "settlement id",
		"P: raw total (all entries, incl. unsummarised)", "S: raw total (all entries, incl. unsummarised)"}
	for _, b := range buckets {
		head = append(head, "P: "+b)
	}
	for _, b := range buckets {
		head = append(head, "S: "+b)
	}
	for _, b := range buckets {
		head = append(head, "Δ: "+b)
	}
	head = append(head, "payment source_row ids", "settlement source_row ids")
	for i, h := range head {
		c, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(s, c, h)
	}

	order := map[string]int{"reconciled": 0, "unreconciled_payment": 1, "unreconciled_settlement": 2}
	sort.SliceStable(recs, func(i, j int) bool {
		if order[recs[i].status] != order[recs[j].status] {
			return order[recs[i].status] < order[recs[j].status]
		}
		return recs[i].recordRef < recs[j].recordRef
	})

	r := 2
	for _, rec := range recs {
		col := 1
		put := func(v any) { c, _ := excelize.CoordinatesToCellName(col, r); f.SetCellValue(s, c, v); col++ }
		put(rec.recordRef)
		put(rec.status)
		put(rec.txnType)
		put(rec.description)
		put(rec.sku)
		put(rec.date)
		put(rec.settlementID)
		put(round2(rec.payRawTotal))
		put(round2(rec.setRawTotal))
		for _, b := range buckets {
			put(round2(rec.payBuckets[b]))
		}
		for _, b := range buckets {
			put(round2(rec.setBuckets[b]))
		}
		for _, b := range buckets {
			put(round2(rec.payBuckets[b] - rec.setBuckets[b]))
		}
		put(rec.payRowIDs)
		put(rec.setRowIDs)
		r++
	}
	f.SetPanes(s, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	return nil
}
