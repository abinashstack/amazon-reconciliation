// Command recon runs the reconciliation pipeline.
//
//	recon migrate                    - (re)create the schema
//	recon ingest                     - load configs + both data files
//	recon reconcile                  - build recon_record from amount_entry
//	recon report <out.xlsx>          - write the workbook
//	recon all <out.xlsx>             - migrate + ingest + reconcile + report
//	recon fixes <MAPPING_FIXES.sql>  - apply a SQL fix file, then re-ingest + reconcile
//	recon mismatches                 - print Summary-sheet lines where Payments != Settlements
//
// Connection string: $RECON_DSN, default
// postgres://postgres:postgres@localhost:5432/recon?sslmode=disable
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abinashstack/amazon-reconciliation/internal/db"
	"github.com/abinashstack/amazon-reconciliation/internal/ingest"
	"github.com/abinashstack/amazon-reconciliation/internal/reconcile"
	"github.com/abinashstack/amazon-reconciliation/internal/report"
)

const (
	paymentsFile    = "data/amazon_payments_data.csv"
	settlementsFile = "data/amazon_settlements_data.txt"
	paymentCfgFile  = "data/amazon_payment_configs_au_old.csv"
	settlementCfg   = "data/amazon_settlement_configs_au.csv"
	migrationsDir   = "migrations"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx)
	must(err)
	defer pool.Close()

	switch os.Args[1] {
	case "migrate":
		must(db.Migrate(ctx, pool, migrationsDir))
	case "loadconfigs":
		must(ingest.ImportConfigCSVs(ctx, pool, paymentCfgFile, settlementCfg))
		fmt.Println("config CSVs imported into payment_config / settlement_config")
	case "ingest":
		runIngest(ctx, pool)
	case "reconcile":
		runReconcile(ctx, pool)
	case "report":
		out := arg(2, "out/report.xlsx")
		must(report.Generate(ctx, pool, out))
		fmt.Println("wrote", out)
	case "all":
		out := arg(2, "out/report.xlsx")
		must(db.Migrate(ctx, pool, migrationsDir))
		must(ingest.ImportConfigCSVs(ctx, pool, paymentCfgFile, settlementCfg))
		runIngest(ctx, pool)
		runReconcile(ctx, pool)
		must(report.Generate(ctx, pool, out))
		fmt.Println("wrote", out)
	case "fixes":
		applyFixes(ctx, pool, arg(2, "sql/MAPPING_FIXES.sql"))
		runIngest(ctx, pool)
		runReconcile(ctx, pool)
	case "mismatches":
		printMismatches(ctx, pool)
	default:
		usage()
	}
}

func runIngest(ctx context.Context, pool *pgxpool.Pool) {
	eng, err := ingest.NewEngine(ctx, pool)
	must(err)
	must(eng.Run(ctx, paymentsFile, settlementsFile))
	fmt.Printf("ingested: %d amount_entry rows\n", eng.EntryCount())
}

func runReconcile(ctx context.Context, pool *pgxpool.Pool) {
	must(reconcile.Run(ctx, pool))
	s, err := reconcile.Summarise(ctx, pool)
	must(err)
	fmt.Printf("reconciled=%d unreconciled_payment=%d unreconciled_settlement=%d\n",
		s.Reconciled, s.UnreconciledPayment, s.UnreconciledSettlement)
}

func applyFixes(ctx context.Context, pool *pgxpool.Pool, path string) {
	sqlBytes, err := os.ReadFile(path)
	must(err)
	_, err = pool.Exec(ctx, string(sqlBytes))
	must(err)
	fmt.Println("applied", path)
}

// printMismatches lists Summary lines where the independently-derived Payments
// and Settlements columns disagree - the driver for MAPPING_FIXES.sql.
func printMismatches(ctx context.Context, pool *pgxpool.Pool) {
	rows, err := pool.Query(ctx, `
		with scoped as (`+report.ScopedSummarySQL+`),
		agg as (
		  select field,
		         sum(amt) filter (where src = 'payments')    as pay,
		         sum(amt) filter (where src = 'settlements')  as setl
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
	must(err)
	defer rows.Close()
	n := 0
	fmt.Printf("%-14s %-34s %14s %14s %12s\n", "section", "line", "payments", "settlements", "diff")
	for rows.Next() {
		var field, section, label string
		var pay, setl, diff float64
		must(rows.Scan(&field, &section, &label, &pay, &setl, &diff))
		fmt.Printf("%-14s %-34s %14.2f %14.2f %12.2f  [%s]\n", section, label, pay, setl, diff, field)
		n++
	}
	if n == 0 {
		fmt.Println("no mismatches - Summary sheet reconciles")
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: recon migrate|loadconfigs|ingest|reconcile|report <xlsx>|all <xlsx>|fixes <sql>|mismatches")
	os.Exit(2)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func arg(i int, def string) string {
	if len(os.Args) > i {
		return os.Args[i]
	}
	return def
}
