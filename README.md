# Amazon Payments / Settlement Reconciliation

Ingests an Amazon **Payment Report** (CSV) and **Settlement Report** (TXT) into
PostgreSQL, routes every amount through the supplied mapping configs, reconciles
the two sources on a shared key, and produces an Excel report (Summary +
Consolidated Data). Pipeline is a single Go binary.

---

## Run the full flow end to end

### Prerequisites
- Go 1.24+
- PostgreSQL 14+ running locally, with an empty database:
  ```sql
  create database recon;
  ```
- Connection string via `RECON_DSN` (default
  `postgres://postgres:postgres@localhost:5432/recon?sslmode=disable`):
  ```bash
  export RECON_DSN='postgres://postgres:YOURPASS@localhost:5432/recon?sslmode=disable'
  ```
- Input files sit in `data/` (already committed):
  `amazon_payments_data.csv`, `amazon_settlements_data.txt`,
  `amazon_payment_configs_au_old.csv`, `amazon_settlement_configs_au.csv`.

### One shot
```bash
go run ./cmd/recon all out/report_before_fix.xlsx
```
`all` = `migrate` → `loadconfigs` → `ingest` → `reconcile` → `report`.

### Step by step
```bash
go run ./cmd/recon migrate                       # (re)create schema  (DROPS existing data)
go run ./cmd/recon loadconfigs                    # CSV configs -> payment_config / settlement_config
go run ./cmd/recon ingest                         # both data files -> source_row / amount_entry / summary_total
go run ./cmd/recon reconcile                      # amount_entry -> recon_record
go run ./cmd/recon report out/report_before_fix.xlsx
go run ./cmd/recon mismatches                     # Summary lines where Payments != Settlements
```

### Apply the mapping fixes and regenerate
```bash
go run ./cmd/recon fixes sql/MAPPING_FIXES.sql    # applies SQL to the config tables, then re-ingest + reconcile
go run ./cmd/recon report out/report_after_fix.xlsx
go run ./cmd/recon mismatches                     # expect: "no mismatches"
```
`fixes` does **not** re-run `loadconfigs`, so the SQL edits to the config tables
persist across the re-ingest.

### Unit tests
```bash
go test ./...
```
No database needed - these exercise the three config-driven ingestion
mechanisms directly:
- `internal/recordref` - `record_ref` template construction: every token
  (`txn_ref`, `sku`, `settlement_id`, `date`, `description`, `merchant_order_id`,
  `shipment_id`, `record_type`), literal segments, a repeated token, and the
  empty-substitution → no-key fallback. Three of those tokens
  (`merchant_order_id`, `shipment_id`, `record_type`) never actually fire
  against the supplied data (checked via `matched_config_id` join counts), so
  this is the only place they're verified at all - and it caught a real bug:
  `record_type` wasn't upper-cased like every other literal/substitution,
  fixed in `internal/recordref/recordref.go` (no numeric effect here since
  that template path is unused in this dataset; re-confirmed by re-running the
  full pipeline before/after - identical `amount_entry` count and mismatches).
- `internal/mapping` - `MatchPayment`/`MatchSettlement` wildcard precedence
  (exact type+desc > exact type+wildcard > catch-all+exact > catch-all+wildcard;
  `amount_field`/`amount_type` never wildcarded; tie-break by `file_line_no`,
  independent of slice order - reproduces the real duplicate-rule defect) and
  `SummaryFor` sign-based routing (positive/negative/zero, asymmetric
  pos≠neg buckets, both-blank stays unsummarised regardless of sign).
- `internal/normalize` - `Key`, the canonicalisation every config match
  depends on: the package-doc examples, collapsing runs of separators to one
  `_`, trimming, and (deliberately) the one real near-miss pair that does
  *not* collapse to the same key (`"To account ending with: 334"` vs the
  config's `TO_ACCOUNT_ENDING`) - documents why Defect 1 was needed instead of
  relying on normalisation to paper over it.
- `internal/ingest` - date/amount parsing (`parsePaymentTime`,
  `parseSettlementTime`, `parseOffset`, `parseAmount`) had no test coverage at
  all before this pass; added it and found two real bugs in the process (see
  Error handling below): `Transaction Release Date` sometimes abbreviates the
  month (`"1 Aug 2026..."`) and wasn't tried; `parseOffset`'s `hh:mm` branch
  silently swallowed a malformed minutes value. Both fixed and covered.

### PostgreSQL dump
```bash
pg_dump --no-owner --no-privileges -Fc "$RECON_DSN" -f out/pg_dump_after_ingest.dump
# restore:  pg_restore --no-owner -d recon out/pg_dump_after_ingest.dump
```

### What's in `out/` (committed)
| file | |
|---|---|
| `report_before_fix.xlsx` | report with the configs as supplied - 5 Summary mismatches |
| `report_after_fix.xlsx`  | report after `MAPPING_FIXES.sql` - Summary reconciles to 0 |
| `pg_dump_after_ingest.dump` | full compressed `pg_dump` (custom format) after the after-fix run |
| `sample_schema.sql` | schema-only dump |
| `sample_rows.csv` | representative rows from every table (configs in full; 200-300 rows each for the large tables) |

---

## Error handling

A single malformed row (a garbled date, a non-numeric amount) must not abort
ingestion of the other 78,000 - but it must not vanish either. `internal/ingest`
draws that line as: recover (treat as zero/blank, exactly as before) **and**
count + sample it, surfaced as a `WARNING:` block on stderr after `recon
ingest` if anything happened:

```bash
go run ./cmd/recon ingest
# WARNING: 3 row(s) had a value that could not be parsed (treated as zero/blank, not skipped):
#   3 x payment amount unparseable
#   examples:
#     payment amount unparseable: line 4821, column "selling fees": "N/A" is not a number
```

This replaced silently discarding the error at three call sites
(`parsePaymentTime`/`parseSettlementTime`/`parseAmount` in `payments.go` /
`settlements.go`), found while reviewing error handling. Writing the tests for
this immediately caught two real, previously-invisible bugs against the actual
supplied data (not synthetic cases):
1. `Transaction Release Date` abbreviates the month for 1,153 of 20,498 real
   rows (`"1 Aug 2026..."` vs the usual `"17 July 2026..."`) - `date/time`
   never does this, only the release-date column, and only sometimes. The
   parser only tried the full-month layout, so these 1,153 release dates were
   silently failing and falling back to the order's `date/time` instead -
   wrong for `record_ref`'s `date` token, though it turned out these
   particular rows are all outside the reconciliation scope (a later
   settlement), so no Summary or reconciled-record impact. Fixed:
   `parsePaymentTime` now tries both `January` and `Jan` layouts.
2. `parseOffset`'s `hh:mm` branch discarded the minutes' parse error
   (`mm, _ = strconv.Atoi(...)`), unlike the branch beside it - a `+09:XX`
   with a bad minutes part would have silently become `+09:00`. Never
   triggered by real data (every offset in this file is whole-hour, `+9`),
   caught only by direct unit tests. Fixed.

`parseAmount` also changed shape: `(0, nil)` for a blank cell (expected -
most of a payments row's 11 amount columns are blank) vs `(0, err)` for a
cell that has content but isn't a number (not expected, must be surfaced).
Before, both cases returned a bare `0`, indistinguishable.

## Idempotent re-ingestion

- `recon migrate` / `recon ingest` / `recon fixes` are each safe to re-run:
  `Engine.Run` truncates `amount_entry`, `source_row`, `summary_total`,
  `recon_record` before rebuilding, so re-running `ingest` (or `all`) any
  number of times converges to the identical end state - verified throughout
  this project by re-running the full pipeline from a dropped/recreated
  database after every change and diffing the results.
- **Found and fixed a real gap**: `recon fixes` was not safe to run twice in a
  row. `MAPPING_FIXES.sql`'s `UPDATE`/`DELETE` statements are naturally
  idempotent, but its one `INSERT` (Defect 5, adding a new config rule) would
  insert a second, duplicate row on a second application - reproduced this
  concretely (`recon fixes` twice → two identical `payment_config` rows at
  `file_line_no=9999`) before fixing it. Fixed two ways: the insert is now
  preceded by `delete from payment_config where file_line_no = 9999` (so
  re-applying the file converges instead of accumulating), and
  `payment_config`/`settlement_config` both gained a `unique (file_line_no)`
  constraint (`migrations/001_schema.sql`) so any *other* accidental duplicate
  insert fails loudly instead of silently corrupting the config table.
  Re-verified: `recon fixes` run twice in a row now leaves exactly one row and
  identical reconciliation numbers both times.
- `ingest_batch` is the one table that is **not** reset by a re-ingest (by
  design - it's a history of ingestion runs, `source_row.batch_id` always
  points at the latest one). Repeated `recon ingest` runs will accumulate
  batch history rows over time; this is intentional and harmless (nothing
  else references a stale batch id), but worth knowing if the table's growth
  is ever surprising.

## Performance

Measured on the supplied files (23,026 payment rows exploding to ~107,940
non-zero amount columns; 54,979 settlement lines) via `recon ingest`'s
per-phase timing (now printed on every run) and an isolated `report.Generate`
timing pass:

| phase | time | rows/sec |
|---|--:|--:|
| CSV parse + config match + date/amount parse (Go, no DB) | ~1.4s | - |
| `source_row` insert (batched `INSERT ... RETURNING id`, batches of 500) | ~4.9s | ~15,900/s |
| `amount_entry` COPY (batches of 20,000) | ~5.1s | ~31,900/s |
| **ingest total** | **~11.5s** | |
| `reconcile` (one SQL statement, full-outer aggregate) | ~3.6s | |
| `report.Generate`: DB query (`recon_record` + the raw-total window function) | ~0.9s | |
| `report.Generate`: excelize in-memory writes (2 sheets, ~884k cells) | ~1.0s | |
| `report.Generate`: `SaveAs` (zip/XML serialise to disk) | ~1.4s | |
| **report total** | **~3.5s** | |

Both DB-writing phases and `reconcile`/`report`'s SQL scale **linearly** with
row count - checked for the classic risk (an accidental cross-join or
per-row-vs-all-rows comparison) and found none: every aggregate goes through
`group by record_ref` (indexed) or a window function partitioned the same
way, never a self-join over the full table. Config matching
(`MatchPayment`/`MatchSettlement`) is a linear scan over the ~150 config
rules per amount entry, but the rule count doesn't grow with file size, so
total matching cost is `O(entries × constant)`, not `O(entries²)`. On a 10x
larger pair of files, naive linear extrapolation puts the full pipeline at
roughly 2-3 minutes - a batch job, not something needing async/background
handling for this use case.

Where the time actually goes is not where a first guess would put it:
`report.Generate`'s Go-side excelize writing + serialisation (~2.4s of 3.5s,
~70%) is larger than the DB query that feeds it (~0.9s), and about as large as
the DB-writing side of ingest. **Tried and measured, not just assumed**: since
excelize's `SetCellValue` is known to re-walk the sheet's internal structure
per call, switched `writeConsolidated` to one `SetSheetRow` call per row
(~884k calls → ~23k) expecting a meaningful win - measured repeatedly before
and after and found **no significant difference** at this row count (~1.0s
either way). Kept the change anyway (it isn't slower, and it's a smaller API
surface), but corrected the comment in `report.go` rather than claim a result
that didn't hold up. The actual largest single cost, `SaveAs`'s zip/XML
serialisation (~1.4s, ~40% of `report.Generate`), would need excelize's
`StreamWriter` API to meaningfully cut - a real, identifiable next step for a
much larger Consolidated Data sheet, deliberately not done here: it changes
`writeConsolidated`'s API from random-access `SetCellValue`/`SetSheetRow` to
strictly-sequential row writing, which is a non-trivial rewrite of the one
function every number in the workbook passes through, and isn't justified by
the data sizes this project actually has to handle.

---

## Results

Reconciliation scope = settlement `12395580393` (the only settlement in the file),
Released payments. **Final run**: `reconciled = 13289`, `unreconciled_payment = 9386`
(other settlements + deferred payments - expected, not an error, see
Assumption 8), `unreconciled_settlement = 0`. Before the config fixes:
`reconciled = 13289`, `unreconciled_payment = 9388`, `unreconciled_settlement
= 0` (the difference from 9386 is Defect 5 below collapsing two fragmented
rows into one each - a Consolidated Data display fix with no Summary-total
effect, not a reconciliation-count fix).

### Before fix — Summary mismatches
| section | line | Payments | Settlements | diff |
|---|---|--:|--:|--:|
| Expenses | Amazon fees     | -266349.92 | -132593.41 | **-133756.51** |
| Refunds  | Refund expenses |     258.87 |     216.28 | **+42.59** |
| Sales    | Other           |      11.61 |      11.97 | **-0.36** |
| Sales    | Product Charges | 349012.55 | 348908.77 | **+103.78** |
| Sales    | Shipping        |   8097.54 |   8200.96 | **-103.42** |

### After `sql/MAPPING_FIXES.sql` — `recon mismatches` → *no mismatches*

Re-verified independently: re-ran the Summary aggregation as a standalone SQL
query (bypassing the Go report code) and it matches the workbook line for line.
Also checked for silent drops - no `summary_field` value produced by
`amount_entry` is missing from `summary_layout`, and every "Unsummarised"
holding-bucket slug nets to zero within scope - so "no mismatches" isn't hiding
anything outside the 21 Summary lines either.

Five config defects (see `sql/MAPPING_FIXES.sql` for the traced records and the
exact statements). The first four fix the Summary sheet; the fifth is a
Consolidated Data-only data-quality fix with no Summary impact:

1. **Bank disbursement booked as an Amazon fee** (-133,756.51). The
   `Transfer` / "To account ending with: 334" payout row doesn't match
   `payment_config`'s exact `TO_ACCOUNT_ENDING` description, so it falls through
   to the empty-`transaction_type` catch-all (`,any,total → expenses_amazon_fees`).
   Fix: widen that transfer rule's description to a wildcard so the payout is
   left unsummarised.
2. **Order-level tax sub-types split away from principal tax**
   (Product Charges / Shipping / Other, ~±104). Settlement books shipping tax,
   gift-wrap tax, promo tax-discount and LVG shipping tax into
   `sales_shipping` / `sales_other`; the payment side rolls all order tax into
   `sales_product_charges`. Fix: route the four settlement sub-types to
   `sales_product_charges`.
3. **Payment low-value-goods tax routed to Shipping.** Duplicate
   `payment_config` rows for `ORDER / low_value_goods`; the `→ sales_shipping`
   copy wins. Fix: delete it so the `→ sales_product_charges` copy applies.
4. **Refund marketplace-facilitator tax summarised on one side only** (+42.59).
   The payment side treats refunded facilitator tax as pass-through; the
   settlement side books it to `refunded_expenses`. Fix: make the settlement
   refund-tax rules pass-through too.
5. **One transaction renders as two Consolidated Data rows.** The two bank-
   disbursement `Transfer` rows in the whole payments file have only `other` and
   `total` populated (same figure); `other` has no TRANSFER-specific rule so it
   falls to the generic catch-all, whose order-keyed template can't resolve
   (no order id on a transfer) and produces a synthetic key different from the
   one Defect 1 gives `total` - one transaction, two rows, both showing an
   all-zero bucket columns (neither is summarised). Found by spot-checking the
   two `unreconciled_payment` rows this produced; confirmed via
   `array_agg(distinct record_ref) ... having count(distinct record_ref) > 1`
   that it's isolated to exactly these two transactions in the whole dataset.
   Fix: add the matching TRANSFER rule for `other` so both columns resolve to
   the same key and collapse into one row.

Also added two **raw total (all entries, incl. unsummarised)** columns to
Consolidated Data (`internal/report/report.go`) so an unsummarised record never
renders as an all-zero row with the real amount invisible except by following
the row-id trace-back - closes the gap Defect 5 surfaced, generally rather than
only for these two rows. A payments row's `total` column is a redundant control
total (equal to the sum of its own other columns) whenever those other columns
exist, so it's excluded from that sum in that case, to avoid silently doubling
the figure - verified against both a Transfer row (raw total = the real
-97,919.76 / -133,756.51, not 2x) and an ordinary reconciled order (raw total =
net of all components, not inflated by the redundant `total` entry).

---

## Schema and why

All DDL is in `migrations/`. `recon migrate` runs every `*.sql` there in order;
the schema file is idempotent (`drop ... if exists`).

### `source_row` — the single ingestion table (both files)
| column | purpose |
|---|---|
| `id` | surrogate key |
| `batch_id` | which `ingest_batch` produced the row |
| `source_file` | `payments` \| `settlements` |
| `file_line_no` | 1-based physical line number in the file |
| `row_kind` | `payment_txn` \| `settlement_header` \| `settlement_line` |
| `raw_line` | the exact original line, untouched |
| `raw` (jsonb) | `{original_header: original_string_value}` |

**Why one table:** the assignment requires both files in a single table. The two
files have completely different structures, so a shared table can only be the
*raw landing*: original text + parsed key/value JSON + provenance. Every number
in the final report is traceable to a set of `source_row.id`s via
`raw_line` / `file_line_no` / `source_file`. Normalisation happens downstream,
never by hand-editing the inputs.

### `payment_config`, `settlement_config` — the mapping configs
Stored verbatim in `*_raw` columns and `raw` (jsonb), plus pre-normalised match
keys (`*_norm`) and an `is_desc_wildcard` flag (config `any`). `file_line_no`
gives every rule a stable identity and a deterministic tie-break order.
Two tables rather than one because the key columns genuinely differ
(payments: `description` + `amount_field`; settlements: `amount_type` +
`amount_description`).

**These tables are the source of truth after `loadconfigs`.** `MAPPING_FIXES.sql`
edits them; re-ingest reads from them, so fixes are config-level and durable.

### `amount_entry` — normalized amount-level rows (derived)
One row per amount. A wide payments row is exploded to one entry per non-zero
amount column; a settlement line maps 1:1. Each entry is FK-linked to its
`source_row`, and carries: the normalised match keys, the record_ref
ingredients, the resolved `record_ref`, the winning `matched_config_id`, the
`summary_field` bucket, and a `match_note` (`exact` / `wildcard` / `catchall` /
`no_match`). Zero-valued payment amount slots are skipped (they add nothing to
any sum and nothing to audit); the full row is always in `source_row`.

Entries whose rule produced no usable key get a synthetic
`NOKEY:<file>:<source_row_id>:<seq>` record_ref so they still appear
(unreconciled) and stay traceable.

### `summary_total` — maintained during ingestion
`(source_file, summary_field) -> amount, entry_count`, the **unscoped** total per
side. Updated in-memory as each `amount_entry` is produced during the ingest pass
(not a post-hoc `GROUP BY` over `amount_entry`), then flushed once. The Summary
sheet re-derives its two columns over the reconciliation scope (Assumption 2)
with the same independence — one query sums only payment entries, the other only
settlement entries (`internal/report.ScopedSummarySQL`).

### `summary_layout` — slug → Summary sheet line
Static map from config summary-field slugs (`sales_product_charges`,
`refunded_sales`, …) onto the sections/labels of the sample report. Slugs with
no corresponding sample line (`total_refund_expense_or_sales_amt`,
`bank_account_transfer_round_off`, …) are marked `Unsummarised`.

### `recon_record` — reconciliation output
One row per `record_ref`: `status`
(`reconciled` / `unreconciled_payment` / `unreconciled_settlement`),
`payments_buckets` and `settlements_buckets` (jsonb `{summary_field: amount}`),
`payments_row_ids` / `settlements_row_ids` arrays carrying the trace-back into
the Consolidated sheet, and `payment_txn_status` / `in_summary_scope` (see
Assumption 2) so the Summary sheet's scope filter is itself auditable from the
workbook, not just the database.

---

## Assumptions

1. **"Single table" = single *raw landing* table.** `source_row` holds every
   physical row of both files. Derived tables (`amount_entry`, `summary_total`,
   `recon_record`) are expected by the rest of the brief and are not "ingestion".
2. **Summary sheet scope.** The settlement file contains one settlement, so the
   two Summary columns are compared over that reconciliation scope: settlement
   `12395580393`, **Released** payments (Deferred payments are not yet part of a
   settlement). Each column is still summed **purely from its own source's**
   `amount_entry` rows - the scope filter uses the settlement id and the payment
   status, never the other source's amounts or the match result. The
   Consolidated sheet is **not** scoped: it lists every record, reconciled or
   not. `summary_total` (maintained during ingest) holds the unscoped totals
   for both files and is in the dump.

   This scope is computed once, in `reconcile.Run`, and stored on every
   `recon_record` as `in_summary_scope` (plus `payment_txn_status`, the
   payments file's Released/Deferred flag) - `report.ScopedSummarySQL` then
   just reads it, rather than re-deriving the rule from `amount_entry`/
   `source_row` a second time. Both columns are also written to Consolidated
   Data (**"payment transaction status"**, **"in Summary sheet scope"**), so a
   reader can reproduce any Summary figure directly from the workbook: filter
   Consolidated Data to `in Summary sheet scope = TRUE` and sum a `P:` column -
   verified to reproduce the Summary sheet's Product Charges figure
   (348,815.93) exactly, computed from the `.xlsx` file alone, no database
   access. Before this column existed, that check was impossible from the
   report itself - summing the whole (unfiltered) `P: sales_product_charges`
   column gives 607,360.68, and there was no way to see *why* from the sheet.
3. **Reconciliation key date = settlement/release date.** The payments file's
   `date/time` is the order's posted date; its `Transaction Release Date`
   (converted to UTC, truncated to a day) equals the settlement file's
   `posted-date-time` for the same money. The `date` token in every `record_ref`
   template resolves to that release date. Payments rows with no release date
   (e.g. some adjustments) fall back to the posted `date/time`.
4. **Normalisation for config matching:** upper-case, collapse every run of
   non-alphanumeric characters to a single `_`, trim `_`. Applied identically to
   source values and config keys. (`ItemPrice`→`ITEMPRICE`,
   `FBA Inventory Reimbursement`→`FBA_INVENTORY_REIMBURSEMENT`.)
5. **Config match precedence:** exact `transaction_type` + exact description >
   exact `transaction_type` + wildcard > catch-all (empty) `transaction_type` +
   exact description > catch-all + wildcard. Settlement matching also requires
   `amount_type` to match exactly. Ties broken by `file_line_no`.
6. **Grain:** both sides are aggregated to `(record_ref, summary_field)` sums
   *before* matching, because one payment row can face many settlement lines and
   vice-versa.
7. **Amounts** keep their sign from the source. The positive/negative summary
   bucket is chosen by the sign of the individual amount; a zero amount routes
   to the *positive* bucket by convention (`internal/mapping/mapping.go:SummaryFor`)
   - inconsequential for every total (adding zero is a no-op either way) but
     worth stating since the config schema only names "positive"/"negative".
8. **Zero payment amount slots** produce no `amount_entry` (payments are wide -
   most of a row's ~11 amount columns are 0 and carry no information). Settlement
   lines are kept even when `amount=0` (47 in this dataset) because each is
   already a distinct, real reported line, not a filled-in wide column - this is
   a deliberate asymmetry between the two ingesters, not an oversight, and has
   no numeric effect (summing zero changes nothing).
9. Currency is AUD throughout (payments file states it; settlement header
   confirms it).

---

## Layout

```
cmd/recon/            CLI entrypoint - argument dispatch + text/exit-code I/O only
internal/db/          pgx pool + .sql migration runner
internal/normalize/   the canonical-key function + payment column map
internal/recordref/   record_ref template evaluator
internal/mapping/     the two config tables, wildcard/precedence matching, sign routing
internal/ingest/      file parsing + Postgres persistence (batching, COPY); calls mapping, never re-implements it
internal/reconcile/   amount_entry -> recon_record (the N:M aggregate-then-match step)
internal/report/      excelize workbook writer + the Mismatches() query cmd/recon's `mismatches` reuses
migrations/           schema + summary_layout seed
sql/MAPPING_FIXES.sql the config-level defect fixes
data/                 the four input files
out/                  generated reports + pg dump
```

Package boundaries deliberately mirror the assignment's four numbered parts:
`mapping` is "apply the mapping configs" (part 2) as its own package, independent
of `ingest`'s file-parsing/persistence mechanics (part 1) and `reconcile`'s
aggregation (part 3) - `ingest` calls `mapping.MatchPayment`/`MatchSettlement`
once per amount and never duplicates the precedence logic itself. `report`
(part 4) owns every "what does the Summary sheet say" computation, including
for `recon mismatches` (`report.Mismatches`) - `cmd/recon` only formats and
prints what `report` returns, so the CLI's text summary and the workbook can
never compute the diff two different ways and silently drift apart (they did,
briefly, during development - see PROGRESS.md).
