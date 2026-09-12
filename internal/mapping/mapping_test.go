package mapping

import "testing"

// --- sign-based summary routing -------------------------------------------

func TestSummaryFor(t *testing.T) {
	cases := []struct {
		name     string
		amount   float64
		pos, neg string
		want     string
	}{
		{"positive amount picks the positive bucket", 12.34, "sales_product_charges", "refunded_sales", "sales_product_charges"},
		{"negative amount picks the negative bucket", -12.34, "sales_product_charges", "refunded_sales", "refunded_sales"},
		{
			"asymmetric buckets (real config pattern, e.g. ADJUSTMENT/FBA_INVENTORY_REIMBURSEMENT_-_LOST:WAREHOUSE): positive",
			5.00, "sales_inventory_reimbursements", "expenses_reversed_reimbursements", "sales_inventory_reimbursements",
		},
		{
			"asymmetric buckets: negative",
			-5.00, "sales_inventory_reimbursements", "expenses_reversed_reimbursements", "expenses_reversed_reimbursements",
		},
		{"zero amount: documented convention routes to the positive bucket", 0, "sales_other", "expenses_other", "sales_other"},
		{"both sides blank -> stays unsummarised regardless of sign", 12.34, "", "", ""},
		{"both sides blank, negative -> stays unsummarised", -12.34, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SummaryFor(tc.amount, tc.pos, tc.neg); got != tc.want {
				t.Fatalf("SummaryFor(%v, %q, %q) = %q, want %q", tc.amount, tc.pos, tc.neg, got, tc.want)
			}
		})
	}
}

// --- wildcard handling + match precedence, payments -------------------------

func TestMatchPayment_Precedence(t *testing.T) {
	// Four tiers for the same amount_field, lowest LineNo first within a tier
	// so a tie-break bug wouldn't be masked by "correct answer happens to be first".
	cfg := &Configs{Payment: []PaymentRule{
		{ID: 1, LineNo: 40, TxnTypeNorm: "", DescWildcard: true, AmountField: "total", SummaryPos: "catchall_wildcard"},
		{ID: 2, LineNo: 30, TxnTypeNorm: "", DescNorm: "SPECIFIC", AmountField: "total", SummaryPos: "catchall_exact"},
		{ID: 3, LineNo: 20, TxnTypeNorm: "ORDER", DescWildcard: true, AmountField: "total", SummaryPos: "order_wildcard"},
		{ID: 4, LineNo: 10, TxnTypeNorm: "ORDER", DescNorm: "SPECIFIC", AmountField: "total", SummaryPos: "order_exact"},
	}}

	t.Run("exact type + exact desc beats everything", func(t *testing.T) {
		r, note := cfg.MatchPayment("ORDER", "SPECIFIC", "total")
		if r == nil || r.SummaryPos != "order_exact" || note != "exact" {
			t.Fatalf("got %+v note=%q", r, note)
		}
	})
	t.Run("exact type + wildcard beats catch-all + exact desc", func(t *testing.T) {
		r, note := cfg.MatchPayment("ORDER", "SOMETHING_ELSE", "total")
		if r == nil || r.SummaryPos != "order_wildcard" || note != "wildcard" {
			t.Fatalf("got %+v note=%q", r, note)
		}
	})
	t.Run("no exact-type rule at all: catch-all + exact desc beats catch-all + wildcard", func(t *testing.T) {
		r, note := cfg.MatchPayment("REFUND", "SPECIFIC", "total")
		if r == nil || r.SummaryPos != "catchall_exact" || note != "catchall" {
			t.Fatalf("got %+v note=%q", r, note)
		}
	})
	t.Run("nothing matches but the empty-type wildcard catch-all", func(t *testing.T) {
		r, note := cfg.MatchPayment("REFUND", "SOMETHING_ELSE", "total")
		if r == nil || r.SummaryPos != "catchall_wildcard" || note != "catchall" {
			t.Fatalf("got %+v note=%q", r, note)
		}
	})
	t.Run("amount_field must match exactly - never wildcarded", func(t *testing.T) {
		r, note := cfg.MatchPayment("ORDER", "SPECIFIC", "product_sales")
		if r != nil {
			t.Fatalf("expected no match on a different amount_field, got %+v", r)
		}
		if note != "no_match" {
			t.Fatalf("note = %q, want no_match", note)
		}
	})
}

func TestMatchPayment_TieBreakByLineNo(t *testing.T) {
	// Reproduces the real defect found in payment_config before the fix:
	// two rules with an IDENTICAL (transaction_type, description, amount_field)
	// key, routing to different buckets. The lower file_line_no must win,
	// deterministically - this is documented as README Assumption 5.
	cfg := &Configs{Payment: []PaymentRule{
		{ID: 5, LineNo: 5, TxnTypeNorm: "ORDER", DescWildcard: true, AmountField: "low_value_goods", SummaryPos: "sales_shipping"},
		{ID: 6, LineNo: 6, TxnTypeNorm: "ORDER", DescWildcard: true, AmountField: "low_value_goods", SummaryPos: "sales_product_charges"},
	}}
	r, note := cfg.MatchPayment("ORDER", "ANY PRODUCT TITLE", "low_value_goods")
	if r == nil || r.ID != 5 || r.SummaryPos != "sales_shipping" || note != "wildcard" {
		t.Fatalf("expected the lower file_line_no (id 5) to win the tie, got %+v note=%q", r, note)
	}
	// order of the slice must not matter - only file_line_no
	cfg.Payment[0], cfg.Payment[1] = cfg.Payment[1], cfg.Payment[0]
	r2, _ := cfg.MatchPayment("ORDER", "ANY PRODUCT TITLE", "low_value_goods")
	if r2 == nil || r2.ID != 5 {
		t.Fatalf("tie-break must be independent of slice order, got %+v", r2)
	}
}

// --- wildcard handling + match precedence, settlements ----------------------

func TestMatchSettlement_Precedence(t *testing.T) {
	cfg := &Configs{Settlement: []SettlementRule{
		{ID: 1, LineNo: 40, TxnTypeNorm: "", AmountTypeNorm: "ITEMFEES", DescWildcard: true, SummaryPos: "catchall_wildcard"},
		{ID: 2, LineNo: 30, TxnTypeNorm: "", AmountTypeNorm: "ITEMFEES", AmountDescNorm: "COMMISSION", SummaryPos: "catchall_exact"},
		{ID: 3, LineNo: 20, TxnTypeNorm: "ORDER", AmountTypeNorm: "ITEMFEES", DescWildcard: true, SummaryPos: "order_wildcard"},
		{ID: 4, LineNo: 10, TxnTypeNorm: "ORDER", AmountTypeNorm: "ITEMFEES", AmountDescNorm: "COMMISSION", SummaryPos: "order_exact"},
	}}

	t.Run("exact type + exact desc beats everything", func(t *testing.T) {
		r, note := cfg.MatchSettlement("ORDER", "ITEMFEES", "COMMISSION")
		if r == nil || r.SummaryPos != "order_exact" || note != "exact" {
			t.Fatalf("got %+v note=%q", r, note)
		}
	})
	t.Run("exact type + wildcard beats catch-all + exact desc", func(t *testing.T) {
		r, note := cfg.MatchSettlement("ORDER", "ITEMFEES", "SOMETHING_ELSE")
		if r == nil || r.SummaryPos != "order_wildcard" || note != "wildcard" {
			t.Fatalf("got %+v note=%q", r, note)
		}
	})
	t.Run("catch-all + exact desc beats catch-all + wildcard", func(t *testing.T) {
		r, note := cfg.MatchSettlement("REFUND", "ITEMFEES", "COMMISSION")
		if r == nil || r.SummaryPos != "catchall_exact" || note != "catchall" {
			t.Fatalf("got %+v note=%q", r, note)
		}
	})
	t.Run("amount_type must match exactly for every rule in this dataset (none has an empty amount_type)", func(t *testing.T) {
		r, _ := cfg.MatchSettlement("ORDER", "ITEMPRICE", "COMMISSION")
		if r != nil {
			t.Fatalf("expected no match on a different amount_type, got %+v", r)
		}
	})
	t.Run("a rule with an empty amount_type (never occurs in the supplied config, but the matcher supports it) matches any amount_type", func(t *testing.T) {
		cfg2 := &Configs{Settlement: []SettlementRule{
			{ID: 9, LineNo: 1, TxnTypeNorm: "ORDER", AmountTypeNorm: "", DescWildcard: true, SummaryPos: "any_amount_type"},
		}}
		r, note := cfg2.MatchSettlement("ORDER", "WHATEVER_TYPE", "WHATEVER_DESC")
		if r == nil || r.SummaryPos != "any_amount_type" || note != "wildcard" {
			t.Fatalf("got %+v note=%q", r, note)
		}
	})
}

func TestMatchSettlement_NoMatch(t *testing.T) {
	cfg := &Configs{Settlement: []SettlementRule{
		{ID: 1, LineNo: 1, TxnTypeNorm: "ORDER", AmountTypeNorm: "ITEMFEES", AmountDescNorm: "COMMISSION"},
	}}
	r, note := cfg.MatchSettlement("REFUND", "ITEMPRICE", "PRINCIPAL")
	if r != nil || note != "no_match" {
		t.Fatalf("got %+v note=%q, want nil/no_match", r, note)
	}
}
