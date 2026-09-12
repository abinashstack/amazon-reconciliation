-- ============================================================================
-- MAPPING_FIXES.sql
--
-- Config-level fixes for the genuine mapping defects found by comparing the
-- independently-derived Payments and Settlements columns of the Summary sheet
-- (reconciliation scope: settlement 12395580393, Released payments).
--
-- Before fix (recon mismatches):
--   Expenses  Amazon fees      -266349.92  vs -132593.41   diff -133756.51
--   Refunds   Refund expenses      258.87  vs      216.28   diff      42.59
--   Sales     Other                 11.61  vs       11.97   diff      -0.36
--   Sales     Product Charges   349012.55  vs   348908.77   diff     103.78
--   Sales     Shipping            8097.54  vs     8200.96   diff    -103.42
--
-- Apply with:  recon fixes sql/MAPPING_FIXES.sql
--             (applies this file to the config tables, then re-ingests +
--              reconciles; loadconfigs is NOT re-run, so these edits persist)
--
-- All fixes are UPDATE/DELETE on the config DATA. No number is plugged; each
-- block routes amounts the same way the opposite source already routes the
-- economically-equivalent amount.
-- ============================================================================

begin;

-- ---------------------------------------------------------------------------
-- DEFECT 1  -  bank disbursement booked as an Amazon fee   (Expenses / Amazon fees, -133756.51)
--
-- source_row payments line for settlement 12395580393:
--   type="Transfer"  description="To account ending with: 334"  total=-133756.51
-- This is the settlement payout to the seller's bank - a movement of money
-- already counted, never a P&L line.
--
-- payment_config L62/L63 exist to drop `TRANSFER ... total` from the summary,
-- but their descriptions are matched exactly (after normalisation):
--   L62 TO_YOUR_ACCOUNT_ENDING   L63 TO_ACCOUNT_ENDING
-- The real value normalises to TO_ACCOUNT_ENDING_WITH_334, matches neither, and
-- the row falls through to the empty-transaction_type catch-all
--   L110  (,any,total)  ->  expenses_amazon_fees
-- which books the -133,756.51 disbursement as an Amazon fee.
--
-- WAS : payment_config L63  transaction_type=TRANSFER  description=TO_ACCOUNT_ENDING  amount_field=total
--       -> summary (positive)= ''   -> summary (negative)= ''      [never reached]
-- NOW : description = any (wildcard) so every TRANSFER `total` is matched here
--       and left unsummarised. Exact-description transfer rules
--       (TRANSFER/MICRO_DEPOSIT ...) still win by match precedence.
-- ---------------------------------------------------------------------------
update payment_config
   set description_raw  = 'any',
       description_norm = 'ANY',
       is_desc_wildcard = true
 where file_line_no = 63
   and transaction_type_raw = 'TRANSFER'
   and amount_field = 'total';

-- ---------------------------------------------------------------------------
-- DEFECT 2  -  order-level tax sub-types split away from principal tax
--             (Sales / Product Charges +103.78, Sales / Shipping -103.42, Sales / Other -0.36)
--
-- The payments file reports one `sales tax collected` figure per order line,
-- covering principal + shipping + gift-wrap tax net of tax discounts;
-- payment_config L71 routes it to sales_product_charges.
--
-- On the settlement side those pieces are itemised. Only ITEMPRICE/TAX (L65)
-- lands in sales_product_charges; the siblings are misrouted:
--   settlement_config L61  ORDER/ITEMPRICE/GIFTWRAPTAX               -> sales_other
--   settlement_config L63  ORDER/ITEMPRICE/SHIPPINGTAX              -> sales_shipping
--   settlement_config L95  ORDER/PROMOTION/TAXDISCOUNT              -> sales_shipping
--   settlement_config L118 ORDER/ITEMWITHHELDTAX/LOWVALUEGOODSTAX-SHIPPING -> sales_shipping
--
-- WAS : the four rules above -> sales_other / sales_shipping
-- NOW : -> sales_product_charges, matching ITEMPRICE/TAX and the payment side.
-- Verified: settlement Tax 13765.71 + ShippingTax 581.68 + TaxDiscount -672.16
--           + GiftWrapTax 0.36 = 13675.59 = payment `sales tax collected`.
-- ---------------------------------------------------------------------------
update settlement_config set summary_pos = 'sales_product_charges', summary_neg = 'sales_product_charges'
 where file_line_no = 61 and transaction_type_raw = 'ORDER' and amount_type_raw = 'ITEMPRICE'
   and amount_description_raw = 'GIFTWRAPTAX';

update settlement_config set summary_pos = 'sales_product_charges', summary_neg = 'sales_product_charges'
 where file_line_no = 63 and transaction_type_raw = 'ORDER' and amount_type_raw = 'ITEMPRICE'
   and amount_description_raw = 'SHIPPINGTAX';

update settlement_config set summary_pos = 'sales_product_charges', summary_neg = 'sales_product_charges'
 where file_line_no = 95 and transaction_type_raw = 'ORDER' and amount_type_raw = 'PROMOTION'
   and amount_description_raw = 'TAXDISCOUNT';

update settlement_config set summary_pos = 'sales_product_charges', summary_neg = 'sales_product_charges'
 where file_line_no = 118 and transaction_type_raw = 'ORDER' and amount_type_raw = 'ITEMWITHHELDTAX'
   and amount_description_raw = 'LOWVALUEGOODSTAX-SHIPPING';

-- ---------------------------------------------------------------------------
-- DEFECT 3  -  payment low-value-goods tax routed to Shipping
--             (contributes to Sales / Product Charges & Sales / Shipping above)
--
-- payment_config has two identical keys for ORDER / low_value_goods:
--   L5  -> sales_shipping          L6  -> sales_product_charges
-- L5 wins by line order, so the AU low-value-goods tax (principally a product
-- tax; settlement books LOWVALUEGOODSTAX-PRINCIPAL to sales_product_charges at
-- L119) lands in sales_shipping.
--
-- WAS : payment_config L5  ORDER/any/low_value_goods -> sales_shipping
-- NOW : deleted, so the duplicate L6 (-> sales_product_charges) applies.
-- ---------------------------------------------------------------------------
delete from payment_config
 where file_line_no = 5 and transaction_type_raw = 'ORDER'
   and amount_field = 'low_value_goods' and summary_pos = 'sales_shipping';

-- Dormant duplicate: payment_config L72 (ORDER/any/sales_tax_collected -> sales_shipping)
-- is shadowed by L71 (-> sales_product_charges) but is the same defect class.
delete from payment_config
 where file_line_no = 72 and transaction_type_raw = 'ORDER'
   and amount_field = 'sales_tax_collected' and summary_pos = 'sales_shipping';

-- ---------------------------------------------------------------------------
-- DEFECT 4  -  refund marketplace-facilitator tax summarised on one side only
--             (Refunds / Refund expenses +42.59)
--
-- Marketplace-facilitator tax is collected and remitted by Amazon - pass-through,
-- not seller P&L. The payment side treats it that way:
--   payment_config L14  REFUND/any/sales_tax_collected -> (unsummarised)
-- The settlement side books the refunded pieces into refunded_expenses:
--   settlement_config L4   REFUND/ITEMPRICE/TAX        (negative leg -> refunded_expenses)
--   settlement_config L11  REFUND/ITEMPRICE/SHIPPINGTAX -> refunded_expenses
--   settlement_config L20  REFUND/PROMOTION/TAXDISCOUNT -> refunded_expenses
--
-- WAS : the rules above -> refunded_expenses (L4 positive leg -> the unsummarised
--       holding bucket total_refund_expense_or_sales_amt)
-- NOW : all -> (unsummarised), matching the payment side.
-- Verified: settlement refunded_expenses 216.28 - Tax(-41.09) - ShippingTax(-3.19)
--           - TaxDiscount(1.69) = 258.87 = payment refunded_expenses.
-- ---------------------------------------------------------------------------
update settlement_config set summary_pos = '', summary_neg = ''
 where file_line_no = 4 and transaction_type_raw = 'REFUND' and amount_type_raw = 'ITEMPRICE'
   and amount_description_raw = 'TAX';

update settlement_config set summary_pos = '', summary_neg = ''
 where file_line_no = 11 and transaction_type_raw = 'REFUND' and amount_type_raw = 'ITEMPRICE'
   and amount_description_raw = 'SHIPPINGTAX';

update settlement_config set summary_pos = '', summary_neg = ''
 where file_line_no = 20 and transaction_type_raw = 'REFUND' and amount_type_raw = 'PROMOTION'
   and amount_description_raw = 'TAXDISCOUNT';

-- consistency (0 in this dataset, same defect class):
update settlement_config set summary_pos = '', summary_neg = ''
 where file_line_no = 12 and transaction_type_raw = 'REFUND' and amount_type_raw = 'ITEMPRICE'
   and amount_description_raw = 'GIFTWRAPTAX';

-- ---------------------------------------------------------------------------
-- DEFECT 5  -  one real transaction renders as two Consolidated Data rows
--             (Consolidated Data only - no Summary-sheet impact; both entries
--              already route to '' so neither line 1 nor line 2 above changes)
--
-- The two Transfer/bank-payout rows in the whole payments file (settlement
-- 12395580393: -133756.51; settlement 12382593803: -97919.76) have only their
-- `other` and `total` amount columns populated, both carrying the same figure.
-- `other` has no rule of its own for TRANSFER, so it falls to the empty-
-- transaction_type catch-all L111 (`,any,other -> ''`), whose record_ref
-- template `txn_ref+settlement_id+date` can't resolve (txn_ref/order id is
-- empty for a Transfer) and falls back to a synthetic NOKEY key - a DIFFERENT
-- key than the `total` column gets from the DEFECT-1 fix
-- (`TRANSFER+description+settlement_id+date`). One transaction, two record_refs,
-- two Consolidated Data rows, each showing the same real dollar amount once
-- traced back - confusing for an auditor even though no money is double-counted
-- in any bucket (both entries are unsummarised).
--
-- WAS : no payment_config rule for TRANSFER/other -> falls through to the
--       generic catch-all's order-keyed template.
-- NOW : add the same TRANSFER-specific rule for `other` that DEFECT 1 gave
--       `total`, so both amount columns of the same row resolve to the SAME
--       record_ref and collapse into one Consolidated Data row. Exact-
--       description transfer rules (TRANSFER/MICRO_DEPOSIT/other, L61) still
--       win by match precedence, so this only catches the generic disbursement.
--
-- Idempotency: `recon fixes` is safe to run more than once (e.g. after a
-- retry) for every UPDATE/DELETE above - re-applying them is a no-op. This is
-- the one INSERT in the file, so it needs its own guard: delete the
-- fixture's own file_line_no first, so applying this file twice ends up with
-- one row, not two (payment_config.file_line_no also carries a unique
-- constraint as a second line of defence - see migrations/001_schema.sql).
-- ---------------------------------------------------------------------------
delete from payment_config where file_line_no = 9999;

insert into payment_config
  (file_line_no, transaction_type_raw, transaction_type_norm,
   description_raw, description_norm, is_desc_wildcard,
   amount_field, record_ref_template, summary_pos, summary_neg, raw)
values
  (9999, 'TRANSFER', 'TRANSFER', 'any', 'ANY', true,
   'other', 'TRANSFER+description+settlement_id+date', '', '',
   '{"note":"MAPPING_FIXES.sql defect 5 - collapses the other/total split for generic transfers"}'::jsonb);

commit;
