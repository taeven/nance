package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	"github.com/taeven/nance/mongo-loadtest/internal/config"
	"github.com/taeven/nance/mongo-loadtest/internal/stats"
)

// Runner executes MongoDB load tests and collects statistics.
type Runner struct {
	cfg    *config.Config
	client *mongo.Client
	coll   *mongo.Collection
	stats  *stats.Collector
	runTag string // tags documents written by this run
}

// New connects to MongoDB and prepares the runner.
func New(ctx context.Context, cfg *config.Config) (*Runner, error) {
	opts := options.Client().
		ApplyURI(cfg.MongoURI).
		SetMaxPoolSize(uint64(max(cfg.ReadConcurrency+cfg.WriteConcurrency+50, 100))).
		SetMinPoolSize(10).
		SetRetryWrites(true)

	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping primary: %w", err)
	}

	coll := client.Database(cfg.Database).Collection(cfg.Collection)
	tag := randomTag()

	return &Runner{
		cfg:    cfg,
		client: client,
		coll:   coll,
		stats:  stats.NewCollector(cfg.MaxErrorRate, cfg.MaxP99Latency, cfg.MinSuccessRate),
		runTag: tag,
	}, nil
}

// Close disconnects the client.
func (r *Runner) Close(ctx context.Context) error {
	if r.client == nil {
		return nil
	}
	return r.client.Disconnect(ctx)
}

// Run executes the configured workload and returns the final report.
func (r *Runner) Run(ctx context.Context) (stats.Report, error) {
	log.Printf("connected; run_id=%s mode=%s db=%s collection=%s tag=%s",
		r.cfg.RunID, r.cfg.Mode, r.cfg.Database, r.cfg.Collection, r.runTag)

	if r.cfg.DropCollection {
		log.Printf("dropping collection %s.%s", r.cfg.Database, r.cfg.Collection)
		_ = r.coll.Drop(ctx)
	}

	// Ensure a basic index for point reads by _id (default) and optional tag field.
	_, _ = r.coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "lt_tag", Value: 1}},
	})

	switch r.cfg.Mode {
	case "write":
		if err := r.runConstant(ctx, 0, r.cfg.WriteConcurrency, stats.PhaseWrite, "write"); err != nil {
			return stats.Report{}, err
		}
	case "read":
		if err := r.ensureSeed(ctx); err != nil {
			return stats.Report{}, err
		}
		if err := r.runConstant(ctx, r.cfg.ReadConcurrency, 0, stats.PhaseRead, "read"); err != nil {
			return stats.Report{}, err
		}
	case "mixed":
		if err := r.ensureSeed(ctx); err != nil {
			return stats.Report{}, err
		}
		if err := r.runConstant(ctx, r.cfg.ReadConcurrency, r.cfg.WriteConcurrency, stats.PhaseMixed, "mixed"); err != nil {
			return stats.Report{}, err
		}
	case "ramp":
		if err := r.ensureSeed(ctx); err != nil {
			return stats.Report{}, err
		}
		if err := r.runRamp(ctx); err != nil {
			return stats.Report{}, err
		}
	default:
		return stats.Report{}, fmt.Errorf("unknown mode %q", r.cfg.Mode)
	}

	cfgSummary := map[string]any{
		"mode":              r.cfg.Mode,
		"duration":          r.cfg.Duration.String(),
		"warmup":            r.cfg.Warmup.String(),
		"read_concurrency":  r.cfg.ReadConcurrency,
		"write_concurrency": r.cfg.WriteConcurrency,
		"read_batch":        r.cfg.ReadBatch,
		"write_batch":       r.cfg.WriteBatch,
		"seed_count":        r.cfg.SeedCount,
		"doc_size_bytes":    r.cfg.DocSizeBytes,
		"ramp_start":        r.cfg.RampStartWorkers,
		"ramp_max":          r.cfg.RampMaxWorkers,
		"ramp_step":         r.cfg.RampStepWorkers,
		"ramp_step_duration": r.cfg.RampStepDuration.String(),
		"max_error_rate":    r.cfg.MaxErrorRate,
		"max_p99":           r.cfg.MaxP99Latency.String(),
		"min_success_rate":  r.cfg.MinSuccessRate,
	}

	report := r.stats.BuildReport(
		r.cfg.RunID,
		stats.RedactURI(r.cfg.MongoURI),
		r.cfg.Database,
		r.cfg.Collection,
		r.cfg.Mode,
		cfgSummary,
	)
	return report, nil
}

func (r *Runner) ensureSeed(ctx context.Context) error {
	countCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	n, err := r.coll.CountDocuments(countCtx, bson.M{})
	if err != nil {
		return fmt.Errorf("count documents: %w", err)
	}
	if n >= int64(r.cfg.SeedCount) {
		log.Printf("collection has %d docs (>= seed %d); skipping seed", n, r.cfg.SeedCount)
		return nil
	}
	need := int64(r.cfg.SeedCount) - n
	log.Printf("seeding %d documents (current=%d target=%d)", need, n, r.cfg.SeedCount)

	r.stats.BeginPhase("seed", stats.PhaseSeed, 0, min(int(need), 32))
	defer r.stats.EndPhase()

	workers := min(32, int(need))
	if workers < 1 {
		workers = 1
	}
	var remaining atomic.Int64
	remaining.Store(need)
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := makePayload(r.cfg.DocSizeBytes)
			batch := make([]any, 0, r.cfg.WriteBatch)
			for {
				left := remaining.Add(-1)
				if left < 0 {
					break
				}
				batch = append(batch, bson.M{
					"lt_tag":  r.runTag,
					"lt_seed": true,
					"ts":      time.Now().UTC(),
					"payload": payload,
					"seq":     need - left,
				})
				if len(batch) >= r.cfg.WriteBatch {
					if err := r.insertBatch(ctx, batch); err != nil {
						select {
						case errCh <- err:
						default:
						}
						return
					}
					batch = batch[:0]
				}
			}
			if len(batch) > 0 {
				if err := r.insertBatch(ctx, batch); err != nil {
					select {
					case errCh <- err:
					default:
					}
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return fmt.Errorf("seed: %w", err)
	default:
	}
	log.Printf("seed complete")
	return nil
}

func (r *Runner) insertBatch(ctx context.Context, docs []any) error {
	start := time.Now()
	_, err := r.coll.InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	r.stats.Record(stats.Sample{
		Op:        stats.OpWrite,
		Latency:   time.Since(start),
		Docs:      int64(len(docs)),
		Err:       err != nil,
		Timestamp: time.Now(),
	})
	return err
}

func (r *Runner) runConstant(ctx context.Context, readW, writeW int, kind stats.PhaseKind, name string) error {
	if r.cfg.Warmup > 0 && (readW > 0 || writeW > 0) {
		log.Printf("warmup %s (read_workers=%d write_workers=%d)", r.cfg.Warmup, readW, writeW)
		if err := r.runPhase(ctx, "warmup-"+name, stats.PhaseWarmup, readW, writeW, r.cfg.Warmup, false); err != nil {
			return err
		}
	}
	log.Printf("phase %s duration=%s read_workers=%d write_workers=%d", name, r.cfg.Duration, readW, writeW)
	return r.runPhase(ctx, name, kind, readW, writeW, r.cfg.Duration, true)
}

func (r *Runner) runRamp(ctx context.Context) error {
	start := r.cfg.RampStartWorkers
	maxW := r.cfg.RampMaxWorkers
	step := r.cfg.RampStepWorkers
	stepDur := r.cfg.RampStepDuration

	if start < 1 {
		start = 1
	}
	for workers := start; workers <= maxW; workers += step {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Scale reads and writes together for mixed pressure; write workers at half if only one pool.
		readW := workers
		writeW := max(1, workers/2)
		name := fmt.Sprintf("ramp-r%dw%d", readW, writeW)
		log.Printf("ramp step %s duration=%s", name, stepDur)
		if err := r.runPhase(ctx, name, stats.PhaseRamp, readW, writeW, stepDur, true); err != nil {
			return err
		}
		if r.stats.Breaking().Detected {
			log.Printf("breaking point detected; stopping ramp")
			break
		}
	}
	return nil
}

// runPhase runs concurrent readers/writers for duration and records a phase snapshot.
// measure=false still records into the collector phase (warmup) but workers behave the same.
func (r *Runner) runPhase(ctx context.Context, name string, kind stats.PhaseKind, readW, writeW int, duration time.Duration, _ bool) error {
	phaseCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	r.stats.BeginPhase(name, kind, readW, writeW)

	var wg sync.WaitGroup
	// Shared cursor-ish state: sequential-ish reads by random skip/limit and _id existence.
	var seq atomic.Uint64

	for i := 0; i < readW; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			r.readWorker(phaseCtx, &seq)
		}(i)
	}
	for i := 0; i < writeW; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			r.writeWorker(phaseCtx)
		}(i)
	}

	// Live progress ticker
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				// lightweight progress via phase counters is inside collector; log phase name only
				log.Printf("... running phase %q", name)
			}
		}
	}()

	<-phaseCtx.Done()
	wg.Wait()
	close(done)

	snap := r.stats.EndPhase()
	log.Printf("phase %q done: read_ops=%d (%.0f/s) write_ops=%d (%.0f/s) err_rate=%.2f%% broken=%v",
		snap.Name, snap.ReadOps, snap.ReadOpsPerSec, snap.WriteOps, snap.WriteOpsPerSec, snap.ErrorRate*100, snap.Broken)
	return nil
}

func (r *Runner) readWorker(ctx context.Context, seq *atomic.Uint64) {
	limit := int64(r.cfg.ReadBatch)
	if limit < 1 {
		limit = 1
	}
	for {
		if ctx.Err() != nil {
			return
		}
		// Alternate between find-with-limit (range scan style) and findOne by pseudo key.
		n := seq.Add(1)
		start := time.Now()
		var docs int64
		var err error

		if n%3 == 0 {
			// findOne on a field that may miss — still counts as a read op against the server.
			sr := r.coll.FindOne(ctx, bson.M{"seq": int64(n % 1_000_000_007)})
			err = sr.Err()
			if err == nil {
				docs = 1
			} else if err == mongo.ErrNoDocuments {
				err = nil
				docs = 0
			}
		} else {
			opts := options.Find().SetLimit(limit).SetProjection(bson.M{"payload": 0})
			// Prefer documents from this run or any; empty filter for max throughput / collection scan pressure.
			filter := bson.M{}
			if n%2 == 0 {
				filter = bson.M{"lt_tag": bson.M{"$exists": true}}
			}
			cur, findErr := r.coll.Find(ctx, filter, opts)
			err = findErr
			if err == nil {
				for cur.Next(ctx) {
					docs++
				}
				if curErr := cur.Err(); curErr != nil {
					err = curErr
				}
				_ = cur.Close(ctx)
			}
		}

		r.stats.Record(stats.Sample{
			Op:        stats.OpRead,
			Latency:   time.Since(start),
			Docs:      docs,
			Err:       err != nil && ctx.Err() == nil,
			Timestamp: time.Now(),
		})
	}
}

func (r *Runner) writeWorker(ctx context.Context) {
	payload := makePayload(r.cfg.DocSizeBytes)
	batchSize := r.cfg.WriteBatch
	if batchSize < 1 {
		batchSize = 1
	}
	for {
		if ctx.Err() != nil {
			return
		}
		docs := make([]any, batchSize)
		now := time.Now().UTC()
		for i := 0; i < batchSize; i++ {
			docs[i] = bson.M{
				"lt_tag":  r.runTag,
				"lt_seed": false,
				"ts":      now,
				"payload": payload,
				"w":       i,
			}
		}
		start := time.Now()
		_, err := r.coll.InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
		r.stats.Record(stats.Sample{
			Op:        stats.OpWrite,
			Latency:   time.Since(start),
			Docs:      int64(batchSize),
			Err:       err != nil && ctx.Err() == nil,
			Timestamp: time.Now(),
		})
	}
}

func makePayload(size int) string {
	if size < 1 {
		size = 1
	}
	// Deterministic-ish printable payload without allocating huge random buffers every time.
	const block = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	buf := make([]byte, size)
	for i := 0; i < size; i++ {
		buf[i] = block[i%len(block)]
	}
	return string(buf)
}

func randomTag() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
