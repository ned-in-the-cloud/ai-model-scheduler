package bench

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestParseSuiteYAML(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
		want    int
	}{
		{
			name: "valid two configs",
			yaml: `name: sweep
configs:
  - label: a
    runtime: vllm
    model: org/model
    gpu: nvidia
    ctx_size: 8192
    extra_args: "--x 1"
    env:
      K: v
  - label: b
    runtime: llamacpp
    model: m.gguf
`,
			want: 2,
		},
		{name: "missing label", yaml: "name: s\nconfigs:\n  - runtime: vllm\n    model: m\n", wantErr: "label"},
		{name: "duplicate label", yaml: "name: s\nconfigs:\n  - {label: a, runtime: vllm, model: m}\n  - {label: a, runtime: vllm, model: m}\n", wantErr: "duplicate"},
		{name: "unknown field", yaml: "name: s\nconfigs:\n  - {label: a, runtime: vllm, model: m, bogus: 1}\n", wantErr: "bogus"},
		{name: "bad runtime", yaml: "name: s\nconfigs:\n  - {label: a, runtime: tgi, model: m}\n", wantErr: "runtime"},
		{name: "no configs", yaml: "name: s\n", wantErr: "no configs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suite, err := ParseSuiteYAML([]byte(tt.yaml))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(suite.Configs) != tt.want {
				t.Fatalf("configs = %d, want %d", len(suite.Configs), tt.want)
			}
			if suite.Configs[0].Env["K"] != "v" {
				t.Errorf("env not parsed: %+v", suite.Configs[0].Env)
			}
			// Round-trips through the YAML renderer.
			out, err := SuiteYAML(*suite)
			if err != nil {
				t.Fatal(err)
			}
			again, err := ParseSuiteYAML(out)
			if err != nil || len(again.Configs) != tt.want {
				t.Fatalf("round trip: %v, %d configs", err, len(again.Configs))
			}
		})
	}
}

func TestStoreSuitesAndRuns(t *testing.T) {
	s := testStore(t)
	suite := &Suite{Name: "s", Configs: []Config{{Label: "a", Runtime: "vllm", Model: "m", GPU: "nvidia"}}}
	if err := s.SaveSuite(suite); err != nil {
		t.Fatal(err)
	}
	if suite.ID == "" {
		t.Fatal("no ID assigned")
	}
	got, err := s.GetSuite(suite.ID)
	if err != nil || got.Name != "s" {
		t.Fatalf("GetSuite: %v %+v", err, got)
	}
	list, _ := s.ListSuites()
	if len(list) != 1 {
		t.Fatalf("ListSuites = %d", len(list))
	}
	if _, err := s.GetSuite("nope"); err != ErrNotFound {
		t.Errorf("missing suite err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetSuite("../etc"); err == nil {
		t.Error("path traversal id accepted")
	}

	run := &Run{SuiteID: suite.ID, Status: RunPending}
	if err := s.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSuite(suite.ID); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetRun(run.ID); err != nil || r.SuiteID != suite.ID {
		t.Fatalf("run survives suite deletion: %v", err)
	}
}

func TestStoreDatasets(t *testing.T) {
	s := testStore(t)
	good := "{\"prompt\":\"2+2?\",\"expected\":\"4\"}\n\n{\"prompt\":\"capital of France\",\"expected\":\"Paris\",\"system\":\"be brief\"}\n"
	ds, err := s.SaveDataset("math", []byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if ds.Rows != 2 {
		t.Errorf("rows = %d, want 2", ds.Rows)
	}
	content, err := s.ReadDataset(ds.ID)
	if err != nil || string(content) != good {
		t.Fatalf("ReadDataset: %v", err)
	}

	bad := []struct{ name, body, want string }{
		{"not json", "hello\n", "not valid JSON"},
		{"missing expected", "{\"prompt\":\"x\"}\n", "expected"},
		{"prompt not string", "{\"prompt\":1,\"expected\":\"x\"}\n", "prompt"},
		{"empty", "\n\n", "no rows"},
		{"too big", strings.Repeat("{\"prompt\":\"x\",\"expected\":\"y\"}\n", MaxDatasetBytes/30+1), "limited"},
	}
	for _, tt := range bad {
		if _, err := s.SaveDataset("d", []byte(tt.body)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want containing %q", tt.name, err, tt.want)
		}
	}
	if err := s.DeleteDataset(ds.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadDataset(ds.ID); err != ErrNotFound {
		t.Errorf("after delete err = %v", err)
	}
}

func TestEvalSpecNormalize(t *testing.T) {
	tests := []struct {
		name    string
		spec    EvalSpec
		wantErr string
	}{
		{name: "defaults", spec: EvalSpec{}},
		{name: "jsonl needs dataset", spec: EvalSpec{Accuracy: "jsonl"}, wantErr: "dataset"},
		{name: "jsonl path traversal", spec: EvalSpec{Accuracy: "jsonl", DatasetPath: "../x"}, wantErr: "invalid dataset path"},
		{name: "tokens needs dataset", spec: EvalSpec{Accuracy: "tokens"}, wantErr: "dataset"},
		{name: "lmeval needs tasks", spec: EvalSpec{Accuracy: "lmeval"}, wantErr: "task"},
		{name: "lmeval bad task", spec: EvalSpec{Accuracy: "lmeval", Tasks: []string{"a b"}}, wantErr: "invalid task"},
		{name: "bad concurrency", spec: EvalSpec{Concurrency: []int{0}}, wantErr: "concurrency"},
		{name: "unknown kind", spec: EvalSpec{Accuracy: "vibes"}, wantErr: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Normalize()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if tt.spec.Accuracy != AccuracyNone || len(tt.spec.Concurrency) != 3 || tt.spec.Prompts != DefaultPrompts || tt.spec.ReadyTimeoutSec != DefaultReadyTimeout {
					t.Errorf("defaults not applied: %+v", tt.spec)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

const sampleResult = `endpoint ready in 12.0s, model=bench-abc-0
PROGRESS single-stream throughput
PROGRESS load test at concurrency 4
RESULT {"wait_sec":12.0,"single":{"tok_per_sec":41.5,"ttft_ms_p50":120,"ttft_ms_p95":200,"requests":8,"errors":0},"concurrency":[{"n":1,"tok_per_sec":40,"latency_ms_p50":6000,"latency_ms_p95":6500,"requests":8,"errors":0},{"n":4,"tok_per_sec":110,"latency_ms_p50":9000,"latency_ms_p95":9800,"requests":8,"errors":0}],"accuracy":{"kind":"jsonl","score":0.75,"correct":3,"total":4}}
`

func TestParseEvalOutput(t *testing.T) {
	m, err := ParseEvalOutput(sampleResult)
	if err != nil {
		t.Fatal(err)
	}
	if m.Single.TokPerSec != 41.5 || len(m.Concurrency) != 2 || m.Accuracy.Score != 0.75 || m.WaitSec != 12 {
		t.Errorf("parsed = %+v", m)
	}
	if _, err := ParseEvalOutput("no result here\n"); err == nil {
		t.Error("expected error without RESULT line")
	}
	if got := LastProgress(sampleResult); got != "load test at concurrency 4" {
		t.Errorf("LastProgress = %q", got)
	}
}

func TestBuildTableAndCSV(t *testing.T) {
	run := Run{Results: []ConfigResult{
		{Label: "fast", Status: ConfigComplete, StartupSec: 30, Metrics: &Metrics{
			Single:      &SingleStream{TokPerSec: 50, TTFTP50Ms: 100, TTFTP95Ms: 150},
			Concurrency: []ConcResult{{N: 4, TokPerSec: 120, LatencyP50Ms: 8000, LatencyP95Ms: 9000}},
			Accuracy:    &Accuracy{Score: 0.5},
			Resources:   &Resources{PeakVRAMBytes: 8 << 30, AvgUtil: 80, Samples: 10},
		}},
		{Label: "accurate", Status: ConfigComplete, StartupSec: 45, Metrics: &Metrics{
			Single:      &SingleStream{TokPerSec: 30, TTFTP50Ms: 200, TTFTP95Ms: 250},
			Concurrency: []ConcResult{{N: 4, TokPerSec: 70, LatencyP50Ms: 12000, LatencyP95Ms: 13000}},
			Accuracy:    &Accuracy{Score: 0.9, Tasks: map[string]TaskScore{"gsm8k": {"exact_match", 0.9}}},
		}},
		{Label: "broken", Status: ConfigFailed, Error: "deploy failed"},
	}}
	table := BuildTable(run)
	if len(table.Columns) != 3 {
		t.Fatalf("columns = %d", len(table.Columns))
	}
	best := map[string]string{}
	for _, row := range table.Rows {
		for i, c := range row.Cells {
			if c.Best {
				best[row.Name] = table.Columns[i].Label
			}
			if i == 2 && c.Text != "—" {
				t.Errorf("failed config has value in %s: %q", row.Name, c.Text)
			}
		}
	}
	want := map[string]string{
		"Startup (s)": "fast", "Single-stream tok/s": "fast", "TTFT p50 (ms)": "fast",
		"@4 aggregate tok/s": "fast", "@4 latency p50 (ms)": "fast",
		"Accuracy (%)": "accurate",
	}
	for name, label := range want {
		if best[name] != label {
			t.Errorf("best for %s = %q, want %q", name, best[name], label)
		}
	}
	// Rows reported by only one config get no highlight; VRAM sampled once.
	if _, ok := best["Peak VRAM (GB)"]; ok {
		t.Error("single-value row should not be highlighted")
	}
	if _, ok := best["gsm8k (%)"]; ok {
		t.Error("single-value task row should not be highlighted")
	}

	csvOut := string(CSV(run))
	if !strings.HasPrefix(csvOut, "metric,fast,accurate,broken\n") {
		t.Errorf("csv header: %q", strings.SplitN(csvOut, "\n", 2)[0])
	}
	if !strings.Contains(csvOut, "Accuracy (%),50.0,90.0,\n") {
		t.Errorf("csv accuracy row missing:\n%s", csvOut)
	}
}

// Runner tests with a fake cluster.

func TestRunnerHappyPath(t *testing.T) {
	store := testStore(t)
	suite := &Suite{Name: "s", Configs: []Config{
		{Label: "a", Runtime: "vllm", Model: "m", GPU: "nvidia"},
		{Label: "b", Runtime: "vllm", Model: "m", GPU: "nvidia", ExtraArgs: "--q awq"},
	}}
	if err := store.SaveSuite(suite); err != nil {
		t.Fatal(err)
	}
	fc := newFakeCluster()
	fc.existing("prod-model", 8000)
	r := NewRunner(store, fc, testEnv(), slog.New(slog.DiscardHandler))
	r.Poll = 5 * time.Millisecond

	run, err := r.Start(suite.ID, EvalSpec{Concurrency: []int{4}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(suite.ID, EvalSpec{}); err != ErrRunActive {
		t.Errorf("second Start err = %v, want ErrRunActive", err)
	}
	r.Wait()

	got, err := store.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != RunComplete {
		t.Fatalf("status = %s (%s)", got.Status, got.Error)
	}
	for _, res := range got.Results {
		if res.Status != ConfigComplete {
			t.Errorf("%s: status %s err %s", res.Label, res.Status, res.Error)
		}
		if res.Metrics == nil || res.Metrics.Single.TokPerSec != 41.5 {
			t.Errorf("%s: metrics %+v", res.Label, res.Metrics)
		}
		if res.StartupSec < 12 {
			t.Errorf("%s: startup %.1f should include the eval job's wait", res.Label, res.StartupSec)
		}
		if res.Metrics.Resources == nil || res.Metrics.Resources.PeakVRAMBytes != 6<<30 {
			t.Errorf("%s: resources %+v", res.Label, res.Metrics.Resources)
		}
	}
	// Prior deployment stopped, then relaunched; bench deployments purged;
	// eval jobs purged.
	if !fc.stopped["prod-model"] || fc.deployed["prod-model"] != 1 {
		t.Errorf("prod-model stopped=%v deploys=%d", fc.stopped["prod-model"], fc.deployed["prod-model"])
	}
	if !got.Restored || len(got.Restore) != 1 || got.Restore[0].Port != 8000 {
		t.Errorf("restore = %+v restored=%v", got.Restore, got.Restored)
	}
	for name := range fc.deployed {
		if strings.HasPrefix(name, deploymentPrefix) && !fc.purged[name] {
			t.Errorf("bench deployment %s not purged", name)
		}
	}
	if len(fc.jobs) != 0 {
		t.Errorf("eval jobs left registered: %v", fc.jobs)
	}
	if fc.jobsSeen != 2 {
		t.Errorf("eval jobs registered = %d, want 2", fc.jobsSeen)
	}
}

func TestRunnerStagesUploadedDataset(t *testing.T) {
	store := testStore(t)
	suite := &Suite{Name: "s", Configs: []Config{
		{Label: "a", Runtime: "vllm", Model: "m", GPU: "nvidia"},
		{Label: "b", Runtime: "vllm", Model: "m", GPU: "nvidia"},
	}}
	_ = store.SaveSuite(suite)
	// Random-ish rows so gzip can't shrink the set into a single part.
	var sb strings.Builder
	for i := 0; sb.Len() < 3<<20; i++ {
		fmt.Fprintf(&sb, "{\"prompt\":\"%x\",\"expected\":\"%d\"}\n", sha256.Sum256([]byte(fmt.Sprint(i))), i)
	}
	content := sb.String()
	ds, err := store.SaveDataset("big", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	fc := newFakeCluster()
	fc.existing("prod-model", 8000)
	r := NewRunner(store, fc, testEnv(), slog.New(slog.DiscardHandler))
	r.Poll = time.Millisecond
	run, err := r.Start(suite.ID, EvalSpec{Accuracy: AccuracyTokens, DatasetID: ds.ID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	got, _ := store.GetRun(run.ID)
	if got.Status != RunComplete {
		t.Fatalf("status = %s (%s)", got.Status, got.Error)
	}

	// Stage jobs come first and together carry the whole dataset; both eval
	// jobs point at the staged copy.
	var encoded strings.Builder
	var evals []*api.Job
	for _, job := range fc.registered {
		task := job.TaskGroups[0].Tasks[0]
		if strings.Contains(*job.ID, "-stage-") {
			if len(evals) > 0 {
				t.Errorf("stage job %s registered after an eval job", *job.ID)
			}
			if n := len(*task.Templates[0].EmbeddedTmpl); n > stageChunkBytes {
				t.Errorf("stage job %s carries %d bytes", *job.ID, n)
			}
			encoded.WriteString(*task.Templates[0].EmbeddedTmpl)
			continue
		}
		evals = append(evals, job)
	}
	if stages := len(fc.registered) - len(evals); stages < 2 {
		t.Errorf("stage jobs = %d, want the dataset split across several", stages)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded.String())
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := io.ReadAll(zr)
	if string(decoded) != content {
		t.Errorf("staged parts decode to %d bytes, want the %d-byte dataset", len(decoded), len(content))
	}
	sum := sha256.Sum256([]byte(content))
	if len(evals) != 2 {
		t.Fatalf("eval jobs = %d, want 2", len(evals))
	}
	for _, job := range evals {
		env := job.TaskGroups[0].Tasks[0].Env
		if env["BENCH_DATASET_PARTS"] != "/models/_benchmarks/"+run.ID+"/dataset" ||
			env["BENCH_DATASET_SHA256"] != hex.EncodeToString(sum[:]) ||
			env["BENCH_ACCURACY"] != "tokens" || env["BENCH_LIMIT"] != "50" {
			t.Errorf("eval env = %v", env)
		}
	}
	if len(fc.jobs) != 0 {
		t.Errorf("jobs left registered: %v", fc.jobs)
	}
}

func TestBuildTableTokenSpeed(t *testing.T) {
	run := Run{Results: []ConfigResult{
		{Label: "a", Status: ConfigComplete, Metrics: &Metrics{Tokens: &TokenSpeed{PrefillTokPerSec: 900, DecodeTokPerSec: 40, TTFTP50Ms: 300, Requests: 10, PromptTokens: 5000}}},
		{Label: "b", Status: ConfigComplete, Metrics: &Metrics{Tokens: &TokenSpeed{PrefillTokPerSec: 1200, DecodeTokPerSec: 35, TTFTP50Ms: 250, Requests: 10, PromptTokens: 5000}}},
	}}
	rows := map[string]Row{}
	for _, row := range BuildTable(run).Rows {
		rows[row.Name] = row
	}
	if r := rows["Dataset prefill tok/s"]; len(r.Cells) != 2 || !r.Cells[1].Best || r.Cells[0].Best {
		t.Errorf("prefill row = %+v", r)
	}
	if r := rows["Dataset decode tok/s"]; len(r.Cells) != 2 || !r.Cells[0].Best {
		t.Errorf("decode row = %+v", r)
	}
	if _, ok := rows["Accuracy (%)"]; ok {
		t.Error("token speed run should have no accuracy row")
	}
}

func TestRunnerFailedConfigContinues(t *testing.T) {
	store := testStore(t)
	suite := &Suite{Name: "s", Configs: []Config{
		{Label: "bad", Runtime: "vllm", Model: "m", GPU: "nvidia"},
		{Label: "good", Runtime: "vllm", Model: "m", GPU: "nvidia"},
	}}
	_ = store.SaveSuite(suite)
	fc := newFakeCluster()
	fc.failDeploy["bench-"+"*"+"-0"] = true
	r := NewRunner(store, fc, testEnv(), slog.New(slog.DiscardHandler))
	r.Poll = 5 * time.Millisecond
	run, err := r.Start(suite.ID, EvalSpec{})
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	got, _ := store.GetRun(run.ID)
	if got.Status != RunComplete {
		t.Fatalf("status = %s", got.Status)
	}
	if got.Results[0].Status != ConfigFailed || !strings.Contains(got.Results[0].Error, "deployment failed") {
		t.Errorf("bad config: %s %q", got.Results[0].Status, got.Results[0].Error)
	}
	if got.Results[1].Status != ConfigComplete {
		t.Errorf("good config: %s %q", got.Results[1].Status, got.Results[1].Error)
	}
}

func TestRunnerCancel(t *testing.T) {
	store := testStore(t)
	suite := &Suite{Name: "s", Configs: []Config{
		{Label: "a", Runtime: "vllm", Model: "m", GPU: "nvidia"},
		{Label: "b", Runtime: "vllm", Model: "m", GPU: "nvidia"},
	}}
	_ = store.SaveSuite(suite)
	fc := newFakeCluster()
	fc.existing("prod-model", 8000)
	fc.hang = true // eval jobs never finish
	r := NewRunner(store, fc, testEnv(), slog.New(slog.DiscardHandler))
	r.Poll = 5 * time.Millisecond
	run, err := r.Start(suite.ID, EvalSpec{})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, _ := store.GetRun(run.ID)
		if got.Results[0].Status == ConfigEvaluating || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := r.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	got, _ := store.GetRun(run.ID)
	if got.Status != RunCancelled {
		t.Fatalf("status = %s", got.Status)
	}
	if got.Results[0].Status != ConfigFailed || got.Results[1].Status != ConfigSkipped {
		t.Errorf("results: %s / %s", got.Results[0].Status, got.Results[1].Status)
	}
	if fc.deployed["prod-model"] != 1 {
		t.Errorf("prod-model not relaunched after cancel: deploys=%d", fc.deployed["prod-model"])
	}
	if r.ActiveID() != "" {
		t.Error("runner still active after cancel")
	}
}

func TestRunnerRecover(t *testing.T) {
	store := testStore(t)
	run := &Run{Status: RunRunning, Restore: []jobspec_Params{{Name: "prod-model", Runtime: "vllm", Model: "m", GPU: "nvidia", Port: 8000}},
		Results: []ConfigResult{{Label: "a", Status: ConfigEvaluating}, {Label: "b", Status: ConfigPending}}}
	_ = store.SaveRun(run)
	fc := newFakeCluster()
	fc.deployedBench("bench-xyz-0")
	r := NewRunner(store, fc, testEnv(), slog.New(slog.DiscardHandler))
	r.Recover()
	got, _ := store.GetRun(run.ID)
	if got.Status != RunInterrupted || !got.Restored {
		t.Fatalf("status = %s restored=%v", got.Status, got.Restored)
	}
	if got.Results[0].Status != ConfigFailed || got.Results[1].Status != ConfigSkipped {
		t.Errorf("results: %s / %s", got.Results[0].Status, got.Results[1].Status)
	}
	if !fc.purged["bench-xyz-0"] || fc.deployed["prod-model"] != 1 {
		t.Errorf("cleanup: purged=%v relaunched=%d", fc.purged["bench-xyz-0"], fc.deployed["prod-model"])
	}
}
