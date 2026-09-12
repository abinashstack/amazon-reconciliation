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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abinashstack/amazon-reconciliation/internal/db"
	"github.com/abinashstack/amazon-reconciliation/internal/ingest"
	"github.com/abinashstack/amazon-reconciliation/internal/mapping"
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
		must(mapping.ImportConfigCSVs(ctx, pool, paymentCfgFile, settlementCfg))
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
		must(mapping.ImportConfigCSVs(ctx, pool, paymentCfgFile, settlementCfg))
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
	srcInsert, entCopy := eng.Timings()
	fmt.Printf("ingested: %d amount_entry rows (source_row insert: %s, amount_entry COPY: %s)\n",
		eng.EntryCount(), srcInsert.Round(time.Millisecond), entCopy.Round(time.Millisecond))
	if n := eng.WarningCount(); n > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: %d row(s) had a value that could not be parsed (treated as zero/blank, not skipped):\n", n)
		for _, line := range eng.WarningLines() {
			fmt.Fprintln(os.Stderr, line)
		}
	}
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

// printMismatches formats report.Mismatches() as text - the query lives in
// the report package (the same one the xlsx Summary sheet is built from) so
// this command can't drift from what the workbook actually shows.
func printMismatches(ctx context.Context, pool *pgxpool.Pool) {
	lines, err := report.Mismatches(ctx, pool)
	must(err)
	fmt.Printf("%-14s %-34s %14s %14s %12s\n", "section", "line", "payments", "settlements", "diff")
	for _, l := range lines {
		fmt.Printf("%-14s %-34s %14.2f %14.2f %12.2f  [%s]\n", l.Section, l.Label, l.Payments, l.Settlements, l.Diff, l.Field)
	}
	if len(lines) == 0 {
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
