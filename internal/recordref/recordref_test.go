package recordref

import (
	"testing"
	"time"
)

func mustParse(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestBuild(t *testing.T) {
	full := Fields{
		OrderRef: "249-3364825-5895055", SKU: "BIO-S000004059_AU",
		SettlementID: "12395580393", ReleaseDate: mustParse("2026-07-17"),
		Description: "To account ending with: 334", MerchantOrderID: "M-1",
		ShipmentID: "SHIP-1", RecordType: "settlement_line",
	}

	cases := []struct {
		name    string
		tmpl    string
		f       Fields
		wantRef string
		wantOK  bool
	}{
		{
			name: "the real reconciling template: txn_ref+sku+date",
			tmpl: "txn_ref+sku+date", f: full,
			wantRef: "249-3364825-5895055|BIO-S000004059_AU|2026-07-17", wantOK: true,
		},
		{
			name: "literal token is upper-cased and passed through verbatim",
			tmpl: "ADJUSTMENT_OTHER+settlement_id+date", f: full,
			wantRef: "ADJUSTMENT_OTHER|12395580393|2026-07-17", wantOK: true,
		},
		{
			name: "a lower-case literal is still upper-cased (config literals are always upper, but Build doesn't assume that)",
			tmpl: "some_literal+settlement_id", f: full,
			wantRef: "SOME_LITERAL|12395580393", wantOK: true,
		},
		{
			name: "description token upper-cases but does NOT normalise separators - exercised for real by the Transfer disbursement rows",
			tmpl: "TRANSFER+description+settlement_id+date", f: full,
			wantRef: "TRANSFER|TO ACCOUNT ENDING WITH: 334|12395580393|2026-07-17", wantOK: true,
		},
		{
			name: "merchant_order_id token - never fires in the supplied dataset (0 matches), verified here instead",
			tmpl: "merchant_order_id+settlement_id+date", f: full,
			wantRef: "M-1|12395580393|2026-07-17", wantOK: true,
		},
		{
			name: "shipment_id token - never fires in the supplied dataset (0 matches), verified here instead",
			tmpl: "shipment_id+INBOUND_DEFECT_FEE+settlement_id+date", f: full,
			wantRef: "SHIP-1|INBOUND_DEFECT_FEE|12395580393|2026-07-17", wantOK: true,
		},
		{
			name: "record_type token, and a token repeated in the same template (settlement_config L138) - never fires in the supplied dataset",
			tmpl: "ADJUSTMENT_OTHER+settlement_id+date+record_type+settlement_id", f: full,
			wantRef: "ADJUSTMENT_OTHER|12395580393|2026-07-17|SETTLEMENT_LINE|12395580393", wantOK: true,
		},
		{
			name: "empty txn_ref -> no key (falls back to synthetic in the caller)",
			tmpl: "txn_ref+sku+date", f: Fields{SKU: "X", ReleaseDate: mustParse("2026-01-01")},
			wantRef: "", wantOK: false,
		},
		{
			name:    "empty description -> no key, even though it's not the 'primary' substitution",
			tmpl:    "TRANSFER+description+settlement_id+date",
			f:       Fields{SettlementID: "S", ReleaseDate: mustParse("2026-01-01")},
			wantRef: "", wantOK: false,
		},
		{
			name: "zero-value release date -> date token empty -> no key",
			tmpl: "txn_ref+sku+date", f: Fields{OrderRef: "O", SKU: "S"},
			wantRef: "", wantOK: false,
		},
		{
			name: "whitespace-only literal segments are dropped, not emitted as empty parts",
			tmpl: "txn_ref++sku", f: Fields{OrderRef: "O", SKU: "S"},
			wantRef: "O|S", wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotRef, gotOK := Build(tc.tmpl, tc.f)
			if gotOK != tc.wantOK {
				t.Fatalf("ok = %v, want %v (ref=%q)", gotOK, tc.wantOK, gotRef)
			}
			if gotOK && gotRef != tc.wantRef {
				t.Fatalf("ref = %q, want %q", gotRef, tc.wantRef)
			}
		})
	}
}

func TestSynthetic(t *testing.T) {
	a := Synthetic("payments", 16595, 9)
	b := Synthetic("payments", 16595, 10)
	c := Synthetic("settlements", 16595, 9)
	if a == b || a == c || b == c {
		t.Fatalf("synthetic keys must be unique per (file, row, seq): got %q %q %q", a, b, c)
	}
	if a != "NOKEY:payments:16595:9" {
		t.Fatalf("unexpected format: %q", a)
	}
}
