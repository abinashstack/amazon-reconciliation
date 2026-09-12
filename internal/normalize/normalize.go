// Package normalize turns the inconsistently-formatted values in the source
// files and the config keys into a single canonical form so they can be
// compared. The SAME function is applied to source values and to config keys
// at load time.
//
// Rule: upper-case, then collapse every run of non-alphanumeric characters to a
// single underscore, then trim leading/trailing underscores.
//
//	"ItemPrice"                       -> "ITEMPRICE"
//	"FBA Inventory Reimbursement"     -> "FBA_INVENTORY_REIMBURSEMENT"
//	"MarketplaceFacilitatorTax-Principal" -> "MARKETPLACEFACILITATORTAX_PRINCIPAL"
//	"FBA Removal Order: Disposal Fee" -> "FBA_REMOVAL_ORDER_DISPOSAL_FEE"
package normalize

import (
	"strings"
	"unicode"
)

// Key canonicalises a value for matching.
func Key(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToUpper(r))
			prevUnderscore = false
		default:
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// PaymentAmountColumns is the ordered list of amount columns exploded from one
// payments row, paired with the config amount_field slug.
type PaymentAmountColumn struct {
	Header string
	Field  string
}

var PaymentAmountColumns = []PaymentAmountColumn{
	{"product sales", "product_sales"},
	{"shipping credits", "shipping_credits"},
	{"gift wrap credits", "gift_wrap_credits"},
	{"promotional rebates", "promotional_rebates"},
	{"sales tax collected", "sales_tax_collected"},
	{"low value goods", "low_value_goods"},
	{"selling fees", "selling_fees"},
	{"fulfilment by amazon fees", "fba_fees"},
	{"other transaction fees", "other_transaction_fees"},
	{"other", "other"},
	{"total", "total"},
}
