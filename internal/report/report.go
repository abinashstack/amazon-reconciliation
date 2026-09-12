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
	recordRef        string
	status           string
	txnType          string
	description      string
	sku              string
	date             string
	settlementID     string
	paymentTxnStatus string // the payments file's Transaction status (Released/Deferred); '' when no payment side
	inSummaryScope   bool   // reproduces the Summary sheet's scope filter row-by-row - see README Assumption 2
	payBuckets       map[string]float64
	setBuckets       map[string]float64
	payRawTotal      float64 // all amount_entry rows for this record, summarised or not
	setRawTotal      float64
	payRowIDs        string
	setRowIDs        string
}

func writeConsolidated(f *excelize.File, recs []reconRow, buckets []string) error {
	const s = "Consolidated Data"
	f.NewSheet(s)

	head := []string{"record_ref", "reconciliation status", "transaction type", "description / amount type",
		"sku", "date", "settlement id",
		"payment transaction status", "in Summary sheet scope",
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

	// One SetSheetRow call per row (the whole row's values in a slice) rather
	// than one SetCellValue call per cell - ~884k individual calls collapsed
	// to ~23k for the supplied data. Measured this against SetCellValue
	// before keeping it: NOT a meaningful speed difference at this row count
	// (~1.0s either way - see README's Performance section for the numbers),
	// so this is not "the fix" for larger files. Kept anyway since it isn't
	// slower and is a smaller API surface than a per-cell closure; the actual
	// cost at this scale is excelize's SaveAs serialization (~1.4s, ~40% of
	// report.Generate), which only a streaming writer would meaningfully cut.
	row := make([]any, 0, 9+3*len(buckets)+2)
	for i, rec := range recs {
		row = row[:0]
		row = append(row, rec.recordRef, rec.status, rec.txnType, rec.description, rec.sku,
			rec.date, rec.settlementID, rec.paymentTxnStatus, rec.inSummaryScope,
			round2(rec.payRawTotal), round2(rec.setRawTotal))
		for _, b := range buckets {
			row = append(row, round2(rec.payBuckets[b]))
		}
		for _, b := range buckets {
			row = append(row, round2(rec.setBuckets[b]))
		}
		for _, b := range buckets {
			row = append(row, round2(rec.payBuckets[b]-rec.setBuckets[b]))
		}
		row = append(row, rec.payRowIDs, rec.setRowIDs)
		if err := f.SetSheetRow(s, cell("A", i+2), &row); err != nil {
			return err
		}
	}
	f.SetPanes(s, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	return nil
}
