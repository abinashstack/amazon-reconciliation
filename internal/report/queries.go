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
// scope: the settlement(s) present in the settlement file, Released payments
// only. Each column is summed purely from its own source's amount_entry rows.
// This is a shared query so `report` and `mismatches` agree.
const ScopedSummarySQL = `
with scope_settlements as (
    select distinct raw->>'settlement-id' as sid
    from source_row
    where source_file = 'settlements' and row_kind = 'settlement_header'
),
pay as (
    select ae.summary_field as field, sum(ae.amount) as amt
    from amount_entry ae
    join source_row sr on sr.id = ae.source_row_id
    where ae.source_file = 'payments'
      and sr.raw->>'Transaction status' = 'Released'
      and ae.settlement_id in (select sid from scope_settlements)
      and ae.summary_field <> ''
    group by 1
),
setl as (
    select ae.summary_field as field, sum(ae.amount) as amt
    from amount_entry ae
    where ae.source_file = 'settlements' and ae.summary_field <> ''
    group by 1
)
select 'payments'::text as src, field, amt from pay
union all
select 'settlements'::text as src, field, amt from setl
`

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

	rows, err := pool.Query(ctx, `
		select record_ref, status,
		       coalesce(transaction_type,''), coalesce(description,''), coalesce(sku,''),
		       coalesce(to_char(event_date,'YYYY-MM-DD'),''), coalesce(settlement_id,''),
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
			&rr.date, &rr.settlementID, &pb, &sb, &pids, &sids); err != nil {
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
