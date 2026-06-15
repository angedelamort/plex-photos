package library

import (
	"sort"
	"sync"
	"time"
)

// scanReportVersion is the schema version of the JSON persisted to scan_reports.
// Bump it whenever the ScanReport shape changes so the frontend can adapt.
const scanReportVersion = 1

// maxReportErrors caps how many per-photo errors are embedded in a stored
// report. The full list still lives in the scan error log; the report keeps a
// short excerpt plus the total count so one broken library can't bloat it.
const maxReportErrors = 15

// scanMetrics accumulates timings, counts, and errors for a single scan run so a
// ScanReport can be produced at the end — including when the scan fails or is
// canceled. Every method is safe for concurrent use by the scan's worker pool
// and nil-safe, so non-scan code paths (e.g. lazy thumbnail serving) can pass a
// nil collector and pay nothing.
type scanMetrics struct {
	mu      sync.Mutex
	started time.Time
	tasks   map[string]*taskAccum   // fine-grained, per-call timings
	phases  map[string]time.Duration // wall-clock per scan phase
	counts  map[string]int64         // arbitrary named counters
	errs    []reportError            // per-photo failures (capped at finalize)
}

// taskAccum is the running min/max/total/count for one timed task.
type taskAccum struct {
	count    int64
	total    time.Duration
	min, max time.Duration
}

// reportError is a single per-photo failure captured during a scan.
type reportError struct {
	Phase string `json:"phase"`
	Item  string `json:"item"`
	Msg   string `json:"msg"`
}

// newScanMetrics starts a fresh collector with its wall-clock running.
func newScanMetrics() *scanMetrics {
	return &scanMetrics{
		started: time.Now(),
		tasks:   map[string]*taskAccum{},
		phases:  map[string]time.Duration{},
		counts:  map[string]int64{},
	}
}

// record adds one timed sample for the named task.
func (m *scanMetrics) record(task string, d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.tasks[task]
	if a == nil {
		m.tasks[task] = &taskAccum{count: 1, total: d, min: d, max: d}
		return
	}
	a.count++
	a.total += d
	if d < a.min {
		a.min = d
	}
	if d > a.max {
		a.max = d
	}
}

// timeIt runs fn, recording its wall-clock under task, and returns fn's error.
func (m *scanMetrics) timeIt(task string, fn func() error) error {
	if m == nil {
		return fn()
	}
	start := time.Now()
	err := fn()
	m.record(task, time.Since(start))
	return err
}

// addPhase accumulates wall-clock time spent in a named scan phase.
func (m *scanMetrics) addPhase(phase string, d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.phases[phase] += d
	m.mu.Unlock()
}

// incr bumps a named counter by one.
func (m *scanMetrics) incr(name string) { m.add(name, 1) }

// add bumps a named counter by n.
func (m *scanMetrics) add(name string, n int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.counts[name] += n
	m.mu.Unlock()
}

// addError records a per-photo failure for the report's (capped) error excerpt.
func (m *scanMetrics) addError(phase, item, msg string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.errs = append(m.errs, reportError{Phase: phase, Item: item, Msg: msg})
	m.mu.Unlock()
}

// ScanReport is the structured, JSON-serializable result of one scan run. It is
// persisted (one row per scan) so the admin UI can render a timing breakdown
// with min/max/avg/total per task.
type ScanReport struct {
	Version int              `json:"version"`
	WallMs  int64            `json:"wallMs"`
	Counts  map[string]int64 `json:"counts"`
	Phases  []reportPhase    `json:"phases"`
	Tasks   []reportTask     `json:"tasks"`
	Errors  reportErrors     `json:"errors"`
}

// reportPhase is the wall-clock time spent in a single scan phase.
type reportPhase struct {
	Name   string `json:"name"`
	WallMs int64  `json:"wallMs"`
}

// reportTask is the aggregated timing for one task, in milliseconds.
type reportTask struct {
	Key     string  `json:"key"`
	Count   int64   `json:"count"`
	TotalMs float64 `json:"totalMs"`
	AvgMs   float64 `json:"avgMs"`
	MinMs   float64 `json:"minMs"`
	MaxMs   float64 `json:"maxMs"`
}

// reportErrors is the capped excerpt of per-photo failures plus the true total.
type reportErrors struct {
	Total     int           `json:"total"`
	Truncated bool          `json:"truncated"`
	Items     []reportError `json:"items"`
}

// msFloat converts a duration to milliseconds with microsecond resolution.
func msFloat(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

// phaseOrder is the canonical display order of scan phases; unknown phases sort
// after these, alphabetically.
var phaseOrder = map[string]int{"index": 0, "thumbnails": 1, "metadata": 2, "cleanup": 3}

// finalize snapshots the collected metrics into a ScanReport. Tasks are sorted
// slowest-total-first so the biggest cost is at the top; phases follow scan
// order; errors are capped to maxReportErrors with the true total preserved.
func (m *scanMetrics) finalize() ScanReport {
	rep := ScanReport{Version: scanReportVersion, Counts: map[string]int64{}}
	if m == nil {
		return rep
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	rep.WallMs = time.Since(m.started).Milliseconds()
	for k, v := range m.counts {
		rep.Counts[k] = v
	}

	phaseNames := make([]string, 0, len(m.phases))
	for name := range m.phases {
		phaseNames = append(phaseNames, name)
	}
	sort.Slice(phaseNames, func(i, j int) bool {
		oi, iok := phaseOrder[phaseNames[i]]
		oj, jok := phaseOrder[phaseNames[j]]
		if iok && jok {
			return oi < oj
		}
		if iok != jok {
			return iok
		}
		return phaseNames[i] < phaseNames[j]
	})
	for _, name := range phaseNames {
		rep.Phases = append(rep.Phases, reportPhase{Name: name, WallMs: m.phases[name].Milliseconds()})
	}

	taskKeys := make([]string, 0, len(m.tasks))
	for k := range m.tasks {
		taskKeys = append(taskKeys, k)
	}
	sort.Slice(taskKeys, func(i, j int) bool {
		if m.tasks[taskKeys[i]].total != m.tasks[taskKeys[j]].total {
			return m.tasks[taskKeys[i]].total > m.tasks[taskKeys[j]].total
		}
		return taskKeys[i] < taskKeys[j]
	})
	for _, k := range taskKeys {
		a := m.tasks[k]
		var avg time.Duration
		if a.count > 0 {
			avg = a.total / time.Duration(a.count)
		}
		rep.Tasks = append(rep.Tasks, reportTask{
			Key:     k,
			Count:   a.count,
			TotalMs: msFloat(a.total),
			AvgMs:   msFloat(avg),
			MinMs:   msFloat(a.min),
			MaxMs:   msFloat(a.max),
		})
	}

	rep.Errors = reportErrors{Total: len(m.errs), Items: []reportError{}}
	if len(m.errs) > maxReportErrors {
		rep.Errors.Truncated = true
		rep.Errors.Items = append(rep.Errors.Items, m.errs[:maxReportErrors]...)
	} else {
		rep.Errors.Items = append(rep.Errors.Items, m.errs...)
	}
	return rep
}
