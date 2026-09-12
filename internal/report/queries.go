package report

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ScopedSummarySQL derives the two Summary columns over the reconciliation
// scope (the settlement(s) present in the settlement file, Released payments
// only - see README Assumption 2). It reads recon_record, not amount_entry:
// `reconcile.Run` is the single place that scope rule is expressed
// (recon_record.in_summary_scope), and every Consolidated Data row carries
// that same flag, so a reader can reproduce this exact total by filtering the
// workbook instead of re-deriving the rule. The settlements side is never
// scope-filtered - every settlement record counts, matching "the Settlements
// column purely from ingested settlement records" even for a hypothetical
// settlement line with no payment counterpart at all.
// This is a shared query so `report` and `mismatches` agree.
const ScopedSummarySQL = `
with pay as (
    select b.key as field, sum(b.value::numeric) as amt
    from recon_record r, jsonb_each_text(r.payments_buckets) as b
    where r.in_summary_scope
    group by 1
),
setl as (
    select b.key as field, sum(b.value::numeric) as amt
    from recon_record r, jsonb_each_text(r.settlements_buckets) as b
    group by 1
)
select 'payments'::text as src, field, amt from pay
union all
select 'settlements'::text as src, field, amt from setl
`

// MismatchLine is one Summary line where the Payments and Settlements columns
// disagree.
type MismatchLine struct {
	Field       string // config summary_field slug, for tracing back to MAPPING_FIXES.sql
	Section     string
	Label       string
	Payments    float64
	Settlements float64
	Diff        float64
}

// Mismatches returns every Summary line with a nonzero Payments-Settlements
// diff, over the same ScopedSummarySQL the xlsx Summary sheet is built from -
// the single source of truth for "does this line tie", used by both
// `recon report` and `recon mismatches` so they can never disagree with each
// other.
func Mismatches(ctx context.Context, pool *pgxpool.Pool) ([]MismatchLine, error) {
	rows, err := pool.Query(ctx, `
		with scoped as (`+ScopedSummarySQL+`),
		agg as (
		  select field,
		         sum(amt) filter (where src = 'payments')    as pay,
		         sum(amt) filter (where src = 'settlements') as setl
		  from scoped
		  group by field
		)
		select a.field, coalesce(l.section,'?'), coalesce(l.line_label, a.field),
		       coalesce(a.pay,0), coalesce(a.setl,0),
		       round((coalesce(a.pay,0) - coalesce(a.setl,0))::numeric, 2) as diff
		from agg a
		left join summary_layout l on l.summary_field = a.field
		where round((coalesce(a.pay,0) - coalesce(a.setl,0))::numeric, 2) <> 0
		order by 2, 3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MismatchLine
	for rows.Next() {
		var m MismatchLine
		if err := rows.Scan(&m.Field, &m.Section, &m.Label, &m.Payments, &m.Settlements, &m.Diff); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func loadSummaryTotals(ctx context.Context, pool *pgxpool.Pool) (map[[2]string]float64, error) {
	rows, err := pool.Query(ctx, ScopedSummarySQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]string]float64{}
	for rows.Next() {
		var sf, field string
		var amt float64
		if err := rows.Scan(&sf, &field, &amt); err != nil {
			return nil, err
		}
		out[[2]string{sf, field}] = amt
	}
	return out, rows.Err()
}

// loadRecon returns the reconciliation rows plus the ordered list of bucket
// names that actually appear (union of both sides), ordered by summary_layout.
func loadRecon(ctx context.Context, pool *pgxpool.Pool) ([]reconRow, []string, error) {
	layoutOrder := map[string]int{}
	{
		lr, err := pool.Query(ctx, `select summary_field, sort_order from summary_layout`)
		if err != nil {
			return nil, nil, err
		}
		for lr.Next() {
			var f string
			var o int
			if err := lr.Scan(&f, &o); err != nil {
				lr.Close()
				return nil, nil, err
			}
			layoutOrder[f] = o
		}
		lr.Close()
	}

	// raw total per (record_ref, source_file), over ALL amount_entry rows -
	// including summary_field='' ones. The bucket columns below only ever show
	// summarised amounts; a record whose entries are all unsummarised (e.g. a
	// bank-disbursement Transfer row) would otherwise render as all-zero with
	// no visible number even though real money moved. This closes that gap.
	//
	// A payments row's `total` column is a control total that repeats the sum
	// of its own other amount columns (that's why every payment_config rule on
	// amount_field='total' is empty-routed for real transaction types) - so it
	// is excluded here whenever the same record_ref also has a non-'total'
	// payment entry, to avoid silently doubling the visible figure. When
	// `total` is the ONLY entry for a record (no components at all), it is
	// kept - it's the only representation of the money.
	rawTotals := map[[2]string]float64{}
	{
		rt, err := pool.Query(ctx, `
			with flagged as (
				select record_ref, source_file, amount_field, amount,
				       bool_or(amount_field <> 'total') over (partition by record_ref, source_file) as has_component
				from amount_entry
			)
			select record_ref, source_file, sum(amount)
			from flagged
			where source_file <> 'payments' or amount_field <> 'total' or not has_component
			group by record_ref, source_file`)
		if err != nil {
			return nil, nil, err
		}
		for rt.Next() {
			var ref, sf string
			var amt float64
			if err := rt.Scan(&ref, &sf, &amt); err != nil {
				rt.Close()
				return nil, nil, err
			}
			rawTotals[[2]string{ref, sf}] = amt
		}
		rt.Close()
		if err := rt.Err(); err != nil {
			return nil, nil, err
		}
	}

	rows, err := pool.Query(ctx, `
		select record_ref, status,
		       coalesce(transaction_type,''), coalesce(description,''), coalesce(sku,''),
		       coalesce(to_char(event_date,'YYYY-MM-DD'),''), coalesce(settlement_id,''),
		       coalesce(payment_txn_status,''), in_summary_scope,
		       payments_buckets, settlements_buckets,
		       payments_row_ids, settlements_row_ids
		from recon_record`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	bucketSeen := map[string]bool{}
	var out []reconRow
	for rows.Next() {
		var rr reconRow
		var pb, sb []byte
		var pids, sids []int64
		if err := rows.Scan(&rr.recordRef, &rr.status, &rr.txnType, &rr.description, &rr.sku,
			&rr.date, &rr.settlementID, &rr.paymentTxnStatus, &rr.inSummaryScope,
			&pb, &sb, &pids, &sids); err != nil {
			return nil, nil, err
		}
		rr.payBuckets = mustJSONMap(pb)
		rr.setBuckets = mustJSONMap(sb)
		for k := range rr.payBuckets {
			bucketSeen[k] = true
		}
		for k := range rr.setBuckets {
			bucketSeen[k] = true
		}
		rr.payRowIDs = joinIDs(pids)
		rr.setRowIDs = joinIDs(sids)
		rr.payRawTotal = rawTotals[[2]string{rr.recordRef, "payments"}]
		rr.setRawTotal = rawTotals[[2]string{rr.recordRef, "settlements"}]
		out = append(out, rr)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	buckets := make([]string, 0, len(bucketSeen))
	for k := range bucketSeen {
		buckets = append(buckets, k)
	}
	sort.SliceStable(buckets, func(i, j int) bool {
		oi, oj := layoutOrder[buckets[i]], layoutOrder[buckets[j]]
		if oi != oj {
			return oi < oj
		}
		return buckets[i] < buckets[j]
	})
	return out, buckets, nil
}

func mustJSONMap(b []byte) map[string]float64 {
	m := map[string]float64{}
	if len(b) == 0 {
		return m
	}
	raw := map[string]json.Number{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return m
	}
	for k, v := range raw {
		f, _ := v.Float64()
		m[k] = f
	}
	return m
}

func joinIDs(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, len(ids))
	for i, v := range ids {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return strings.Join(parts, ",")
}

var _ = time.Time{}
