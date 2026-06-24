package config

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config holds all CLI / environment configuration for a load test run.
type Config struct {
	// MongoURI is the full MongoDB connection string.
	MongoURI string
	// Database is the target database name.
	Database string
	// Collection is the target collection name.
	Collection string

	// Mode selects which workload to run: "read", "write", "mixed", or "ramp".
	Mode string

	// Duration is how long the constant-load phases run.
	Duration time.Duration
	// Warmup is an optional quiet period before measurement starts.
	Warmup time.Duration

	// ReadConcurrency is the number of concurrent read workers.
	ReadConcurrency int
	// WriteConcurrency is the number of concurrent write workers.
	WriteConcurrency int

	// ReadBatch is how many documents each read worker fetches per operation (find limit).
	ReadBatch int
	// WriteBatch is how many documents each write worker inserts per operation.
	WriteBatch int

	// SeedCount is how many documents to insert before read/mixed tests if the collection is empty.
	SeedCount int
	// DocSizeBytes is the approximate payload size of each written document.
	DocSizeBytes int

	// Ramp settings (used when Mode == "ramp")
	RampStartWorkers int
	RampMaxWorkers   int
	RampStepWorkers  int
	RampStepDuration time.Duration

	// BreakingPoint thresholds — if sustained error rate or latency exceeds these,
	// the runner records a breaking point and may stop the ramp.
	MaxErrorRate    float64       // fraction 0..1 (e.g. 0.05 = 5%)
	MaxP99Latency   time.Duration // e.g. 2s
	MinSuccessRate  float64       // fraction 0..1; below this counts as broken

	// OutputDir is where the final stats JSON/Markdown report is written.
	OutputDir string
	// RunID overrides the auto-generated run identifier.
	RunID string

	// DropCollection drops the target collection at the start (dangerous; opt-in).
	DropCollection bool
	// KeepData leaves inserted data in place after the run (default: clean up loadtest-owned docs only when false).
	KeepData bool
}

// Load parses flags and environment variables into a Config.
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("mongo-loadtest", flag.ContinueOnError)

	cfg := &Config{}

	fs.StringVar(&cfg.MongoURI, "uri", envOr("MONGO_URI", ""), "MongoDB connection string (or MONGO_URI)")
	fs.StringVar(&cfg.Database, "db", envOr("MONGO_DB", "loadtest"), "Database name (or MONGO_DB)")
	fs.StringVar(&cfg.Collection, "collection", envOr("MONGO_COLLECTION", "loadtest_docs"), "Collection name (or MONGO_COLLECTION)")
	fs.StringVar(&cfg.Mode, "mode", "mixed", "Workload mode: read | write | mixed | ramp")
	fs.DurationVar(&cfg.Duration, "duration", 60*time.Second, "Duration of constant-load phases")
	fs.DurationVar(&cfg.Warmup, "warmup", 5*time.Second, "Warmup period before measurement")
	fs.IntVar(&cfg.ReadConcurrency, "read-concurrency", 100, "Concurrent read workers")
	fs.IntVar(&cfg.WriteConcurrency, "write-concurrency", 50, "Concurrent write workers")
	fs.IntVar(&cfg.ReadBatch, "read-batch", 10, "Documents fetched per read operation")
	fs.IntVar(&cfg.WriteBatch, "write-batch", 10, "Documents inserted per write operation")
	fs.IntVar(&cfg.SeedCount, "seed", 10000, "Seed documents if collection is empty (read/mixed modes)")
	fs.IntVar(&cfg.DocSizeBytes, "doc-size", 1024, "Approximate document payload size in bytes")
	fs.IntVar(&cfg.RampStartWorkers, "ramp-start", 10, "Ramp mode: starting workers per type")
	fs.IntVar(&cfg.RampMaxWorkers, "ramp-max", 2000, "Ramp mode: maximum workers per type")
	fs.IntVar(&cfg.RampStepWorkers, "ramp-step", 50, "Ramp mode: workers added each step")
	fs.DurationVar(&cfg.RampStepDuration, "ramp-step-duration", 15*time.Second, "Ramp mode: duration of each step")
	fs.Float64Var(&cfg.MaxErrorRate, "max-error-rate", 0.05, "Breaking point: max sustained error fraction (0-1)")
	fs.DurationVar(&cfg.MaxP99Latency, "max-p99", 2*time.Second, "Breaking point: max p99 latency")
	fs.Float64Var(&cfg.MinSuccessRate, "min-success-rate", 0.90, "Breaking point: min success fraction (0-1)")
	fs.StringVar(&cfg.OutputDir, "output", "results", "Directory for stats report output")
	fs.StringVar(&cfg.RunID, "run-id", "", "Optional run identifier (default: timestamp)")
	fs.BoolVar(&cfg.DropCollection, "drop", false, "Drop the target collection before starting")
	fs.BoolVar(&cfg.KeepData, "keep-data", true, "Keep inserted documents after the run")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if cfg.MongoURI == "" {
		return nil, fmt.Errorf("MongoDB URI is required: pass -uri or set MONGO_URI")
	}
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch cfg.Mode {
	case "read", "write", "mixed", "ramp":
	default:
		return nil, fmt.Errorf("invalid -mode %q (want read|write|mixed|ramp)", cfg.Mode)
	}
	if cfg.ReadConcurrency < 0 || cfg.WriteConcurrency < 0 {
		return nil, fmt.Errorf("concurrency must be >= 0")
	}
	if cfg.DocSizeBytes < 16 {
		cfg.DocSizeBytes = 16
	}
	if cfg.RunID == "" {
		cfg.RunID = time.Now().UTC().Format("20060102T150405Z")
	}
	if cfg.RampStepWorkers < 1 {
		cfg.RampStepWorkers = 1
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
