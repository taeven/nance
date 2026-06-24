package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/taeven/nance/mongo-loadtest/internal/config"
	"github.com/taeven/nance/mongo-loadtest/internal/runner"
	"github.com/taeven/nance/mongo-loadtest/internal/stats"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	r, err := runner.New(ctx, cfg)
	if err != nil {
		log.Fatalf("runner init: %v", err)
	}
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = r.Close(dctx)
	}()

	started := time.Now()
	report, err := r.Run(ctx)
	if err != nil && ctx.Err() == nil {
		log.Fatalf("run failed: %v", err)
	}
	if ctx.Err() != nil {
		log.Printf("interrupted; writing partial stats")
	}

	jsonPath, mdPath, err := stats.WriteReport(cfg.OutputDir, report)
	if err != nil {
		log.Fatalf("write report: %v", err)
	}

	elapsed := time.Since(started).Round(time.Millisecond)
	fmt.Println()
	fmt.Println("========== LOAD TEST COMPLETE ==========")
	fmt.Printf("Run ID:        %s\n", report.RunID)
	fmt.Printf("Elapsed:       %s\n", elapsed)
	fmt.Printf("Mode:          %s\n", report.Mode)
	fmt.Printf("Verdict:       %s\n", report.Verdict)
	fmt.Println("----------------------------------------")
	fmt.Printf("Peak read:     %.1f ops/s  |  %.1f docs/s\n",
		report.Throughput.BestReadOpsPerSec, report.Throughput.BestReadDocsPerSec)
	fmt.Printf("Peak write:    %.1f ops/s  |  %.1f docs/s\n",
		report.Throughput.BestWriteOpsPerSec, report.Throughput.BestWriteDocsPerSec)
	fmt.Printf("Total reads:   %d ops / %d docs\n", report.Totals.ReadOps, report.Totals.ReadDocs)
	fmt.Printf("Total writes:  %d ops / %d docs\n", report.Totals.WriteOps, report.Totals.WriteDocs)
	if report.BreakingPoint.Detected {
		fmt.Println("----------------------------------------")
		fmt.Println("BREAKING POINT DETECTED")
		fmt.Printf("  %s\n", report.BreakingPoint.Summary)
		fmt.Printf("  At ~%s read ops / ~%s write ops cumulative\n",
			human(report.BreakingPoint.ReadOpsTotal), human(report.BreakingPoint.WriteOpsTotal))
	} else {
		fmt.Println("----------------------------------------")
		fmt.Println("No breaking point detected (within thresholds).")
	}
	fmt.Println("----------------------------------------")
	fmt.Printf("JSON report:   %s\n", jsonPath)
	fmt.Printf("Markdown:      %s\n", mdPath)
	fmt.Println("========================================")
}

func human(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}
