package ingest

import (
	"bufio"
	"context"
	"encoding/csv"
	"io"
	"os"
	"strings"

	"github.com/abinashstack/amazon-reconciliation/internal/normalize"
	"github.com/abinashstack/amazon-reconciliation/internal/recordref"
)

// ingestSettlements reads amazon_settlements_data.txt (tab-separated):
//   - row 1 is the header.
//   - a row with an empty transaction-type but a total-amount is a settlement
//     header (the deposit) -> stored in source_row only.
//   - every other row is a settlement line -> one amount_entry, matched against
//     the settlement config on (transaction-type, amount-type, amount-description).
func (e *Engine) ingestSettlements(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	raw, err := io.ReadAll(bufio.NewReader(f))
	if err != nil {
		return err
	}
	raw = trimBOM(raw)
	physLines := strings.Split(string(raw), "\n")

	r := csv.NewReader(strings.NewReader(string(raw)))
	r.Comma = '\t'
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	header, err := r.Read()
	if err != nil {
		return err
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		line, _ := r.FieldPos(0)
		m := rowMap(header, rec)

		txnRaw := strings.TrimSpace(m["transaction-type"])
		isHeader := txnRaw == "" && strings.TrimSpace(m["total-amount"]) != ""

		s := srcRow{
			file:    "settlements",
			lineNo:  line,
			rawLine: physLine(physLines, line),
			raw:     m,
		}
		if isHeader {
			s.kind = "settlement_header"
			if err := e.addSource(ctx, s); err != nil {
				return err
			}
			continue
		}
		s.kind = "settlement_line"

		amt := parseAmount(m["amount"])
		amountTypeRaw := m["amount-type"]
		amountDescRaw := m["amount-description"]
		txnNorm := normalize.Key(txnRaw)
		amountTypeNorm := normalize.Key(amountTypeRaw)
		amountDescNorm := normalize.Key(amountDescRaw)

		posted, _ := parseSettlementTime(firstNonEmpty(m["posted-date-time"], m["posted-date"]))

		rf := recordref.Fields{
			OrderRef:        strings.TrimSpace(m["order-id"]),
			SKU:             strings.TrimSpace(m["sku"]),
			SettlementID:    strings.TrimSpace(m["settlement-id"]),
			ReleaseDate:     dateOnly(posted),
			Description:     amountDescRaw,
			MerchantOrderID: strings.TrimSpace(m["merchant-order-id"]),
			ShipmentID:      strings.TrimSpace(m["shipment-id"]),
			RecordType:      "settlement_line",
		}

		pe := pendingEntry{
			seq:             0,
			txnTypeRaw:      txnRaw,
			txnTypeNorm:     txnNorm,
			matchDescRaw:    amountDescRaw,
			matchDescNorm:   amountDescNorm,
			amountField:     amountTypeNorm,
			amountTypeNorm:  amountTypeNorm,
			orderRef:        rf.OrderRef,
			sku:             rf.SKU,
			settlementID:    rf.SettlementID,
			eventDate:       dateOnly(posted),
			releaseDate:     dateOnly(posted),
			shipmentID:      rf.ShipmentID,
			merchantOrderID: rf.MerchantOrderID,
			descLiteral:     amountDescRaw,
			amount:          amt,
			currency:        firstNonEmpty(m["currency"], "AUD"),
			configKind:      "settlement",
		}
		if rule, note := e.cfg.MatchSettlement(txnNorm, amountTypeNorm, amountDescNorm); rule != nil {
			id := rule.ID
			pe.matchedConfigID = &id
			pe.matchNote = note
			pe.summaryField = summaryFor(amt, rule.SummaryPos, rule.SummaryNeg)
			if ref, ok := recordref.Build(rule.RecordRefTmpl, rf); ok {
				pe.recordRef = ref
			}
		} else {
			pe.matchNote = "no_match"
		}
		e.accumulate("settlements", pe.summaryField, amt)
		s.pending = append(s.pending, pe)

		if err := e.addSource(ctx, s); err != nil {
			return err
		}
	}
	return e.flushSource(ctx)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
