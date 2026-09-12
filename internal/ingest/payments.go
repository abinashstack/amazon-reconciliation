package ingest

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/abinashstack/amazon-reconciliation/internal/mapping"
	"github.com/abinashstack/amazon-reconciliation/internal/normalize"
	"github.com/abinashstack/amazon-reconciliation/internal/recordref"
)

// ingestPayments reads amazon_payments_data.csv:
//   - BOM + 7 free-text preamble lines, then the real header, then data.
//   - one physical row = one transaction with up to 11 signed amount columns.
//
// Each non-zero amount column becomes one amount_entry, matched independently
// against the payment config on (type, description, amount_field).
func (e *Engine) ingestPayments(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// physical lines, for raw_line trace-back
	raw, err := io.ReadAll(bufio.NewReader(f))
	if err != nil {
		return err
	}
	raw = trimBOM(raw)
	physLines := strings.Split(string(raw), "\n")

	r := csv.NewReader(strings.NewReader(string(raw)))
	r.FieldsPerRecord = -1

	// advance to header
	var header []string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return fmt.Errorf("payments: header row not found")
		}
		if err != nil {
			return err
		}
		if len(rec) > 0 && strings.TrimSpace(rec[0]) == "date/time" {
			header = rec
			break
		}
	}
	col := indexHeaders(header)

	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		line, _ := r.FieldPos(0)
		m := rowMap(header, rec)

		s := srcRow{
			file:    "payments",
			lineNo:  line,
			kind:    "payment_txn",
			rawLine: physLine(physLines, line),
			raw:     m,
		}

		txnRaw := m["type"]
		txnNorm := normalize.Key(txnRaw)
		descRaw := m["description"]
		descNorm := normalize.Key(descRaw)

		event, _ := parsePaymentTime(m["date/time"])
		release, _ := parsePaymentTime(m["Transaction Release Date"])
		if release.IsZero() {
			release = event // fallback: unreleased/adjustment rows key on posted date
		}

		rf := recordref.Fields{
			OrderRef:        strings.TrimSpace(m["order ID"]),
			SKU:             strings.TrimSpace(m["sku"]),
			SettlementID:    strings.TrimSpace(m["settlement ID"]),
			ReleaseDate:     dateOnly(release),
			Description:     descRaw,
			MerchantOrderID: strings.TrimSpace(m["order ID"]),
			RecordType:      "payment_txn",
		}

		for seq, ac := range normalize.PaymentAmountColumns {
			cell, ok := col[ac.Header]
			if !ok || cell >= len(rec) {
				continue
			}
			amt := parseAmount(rec[cell])
			if amt == 0 {
				continue
			}
			pe := pendingEntry{
				seq:           seq,
				txnTypeRaw:    txnRaw,
				txnTypeNorm:   txnNorm,
				matchDescRaw:  descRaw,
				matchDescNorm: descNorm,
				amountField:   ac.Field,
				orderRef:      rf.OrderRef,
				sku:           rf.SKU,
				settlementID:  rf.SettlementID,
				eventDate:     dateOnly(event),
				releaseDate:   dateOnly(release),
				descLiteral:   descRaw,
				amount:        amt,
				currency:      "AUD",
				configKind:    "payment",
			}
			if rule, note := e.cfg.MatchPayment(txnNorm, descNorm, ac.Field); rule != nil {
				id := rule.ID
				pe.matchedConfigID = &id
				pe.matchNote = note
				pe.summaryField = mapping.SummaryFor(amt, rule.SummaryPos, rule.SummaryNeg)
				if ref, ok := recordref.Build(rule.RecordRefTmpl, rf); ok {
					pe.recordRef = ref
				}
			} else {
				pe.matchNote = "no_match"
			}
			e.accumulate("payments", pe.summaryField, amt)
			s.pending = append(s.pending, pe)
		}
		if err := e.addSource(ctx, s); err != nil {
			return err
		}
	}
	return nil // Engine.Run flushes any remaining buffered rows once, after both files
}

// --- helpers shared with settlements.go ---

func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

func indexHeaders(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, h := range header {
		m[strings.TrimSpace(h)] = i
	}
	return m
}

func rowMap(header, rec []string) map[string]string {
	m := make(map[string]string, len(header))
	for i, h := range header {
		if i < len(rec) {
			m[strings.TrimSpace(h)] = rec[i]
		}
	}
	return m
}

func physLine(lines []string, n int) string {
	if n >= 1 && n <= len(lines) {
		return strings.TrimRight(lines[n-1], "\r")
	}
	return ""
}

func parseAmount(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

var _ = time.Time{}
