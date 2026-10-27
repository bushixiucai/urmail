package loadbench

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
)

// Result is one structured scenario measurement.
type Result struct {
	Scenario       string        `json:"scenario"`
	VUs            int           `json:"vus"`
	Duration       time.Duration `json:"duration"`
	Count          int64         `json:"count"`
	Errors         int64         `json:"errors"`
	P50            time.Duration `json:"p50"`
	P95            time.Duration `json:"p95"`
	P99            time.Duration `json:"p99"`
	Throughput     float64       `json:"throughput_ops_per_sec"`
	ErrorRate      float64       `json:"error_rate"`
	MeasuredMillis bool          `json:"measured"`
}

// Report renders one structured result line.
func (r Result) Report() string {
	return fmt.Sprintf(
		"scenario=%s vus=%d window=%s ops=%d errors=%d p50=%s p95=%s p99=%s throughput=%.1f/s error_rate=%.4f",
		r.Scenario, r.VUs, r.Duration, r.Count, r.Errors, r.P50, r.P95, r.P99, r.Throughput, r.ErrorRate)
}

// Check compares a result against a budget and returns every violation.
func (r Result) Check(b Budget) []string {
	var bad []string
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	if r.MeasuredMillis && r.P50 > time.Duration(b.P50Ms*float64(time.Millisecond)) {
		bad = append(bad, fmt.Sprintf("p50 %.1fms > %.1fms", ms(r.P50), b.P50Ms))
	}
	if r.MeasuredMillis && r.P95 > time.Duration(b.P95Ms*float64(time.Millisecond)) {
		bad = append(bad, fmt.Sprintf("p95 %.1fms > %.1fms", ms(r.P95), b.P95Ms))
	}
	if r.MeasuredMillis && r.P99 > time.Duration(b.P99Ms*float64(time.Millisecond)) {
		bad = append(bad, fmt.Sprintf("p99 %.1fms > %.1fms", ms(r.P99), b.P99Ms))
	}
	if r.Throughput < b.MinThroughput {
		bad = append(bad, fmt.Sprintf("throughput %.1f/s < %.1f/s", r.Throughput, b.MinThroughput))
	}
	if r.ErrorRate > b.ErrorRateMax {
		bad = append(bad, fmt.Sprintf("error_rate %.4f > %.4f", r.ErrorRate, b.ErrorRateMax))
	}
	return bad
}

// Harness runs concurrent virtual users against scenario operations.
// It is deterministic in shape: fixed VUs, fixed window, fixed seed data.
type Harness struct {
	Pool *db.Pool
}

// Op is one iteration of a scenario. A returned error counts as a request error.
type Op func(ctx context.Context) error

// Run executes op with cfg.VUs virtual users until cfg.Duration elapses.
// The warmup prefix of each VU is discarded so cold caches do not skew data.
func (h *Harness) Run(ctx context.Context, scenario string, cfg Config, op Op) Result {
	if cfg.Warmup == 0 {
		cfg.Warmup = 500 * time.Millisecond
	}
	deadline := time.Now().Add(cfg.Duration)
	var mu sync.Mutex
	var lat []time.Duration
	var count, errors int64
	start := time.Now()
	var wg sync.WaitGroup
	for v := 0; v < cfg.VUs; v++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vuStart := time.Now()
			for time.Now().Before(deadline) {
				iterStart := time.Now()
				err := op(ctx)
				elapsed := time.Since(iterStart)
				if time.Since(vuStart) < cfg.Warmup {
					continue // warmup sample discarded
				}
				mu.Lock()
				count++
				lat = append(lat, elapsed)
				if err != nil {
					errors++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	window := time.Since(start)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		idx := int(p * float64(len(lat)-1))
		return lat[idx]
	}
	r := Result{
		Scenario:   scenario,
		VUs:        cfg.VUs,
		Duration:   window,
		Count:      count,
		Errors:     errors,
		P50:        pct(0.50),
		P95:        pct(0.95),
		P99:        pct(0.99),
		Throughput: float64(count) / window.Seconds(),
	}
	if count > 0 {
		r.ErrorRate = float64(errors) / float64(count)
		r.MeasuredMillis = true
	}
	fmt.Println("loadbench " + r.Report())
	return r
}

// Config controls one harness run.
type Config struct {
	VUs      int
	Duration time.Duration
	// Warmup is discarded per VU before samples are recorded.
	Warmup time.Duration
}

// FullConfig is the frozen full-mode load profile.
func FullConfig() Config { return Config{VUs: FullVUs, Duration: FullDuration} }

// SmokeConfig is the tiny -short profile; it checks wiring, not budgets.
func SmokeConfig() Config { return Config{VUs: 2, Duration: 2 * time.Second} }
