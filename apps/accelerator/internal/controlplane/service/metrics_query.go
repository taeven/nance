package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MetricsQuerier fetches org metrics from Prometheus (or returns degraded stubs).
type MetricsQuerier struct {
	baseURL    string
	httpClient *http.Client
	bearer     string
	basicUser  string
	basicPass  string
	cacheTTL   time.Duration
	maxPerPod  int

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	at   time.Time
	body any
}

// NewMetricsQuerierFromEnv builds a querier from NANCE_METRICS_* env vars.
// If NANCE_METRICS_PROM_URL is empty, Snapshot always returns degraded.
func NewMetricsQuerierFromEnv() *MetricsQuerier {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("NANCE_METRICS_PROM_URL")), "/")
	timeout := 3 * time.Second
	if v := strings.TrimSpace(os.Getenv("NANCE_METRICS_PROM_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			timeout = d
		}
	}
	ttl := 10 * time.Second
	if v := strings.TrimSpace(os.Getenv("NANCE_METRICS_CACHE_TTL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			ttl = d
		}
	}
	maxPerPod := 200
	if v := strings.TrimSpace(os.Getenv("NANCE_PROXY_MAX_CONNS_PER_TENANT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			maxPerPod = n
		}
	}
	return &MetricsQuerier{
		baseURL: base,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		bearer:    strings.TrimSpace(os.Getenv("NANCE_METRICS_PROM_BEARER_TOKEN")),
		basicUser: strings.TrimSpace(os.Getenv("NANCE_METRICS_PROM_BASIC_USER")),
		basicPass: strings.TrimSpace(os.Getenv("NANCE_METRICS_PROM_BASIC_PASS")),
		cacheTTL:  ttl,
		maxPerPod: maxPerPod,
		cache:     make(map[string]cacheEntry),
	}
}

var allowedWindows = map[string]struct{}{
	"5m": {}, "15m": {}, "1h": {}, "6h": {}, "24h": {}, "7d": {},
}

func NormalizeWindow(w string, def string) (string, error) {
	w = strings.TrimSpace(w)
	if w == "" {
		w = def
	}
	if _, ok := allowedWindows[w]; !ok {
		return "", fmt.Errorf("invalid window %q (allowed: 5m,15m,1h,6h,24h,7d)", w)
	}
	return w, nil
}

// TenantMetrics is the product API response.
type TenantMetrics struct {
	TenantID string       `json:"tenantId"`
	AsOf     string       `json:"asOf"`
	Window   string       `json:"window"`
	Source   string       `json:"source"` // prometheus | none
	Degraded bool         `json:"degraded"`
	Note     string       `json:"note,omitempty"`
	Errors   []FieldError `json:"errors,omitempty"`

	Connections ConnectionsMetrics `json:"connections"`
	Cache       CacheMetrics       `json:"cache"`
	Throughput  ThroughputMetrics  `json:"throughput"`
	Collections CollectionsNote    `json:"collections"`
}

type FieldError struct {
	Field string `json:"field"`
	Error string `json:"error"`
}

type ConnectionsMetrics struct {
	ClientAuthenticated *float64 `json:"clientAuthenticated"`
	ClientBusy          *float64 `json:"clientBusy,omitempty"`
	BackendInUse        *float64 `json:"backendInUse"`
	BackendIdle         *float64 `json:"backendIdle"`
	MaxPerPod           int      `json:"maxPerPod"`
	LimitNote           string   `json:"limitNote"`
}

type CacheMetrics struct {
	Hits             *float64 `json:"hits"`
	Misses           *float64 `json:"misses"`
	Bypass           *float64 `json:"bypass"`
	HitRatio         *float64 `json:"hitRatio"`
	BytesFromCache   *float64 `json:"bytesFromCache"`
	BytesFromBackend *float64 `json:"bytesFromBackend"`
	QueriesSaved     *float64 `json:"queriesSaved"`
}

type ThroughputMetrics struct {
	CommandsPerSecond    *float64 `json:"commandsPerSecond"`
	RateLimitedPerSecond *float64 `json:"rateLimitedPerSecond"`
}

type CollectionsNote struct {
	Top  []any  `json:"top"`
	Note string `json:"note"`
}

func (q *MetricsQuerier) Snapshot(ctx context.Context, tenantID, window string) (TenantMetrics, error) {
	window, err := NormalizeWindow(window, "1h")
	if err != nil {
		return TenantMetrics{}, err
	}
	now := time.Now().UTC()
	out := TenantMetrics{
		TenantID: tenantID,
		AsOf:     now.Format(time.RFC3339),
		Window:   window,
		Source:   "none",
		Degraded: true,
		Connections: ConnectionsMetrics{
			MaxPerPod: q.maxPerPod,
			LimitNote: "Connection limits are enforced per proxy pod, not cluster-wide.",
		},
		Collections: CollectionsNote{
			Top:  []any{},
			Note: "Collection breakdown is not multi-pod aggregated in v1",
		},
	}
	if q == nil || q.baseURL == "" {
		out.Note = "Metrics require Prometheus (NANCE_METRICS_PROM_URL). Proxy process-local counters are not visible to the control plane."
		return out, nil
	}

	cacheKey := "snap:" + tenantID + ":" + window
	if q.cacheTTL > 0 {
		q.mu.Lock()
		if e, ok := q.cache[cacheKey]; ok && time.Since(e.at) < q.cacheTTL {
			if tm, ok := e.body.(TenantMetrics); ok {
				q.mu.Unlock()
				return tm, nil
			}
		}
		q.mu.Unlock()
	}

	out.Source = "prometheus"
	out.Degraded = false
	out.Note = ""
	tEsc := escapePromLabelValue(tenantID)

	var errs []FieldError
	setF := func(field string, ptr **float64, query string) {
		v, err := q.instant(ctx, query)
		if err != nil {
			errs = append(errs, FieldError{Field: field, Error: err.Error()})
			out.Degraded = true
			return
		}
		*ptr = &v
	}

	setF("connections.clientAuthenticated", &out.Connections.ClientAuthenticated,
		fmt.Sprintf(`sum(nance_proxy_client_connections_authenticated{tenant="%s"}) or vector(0)`, tEsc))
	setF("connections.clientBusy", &out.Connections.ClientBusy,
		fmt.Sprintf(`sum(nance_proxy_client_connections_busy{tenant="%s"}) or vector(0)`, tEsc))
	setF("connections.backendInUse", &out.Connections.BackendInUse,
		fmt.Sprintf(`sum(nance_proxy_backend_clients{tenant="%s",state="in_use"}) or vector(0)`, tEsc))
	setF("connections.backendIdle", &out.Connections.BackendIdle,
		fmt.Sprintf(`sum(nance_proxy_backend_clients{tenant="%s",state="idle"}) or vector(0)`, tEsc))

	setF("cache.hits", &out.Cache.Hits,
		fmt.Sprintf(`sum(increase(nance_cache_requests_total{tenant="%s",result="hit"}[%s])) or vector(0)`, tEsc, window))
	setF("cache.misses", &out.Cache.Misses,
		fmt.Sprintf(`sum(increase(nance_cache_requests_total{tenant="%s",result="miss"}[%s])) or vector(0)`, tEsc, window))
	setF("cache.bypass", &out.Cache.Bypass,
		fmt.Sprintf(`sum(increase(nance_cache_requests_total{tenant="%s",result="bypass"}[%s])) or vector(0)`, tEsc, window))
	setF("cache.bytesFromCache", &out.Cache.BytesFromCache,
		fmt.Sprintf(`sum(increase(nance_cache_bytes_served_total{tenant="%s",source="cache"}[%s])) or vector(0)`, tEsc, window))
	setF("cache.bytesFromBackend", &out.Cache.BytesFromBackend,
		fmt.Sprintf(`sum(increase(nance_cache_bytes_served_total{tenant="%s",source="backend"}[%s])) or vector(0)`, tEsc, window))
	setF("throughput.commandsPerSecond", &out.Throughput.CommandsPerSecond,
		fmt.Sprintf(`sum(rate(nance_proxy_commands_total{tenant="%s"}[%s])) or vector(0)`, tEsc, window))
	setF("throughput.rateLimitedPerSecond", &out.Throughput.RateLimitedPerSecond,
		fmt.Sprintf(`sum(rate(nance_proxy_rate_limited_total{tenant="%s"}[%s])) or vector(0)`, tEsc, window))

	if out.Cache.Hits != nil {
		out.Cache.QueriesSaved = out.Cache.Hits
	}
	if out.Cache.Hits != nil && out.Cache.Misses != nil {
		h, m := *out.Cache.Hits, *out.Cache.Misses
		if h+m > 0 {
			r := h / (h + m)
			out.Cache.HitRatio = &r
		}
	}
	out.Errors = errs

	if q.cacheTTL > 0 {
		q.mu.Lock()
		q.cache[cacheKey] = cacheEntry{at: time.Now(), body: out}
		q.mu.Unlock()
	}
	return out, nil
}

// TimeseriesPoint is one range sample.
type TimeseriesPoint struct {
	T int64    `json:"t"`
	V *float64 `json:"v"`
}

type TimeseriesResponse struct {
	Metric   string            `json:"metric"`
	Window   string            `json:"window"`
	Step     string            `json:"step"`
	Points   []TimeseriesPoint `json:"points"`
	Source   string            `json:"source"`
	Degraded bool              `json:"degraded"`
	Note     string            `json:"note,omitempty"`
}

var allowedMetrics = map[string]func(tenant, step string) string{
	"hit_ratio": func(t, step string) string {
		return fmt.Sprintf(
			`sum(rate(nance_cache_requests_total{tenant="%s",result="hit"}[%s])) / clamp_min(sum(rate(nance_cache_requests_total{tenant="%s",result=~"hit|miss"}[%s])), 1e-9)`,
			t, step, t, step)
	},
	"hits": func(t, step string) string {
		return fmt.Sprintf(`sum(rate(nance_cache_requests_total{tenant="%s",result="hit"}[%s]))`, t, step)
	},
	"misses": func(t, step string) string {
		return fmt.Sprintf(`sum(rate(nance_cache_requests_total{tenant="%s",result="miss"}[%s]))`, t, step)
	},
	"commands_per_second": func(t, step string) string {
		return fmt.Sprintf(`sum(rate(nance_proxy_commands_total{tenant="%s"}[%s]))`, t, step)
	},
	"client_authenticated": func(t, step string) string {
		return fmt.Sprintf(`sum(nance_proxy_client_connections_authenticated{tenant="%s"})`, t)
	},
	"bytes_from_cache": func(t, step string) string {
		return fmt.Sprintf(`sum(rate(nance_cache_bytes_served_total{tenant="%s",source="cache"}[%s]))`, t, step)
	},
}

var allowedSteps = map[string]struct{}{"1m": {}, "5m": {}, "15m": {}, "1h": {}}

func (q *MetricsQuerier) Timeseries(ctx context.Context, tenantID, metric, window, step string) (TimeseriesResponse, error) {
	window, err := NormalizeWindow(window, "24h")
	if err != nil {
		return TimeseriesResponse{}, err
	}
	if step == "" {
		step = autoStep(window)
	}
	if _, ok := allowedSteps[step]; !ok {
		return TimeseriesResponse{}, fmt.Errorf("invalid step %q", step)
	}
	build, ok := allowedMetrics[metric]
	if !ok {
		return TimeseriesResponse{}, fmt.Errorf("invalid metric %q", metric)
	}
	out := TimeseriesResponse{Metric: metric, Window: window, Step: step, Points: []TimeseriesPoint{}, Source: "none", Degraded: true}
	if q == nil || q.baseURL == "" {
		out.Note = "Metrics require Prometheus (NANCE_METRICS_PROM_URL)."
		return out, nil
	}
	tEsc := escapePromLabelValue(tenantID)
	query := build(tEsc, step)
	end := time.Now()
	start := end.Add(-parseWindow(window))
	points, err := q.rangeQuery(ctx, query, start, end, step)
	if err != nil {
		out.Degraded = true
		out.Source = "prometheus"
		out.Note = err.Error()
		return out, nil
	}
	out.Source = "prometheus"
	out.Degraded = false
	out.Points = points
	return out, nil
}

func autoStep(window string) string {
	switch window {
	case "5m", "15m":
		return "1m"
	case "1h", "6h":
		return "5m"
	case "24h":
		return "15m"
	default:
		return "1h"
	}
}

func parseWindow(w string) time.Duration {
	d, err := time.ParseDuration(w)
	if err != nil {
		// 7d
		if strings.HasSuffix(w, "d") {
			n, _ := strconv.Atoi(strings.TrimSuffix(w, "d"))
			return time.Duration(n) * 24 * time.Hour
		}
		return time.Hour
	}
	return d
}

func escapePromLabelValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

type promAPIResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Value  []any             `json:"value"`
			Values [][]any           `json:"values"`
			Metric map[string]string `json:"metric"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

func (q *MetricsQuerier) instant(ctx context.Context, query string) (float64, error) {
	u := q.baseURL + "/api/v1/query"
	vals := url.Values{}
	vals.Set("query", query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+vals.Encode(), nil)
	if err != nil {
		return 0, err
	}
	q.auth(req)
	res, err := q.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 200 {
		return 0, fmt.Errorf("prometheus status %d: %s", res.StatusCode, truncate(string(body), 200))
	}
	var pr promAPIResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return 0, err
	}
	if pr.Status != "success" {
		return 0, fmt.Errorf("prometheus error: %s", pr.Error)
	}
	if len(pr.Data.Result) == 0 {
		return 0, nil
	}
	return parsePromSample(pr.Data.Result[0].Value)
}

func (q *MetricsQuerier) rangeQuery(ctx context.Context, query string, start, end time.Time, step string) ([]TimeseriesPoint, error) {
	u := q.baseURL + "/api/v1/query_range"
	vals := url.Values{}
	vals.Set("query", query)
	vals.Set("start", strconv.FormatFloat(float64(start.Unix()), 'f', 0, 64))
	vals.Set("end", strconv.FormatFloat(float64(end.Unix()), 'f', 0, 64))
	vals.Set("step", step)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+vals.Encode(), nil)
	if err != nil {
		return nil, err
	}
	q.auth(req)
	res, err := q.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("prometheus status %d: %s", res.StatusCode, truncate(string(body), 200))
	}
	var pr promAPIResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, err
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prometheus error: %s", pr.Error)
	}
	if len(pr.Data.Result) == 0 {
		return []TimeseriesPoint{}, nil
	}
	var out []TimeseriesPoint
	for _, pair := range pr.Data.Result[0].Values {
		if len(pair) < 2 {
			continue
		}
		ts, _ := pair[0].(float64)
		vstr, _ := pair[1].(string)
		v, err := strconv.ParseFloat(vstr, 64)
		pt := TimeseriesPoint{T: int64(ts)}
		if err == nil {
			pt.V = &v
		}
		out = append(out, pt)
	}
	return out, nil
}

func (q *MetricsQuerier) auth(req *http.Request) {
	if q.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+q.bearer)
	} else if q.basicUser != "" {
		req.SetBasicAuth(q.basicUser, q.basicPass)
	}
}

func parsePromSample(value []any) (float64, error) {
	if len(value) < 2 {
		return 0, fmt.Errorf("empty sample")
	}
	switch v := value[1].(type) {
	case string:
		return strconv.ParseFloat(v, 64)
	case float64:
		return v, nil
	default:
		return 0, fmt.Errorf("unexpected sample type %T", value[1])
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
