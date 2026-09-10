-- ============================================================================
-- Amazon payments / settlement reconciliation - schema
--
-- Design notes (see README for the full rationale):
--   * source_row      : THE single ingestion table. Every physical line from
--                       BOTH input files lands here as raw text + parsed JSON,
--                       with source_file + file_line_no for full trace-back.
--   * *_config         : the two mapping-config CSVs, stored verbatim + with
--                       pre-normalised match keys.
--   * amount_entry     : derived. Wide payment rows are exploded to one row per
--                       amount column; settlement lines map 1:1. Each entry is
--                       FK-linked to its source_row and carries the resolved
--                       record_ref / matched config / summary bucket.
--   * summary_total    : per (source_file, summary_field) running total,
--                       maintained DURING ingestion (no post-hoc pass).
--   * summary_layout   : static - maps config summary-field slugs onto the
--                       sections/lines of the sample report's Summary sheet.
--   * recon_record     : one row per record_ref after reconciliation, with the
--                       per-bucket payment & settlement sums and the source row
--                       id arrays behind every number.
-- ============================================================================

drop table if exists recon_record       cascade;
drop table if exists summary_total       cascade;
drop table if exists summary_layout      cascade;
drop table if exists amount_entry        cascade;
drop table if exists payment_config      cascade;
drop table if exists settlement_config   cascade;
drop table if exists source_row          cascade;
drop table if exists ingest_batch        cascade;

-- ---------------------------------------------------------------------------
-- ingestion bookkeeping
-- ---------------------------------------------------------------------------
create table ingest_batch (
    id           uuid primary key,
    started_at   timestamptz not null default now(),
    finished_at  timestamptz,
    note         text
);

-- ---------------------------------------------------------------------------
-- THE single ingestion table (both files)
-- ---------------------------------------------------------------------------
create table source_row (
    id            bigint generated always as identity primary key,
    batch_id      uuid        not null references ingest_batch(id),
    source_file   text        not null check (source_file in ('payments','settlements')),
    file_line_no  int         not null,                 -- 1-based physical line in the file
    row_kind      text        not null,                 -- payment_txn | settlement_header | settlement_line
    raw_line      text        not null,                 -- exact original line, untouched
    raw           jsonb       not null,                 -- {original_header: original_string_value}
    ingested_at   timestamptz not null default now()
);
create index source_row_file_kind_idx on source_row (source_file, row_kind);
create index source_row_batch_idx     on source_row (batch_id);

-- ---------------------------------------------------------------------------
-- mapping configs
-- ---------------------------------------------------------------------------
create table payment_config (
    id                      bigint generated always as identity primary key,
    file_line_no            int  not null,
    transaction_type_raw    text not null,
    transaction_type_norm   text not null,              -- '' => catch-all / fallback
    description_raw         text not null,
    description_norm        text not null,
    is_desc_wildcard        boolean not null,           -- description == 'any'
    amount_field            text not null,              -- normalised payment amount column
    record_ref_template     text not null,
    summary_pos             text not null default '',   -- to_summary_field_when_positive_amount
    summary_neg             text not null default '',   -- to_summary_field_when_negative_amount
    raw                     jsonb not null
);

create table settlement_config (
    id                      bigint generated always as identity primary key,
    file_line_no            int  not null,
    transaction_type_raw    text not null,
    transaction_type_norm   text not null,
    amount_type_raw         text not null,
    amount_type_norm        text not null,
    amount_description_raw  text not null,
    amount_description_norm text not null,
    is_desc_wildcard        boolean not null,           -- amount_description == 'any'
    record_ref_template     text not null,
    summary_pos             text not null default '',
    summary_neg             text not null default '',
    raw                     jsonb not null
);

-- ---------------------------------------------------------------------------
-- normalized amount-level entries (derived from source_row)
-- ---------------------------------------------------------------------------
create table amount_entry (
    id                      bigint generated always as identity primary key,
    source_row_id           bigint not null references source_row(id),
    source_file             text   not null,
    seq                     int    not null,            -- amount slot within the source row

    transaction_type_raw    text,
    transaction_type_norm   text,
    match_desc_raw          text,                       -- payments: description; settlements: amount-description
    match_desc_norm         text,
    amount_field            text,                       -- payments: normalised column; settlements: amount_type_norm
    amount_type_norm        text,

    order_ref               text,
    sku                     text,
    settlement_id           text,
    event_date              date,                       -- posted date of the underlying transaction
    release_date            date,                       -- settlement/release date (drives record_ref 'date')
    shipment_id             text,
    merchant_order_id       text,
    description_literal      text,                       -- for templates that embed 'description'

    amount                  numeric(18,4) not null,
    currency                text,

    record_ref              text,                       -- synthetic NOKEY:* when no key could be built
    matched_config_id       bigint,
    config_kind             text,                       -- payment | settlement
    summary_field           text not null default '',   -- '' => ingested but not summarised
    match_note              text not null               -- exact | wildcard | catchall | no_match
);
create index amount_entry_recordref_idx on amount_entry (record_ref);
create index amount_entry_summary_idx   on amount_entry (source_file, summary_field);
create index amount_entry_srcrow_idx    on amount_entry (source_row_id);

-- ---------------------------------------------------------------------------
-- summary - maintained incrementally during ingest
-- ---------------------------------------------------------------------------
create table summary_total (
    source_file   text not null,
    summary_field text not null,
    amount        numeric(18,4) not null default 0,
    entry_count   bigint        not null default 0,
    primary key (source_file, summary_field)
);

create table summary_layout (
    summary_field text primary key,
    section       text not null,        -- Sales | Refunds | Expenses | Paid To Amazon
    line_label    text not null,        -- label as printed on the Summary sheet
    sort_order    int  not null
);

-- ---------------------------------------------------------------------------
-- reconciliation output - one row per record_ref
-- ---------------------------------------------------------------------------
create table recon_record (
    record_ref          text primary key,
    status              text not null,   -- reconciled | unreconciled_payment | unreconciled_settlement
    payments_buckets    jsonb  not null default '{}'::jsonb,   -- {summary_field: amount}
    settlements_buckets jsonb  not null default '{}'::jsonb,
    payments_row_ids    bigint[] not null default '{}',
    settlements_row_ids bigint[] not null default '{}',
    -- denormalised audit attributes (first non-null wins during build)
    transaction_type    text,
    description          text,
    sku                 text,
    event_date          date,
    settlement_id       text
);
create index recon_record_status_idx on recon_record (status);
