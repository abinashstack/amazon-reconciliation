// Package recordref evaluates a config record_ref template against an entry.
//
// A template is a '+'-joined list of tokens. A token is either a known
// substitution or a literal string that is emitted as-is (upper-cased).
//
// Substitutions:
//
//	txn_ref          -> order / transaction reference (order id)
//	sku              -> sku
//	settlement_id    -> settlement id
//	date             -> release/settlement date, YYYY-MM-DD  (see README, Assumption 4)
//	description      -> the row's description / amount-description literal
//	merchant_order_id-> merchant order id
//	shipment_id      -> shipment id
//	record_type      -> source row kind
//
// If any *substitution* token resolves to empty, the key cannot be built and
// the caller falls back to a synthetic NOKEY ref.
package recordref

import (
	"fmt"
	"strings"
	"time"
)

// Fields carries everything a template might reference.
type Fields struct {
	OrderRef        string
	SKU             string
	SettlementID    string
	ReleaseDate     time.Time
	Description     string
	MerchantOrderID string
	ShipmentID      string
	RecordType      string
}

var substitutions = map[string]bool{
	"txn_ref": true, "sku": true, "settlement_id": true, "date": true,
	"description": true, "merchant_order_id": true, "shipment_id": true,
	"record_type": true,
}

// Build renders template. ok is false when a substitution token is empty.
func Build(template string, f Fields) (ref string, ok bool) {
	tokens := strings.Split(template, "+")
	parts := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if !substitutions[tok] {
			parts = append(parts, strings.ToUpper(tok)) // literal
			continue
		}
		var v string
		switch tok {
		case "txn_ref":
			v = f.OrderRef
		case "sku":
			v = f.SKU
		case "settlement_id":
			v = f.SettlementID
		case "date":
			if !f.ReleaseDate.IsZero() {
				v = f.ReleaseDate.Format("2006-01-02")
			}
		case "description":
			v = strings.ToUpper(strings.TrimSpace(f.Description))
		case "merchant_order_id":
			v = f.MerchantOrderID
		case "shipment_id":
			v = f.ShipmentID
		case "record_type":
			v = f.RecordType
		}
		if strings.TrimSpace(v) == "" {
			return "", false
		}
		parts = append(parts, v)
	}
	return strings.Join(parts, "|"), true
}

// Synthetic returns a stable, unique key for an entry that could not produce a
// real record_ref, so it still appears (unreconciled) and stays traceable.
func Synthetic(sourceFile string, sourceRowID int64, seq int) string {
	return fmt.Sprintf("NOKEY:%s:%d:%d", sourceFile, sourceRowID, seq)
}
