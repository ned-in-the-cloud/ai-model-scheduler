package bench

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The eval job's stdout protocol: free-form log lines, "PROGRESS <text>"
// lines while working, and exactly one "RESULT <json>" line at the end.
const (
	progressPrefix = "PROGRESS "
	resultPrefix   = "RESULT "
)

// ParseEvalOutput extracts the final metrics from the eval job's stdout.
func ParseEvalOutput(stdout string) (*Metrics, error) {
	var last string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, resultPrefix) {
			last = strings.TrimPrefix(line, resultPrefix)
		}
	}
	if last == "" {
		return nil, fmt.Errorf("eval job produced no RESULT line")
	}
	var m Metrics
	if err := json.Unmarshal([]byte(last), &m); err != nil {
		return nil, fmt.Errorf("decoding eval result: %w", err)
	}
	return &m, nil
}

// LastProgress returns the most recent PROGRESS line in a log tail.
func LastProgress(tail string) string {
	var last string
	for _, line := range strings.Split(tail, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, progressPrefix) {
			last = strings.TrimPrefix(line, progressPrefix)
		}
	}
	return last
}

// Comparison table

// Cell is one value in the comparison table.
type Cell struct {
	Text string
	Best bool
}

// Row is one metric across all configs.
type Row struct {
	Name  string
	Cells []Cell
}

// Column heads one config's column.
type Column struct {
	Label  string
	Status string
	Error  string
}

// Table is the side-by-side comparison: metrics as rows, configs as columns.
type Table struct {
	Columns []Column
	Rows    []Row
}

type metricRow struct {
	name   string
	get    func(m *Metrics) (float64, bool)
	format func(v float64) string
	best   int // +1 higher is better, -1 lower is better, 0 no highlight
}

func fmtFloat(prec int, unit string) func(float64) string {
	return func(v float64) string { return fmt.Sprintf("%.*f%s", prec, v, unit) }
}

// BuildTable lays out a run's results for display and export. Rows appear
// only when at least one config reports them, so a perf-only run shows no
// accuracy rows.
func BuildTable(run Run) Table {
	t := Table{}
	for _, r := range run.Results {
		t.Columns = append(t.Columns, Column{Label: r.Label, Status: r.Status, Error: r.Error})
	}

	rows := []metricRow{
		{"Startup (s)", nil, fmtFloat(0, ""), -1}, // filled from ConfigResult below
		{"Single-stream tok/s", func(m *Metrics) (float64, bool) {
			if m.Single == nil {
				return 0, false
			}
			return m.Single.TokPerSec, true
		}, fmtFloat(1, ""), +1},
		{"TTFT p50 (ms)", func(m *Metrics) (float64, bool) {
			if m.Single == nil {
				return 0, false
			}
			return m.Single.TTFTP50Ms, true
		}, fmtFloat(0, ""), -1},
		{"TTFT p95 (ms)", func(m *Metrics) (float64, bool) {
			if m.Single == nil {
				return 0, false
			}
			return m.Single.TTFTP95Ms, true
		}, fmtFloat(0, ""), -1},
	}

	// Concurrency levels present in any result, ascending.
	levels := map[int]bool{}
	for _, r := range run.Results {
		if r.Metrics == nil {
			continue
		}
		for _, c := range r.Metrics.Concurrency {
			levels[c.N] = true
		}
	}
	var ns []int
	for n := range levels {
		ns = append(ns, n)
	}
	sort.Ints(ns)
	for _, n := range ns {
		n := n
		find := func(m *Metrics) *ConcResult {
			for i := range m.Concurrency {
				if m.Concurrency[i].N == n {
					return &m.Concurrency[i]
				}
			}
			return nil
		}
		rows = append(rows,
			metricRow{fmt.Sprintf("@%d aggregate tok/s", n), func(m *Metrics) (float64, bool) {
				if c := find(m); c != nil {
					return c.TokPerSec, true
				}
				return 0, false
			}, fmtFloat(1, ""), +1},
			metricRow{fmt.Sprintf("@%d latency p50 (ms)", n), func(m *Metrics) (float64, bool) {
				if c := find(m); c != nil {
					return c.LatencyP50Ms, true
				}
				return 0, false
			}, fmtFloat(0, ""), -1},
			metricRow{fmt.Sprintf("@%d latency p95 (ms)", n), func(m *Metrics) (float64, bool) {
				if c := find(m); c != nil {
					return c.LatencyP95Ms, true
				}
				return 0, false
			}, fmtFloat(0, ""), -1},
		)
	}

	tokens := func(get func(t *TokenSpeed) float64) func(m *Metrics) (float64, bool) {
		return func(m *Metrics) (float64, bool) {
			if m.Tokens == nil || m.Tokens.Requests == 0 {
				return 0, false
			}
			return get(m.Tokens), true
		}
	}
	rows = append(rows,
		metricRow{"Dataset prefill tok/s", tokens(func(t *TokenSpeed) float64 { return t.PrefillTokPerSec }), fmtFloat(1, ""), +1},
		metricRow{"Dataset decode tok/s", tokens(func(t *TokenSpeed) float64 { return t.DecodeTokPerSec }), fmtFloat(1, ""), +1},
		metricRow{"Dataset TTFT p50 (ms)", tokens(func(t *TokenSpeed) float64 { return t.TTFTP50Ms }), fmtFloat(0, ""), -1},
		metricRow{"Dataset TTFT p95 (ms)", tokens(func(t *TokenSpeed) float64 { return t.TTFTP95Ms }), fmtFloat(0, ""), -1},
		metricRow{"Dataset prompt tokens", tokens(func(t *TokenSpeed) float64 { return float64(t.PromptTokens) }), fmtFloat(0, ""), 0},
		metricRow{"Dataset completion tokens", tokens(func(t *TokenSpeed) float64 { return float64(t.CompletionTokens) }), fmtFloat(0, ""), 0},
	)

	rows = append(rows, metricRow{"Accuracy (%)", func(m *Metrics) (float64, bool) {
		if m.Accuracy == nil {
			return 0, false
		}
		return m.Accuracy.Score * 100, true
	}, fmtFloat(1, ""), +1})

	// lm-eval task rows, sorted by name.
	tasks := map[string]bool{}
	for _, r := range run.Results {
		if r.Metrics != nil && r.Metrics.Accuracy != nil {
			for name := range r.Metrics.Accuracy.Tasks {
				tasks[name] = true
			}
		}
	}
	var taskNames []string
	for name := range tasks {
		taskNames = append(taskNames, name)
	}
	sort.Strings(taskNames)
	for _, name := range taskNames {
		name := name
		rows = append(rows, metricRow{name + " (%)", func(m *Metrics) (float64, bool) {
			if m.Accuracy == nil {
				return 0, false
			}
			ts, ok := m.Accuracy.Tasks[name]
			return ts.Value * 100, ok
		}, fmtFloat(1, ""), +1})
	}

	rows = append(rows,
		metricRow{"Peak VRAM (GB)", func(m *Metrics) (float64, bool) {
			if m.Resources == nil || m.Resources.PeakVRAMBytes == 0 {
				return 0, false
			}
			return float64(m.Resources.PeakVRAMBytes) / (1 << 30), true
		}, fmtFloat(2, ""), -1},
		metricRow{"Avg GPU util (%)", func(m *Metrics) (float64, bool) {
			if m.Resources == nil || m.Resources.Samples == 0 {
				return 0, false
			}
			return m.Resources.AvgUtil, true
		}, fmtFloat(0, ""), 0},
	)

	for _, mr := range rows {
		row := Row{Name: mr.name}
		values := make([]float64, len(run.Results))
		present := make([]bool, len(run.Results))
		for i, r := range run.Results {
			if mr.get == nil {
				// Startup row comes from the result itself.
				if r.StartupSec > 0 {
					values[i], present[i] = r.StartupSec, true
				}
				continue
			}
			if r.Metrics != nil {
				values[i], present[i] = mr.get(r.Metrics)
			}
		}
		any := false
		bestIdx := -1
		for i := range values {
			if !present[i] {
				continue
			}
			any = true
			if bestIdx < 0 ||
				(mr.best > 0 && values[i] > values[bestIdx]) ||
				(mr.best < 0 && values[i] < values[bestIdx]) {
				bestIdx = i
			}
		}
		if !any {
			continue
		}
		// Only highlight when there is something to compare.
		highlight := mr.best != 0 && countTrue(present) > 1
		for i := range values {
			if !present[i] {
				row.Cells = append(row.Cells, Cell{Text: "—"})
				continue
			}
			row.Cells = append(row.Cells, Cell{Text: mr.format(values[i]), Best: highlight && i == bestIdx})
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

func countTrue(b []bool) int {
	n := 0
	for _, v := range b {
		if v {
			n++
		}
	}
	return n
}

// CSV renders the comparison table with configs as columns.
func CSV(run Run) []byte {
	t := BuildTable(run)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	header := []string{"metric"}
	for _, c := range t.Columns {
		header = append(header, c.Label)
	}
	_ = w.Write(header)
	status := []string{"status"}
	for _, c := range t.Columns {
		status = append(status, c.Status)
	}
	_ = w.Write(status)
	for _, row := range t.Rows {
		rec := []string{row.Name}
		for _, c := range row.Cells {
			if c.Text == "—" {
				rec = append(rec, "")
			} else {
				rec = append(rec, c.Text)
			}
		}
		_ = w.Write(rec)
	}
	w.Flush()
	return buf.Bytes()
}
