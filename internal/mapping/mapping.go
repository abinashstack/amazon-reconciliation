// Package mapping is "step 2" of the pipeline: turning a normalised raw value
// into a reconcilable, summarisable record by applying the two mapping
// config files. It owns the config tables (payment_config / settlement_config),
// the wildcard/precedence match rules, and the sign-based summary-bucket
// choice - deliberately separate from internal/ingest (file parsing and
// Postgres persistence mechanics) and internal/reconcile (aggregating already-
// mapped amount_entry rows). ingest calls into this package once per amount to
// get back a matched rule + a match_note; it never re-implements matching
// itself.
package mapping

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abinashstack/amazon-reconciliation/internal/normalize"
)

// PaymentRule is one row of amazon_payment_configs_au_old.csv.
type PaymentRule struct {
	ID            int64
	LineNo        int
	TxnTypeNorm   string
	DescNorm      string
	DescWildcard  bool
	AmountField   string
	RecordRefTmpl string
	SummaryPos    string
	SummaryNeg    string
}

// SettlementRule is one row of amazon_settlement_configs_au.csv.
type SettlementRule struct {
	ID             int64
	LineNo         int
	TxnTypeNorm    string
	AmountTypeNorm string
	AmountDescNorm string
	DescWildcard   bool
	RecordRefTmpl  string
	SummaryPos     string
	SummaryNeg     string
}

// Configs holds both rule sets in memory for matching during ingest.
type Configs struct {
	Payment    []PaymentRule
	Settlement []SettlementRule
}

// SummaryFor picks the positive/negative bucket for a signed amount. Zero
// routes to pos by convention (adding zero never changes a total either way).
func SummaryFor(amount float64, pos, neg string) string {
	if amount < 0 {
		return neg
	}
	return pos
}

// MatchPayment returns the winning rule for a payment amount entry, or nil.
// Precedence: exact txn_type + exact desc > exact txn_type + wildcard >
// catch-all txn_type + exact desc > catch-all txn_type + wildcard; ties by LineNo.
func (c *Configs) MatchPayment(txnType, desc, amountField string) (*PaymentRule, string) {
	type cand struct {
		r     *PaymentRule
		score int
	}
	var cands []cand
	for i := range c.Payment {
		r := &c.Payment[i]
		if r.AmountField != amountField {
			continue
		}
		ttExact := r.TxnTypeNorm != "" && r.TxnTypeNorm == txnType
		ttCatch := r.TxnTypeNorm == ""
		if !ttExact && !ttCatch {
			continue
		}
		descExact := !r.DescWildcard && r.DescNorm == desc
		if !r.DescWildcard && !descExact {
			continue
		}
		score := 0
		if ttExact {
			score += 2
		}
		if descExact {
			score++
		}
		cands = append(cands, cand{r, score})
	}
	if len(cands) == 0 {
		return nil, "no_match"
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].r.LineNo < cands[j].r.LineNo
	})
	best := cands[0]
	note := "exact"
	switch {
	case best.score == 3:
		note = "exact"
	case best.score == 2:
		note = "wildcard"
	case best.score == 1:
		note = "catchall"
	case best.score == 0:
		note = "catchall"
	}
	return best.r, note
}

// MatchSettlement mirrors MatchPayment for settlement line entries.
func (c *Configs) MatchSettlement(txnType, amountType, amountDesc string) (*SettlementRule, string) {
	type cand struct {
		r     *SettlementRule
		score int
	}
	var cands []cand
	for i := range c.Settlement {
		r := &c.Settlement[i]
		if r.AmountTypeNorm != "" && r.AmountTypeNorm != amountType {
			continue
		}
		ttExact := r.TxnTypeNorm != "" && r.TxnTypeNorm == txnType
		ttCatch := r.TxnTypeNorm == ""
		if !ttExact && !ttCatch {
			continue
		}
		descExact := !r.DescWildcard && r.AmountDescNorm == amountDesc
		if !r.DescWildcard && !descExact {
			continue
		}
		score := 0
		if ttExact {
			score += 2
		}
		if descExact {
			score++
		}
		if r.AmountTypeNorm != "" {
			score++
		}
		cands = append(cands, cand{r, score})
	}
	if len(cands) == 0 {
		return nil, "no_match"
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].r.LineNo < cands[j].r.LineNo
	})
	best := cands[0]
	note := "exact"
	if best.r.DescWildcard {
		note = "wildcard"
	}
	if best.r.TxnTypeNorm == "" {
		note = "catchall"
	}
	return best.r, note
}

// ImportConfigCSVs loads both config CSVs into their tables, verbatim in the
// *_raw columns plus pre-normalised match keys. This is the ONLY path that
// reads the CSV files; after this, the tables are the source of truth so that
// MAPPING_FIXES.sql edits survive a re-ingest.
func ImportConfigCSVs(ctx context.Context, pool *pgxpool.Pool, paymentCSV, settlementCSV string) error {
	if _, err := pool.Exec(ctx, `truncate payment_config restart identity`); err != nil {
		return err
	}
	prows, err := readCSV(paymentCSV)
	if err != nil {
		return err
	}
	for i, rec := range prows.rows {
		lineNo := i + 2 // header is line 1
		m := prows.asMap(rec)
		descRaw := m["description"]
		raw, _ := json.Marshal(m)
		if _, err := pool.Exec(ctx, `
			insert into payment_config
			 (file_line_no, transaction_type_raw, transaction_type_norm,
			  description_raw, description_norm, is_desc_wildcard,
			  amount_field, record_ref_template, summary_pos, summary_neg, raw)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			lineNo, m["transaction_type"], normalize.Key(m["transaction_type"]),
			descRaw, normalize.Key(descRaw), strings.EqualFold(strings.TrimSpace(descRaw), "any"),
			strings.TrimSpace(m["amount_field"]), strings.TrimSpace(m["record_ref"]),
			m["to_summary_field_when_positive_amount"], m["to_summary_field_when_negative_amount"],
			raw,
		); err != nil {
			return fmt.Errorf("payment_config line %d: %w", lineNo, err)
		}
	}

	if _, err := pool.Exec(ctx, `truncate settlement_config restart identity`); err != nil {
		return err
	}
	srows, err := readCSV(settlementCSV)
	if err != nil {
		return err
	}
	for i, rec := range srows.rows {
		lineNo := i + 2
		m := srows.asMap(rec)
		descRaw := m["amount_description"]
		raw, _ := json.Marshal(m)
		if _, err := pool.Exec(ctx, `
			insert into settlement_config
			 (file_line_no, transaction_type_raw, transaction_type_norm,
			  amount_type_raw, amount_type_norm,
			  amount_description_raw, amount_description_norm, is_desc_wildcard,
			  record_ref_template, summary_pos, summary_neg, raw)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			lineNo, m["transaction_type"], normalize.Key(m["transaction_type"]),
			m["amount_type"], normalize.Key(m["amount_type"]),
			descRaw, normalize.Key(descRaw), strings.EqualFold(strings.TrimSpace(descRaw), "any"),
			strings.TrimSpace(m["record_ref"]),
			m["to_summary_field_when_positive_amount"], m["to_summary_field_when_negative_amount"],
			raw,
		); err != nil {
			return fmt.Errorf("settlement_config line %d: %w", lineNo, err)
		}
	}
	return nil
}

// LoadConfigsFromDB builds the in-memory matcher from the config tables (which
// may already carry MAPPING_FIXES.sql edits). Rows with an empty summary
// bucket, empty template, etc. are kept as-is.
func LoadConfigsFromDB(ctx context.Context, pool *pgxpool.Pool) (*Configs, error) {
	cfg := &Configs{}

	prows, err := pool.Query(ctx, `
		select id, file_line_no, transaction_type_norm, description_norm, is_desc_wildcard,
		       amount_field, record_ref_template, summary_pos, summary_neg
		from payment_config order by file_line_no, id`)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var r PaymentRule
		if err := prows.Scan(&r.ID, &r.LineNo, &r.TxnTypeNorm, &r.DescNorm, &r.DescWildcard,
			&r.AmountField, &r.RecordRefTmpl, &r.SummaryPos, &r.SummaryNeg); err != nil {
			prows.Close()
			return nil, err
		}
		cfg.Payment = append(cfg.Payment, r)
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return nil, err
	}

	srows, err := pool.Query(ctx, `
		select id, file_line_no, transaction_type_norm, amount_type_norm, amount_description_norm,
		       is_desc_wildcard, record_ref_template, summary_pos, summary_neg
		from settlement_config order by file_line_no, id`)
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		var r SettlementRule
		if err := srows.Scan(&r.ID, &r.LineNo, &r.TxnTypeNorm, &r.AmountTypeNorm, &r.AmountDescNorm,
			&r.DescWildcard, &r.RecordRefTmpl, &r.SummaryPos, &r.SummaryNeg); err != nil {
			srows.Close()
			return nil, err
		}
		cfg.Settlement = append(cfg.Settlement, r)
	}
	srows.Close()
	return cfg, srows.Err()
}

// --- tiny CSV helper ---

type csvFile struct {
	header []string
	rows   [][]string
}

func (c *csvFile) asMap(rec []string) map[string]string {
	m := make(map[string]string, len(c.header))
	for i, h := range c.header {
		if i < len(rec) {
			m[strings.TrimSpace(h)] = rec[i]
		}
	}
	return m
}

func readCSV(path string) (*csvFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	all, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("%s: empty", path)
	}
	return &csvFile{header: all[0], rows: all[1:]}, nil
}
