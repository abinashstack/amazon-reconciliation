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

### PostgreSQL dump
```bash
pg_dump --no-owner --no-privileges "$RECON_DSN" > out/pg_dump_after_ingest.sql
```

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
`(source_file, summary_field) -> amount, entry_count`. Updated in-memory as each
`amount_entry` is produced during the ingest pass (not a post-hoc `GROUP BY`
over `amount_entry`), then flushed once. The Payments and Settlements columns of
the Summary sheet are therefore derived **independently** — one purely from
payment entries, one purely from settlement entries.

### `summary_layout` — slug → Summary sheet line
Static map from config summary-field slugs (`sales_product_charges`,
`refunded_sales`, …) onto the sections/labels of the sample report. Slugs with
no corresponding sample line (`total_refund_expense_or_sales_amt`,
`bank_account_transfer_round_off`, …) are marked `Unsummarised`.

### `recon_record` — reconciliation output
One row per `record_ref`: `status`
(`reconciled` / `unreconciled_payment` / `unreconciled_settlement`),
`payments_buckets` and `settlements_buckets` (jsonb `{summary_field: amount}`),
and `payments_row_ids` / `settlements_row_ids` arrays carrying the trace-back
into the Consolidated sheet.

---

## Assumptions

1. **"Single table" = single *raw landing* table.** `source_row` holds every
   physical row of both files. Derived tables (`amount_entry`, `summary_total`,
   `recon_record`) are expected by the rest of the brief and are not "ingestion".
2. **Reconciliation key date = settlement/release date.** The payments file's
   `date/time` is the order's posted date; its `Transaction Release Date`
   (converted to UTC, truncated to a day) equals the settlement file's
   `posted-date-time` for the same money. The `date` token in every `record_ref`
   template resolves to that release date. Payments rows with no release date
   (e.g. some adjustments) fall back to the posted `date/time`.
3. **Normalisation for config matching:** upper-case, collapse every run of
   non-alphanumeric characters to a single `_`, trim `_`. Applied identically to
   source values and config keys. (`ItemPrice`→`ITEMPRICE`,
   `FBA Inventory Reimbursement`→`FBA_INVENTORY_REIMBURSEMENT`.)
4. **Config match precedence:** exact `transaction_type` + exact description >
   exact `transaction_type` + wildcard > catch-all (empty) `transaction_type` +
   exact description > catch-all + wildcard. Settlement matching also requires
   `amount_type` to match exactly. Ties broken by `file_line_no`.
5. **Grain:** both sides are aggregated to `(record_ref, summary_field)` sums
   *before* matching, because one payment row can face many settlement lines and
   vice-versa.
6. **Amounts** keep their sign from the source. The positive/negative summary
   bucket is chosen by the sign of the individual amount.
7. **Zero payment amount slots** produce no `amount_entry`.
8. The settlement file contains a **single settlement**; most payment rows
   therefore reconcile to nothing on the settlement side and are reported as
   `unreconciled_payment`. This is expected, not an error.
9. Currency is AUD throughout (payments file states it; settlement header
   confirms it).

---

## Layout

```
cmd/recon/            CLI entrypoint
internal/db/          pgx pool + .sql migration runner
internal/normalize/   the canonical-key function + payment column map
internal/recordref/   record_ref template evaluator
internal/ingest/      config import, payments + settlements ingesters, summary
internal/reconcile/   amount_entry -> recon_record
internal/report/      excelize workbook writer
migrations/           schema + summary_layout seed
sql/MAPPING_FIXES.sql the config-level defect fixes
data/                 the four input files
out/                  generated reports + pg dump
```
