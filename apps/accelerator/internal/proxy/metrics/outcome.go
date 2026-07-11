package metrics

import (
	"github.com/taeven/nance/accelerator/internal/proxy/cachestats"
	"github.com/taeven/nance/accelerator/internal/proxy/savings"
	"github.com/taeven/nance/accelerator/internal/telemetry"
)

// Outcome is a cache path result for centralized accounting.
type Outcome string

const (
	OutcomeHit    Outcome = "hit"
	OutcomeMiss   Outcome = "miss"
	OutcomeBypass Outcome = "bypass"
)

// Recorder fans out one cache outcome to Prometheus, cachestats, and savings.
// Hot path: no I/O.
type Recorder struct {
	CacheStats *cachestats.Tracker
	Savings    *savings.Tracker
	// LegacyNS dual-writes nance_cache_hits/misses_total with ns+command labels.
	LegacyNS bool
}

// RecordCacheOutcome is the single fan-out for hit/miss/bypass (+ bytes).
// bypassReason is only used for CacheBypass reason label when result is bypass.
// nsLabel and cmdLabel are for legacy dual-write only.
func (r *Recorder) RecordCacheOutcome(
	tenantID, db, coll, nsLabel, cmdLabel string,
	result Outcome,
	bytes int,
	bypassReason string,
) {
	if tenantID == "" {
		return
	}
	cmd := CommandLabel(cmdLabel)

	switch result {
	case OutcomeHit:
		telemetry.CacheRequests.WithLabelValues(tenantID, "hit").Inc()
		if bytes > 0 {
			telemetry.CacheBytesServed.WithLabelValues(tenantID, "cache").Add(float64(bytes))
		}
		if r != nil && r.CacheStats != nil {
			r.CacheStats.RecordHit(tenantID, db, coll)
		}
		if r != nil && r.Savings != nil {
			r.Savings.RecordHit(tenantID, bytes)
		}
		if r == nil || r.LegacyNS {
			telemetry.CacheHits.WithLabelValues(tenantID, nsLabel, cmd).Inc()
		}
	case OutcomeMiss:
		telemetry.CacheRequests.WithLabelValues(tenantID, "miss").Inc()
		if bytes > 0 {
			telemetry.CacheBytesServed.WithLabelValues(tenantID, "backend").Add(float64(bytes))
		}
		if r != nil && r.CacheStats != nil {
			r.CacheStats.RecordMiss(tenantID, db, coll)
		}
		if r != nil && r.Savings != nil {
			r.Savings.RecordMiss(tenantID, bytes)
		}
		if r == nil || r.LegacyNS {
			telemetry.CacheMisses.WithLabelValues(tenantID, nsLabel, cmd).Inc()
		}
	case OutcomeBypass:
		telemetry.CacheRequests.WithLabelValues(tenantID, "bypass").Inc()
		reason := bypassReason
		if reason == "" {
			reason = "unknown"
		}
		telemetry.CacheBypass.WithLabelValues(tenantID, reason).Inc()
	}
}
