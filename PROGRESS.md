# Progress log

## 2026-09-10 → 09-11

### Exploration
- Payments CSV is **wide** (1 row = 1 txn, ~11 signed amount columns), BOM + 7
  preamble lines, 23,026 data rows, types Order/Refund/Adjustment/Transfer/…,
  Released + Deferred.
- Settlement TXT is **long** (1 row = 1 amount), tab-separated, 1 settlement
  header + 54,979 line rows, a single settlement-id (`12395580393`).
- Reconciliation key: payment **Transaction Release Date** (→UTC, day) ==
  settlement **posted-date-time**. Verified on order `249-3364825-5895055` /
  `BIO-S000004059_AU`: payment "17 July 2026 4:26:32 pm GMT+9" == settlement
  "17.07.2026 07:26:32 UTC". The `date` token in `record_ref` is the release
  date, not the order `date/time`.
- Read both configs end to end: shared `summary_field` vocabulary and
  `record_ref` template language; payment keyed on (type, description,
  amount_field), settlement on (type, amount_type, amount_description).

### Schema + pipeline
- Single ingestion table `source_row` for both files (raw line + jsonb +
  provenance). Derived `amount_entry` (wide→long), config tables as source of
  truth after import, `summary_total` maintained during the ingest pass,
  `recon_record` from a `record_ref` aggregate.
- Go CLI: `migrate | loadconfigs | ingest | reconcile | report | all | fixes |
  mismatches`. Configs imported from CSV once; `MAPPING_FIXES.sql` edits the
  tables; `fixes` re-ingests from the tables so edits persist.
- Ingest: 162,919 `amount_entry` rows in ~12 s. Match notes: 107,730 wildcard,
  206 exact, 4 catch-all (payments); 54,979 exact (settlements); 0 no_match.

### Reconciliation
- `reconciled = 13289`, `unreconciled_payment = 9389` (other settlements +
  deferred), `unreconciled_settlement = 0`.
- Summary scope decision: settlement `12395580393`, Released payments. Each
  column summed purely from its own source.

### Defect hunt (Summary mismatches, before fix)
| line | Payments | Settlements | diff |
|---|--:|--:|--:|
| Expenses / Amazon fees   | -266349.92 | -132593.41 | -133756.51 |
| Refunds / Refund expenses |     258.87 |     216.28 |     +42.59 |
| Sales / Other             |      11.61 |      11.97 |      -0.36 |
| Sales / Product Charges   |  349012.55 |  348908.77 |    +103.78 |
| Sales / Shipping          |    8097.54 |    8200.96 |    -103.42 |

Traced (all in `sql/MAPPING_FIXES.sql`):
1. `-133756.51` = one payment row, `Transfer` / "To account ending with: 334"
   (the bank payout), matched by the empty-`transaction_type` catch-all
   `(,any,total → expenses_amazon_fees)` because the exact `TO_ACCOUNT_ENDING`
   transfer rule doesn't match the "with: 334" suffix. → widen that rule's
   description to wildcard.
2. `±103` = order tax sub-types (`SHIPPINGTAX`, `GIFTWRAPTAX`, `TAXDISCOUNT`,
   `LOWVALUEGOODSTAX-SHIPPING`) routed to `sales_shipping` / `sales_other` on the
   settlement side while the payment side rolls all order tax into
   `sales_product_charges`. Verified the four settlement pieces sum to exactly
   the payment `sales tax collected` figure. → route them to
   `sales_product_charges`.
3. Duplicate `payment_config` rows for `ORDER / low_value_goods`; the
   `→ sales_shipping` copy wins. → delete it (+ delete the dormant duplicate
   `sales_tax_collected → sales_shipping`).
4. `+42.59` = settlement books refunded marketplace-facilitator tax
   (`REFUND/ITEMPRICE/TAX` neg leg, `SHIPPINGTAX`, `PROMOTION/TAXDISCOUNT`) into
   `refunded_expenses`; payment side treats it as pass-through. → make the
   settlement refund-tax rules pass-through.

### After fix
- `recon fixes sql/MAPPING_FIXES.sql` → `recon mismatches` → **no mismatches**.
  Every "Payments − Settlements" cell on the Summary sheet is 0.
- Generated `out/report_before_fix.xlsx`, `out/report_after_fix.xlsx`,
  `out/pg_dump_after_ingest.dump` (9.4 MB, custom format), `out/sample_*`.

### Notes / limits
- One settlement in the file → the report exercises one settlement. The pipeline
  is not settlement-specific; more settlements would just widen the scope.
- `total_refund_expense_or_sales_amt` and the other holding-bucket slugs have no
  Summary line and net to zero here; left as `Unsummarised`.

## 2026-09-11 (later) — audit pass, prompted by "check the 2 unreconciled entries"

- Independently re-verified the after-fix Summary with a standalone SQL query
  bypassing the Go report code - matches the workbook exactly. Checked for
  silently-dropped `summary_field` values (none) and non-zero amounts hiding in
  the `Unsummarised` holding buckets within scope (none).
- User asked me to check the 2 `unreconciled_payment` entries that carry no
  Summary bucket (both legitimately unsummarised - the bank-disbursement
  `Transfer` rows). Checking them surfaced a real Consolidated Data defect:
  each Transfer row's `other` and `total` columns (same underlying amount)
  resolved to two *different* `record_ref`s, so one real transaction rendered
  as two Consolidated Data rows, both all-zero in every bucket column - money
  invisible except by following the row-id trace-back. Confirmed isolated to
  exactly the 2 Transfer rows in the whole dataset (`amount_entry` grouped by
  `source_row_id having count(distinct record_ref) > 1`); no `reconciled`
  record affected, no Summary impact (both entries were already unsummarised).
- Fix (Defect 5, `sql/MAPPING_FIXES.sql`): added the missing TRANSFER-specific
  rule for the `other` column so both columns resolve to the same key. The two
  rows collapsed to one each (`unreconciled_payment` 9389 → 9387); Summary
  still reconciles to 0.
- Added "raw total (all entries, incl. unsummarised)" columns to Consolidated
  Data (both sides) so an unsummarised record is never an invisible all-zero
  row again, generally. First attempt double-counted every ordinary reconciled
  order (summed the redundant `total` entry on top of its own components) -
  caught by checking a normal order's raw total against its bucket total before
  shipping it, not just the 2 known rows. Fixed: a payments row's `total` is
  excluded from the raw-total sum whenever the same record has a non-`total`
  entry (it's a control total, always redundant with components on this file);
  kept when `total` is the only entry. Re-verified against both a Transfer row
  (raw total = real amount, not 2x) and a normal order (raw total = net of
  components, not inflated).
- Regenerated `out/report_before_fix.xlsx`, `out/report_after_fix.xlsx`,
  `out/pg_dump_after_ingest.dump` from a clean drop/recreate of `recon`.
