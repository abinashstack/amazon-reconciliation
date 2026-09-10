-- ============================================================================
-- summary_layout: maps each config summary-field slug onto a line of the
-- report's Summary sheet.
--
-- Derived by reading amazon_sample_output_report.xlsx (sheet "Summary") against
-- the set of slugs the two configs route into. Section/label/sort match the
-- sample sheet exactly. Slugs that no sample line corresponds to (holding
-- buckets that net inside a record but are never summarised - e.g.
-- total_refund_expense_or_sales_amt) are given section 'Unsummarised' and are
-- excluded from the sheet but kept for auditing in the Consolidated sheet.
-- ============================================================================

truncate summary_layout;

insert into summary_layout (summary_field, section, line_label, sort_order) values
  -- ---- Sales ----
  ('sales_product_charges',            'Sales',    'Product Charges',                 10),
  ('sales_tax',                        'Sales',    'Tax',                             20),
  ('sales_shipping',                   'Sales',    'Shipping',                        30),
  ('sales_amazon_fees',                'Sales',    'Amazon fees',                     40),
  ('sales_inventory_reimbursements',   'Sales',    'Inventory Reimbursements',        50),
  ('sales_other',                      'Sales',    'Other',                           70),
  -- ---- Refunds ----
  ('refunded_expenses',                'Refunds',  'Refund expenses',               110),
  ('refunded_sales',                   'Refunds',  'Refunded sales',                120),
  -- ---- Expenses ----
  ('expenses_promotional_rebates',     'Expenses', 'Promo rebates',                 210),
  ('expenses_fba_fees',                'Expenses', 'FBA fees',                      220),
  ('expenses_cost_of_advertising',     'Expenses', 'Cost of Advertising',           230),
  ('expenses_amazon_fees',             'Expenses', 'Amazon fees',                   250),
  ('expenses_reversed_reimbursements', 'Expenses', 'Reversed Reimbursements',       260),
  ('expenses_other',                   'Expenses', 'Other',                         280),
  -- ---- Paid To Amazon ----
  ('paid_to_amazon',                   'Paid To Amazon', 'Paid To Amazon',          400),
  -- ---- holding buckets: not on the Summary sheet ----
  ('total_refund_expense_or_sales_amt',      'Unsummarised', 'total_refund_expense_or_sales_amt',      900),
  ('total_adjustment_other_buyer_recharge_amt','Unsummarised','total_adjustment_other_buyer_recharge_amt',910),
  ('bank_account_transfer_round_off',        'Unsummarised', 'bank_account_transfer_round_off',        920),
  ('beginning_balance',                      'Unsummarised', 'beginning_balance',                      930),
  ('amazon_carried_forward',                 'Unsummarised', 'amazon_carried_forward',                 940),
  ('current_reserve_amount',                 'Unsummarised', 'current_reserve_amount',                 950);
