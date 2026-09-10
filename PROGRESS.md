# Progress log

## 2026-09-10 / 09-11

- Explored the three inputs. Established:
  - Payments CSV is **wide** (1 row = 1 txn, ~11 signed amount columns), BOM + 7
    preamble lines, 23,026 data rows.
  - Settlement TXT is **long** (1 row = 1 amount), tab-separated, 1 settlement
    header + 54,979 line rows, a single settlement-id in the file.
  - Reconciliation key: payment **Transaction Release Date** (→UTC) == settlement
    **posted-date-time**; the `date` token in `record_ref` is the release /
    settlement date, not the order `date/time`. Verified on order
    `249-3364825-5895055` / `BIO-S000004059_AU`.
- Designed the schema (see README). Single ingestion table `source_row` for both
  files; derived `amount_entry`; config tables; `summary_total` maintained during
  the ingest pass; `recon_record` from a full-outer aggregate on `record_ref`.
- Built the Go pipeline: `recon migrate | loadconfigs | ingest | reconcile |
  report | all | fixes | mismatches`. Configs are imported from CSV once, then
  the tables are the source of truth so `MAPPING_FIXES.sql` survives re-ingest.
- Candidate mapping defects noted for verification against the Summary diff
  (not yet confirmed):
  - payment_config L5/L6: `ORDER/low_value_goods` routed to both `sales_shipping`
    and `sales_product_charges` (duplicate key, wrong column - L67/L68 already do
    product_sales/shipping_credits correctly).
  - payment_config L71/L72: `ORDER/sales_tax_collected` routed to both
    `sales_product_charges` and `sales_shipping` (duplicate key).
  - settlement_config L95: `ORDER/PROMOTION/TAXDISCOUNT -> sales_shipping`
    (looks like it should be a promo-rebate expense).

### Pending
- Install PostgreSQL locally (user).
- Run the flow, generate the before-fix report + PG dump.
- Confirm defects from the Summary mismatch, write `sql/MAPPING_FIXES.sql`,
  regenerate until the Summary sheet reconciles.
- Finish README, push to GitHub.
