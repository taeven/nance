package stats

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRedactURI(t *testing.T) {
	in := "mongodb+srv://alice:s3cret@cluster0.example.mongodb.net/?retryWrites=true"
	out := RedactURI(in)
	if out == in {
		t.Fatalf("expected redaction, got identical string")
	}
	if want := "mongodb+srv://***:***@cluster0.example.mongodb.net/?retryWrites=true"; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestBreakingPointAndReport(t *testing.T) {
	c := NewCollector(0.05, 50*time.Millisecond, 0.90)

	c.BeginPhase("hot", PhaseMixed, 100, 50)
	// Record mostly failing ops to trip error rate.
	for i := 0; i < 100; i++ {
		c.Record(Sample{Op: OpRead, Latency: time.Millisecond, Docs: 1, Err: true})
		c.Record(Sample{Op: OpWrite, Latency: time.Millisecond, Docs: 1, Err: true})
	}
	// tiny sleep so duration > 0
	time.Sleep(2 * time.Millisecond)
	snap := c.EndPhase()
	if !snap.Broken {
		t.Fatalf("expected phase to be broken, err_rate=%v", snap.ErrorRate)
	}
	bp := c.Breaking()
	if !bp.Detected {
		t.Fatal("expected breaking point")
	}
	if bp.ReadOpsTotal == 0 && bp.WriteOpsTotal == 0 {
		t.Fatal("expected cumulative ops at break")
	}

	report := c.BuildReport("test-run", "mongodb://***:***@h", "db", "col", "mixed", map[string]any{"mode": "mixed"})
	if report.Verdict != "breaking_point_detected" {
		t.Fatalf("verdict=%s", report.Verdict)
	}

	dir := t.TempDir()
	jp, mp, err := WriteReport(dir, report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jp); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mp); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(mp)
	if len(raw) < 50 {
		t.Fatalf("markdown too short: %s", filepath.Base(mp))
	}
}

func TestPercentileAndLatency(t *testing.T) {
	samples := []time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		3 * time.Millisecond,
		4 * time.Millisecond,
		100 * time.Millisecond,
	}
	ls := computeLatency(samples)
	if ls.Count != 5 {
		t.Fatalf("count=%d", ls.Count)
	}
	if ls.Min != time.Millisecond {
		t.Fatalf("min=%v", ls.Min)
	}
	if ls.P99 < 4*time.Millisecond {
		t.Fatalf("p99=%v", ls.P99)
	}
}
