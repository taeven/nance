package stats

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// PhaseKind labels a measurement window.
type PhaseKind string

const (
	PhaseWarmup  PhaseKind = "warmup"
	PhaseRead    PhaseKind = "read"
	PhaseWrite   PhaseKind = "write"
	PhaseMixed   PhaseKind = "mixed"
	PhaseRamp    PhaseKind = "ramp"
	PhaseSeed    PhaseKind = "seed"
)

// OpKind is the type of MongoDB operation measured.
type OpKind string

const (
	OpRead  OpKind = "read"
	OpWrite OpKind = "write"
)

// Sample is a single operation observation.
type Sample struct {
	Op        OpKind
	Latency   time.Duration
	Docs      int64 // documents affected/returned
	Err       bool
	Timestamp time.Time
}

// PhaseSnapshot is an immutable summary of one measurement phase.
type PhaseSnapshot struct {
	Name            string        `json:"name"`
	Kind            PhaseKind     `json:"kind"`
	StartedAt       time.Time     `json:"started_at"`
	EndedAt         time.Time     `json:"ended_at"`
	Duration        time.Duration `json:"duration_ns"`
	DurationHuman   string        `json:"duration"`
	ReadWorkers     int           `json:"read_workers,omitempty"`
	WriteWorkers    int           `json:"write_workers,omitempty"`
	ReadOps         int64         `json:"read_ops"`
	WriteOps        int64         `json:"write_ops"`
	ReadDocs        int64         `json:"read_docs"`
	WriteDocs       int64         `json:"write_docs"`
	ReadErrors      int64         `json:"read_errors"`
	WriteErrors     int64         `json:"write_errors"`
	ReadOpsPerSec   float64       `json:"read_ops_per_sec"`
	WriteOpsPerSec  float64       `json:"write_ops_per_sec"`
	ReadDocsPerSec  float64       `json:"read_docs_per_sec"`
	WriteDocsPerSec float64       `json:"write_docs_per_sec"`
	ReadLatency     LatencyStats  `json:"read_latency"`
	WriteLatency    LatencyStats  `json:"write_latency"`
	ErrorRate       float64       `json:"error_rate"`
	SuccessRate     float64       `json:"success_rate"`
	Broken          bool          `json:"broken,omitempty"`
	BreakReason     string        `json:"break_reason,omitempty"`
}

// LatencyStats summarizes latency distributions (nanoseconds in JSON via Duration fields).
type LatencyStats struct {
	Count    int64         `json:"count"`
	Min      time.Duration `json:"min_ns"`
	Max      time.Duration `json:"max_ns"`
	Mean     time.Duration `json:"mean_ns"`
	P50      time.Duration `json:"p50_ns"`
	P95      time.Duration `json:"p95_ns"`
	P99      time.Duration `json:"p99_ns"`
	MinHuman string        `json:"min"`
	MaxHuman string        `json:"max"`
	MeanHuman string       `json:"mean"`
	P50Human string        `json:"p50"`
	P95Human string        `json:"p95"`
	P99Human string        `json:"p99"`
}

// BreakingPoint records the load level at which the database failed thresholds.
type BreakingPoint struct {
	Detected        bool      `json:"detected"`
	PhaseName       string    `json:"phase_name,omitempty"`
	AtTime          time.Time `json:"at_time,omitempty"`
	ReadWorkers     int       `json:"read_workers,omitempty"`
	WriteWorkers    int       `json:"write_workers,omitempty"`
	ReadOpsTotal    int64     `json:"read_ops_total_at_break,omitempty"`
	WriteOpsTotal   int64     `json:"write_ops_total_at_break,omitempty"`
	ReadOpsPerSec   float64   `json:"read_ops_per_sec_at_break,omitempty"`
	WriteOpsPerSec  float64   `json:"write_ops_per_sec_at_break,omitempty"`
	ReadDocsPerSec  float64   `json:"read_docs_per_sec_at_break,omitempty"`
	WriteDocsPerSec float64   `json:"write_docs_per_sec_at_break,omitempty"`
	ErrorRate       float64   `json:"error_rate_at_break,omitempty"`
	P99Read         string    `json:"p99_read_at_break,omitempty"`
	P99Write        string    `json:"p99_write_at_break,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	Summary         string    `json:"summary,omitempty"`
}

// Report is the full load-test statistics artifact written at the end of a run.
type Report struct {
	RunID           string          `json:"run_id"`
	GeneratedAt     time.Time       `json:"generated_at"`
	MongoURIRedacted string         `json:"mongo_uri_redacted"`
	Database        string          `json:"database"`
	Collection      string          `json:"collection"`
	Mode            string          `json:"mode"`
	ConfigSummary   map[string]any  `json:"config_summary"`
	Phases          []PhaseSnapshot `json:"phases"`
	Totals          Totals          `json:"totals"`
	Throughput      ThroughputPeak  `json:"throughput_peak"`
	BreakingPoint   BreakingPoint   `json:"breaking_point"`
	Verdict         string          `json:"verdict"`
}

// Totals aggregates across all non-warmup measurement phases.
type Totals struct {
	Duration        time.Duration `json:"duration_ns"`
	DurationHuman   string        `json:"duration"`
	ReadOps         int64         `json:"read_ops"`
	WriteOps        int64         `json:"write_ops"`
	ReadDocs        int64         `json:"read_docs"`
	WriteDocs       int64         `json:"write_docs"`
	ReadErrors      int64         `json:"read_errors"`
	WriteErrors     int64         `json:"write_errors"`
	ReadOpsPerSec   float64       `json:"read_ops_per_sec_avg"`
	WriteOpsPerSec  float64       `json:"write_ops_per_sec_avg"`
	ReadDocsPerSec  float64       `json:"read_docs_per_sec_avg"`
	WriteDocsPerSec float64       `json:"write_docs_per_sec_avg"`
}

// ThroughputPeak captures the best observed sustained rates.
type ThroughputPeak struct {
	BestReadOpsPerSec    float64 `json:"best_read_ops_per_sec"`
	BestWriteOpsPerSec   float64 `json:"best_write_ops_per_sec"`
	BestReadDocsPerSec   float64 `json:"best_read_docs_per_sec"`
	BestWriteDocsPerSec  float64 `json:"best_write_docs_per_sec"`
	BestReadPhase        string  `json:"best_read_phase,omitempty"`
	BestWritePhase       string  `json:"best_write_phase,omitempty"`
}

// Collector accumulates samples for the current phase and builds snapshots.
type Collector struct {
	mu sync.Mutex

	phaseName  string
	phaseKind  PhaseKind
	startedAt  time.Time
	readW      int
	writeW     int

	readLats  []time.Duration
	writeLats []time.Duration

	readOps    atomic.Int64
	writeOps   atomic.Int64
	readDocs   atomic.Int64
	writeDocs  atomic.Int64
	readErrs   atomic.Int64
	writeErrs  atomic.Int64

	// global totals across completed phases (for breaking point context)
	globalReadOps  atomic.Int64
	globalWriteOps atomic.Int64

	phases []PhaseSnapshot

	// thresholds for breaking detection
	maxErrorRate   float64
	maxP99         time.Duration
	minSuccessRate float64

	breaking BreakingPoint
}

// NewCollector creates a stats collector with breaking-point thresholds.
func NewCollector(maxErrorRate float64, maxP99 time.Duration, minSuccessRate float64) *Collector {
	return &Collector{
		maxErrorRate:   maxErrorRate,
		maxP99:         maxP99,
		minSuccessRate: minSuccessRate,
	}
}

// BeginPhase starts recording a new measurement window.
func (c *Collector) BeginPhase(name string, kind PhaseKind, readWorkers, writeWorkers int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.phaseName = name
	c.phaseKind = kind
	c.startedAt = time.Now()
	c.readW = readWorkers
	c.writeW = writeWorkers
	c.readLats = c.readLats[:0]
	c.writeLats = c.writeLats[:0]
	c.readOps.Store(0)
	c.writeOps.Store(0)
	c.readDocs.Store(0)
	c.writeDocs.Store(0)
	c.readErrs.Store(0)
	c.writeErrs.Store(0)
}

// Record adds one operation sample.
func (c *Collector) Record(s Sample) {
	switch s.Op {
	case OpRead:
		c.readOps.Add(1)
		c.readDocs.Add(s.Docs)
		if s.Err {
			c.readErrs.Add(1)
		} else {
			c.mu.Lock()
			c.readLats = append(c.readLats, s.Latency)
			c.mu.Unlock()
		}
	case OpWrite:
		c.writeOps.Add(1)
		c.writeDocs.Add(s.Docs)
		if s.Err {
			c.writeErrs.Add(1)
		} else {
			c.mu.Lock()
			c.writeLats = append(c.writeLats, s.Latency)
			c.mu.Unlock()
		}
	}
}

// EndPhase finalizes the current phase and returns its snapshot.
// If thresholds are breached (and kind is not warmup/seed), a breaking point is recorded once.
func (c *Collector) EndPhase() PhaseSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	ended := time.Now()
	dur := ended.Sub(c.startedAt)
	if dur <= 0 {
		dur = time.Nanosecond
	}
	secs := dur.Seconds()

	ro := c.readOps.Load()
	wo := c.writeOps.Load()
	rd := c.readDocs.Load()
	wd := c.writeDocs.Load()
	re := c.readErrs.Load()
	we := c.writeErrs.Load()

	totalOps := ro + wo
	totalErrs := re + we
	var errRate, successRate float64
	if totalOps > 0 {
		errRate = float64(totalErrs) / float64(totalOps)
		successRate = 1 - errRate
	} else {
		successRate = 1
	}

	readLat := computeLatency(c.readLats)
	writeLat := computeLatency(c.writeLats)

	snap := PhaseSnapshot{
		Name:            c.phaseName,
		Kind:            c.phaseKind,
		StartedAt:       c.startedAt,
		EndedAt:         ended,
		Duration:        dur,
		DurationHuman:   dur.Round(time.Millisecond).String(),
		ReadWorkers:     c.readW,
		WriteWorkers:    c.writeW,
		ReadOps:         ro,
		WriteOps:        wo,
		ReadDocs:        rd,
		WriteDocs:       wd,
		ReadErrors:      re,
		WriteErrors:     we,
		ReadOpsPerSec:   float64(ro) / secs,
		WriteOpsPerSec:  float64(wo) / secs,
		ReadDocsPerSec:  float64(rd) / secs,
		WriteDocsPerSec: float64(wd) / secs,
		ReadLatency:     readLat,
		WriteLatency:    writeLat,
		ErrorRate:       errRate,
		SuccessRate:     successRate,
	}

	// Track global ops for breaking-point totals (exclude warmup/seed from global).
	if c.phaseKind != PhaseWarmup && c.phaseKind != PhaseSeed {
		c.globalReadOps.Add(ro)
		c.globalWriteOps.Add(wo)
	}

	// Detect breaking point on measurement phases only.
	if c.phaseKind != PhaseWarmup && c.phaseKind != PhaseSeed && !c.breaking.Detected {
		reason := c.checkBreak(snap)
		if reason != "" {
			snap.Broken = true
			snap.BreakReason = reason
			c.breaking = BreakingPoint{
				Detected:        true,
				PhaseName:       snap.Name,
				AtTime:          ended,
				ReadWorkers:     snap.ReadWorkers,
				WriteWorkers:    snap.WriteWorkers,
				ReadOpsTotal:    c.globalReadOps.Load(),
				WriteOpsTotal:   c.globalWriteOps.Load(),
				ReadOpsPerSec:   snap.ReadOpsPerSec,
				WriteOpsPerSec:  snap.WriteOpsPerSec,
				ReadDocsPerSec:  snap.ReadDocsPerSec,
				WriteDocsPerSec: snap.WriteDocsPerSec,
				ErrorRate:       snap.ErrorRate,
				P99Read:         snap.ReadLatency.P99Human,
				P99Write:        snap.WriteLatency.P99Human,
				Reason:          reason,
				Summary: fmt.Sprintf(
					"Breaking point at phase %q: ~%.0f read ops/s (workers=%d), ~%.0f write ops/s (workers=%d); cumulative ops at break: %s reads / %s writes. Reason: %s",
					snap.Name,
					snap.ReadOpsPerSec, snap.ReadWorkers,
					snap.WriteOpsPerSec, snap.WriteWorkers,
					formatCount(c.globalReadOps.Load()),
					formatCount(c.globalWriteOps.Load()),
					reason,
				),
			}
		}
	}

	c.phases = append(c.phases, snap)
	return snap
}

func (c *Collector) checkBreak(snap PhaseSnapshot) string {
	var reasons []string
	if snap.ErrorRate > c.maxErrorRate && (snap.ReadOps+snap.WriteOps) >= 10 {
		reasons = append(reasons, fmt.Sprintf("error_rate %.2f%% > max %.2f%%", snap.ErrorRate*100, c.maxErrorRate*100))
	}
	if snap.SuccessRate < c.minSuccessRate && (snap.ReadOps+snap.WriteOps) >= 10 {
		reasons = append(reasons, fmt.Sprintf("success_rate %.2f%% < min %.2f%%", snap.SuccessRate*100, c.minSuccessRate*100))
	}
	if c.maxP99 > 0 {
		if snap.ReadLatency.Count > 0 && snap.ReadLatency.P99 > c.maxP99 {
			reasons = append(reasons, fmt.Sprintf("read p99 %s > max %s", snap.ReadLatency.P99, c.maxP99))
		}
		if snap.WriteLatency.Count > 0 && snap.WriteLatency.P99 > c.maxP99 {
			reasons = append(reasons, fmt.Sprintf("write p99 %s > max %s", snap.WriteLatency.P99, c.maxP99))
		}
	}
	return strings.Join(reasons, "; ")
}

// Breaking returns the recorded breaking point if any.
func (c *Collector) Breaking() BreakingPoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.breaking
}

// Phases returns completed phase snapshots.
func (c *Collector) Phases() []PhaseSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]PhaseSnapshot, len(c.phases))
	copy(out, c.phases)
	return out
}

// BuildReport assembles the final report structure.
func (c *Collector) BuildReport(runID, uriRedacted, database, collection, mode string, configSummary map[string]any) Report {
	phases := c.Phases()
	bp := c.Breaking()

	var totals Totals
	var peak ThroughputPeak
	var measureStart, measureEnd time.Time

	for _, p := range phases {
		if p.Kind == PhaseWarmup || p.Kind == PhaseSeed {
			continue
		}
		if measureStart.IsZero() || p.StartedAt.Before(measureStart) {
			measureStart = p.StartedAt
		}
		if p.EndedAt.After(measureEnd) {
			measureEnd = p.EndedAt
		}
		totals.ReadOps += p.ReadOps
		totals.WriteOps += p.WriteOps
		totals.ReadDocs += p.ReadDocs
		totals.WriteDocs += p.WriteDocs
		totals.ReadErrors += p.ReadErrors
		totals.WriteErrors += p.WriteErrors

		if p.ReadOpsPerSec > peak.BestReadOpsPerSec {
			peak.BestReadOpsPerSec = p.ReadOpsPerSec
			peak.BestReadPhase = p.Name
		}
		if p.WriteOpsPerSec > peak.BestWriteOpsPerSec {
			peak.BestWriteOpsPerSec = p.WriteOpsPerSec
			peak.BestWritePhase = p.Name
		}
		if p.ReadDocsPerSec > peak.BestReadDocsPerSec {
			peak.BestReadDocsPerSec = p.ReadDocsPerSec
		}
		if p.WriteDocsPerSec > peak.BestWriteDocsPerSec {
			peak.BestWriteDocsPerSec = p.WriteDocsPerSec
		}
	}

	if !measureStart.IsZero() && measureEnd.After(measureStart) {
		totals.Duration = measureEnd.Sub(measureStart)
		totals.DurationHuman = totals.Duration.Round(time.Millisecond).String()
		secs := totals.Duration.Seconds()
		if secs > 0 {
			totals.ReadOpsPerSec = float64(totals.ReadOps) / secs
			totals.WriteOpsPerSec = float64(totals.WriteOps) / secs
			totals.ReadDocsPerSec = float64(totals.ReadDocs) / secs
			totals.WriteDocsPerSec = float64(totals.WriteDocs) / secs
		}
	}

	verdict := "completed_without_detected_breaking_point"
	if bp.Detected {
		verdict = "breaking_point_detected"
	}

	return Report{
		RunID:            runID,
		GeneratedAt:      time.Now().UTC(),
		MongoURIRedacted: uriRedacted,
		Database:         database,
		Collection:       collection,
		Mode:             mode,
		ConfigSummary:    configSummary,
		Phases:           phases,
		Totals:           totals,
		Throughput:       peak,
		BreakingPoint:    bp,
		Verdict:          verdict,
	}
}

// WriteReport writes JSON and Markdown reports under outputDir and returns the paths.
func WriteReport(outputDir string, report Report) (jsonPath, mdPath string, err error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", "", err
	}
	base := fmt.Sprintf("loadtest-%s", report.RunID)
	jsonPath = filepath.Join(outputDir, base+".json")
	mdPath = filepath.Join(outputDir, base+".md")

	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(mdPath, []byte(RenderMarkdown(report)), 0o644); err != nil {
		return "", "", err
	}
	return jsonPath, mdPath, nil
}

// RenderMarkdown produces a human-readable stats summary.
func RenderMarkdown(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# MongoDB Load Test Report\n\n")
	fmt.Fprintf(&b, "| Field | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Run ID | `%s` |\n", r.RunID)
	fmt.Fprintf(&b, "| Generated | %s |\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "| URI | `%s` |\n", r.MongoURIRedacted)
	fmt.Fprintf(&b, "| Database | `%s` |\n", r.Database)
	fmt.Fprintf(&b, "| Collection | `%s` |\n", r.Collection)
	fmt.Fprintf(&b, "| Mode | `%s` |\n", r.Mode)
	fmt.Fprintf(&b, "| Verdict | **%s** |\n\n", r.Verdict)

	fmt.Fprintf(&b, "## Peak Throughput\n\n")
	fmt.Fprintf(&b, "| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Best read ops/s | **%.1f** (`%s`) |\n", r.Throughput.BestReadOpsPerSec, r.Throughput.BestReadPhase)
	fmt.Fprintf(&b, "| Best write ops/s | **%.1f** (`%s`) |\n", r.Throughput.BestWriteOpsPerSec, r.Throughput.BestWritePhase)
	fmt.Fprintf(&b, "| Best read docs/s | **%.1f** |\n", r.Throughput.BestReadDocsPerSec)
	fmt.Fprintf(&b, "| Best write docs/s | **%.1f** |\n\n", r.Throughput.BestWriteDocsPerSec)

	fmt.Fprintf(&b, "## Totals (measurement phases)\n\n")
	fmt.Fprintf(&b, "| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Duration | %s |\n", r.Totals.DurationHuman)
	fmt.Fprintf(&b, "| Read ops | %s (avg %.1f/s) |\n", formatCount(r.Totals.ReadOps), r.Totals.ReadOpsPerSec)
	fmt.Fprintf(&b, "| Write ops | %s (avg %.1f/s) |\n", formatCount(r.Totals.WriteOps), r.Totals.WriteOpsPerSec)
	fmt.Fprintf(&b, "| Read docs | %s (avg %.1f/s) |\n", formatCount(r.Totals.ReadDocs), r.Totals.ReadDocsPerSec)
	fmt.Fprintf(&b, "| Write docs | %s (avg %.1f/s) |\n", formatCount(r.Totals.WriteDocs), r.Totals.WriteDocsPerSec)
	fmt.Fprintf(&b, "| Read errors | %s |\n", formatCount(r.Totals.ReadErrors))
	fmt.Fprintf(&b, "| Write errors | %s |\n\n", formatCount(r.Totals.WriteErrors))

	fmt.Fprintf(&b, "## Breaking Point\n\n")
	if r.BreakingPoint.Detected {
		fmt.Fprintf(&b, "**Detected.** %s\n\n", r.BreakingPoint.Summary)
		fmt.Fprintf(&b, "| Field | Value |\n|---|---|\n")
		fmt.Fprintf(&b, "| Phase | `%s` |\n", r.BreakingPoint.PhaseName)
		fmt.Fprintf(&b, "| Time | %s |\n", r.BreakingPoint.AtTime.Format(time.RFC3339))
		fmt.Fprintf(&b, "| Read workers | %d |\n", r.BreakingPoint.ReadWorkers)
		fmt.Fprintf(&b, "| Write workers | %d |\n", r.BreakingPoint.WriteWorkers)
		fmt.Fprintf(&b, "| Cumulative read ops at break | **%s** |\n", formatCount(r.BreakingPoint.ReadOpsTotal))
		fmt.Fprintf(&b, "| Cumulative write ops at break | **%s** |\n", formatCount(r.BreakingPoint.WriteOpsTotal))
		fmt.Fprintf(&b, "| Read ops/s at break | %.1f |\n", r.BreakingPoint.ReadOpsPerSec)
		fmt.Fprintf(&b, "| Write ops/s at break | %.1f |\n", r.BreakingPoint.WriteOpsPerSec)
		fmt.Fprintf(&b, "| Error rate | %.2f%% |\n", r.BreakingPoint.ErrorRate*100)
		fmt.Fprintf(&b, "| P99 read | %s |\n", r.BreakingPoint.P99Read)
		fmt.Fprintf(&b, "| P99 write | %s |\n", r.BreakingPoint.P99Write)
		fmt.Fprintf(&b, "| Reason | %s |\n\n", r.BreakingPoint.Reason)
	} else {
		fmt.Fprintf(&b, "No breaking point detected within configured thresholds for this run.\n\n")
	}

	fmt.Fprintf(&b, "## Phases\n\n")
	for _, p := range r.Phases {
		flag := ""
		if p.Broken {
			flag = " **[BREAK]**"
		}
		fmt.Fprintf(&b, "### %s (%s)%s\n\n", p.Name, p.Kind, flag)
		fmt.Fprintf(&b, "- Duration: %s | workers: read=%d write=%d\n", p.DurationHuman, p.ReadWorkers, p.WriteWorkers)
		fmt.Fprintf(&b, "- Read: %s ops (%.1f/s), %s docs (%.1f/s), %s errors, p50=%s p99=%s\n",
			formatCount(p.ReadOps), p.ReadOpsPerSec, formatCount(p.ReadDocs), p.ReadDocsPerSec,
			formatCount(p.ReadErrors), p.ReadLatency.P50Human, p.ReadLatency.P99Human)
		fmt.Fprintf(&b, "- Write: %s ops (%.1f/s), %s docs (%.1f/s), %s errors, p50=%s p99=%s\n",
			formatCount(p.WriteOps), p.WriteOpsPerSec, formatCount(p.WriteDocs), p.WriteDocsPerSec,
			formatCount(p.WriteErrors), p.WriteLatency.P50Human, p.WriteLatency.P99Human)
		fmt.Fprintf(&b, "- Error rate: %.2f%% | success rate: %.2f%%\n\n", p.ErrorRate*100, p.SuccessRate*100)
	}

	return b.String()
}

// RedactURI masks credentials in a MongoDB URI for reports.
func RedactURI(uri string) string {
	// mongodb://user:pass@host -> mongodb://***:***@host
	if i := strings.Index(uri, "://"); i >= 0 {
		rest := uri[i+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			creds := rest[:at]
			hostpart := rest[at:]
			if strings.Contains(creds, ":") {
				return uri[:i+3] + "***:***" + hostpart
			}
			return uri[:i+3] + "***" + hostpart
		}
	}
	return uri
}

func computeLatency(samples []time.Duration) LatencyStats {
	ls := LatencyStats{}
	n := len(samples)
	if n == 0 {
		return ls
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	ls.Count = int64(n)
	ls.Min = sorted[0]
	ls.Max = sorted[n-1]
	ls.Mean = time.Duration(int64(sum) / int64(n))
	ls.P50 = percentile(sorted, 0.50)
	ls.P95 = percentile(sorted, 0.95)
	ls.P99 = percentile(sorted, 0.99)
	ls.MinHuman = ls.Min.Round(time.Microsecond).String()
	ls.MaxHuman = ls.Max.Round(time.Microsecond).String()
	ls.MeanHuman = ls.Mean.Round(time.Microsecond).String()
	ls.P50Human = ls.P50.Round(time.Microsecond).String()
	ls.P95Human = ls.P95.Round(time.Microsecond).String()
	ls.P99Human = ls.P99.Round(time.Microsecond).String()
	return ls
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func formatCount(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%d (%.1fK)", n, float64(n)/1_000)
	}
	if n < 1_000_000_000 {
		return fmt.Sprintf("%d (%.2fM)", n, float64(n)/1_000_000)
	}
	return fmt.Sprintf("%d (%.2fB)", n, float64(n)/1_000_000_000)
}
