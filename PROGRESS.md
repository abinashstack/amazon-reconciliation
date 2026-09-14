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

## 2026-09-12 — unit tests for record_ref / wildcard / sign-routing correctness

- Prompted to verify correctness of config-driven ingestion specifically:
  record_ref construction, wildcard handling, sign-based summary routing.
  Checked empirically first (real DB queries) before writing anything: the
  `merchant_order_id`, `shipment_id`, and `record_type` record_ref tokens never
  fire against the supplied data (0 matches each, confirmed via
  `matched_config_id` join counts) - so nothing in the actual run exercises
  those code paths. Wrote real unit tests instead of asserting "looks fine".
- `internal/recordref/recordref_test.go`: covers every token, literal
  handling, the repeated-token template (settlement_config L138), and the
  empty-substitution → no-key fallback. Caught a real bug: `record_type` was
  the only token not upper-cased like every other literal/substitution
  (`internal/recordref/recordref.go`). Fixed. No numeric effect on this
  dataset (the template using it never matches a real row) - confirmed by
  re-running the full pipeline before/after: identical 162,919 `amount_entry`
  rows, identical before-fix mismatches, identical after-fix result.
- `internal/ingest/config_test.go`: `MatchPayment`/`MatchSettlement` precedence
  (all 4 tiers, `amount_field`/`amount_type` never wildcarded, tie-break by
  `file_line_no` independent of slice order - reproduces the real
  `low_value_goods` duplicate-rule defect as a regression test) and
  `summaryFor` sign routing (positive/negative/zero, asymmetric pos≠neg
  buckets, both-blank stays unsummarised). Also confirmed via query: no
  `settlement_config` row has an empty `amount_type` and no `payment_config`
  row has an empty `amount_field` in this file, so the matcher's support for
  those (tested synthetically) is unexercised by real data too; and no other
  exact-duplicate config keys remain beyond the two already fixed.
- Found and documented (not a bug, but worth stating): settlement lines with a
  genuine `amount=0` (47 in this dataset) still get an `amount_entry`, unlike
  zero-valued payment amount columns which are skipped - deliberate asymmetry,
  README Assumption 8, no numeric effect.
- `go test ./...`, `go vet ./...`, `gofmt -l .` all clean. Regenerated
  `out/report_before_fix.xlsx`, `out/report_after_fix.xlsx`,
  `out/pg_dump_after_ingest.dump` from a clean drop/recreate.

## 2026-09-12 (later) — reconciliation logic + granularity-mismatch audit

Prompted to verify the reconciliation logic itself, specifically the
many-to-many aggregation. All checks run against the live after-fix DB
(`internal/reconcile/reconcile.go`'s SQL isn't Go-unit-testable, so this is
empirical, not `go test`):

- **How common is the N:M case, really?** 13,248 of 13,289 reconciled records
  (99.7%) have >1 amount_entry on BOTH sides sharing one record_ref - the
  granularity mismatch is the dominant case in this data, not an edge case.
- **Hand-verified one real N:M record** (order `249-0001574-.../BDCEM030SAFR1A_AU`):
  1 payment row's 5 real amount columns vs. 6 settlement lines from 6 distinct
  source rows both collapse to the identical bucket map
  `{sales_shipping: 1.43, expenses_amazon_fees: -11.61, sales_product_charges:
  32.24, expenses_promotional_rebates: -4.65}`, with correct row-id sets on
  both sides ({10441} vs {30618..30623}).
- **Checked this at scale, not just one example**: across all 13,289 reconciled
  records, exploded to (record_ref, bucket) = 33,371 line items. Every single
  one ties to exactly 0.00, AND the *gross* diff (sum of `abs(P-S)`) is also
  0.00 - not just net. This rules out the failure mode where individual
  records don't tie but happen to cancel out in aggregate; the Summary sheet
  reconciling to zero isn't hiding any offsetting errors underneath it.
- **Trace-back completeness**: every distinct `source_row_id` that produced an
  `amount_entry` (22,964 payments, 54,979 settlements) appears in exactly one
  `recon_record.*_row_ids` array on its side - no source row missing, none
  double-attributed to two different record_refs.
- **`unreconciled_settlement` is never hit by real data** (0 in this dataset,
  since the settlement file's every line found a payment match) - same
  "never fires" caveat as the record_ref tokens found last session. Rather
  than assume correctness by symmetry with the well-exercised payment-only
  path, fabricated one settlement-only `amount_entry` inside a transaction,
  ran the literal `reconcile.go` SQL against it, confirmed the row classifies
  as `unreconciled_settlement` with an empty `payments_buckets`/`payments_row_ids`
  and correct settlement-sourced `transaction_type`/`sku`/`event_date`/
  `settlement_id`, then rolled the transaction back (confirmed real data
  unchanged afterward: same 13289/9387/0 counts). No bug found; the branch
  works as designed. (One test-fixture slip on my part, not a system bug: I
  forgot to set `description_literal` on the fabricated row, so that one
  field came back blank in the test - unrelated to the real ingesters, which
  always populate it.)
- No code changes this round - purely verification. No re-ingest needed.

## 2026-09-12 (later still) — code structure review: separation of ingest/recon/report

Prompted to review clarity and structure, specifically the separation between
ingest / recon / report. Found and fixed two real structural issues rather
than just describing them:

1. **Config matching lived inside the `ingest` package** (`config.go`:
   `Configs`, `MatchPayment`, `MatchSettlement`, `summaryFor`,
   `ImportConfigCSVs`, `LoadConfigsFromDB`) even though "apply the mapping
   configs" is its own concern (assignment part 2), distinct from `ingest`'s
   actual job (parse files, batch-insert, COPY). Extracted to a new
   `internal/mapping` package - mechanical move, confirmed low-risk first by
   grepping for any file outside config.go/config_test.go that referenced
   `PaymentRule`/`SettlementRule`/`Configs` directly (only one: `ingest.go`'s
   `cfg.Payment`/`cfg.Settlement` field access, which keeps compiling
   unchanged since the field names didn't move). `summaryFor` exported as
   `SummaryFor` for cross-package use. Package boundaries now mirror the
   assignment's four numbered parts one-to-one.

2. **The Summary-sheet diff computation existed in two places that had to be
   kept in sync by hand**: `report.writeSummary` (drives the xlsx) and a
   second, independent SQL query inline in `cmd/recon/main.go`'s
   `printMismatches` (drives the `recon mismatches` CLI text output) - both
   reading `ScopedSummarySQL` but then re-deriving the pivot/diff/
   summary_layout join separately. Moved that logic into
   `report.Mismatches()`; `main.go` is now a thin formatter that calls it. The
   CLI text and the workbook can no longer silently compute the diff two
   different ways.

Also, while re-reading `ingest.go`: found `ingestPayments`/`ingestSettlements`
each ended with their own `flushSource()` call, immediately followed by
`Engine.Run()` calling `flushSource()` again - the second call was always a
no-op (the buffer both ingesters flush is guaranteed empty by then), just
confusing to a reader wondering why it's called three times. Removed the two
per-ingester calls; `Run()` is now the single place the terminal flush
happens. Added a comment on the `srcRow`/`pendingEntry`/`entRow` split
explaining *why* it's a three-stage buffer (Postgres generates
`source_row.id`, which `amount_entry` FKs to, only after INSERT - not
incidental complexity).

Verified zero behavioural change: `go build`, `go vet`, `go test ./...` all
clean, and a full clean pipeline re-run (`all` -> `mismatches` -> `fixes` ->
`report` -> `mismatches`) produced byte-identical results to before the
refactor (162,919 `amount_entry`, same 5 before-fix mismatches, same
9387/13289/0 and "no mismatches" after).

## 2026-09-12 (yet later) — auditability: can every number really be traced?

Prompted (combined with a repeat structure check) specifically: can any number
in the report be traced back to source rows? The row-id trace-back mechanism
itself was already verified complete in the reconciliation-logic audit. This
pass asked a sharper question: can a reader reproduce the Summary sheet's
*scope* filter (Released payments + the settlement in the file) from the
report alone, not just trace an individual number?

- **Found a real gap**: summing the whole (unfiltered) `P: sales_product_charges`
  column in Consolidated Data gives 607,360.68, not the Summary sheet's
  348,815.93 - and there was no "Released/Deferred" column anywhere in the
  report to explain the difference or let a reader filter it out.
  `settlement_id` alone wasn't enough either (filtering by settlement without
  also filtering by status still over-counts). The scope rule lived only in
  `report.ScopedSummarySQL`'s SQL text, invisible to anyone reading the
  workbook.
- **Fixed**: `reconcile.Run` now computes `payment_txn_status` (the payments
  file's Transaction status) and `in_summary_scope` (Released AND
  settlement-in-file) once per `record_ref`, stored on `recon_record`. Both
  are written to Consolidated Data as new columns ("payment transaction
  status", "in Summary sheet scope"). Verified end-to-end, from the actual
  generated `.xlsx` file (not the database): filtering Consolidated Data to
  `in Summary sheet scope = TRUE` and summing `P: sales_product_charges`
  reproduces 348,815.93 exactly, using nothing but the spreadsheet.
- **Also improved the report/reconcile boundary** (ties back into the
  structure question asked in the same message): `report.ScopedSummarySQL`
  previously re-derived the scope directly from `amount_entry`/`source_row`
  (reaching into `source_row.raw->>'Transaction status'` and
  `row_kind='settlement_header'` - implementation details of `ingest`'s raw
  storage). It now just reads `recon_record.in_summary_scope`/
  `payments_buckets`, so the scope rule is expressed exactly once (in
  `reconcile`), and `report` no longer needs to know anything about how
  `ingest` stores raw payloads. Verified the switch is a pure refactor: summed
  `recon_record.settlements_buckets` unconditionally across all rows and
  `payments_buckets` where `in_summary_scope`, and both matched a direct
  `amount_entry` aggregate to the cent, before wiring `report` to use it.
- Schema change: `recon_record` gains `payment_txn_status text` and
  `in_summary_scope boolean not null default false` (amended `001_schema.sql`
  directly - no production data/deployment history to preserve across
  migrations in this project).
- Verified zero numeric regression throughout: full clean pipeline re-run
  gives the same 162,919 `amount_entry`, same 5 before-fix mismatches, same
  9387/13289/0 and "no mismatches" after.

## 2026-09-12 (final pass) — error handling, idempotency, tests, performance

Prompted for all four together. Found and fixed real issues in three of the
four; the fourth (performance) turned up an honest non-result that's more
valuable reported accurately than oversold.

### Error handling
- `parsePaymentTime`/`parseSettlementTime`/`parseAmount` errors were
  discarded at all 3 call sites (`event, _ := ...`) - a malformed date/amount
  would silently become zero/blank with zero visibility. Added a `warnings`
  accumulator to `Engine` (exact counts, capped example messages) surfaced as
  a `WARNING:` block on stderr after `recon ingest`.
- Writing tests for this immediately caught two REAL bugs against the actual
  data, not synthetic cases: (1) `Transaction Release Date` abbreviates the
  month for 1,153 of 20,498 real rows (`"1 Aug 2026"` vs `"17 July 2026"`) -
  silently falling back to the wrong date every prior run of this pipeline;
  turned out to be out-of-scope rows (no Summary/reconciled impact) once
  fixed, but was a real latent correctness bug. (2) `parseOffset`'s `hh:mm`
  branch silently swallowed a malformed-minutes error (never triggered by
  real data - every offset here is whole-hour). Both fixed and covered by
  `internal/ingest/parse_test.go` (new - this package had zero test coverage
  before this pass).
- `parseAmount` signature changed to distinguish "blank" (expected, `(0,nil)`)
  from "garbage" (not expected, `(0,err)`) - previously both returned a bare
  `0`.

### Idempotent re-ingestion
- `migrate`/`ingest`/`fixes` are each safe to re-run - verified throughout
  this project via repeated drop/recreate/re-run cycles.
- Found a real gap: `recon fixes` was NOT idempotent - its one INSERT
  (Defect 5) duplicates on a second application. Reproduced concretely (two
  applications -> two identical `payment_config` rows at `file_line_no=9999`)
  before fixing: the insert is now preceded by a `delete ... where
  file_line_no = 9999`, and both config tables gained a `unique(file_line_no)`
  constraint as a second line of defence against any other accidental
  duplicate. Re-verified: two applications in a row now leave one row and
  identical numbers.

### Tests
- New: `internal/ingest/parse_test.go` (dates + `parseAmount` + the
  `warnings` accumulator), `internal/normalize/normalize_test.go` (the `Key`
  canonicaliser every config match depends on, including the specific
  near-miss pair that documents why Defect 1 was needed).
- Also removed genuinely dead code found while adding these tests:
  `normalize.PaymentAmountField`, a map defined but never referenced anywhere
  (superseded by `PaymentAmountColumns` some time earlier, never cleaned up).

### Performance
- Instrumented `recon ingest` to print per-phase DB time (now permanent, not
  throwaway): `source_row` insert ~4.9s (~15,900 rows/s), `amount_entry` COPY
  ~5.1s (~31,900 rows/s), for the supplied 162,919-entry ingest (~11.5s total).
  `reconcile` ~3.6s, `report.Generate` ~3.5s (DB query ~0.9s, excelize writes
  ~1.0s, SaveAs serialisation ~1.4s).
- Checked for the real risk (quadratic blowup) rather than guessing: every
  aggregate in `reconcile`/`report` goes through an indexed `group by
  record_ref` or a same-partitioned window function, never a self-join or
  per-row all-rows comparison. Config matching is linear per entry against a
  fixed-size (~150-row) rule set, not per-entry-per-entry. Nothing quadratic
  found; both DB-writing phases scale linearly with row count.
- Tried switching `writeConsolidated` from per-cell `SetCellValue` to one
  `SetSheetRow` per row, expecting a meaningful win at ~884k cells written.
  Measured repeatedly before shipping the claim: no significant difference
  (~1.0s either way). Kept the change (not slower, smaller API surface) but
  wrote the honest (non-)result in the code comment instead of the originally
  assumed "~40% faster" - caught this before committing by re-running the
  timing probe multiple times rather than trusting one measurement.
- Documented the actual largest cost (`SaveAs`'s zip/XML serialisation, ~40%
  of report generation) and the real next step for much larger files
  (excelize `StreamWriter`), deliberately not implemented - it's a non-trivial
  rewrite of the one function every reported number passes through, not
  justified by this project's actual data sizes.

Full clean pipeline re-run after all of the above: same 162,919 `amount_entry`,
same 5 before-fix mismatches, same after-fix "no mismatches", reconciled=13289
unchanged (only `unreconciled_payment` shifted by a couple, out-of-scope
rows only, from the date-parsing correction).

## 2026-09-14 — MAPPING_FIXES.sql: document non-config-fixable items too

User's deliverable checklist called out one gap explicitly: MAPPING_FIXES.sql
had a commented block per config-fixable defect (5, all fully fixed in config
data), but nothing for a defect that ISN'T fixable in config, or where the
real fix belongs in matching logic instead - required by the original brief
too ("if you believe a fix genuinely cannot be expressed in the config
schema, say so").

Reviewed the session for genuine non-config cases rather than inventing one:
all 5 Summary-sheet routing defects were fully config-fixable (said so
explicitly now, in the file's header). Four *other* real issues found during
this engagement were NOT expressible in config at all - added as a clearly
separated, non-executing SQL comment block (Section 2) in MAPPING_FIXES.sql,
each stating what the defect is, why config can't express the fix, where the
real fix lives, and what was done:
  2.1 Consolidated Data showing an all-zero row for an all-unsummarised
      record (companion to Defect 5) - fixed with the raw-total columns in
      internal/report.
  2.2 Summary sheet's scope filter not reconstructable from the report -
      fixed with payment_txn_status/in_summary_scope in
      internal/reconcile + internal/report.
  2.3 record_ref's record_type token not upper-cased - fixed in
      internal/recordref/recordref.go.
  2.4 Transaction Release Date's abbreviated-month rows silently mis-parsing
      - fixed in internal/ingest/dates.go.

Verified the file still applies cleanly with the added comment block (valid
SQL `/* */`, nothing executes from it): full clean pipeline re-run, identical
results to before this edit - reconciled=13289, unreconciled_payment=9386,
unreconciled_settlement=0, before-fix mismatches unchanged, after-fix "no
mismatches" unchanged. Updated README's Results section, which had drifted
slightly out of date (9389/9387 vs the current 9388/9386 - the 1-count shift
from the Transaction Release Date fix a few commits back had not been
reflected there).
