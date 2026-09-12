// Package reconcile aggregates amount_entry rows to the record_ref grain and
// classifies each key as reconciled / unreconciled.
//
// Granularity: the two files disagree on grain (one payment row can face many
// settlement lines and vice-versa), so BOTH sides are aggregated to
// (record_ref, summary_field) sums BEFORE matching. A record_ref seen on both
// sides is reconciled; the per-bucket difference is payments - settlements.
package reconcile

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

const reconSQL = `
truncate recon_record;

with scope_settlements as (
    select distinct raw->>'settlement-id' as sid
    from source_row
    where source_file = 'settlements' and row_kind = 'settlement_header'
),
sides as (
    select record_ref, source_file,
           count(*)                       as n,
           array_agg(distinct source_row_id) as row_ids
    from amount_entry
    group by record_ref, source_file
),
grp as (
    select record_ref, source_file, summary_field, sum(amount) as amt
    from amount_entry
    group by record_ref, source_file, summary_field
),
buckets as (
    select record_ref, source_file,
           coalesce(
             jsonb_object_agg(summary_field, amt) filter (where summary_field <> ''),
             '{}'::jsonb) as b
    from grp
    group by record_ref, source_file
),
-- the payments-side transaction status, per record_ref. A record's payment
-- amount_entries all come from ONE physical payments row (see internal/ingest),
-- so this is unambiguous whenever a payment side exists at all.
pay_status as (
    select distinct on (ae.record_ref)
           ae.record_ref, sr.raw->>'Transaction status' as st
    from amount_entry ae
    join source_row sr on sr.id = ae.source_row_id
    where ae.source_file = 'payments'
    order by ae.record_ref, ae.id
),
attrs as (
    select distinct on (record_ref)
           record_ref,
           transaction_type_raw as tt,
           description_literal  as descr,
           sku, event_date, settlement_id
    from amount_entry
    order by record_ref, (source_file = 'payments') desc, id
)
insert into recon_record
  (record_ref, status, payments_buckets, settlements_buckets,
   payments_row_ids, settlements_row_ids,
   transaction_type, description, sku, event_date, settlement_id,
   payment_txn_status, in_summary_scope)
select
  k.record_ref,
  case when ps.n is not null and ss.n is not null then 'reconciled'
       when ps.n is not null                     then 'unreconciled_payment'
       else                                           'unreconciled_settlement' end,
  coalesce(pb.b, '{}'::jsonb),
  coalesce(sb.b, '{}'::jsonb),
  coalesce(ps.row_ids, '{}'::bigint[]),
  coalesce(ss.row_ids, '{}'::bigint[]),
  a.tt, a.descr, a.sku, a.event_date, a.settlement_id,
  pst.st,
  coalesce(pst.st = 'Released' and a.settlement_id in (select sid from scope_settlements), false)
from (select distinct record_ref from amount_entry) k
left join sides      ps  on ps.record_ref = k.record_ref and ps.source_file = 'payments'
left join sides      ss  on ss.record_ref = k.record_ref and ss.source_file = 'settlements'
left join buckets    pb  on pb.record_ref = k.record_ref and pb.source_file = 'payments'
left join buckets    sb  on sb.record_ref = k.record_ref and sb.source_file = 'settlements'
left join attrs      a   on a.record_ref  = k.record_ref
left join pay_status pst on pst.record_ref = k.record_ref;
`

// Run rebuilds recon_record from amount_entry.
func Run(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, reconSQL)
	return err
}

// Stats is a quick summary of a reconciliation pass.
type Stats struct {
	Reconciled             int64
	UnreconciledPayment    int64
	UnreconciledSettlement int64
}

// Summarise counts recon_record by status.
func Summarise(ctx context.Context, pool *pgxpool.Pool) (Stats, error) {
	var s Stats
	rows, err := pool.Query(ctx, `select status, count(*) from recon_record group by status`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return s, err
		}
		switch st {
		case "reconciled":
			s.Reconciled = n
		case "unreconciled_payment":
			s.UnreconciledPayment = n
		case "unreconciled_settlement":
			s.UnreconciledSettlement = n
		}
	}
	return s, rows.Err()
}
