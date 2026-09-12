package normalize

import "testing"

// These are the exact examples from the package doc comment, plus real
// values pulled from the two config files and the source data during the
// original defect hunt - this function is what MAPPING_FIXES.sql's fixes
// depend on matching correctly.
func TestKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ItemPrice", "ITEMPRICE"},
		{"FBA Inventory Reimbursement", "FBA_INVENTORY_REIMBURSEMENT"},
		{"MarketplaceFacilitatorTax-Principal", "MARKETPLACEFACILITATORTAX_PRINCIPAL"},
		{"FBA Removal Order: Disposal Fee", "FBA_REMOVAL_ORDER_DISPOSAL_FEE"},
		// the specific pair that has to collapse identically for the record_ref
		// / config match to line up (payment config vs. real settlement data):
		{"FBA_INVENTORY_REIMBURSEMENT_-_DAMAGED:WAREHOUSE", "FBA_INVENTORY_REIMBURSEMENT_DAMAGED_WAREHOUSE"},
		{"FBA Inventory Reimbursement - Damaged:Warehouse", "FBA_INVENTORY_REIMBURSEMENT_DAMAGED_WAREHOUSE"},
		// wildcard sentinel must normalise predictably too (is_desc_wildcard
		// compares TrimSpace+EqualFold separately, but Key() still runs on it)
		{"any", "ANY"},
		{"  any  ", "ANY"},
		// collapsing: runs of separators become ONE underscore, not one each
		{"To account ending with: 334", "TO_ACCOUNT_ENDING_WITH_334"},
		{"A---B", "A_B"},
		{"A   B", "A_B"},
		// leading/trailing separators are trimmed, not left as stray underscores
		{"-LEADING", "LEADING"},
		{"TRAILING-", "TRAILING"},
		{"  spaced  ", "SPACED"},
		// empty and separator-only input normalises to empty, not "_"
		{"", ""},
		{"   ", ""},
		{"---", ""},
		// digits pass through; case-only difference for an already-clean key
		{"AWD_STORAGE_FEE", "AWD_STORAGE_FEE"},
		{"awd_storage_fee", "AWD_STORAGE_FEE"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := Key(tc.in); got != tc.want {
				t.Fatalf("Key(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The whole point of Key(): a source value and a config literal that "mean
// the same thing" but are spelled differently must collapse to the identical
// string, or the config match silently fails.
func TestKey_SourceAndConfigAgree(t *testing.T) {
	pairs := [][2]string{
		{"ItemPrice", "ITEMPRICE"},
		{"To account ending with: 334", "TO_ACCOUNT_ENDING"}, // NOT expected equal - documents the near-miss MAPPING_FIXES.sql defect 1 worked around
	}
	// first pair: must match
	if Key(pairs[0][0]) != Key(pairs[0][1]) {
		t.Fatalf("Key(%q)=%q must equal Key(%q)=%q", pairs[0][0], Key(pairs[0][0]), pairs[0][1], Key(pairs[0][1]))
	}
	// second pair: DELIBERATELY documents that these do NOT collapse to the
	// same key - this is exactly why the exact-description TRANSFER rule in
	// payment_config didn't match the real "with: 334" row, and had to be
	// widened to a wildcard (MAPPING_FIXES.sql defect 1).
	if Key(pairs[1][0]) == Key(pairs[1][1]) {
		t.Fatalf("Key(%q) and Key(%q) were expected to differ (that's the real defect this documents)", pairs[1][0], pairs[1][1])
	}
}
