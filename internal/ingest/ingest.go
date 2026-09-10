package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abinashstack/amazon-reconciliation/internal/recordref"
)

// Engine runs a full ingestion pass over both files.
type Engine struct {
	pool *pgxpool.Pool
	cfg  *Configs

	batchID uuid.UUID

	// summary accumulator - updated as each amount_entry is produced,
	// i.e. DURING ingestion, then flushed once at the end.
	summary map[[2]string]*summ

	srcBuf []srcRow // pending source_row inserts
	entBuf []entRow // pending amount_entry COPY rows
	nEnt   int64
}

type summ struct {
	amount float64
	count  int64
}

type srcRow struct {
	file    string
	lineNo  int
	kind    string
	rawLine string
	raw     map[string]string
	// filled after insert
	id int64
	// entries to emit once id is known
	pending []pendingEntry
}

type pendingEntry struct {
	seq             int
	txnTypeRaw      string
	txnTypeNorm     string
	matchDescRaw    string
	matchDescNorm   string
	amountField     string
	amountTypeNorm  string
	orderRef        string
	sku             string
	settlementID    string
	eventDate       time.Time
	releaseDate     time.Time
	shipmentID      string
	merchantOrderID string
	descLiteral     string
	amount          float64
	currency        string
	recordRef       string
	matchedConfigID *int64
	configKind      string
	summaryField    string
	matchNote       string
}

type entRow struct {
	p        pendingEntry
	srcRowID int64
	file     string
}

// NewEngine loads the config matcher from the DB tables and prepares a batch.
// Import the CSVs first with ImportConfigCSVs (once) so the tables exist.
func NewEngine(ctx context.Context, pool *pgxpool.Pool) (*Engine, error) {
	cfg, err := LoadConfigsFromDB(ctx, pool)
	if err != nil {
		return nil, err
	}
	if len(cfg.Payment) == 0 || len(cfg.Settlement) == 0 {
		return nil, fmt.Errorf("config tables are empty - run `recon loadconfigs` first")
	}
	bid := uuid.New()
	if _, err := pool.Exec(ctx, `insert into ingest_batch (id, note) values ($1,$2)`,
		bid, "full ingest"); err != nil {
		return nil, err
	}
	return &Engine{
		pool:    pool,
		cfg:     cfg,
		batchID: bid,
		summary: map[[2]string]*summ{},
	}, nil
}

// Run truncates prior derived data and ingests both files.
func (e *Engine) Run(ctx context.Context, paymentsFile, settlementsFile string) error {
	for _, t := range []string{"amount_entry", "source_row", "summary_total", "recon_record"} {
		if _, err := e.pool.Exec(ctx, "truncate "+t+" restart identity cascade"); err != nil {
			return err
		}
	}
	if err := e.ingestPayments(ctx, paymentsFile); err != nil {
		return fmt.Errorf("payments: %w", err)
	}
	if err := e.ingestSettlements(ctx, settlementsFile); err != nil {
		return fmt.Errorf("settlements: %w", err)
	}
	if err := e.flushSource(ctx); err != nil {
		return err
	}
	if err := e.flushEntries(ctx); err != nil {
		return err
	}
	if err := e.flushSummary(ctx); err != nil {
		return err
	}
	_, err := e.pool.Exec(ctx, `update ingest_batch set finished_at = now() where id = $1`, e.batchID)
	return err
}

// addSource queues a source row + its pending entries, flushing when the
// buffer is large enough to amortise the round trip.
func (e *Engine) addSource(ctx context.Context, s srcRow) error {
	e.srcBuf = append(e.srcBuf, s)
	if len(e.srcBuf) >= 500 {
		return e.flushSource(ctx)
	}
	return nil
}

// accumulate adds into the running summary (during ingestion).
func (e *Engine) accumulate(file, field string, amount float64) {
	if field == "" {
		return
	}
	k := [2]string{file, field}
	s := e.summary[k]
	if s == nil {
		s = &summ{}
		e.summary[k] = s
	}
	s.amount += amount
	s.count++
}

func (e *Engine) flushSource(ctx context.Context) error {
	if len(e.srcBuf) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for i := range e.srcBuf {
		s := &e.srcBuf[i]
		raw, _ := json.Marshal(s.raw)
		b.Queue(`insert into source_row (batch_id, source_file, file_line_no, row_kind, raw_line, raw)
		         values ($1,$2,$3,$4,$5,$6) returning id`,
			e.batchID, s.file, s.lineNo, s.kind, s.rawLine, raw)
	}
	br := e.pool.SendBatch(ctx, b)
	for i := range e.srcBuf {
		var id int64
		if err := br.QueryRow().Scan(&id); err != nil {
			br.Close()
			return err
		}
		e.srcBuf[i].id = id
	}
	if err := br.Close(); err != nil {
		return err
	}
	// promote pending entries now that ids are known
	for i := range e.srcBuf {
		s := &e.srcBuf[i]
		for _, pe := range s.pending {
			if pe.recordRef == "" {
				pe.recordRef = recordref.Synthetic(s.file, s.id, pe.seq)
				if pe.matchNote == "" {
					pe.matchNote = "no_match"
				}
			}
			e.entBuf = append(e.entBuf, entRow{p: pe, srcRowID: s.id, file: s.file})
		}
	}
	e.srcBuf = e.srcBuf[:0]
	if len(e.entBuf) >= 20000 {
		return e.flushEntries(ctx)
	}
	return nil
}

func (e *Engine) flushEntries(ctx context.Context) error {
	if len(e.entBuf) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(e.entBuf))
	for _, er := range e.entBuf {
		p := er.p
		rows = append(rows, []any{
			er.srcRowID, er.file, p.seq,
			nz(p.txnTypeRaw), nz(p.txnTypeNorm), nz(p.matchDescRaw), nz(p.matchDescNorm),
			nz(p.amountField), nz(p.amountTypeNorm),
			nz(p.orderRef), nz(p.sku), nz(p.settlementID),
			nzDate(p.eventDate), nzDate(p.releaseDate),
			nz(p.shipmentID), nz(p.merchantOrderID), nz(p.descLiteral),
			p.amount, nz(p.currency),
			p.recordRef, p.matchedConfigID, nz(p.configKind), p.summaryField, p.matchNote,
		})
	}
	_, err := e.pool.CopyFrom(ctx,
		pgx.Identifier{"amount_entry"},
		[]string{
			"source_row_id", "source_file", "seq",
			"transaction_type_raw", "transaction_type_norm", "match_desc_raw", "match_desc_norm",
			"amount_field", "amount_type_norm",
			"order_ref", "sku", "settlement_id",
			"event_date", "release_date",
			"shipment_id", "merchant_order_id", "description_literal",
			"amount", "currency",
			"record_ref", "matched_config_id", "config_kind", "summary_field", "match_note",
		},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return err
	}
	e.nEnt += int64(len(e.entBuf))
	e.entBuf = e.entBuf[:0]
	return nil
}

func (e *Engine) flushSummary(ctx context.Context) error {
	b := &pgx.Batch{}
	for k, v := range e.summary {
		b.Queue(`insert into summary_total (source_file, summary_field, amount, entry_count)
		         values ($1,$2,$3,$4)`, k[0], k[1], v.amount, v.count)
	}
	br := e.pool.SendBatch(ctx, b)
	defer br.Close()
	for range e.summary {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// EntryCount reports how many amount_entry rows were written.
func (e *Engine) EntryCount() int64 { return e.nEnt }

func nz(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nzDate(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
