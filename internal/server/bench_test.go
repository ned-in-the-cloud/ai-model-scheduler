package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-model-scheduler/internal/bench"
)

func TestBenchmarkSuitesAPIAndPages(t *testing.T) {
	srv := newTestServer(t, fakeNomad(t, healthyNomadMux()).URL, nil)
	h := srv.Handler()

	do := func(method, path, body, contentType string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Create via YAML.
	rec := do("POST", "/api/v1/benchmarks/suites",
		"name: sweep\nconfigs:\n  - {label: a, runtime: vllm, model: m, gpu: nvidia}\n", "application/yaml")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create suite: %d %s", rec.Code, rec.Body)
	}
	var suite bench.Suite
	_ = json.Unmarshal(rec.Body.Bytes(), &suite)
	if suite.ID == "" || suite.Name != "sweep" {
		t.Fatalf("suite = %+v", suite)
	}

	// Invalid JSON suite is rejected.
	rec = do("POST", "/api/v1/benchmarks/suites", `{"name":"x","configs":[{"label":"a","runtime":"tgi"}]}`, "application/json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid suite: %d", rec.Code)
	}

	// Pages render fully: a template error mid-page is only logged, so check
	// for text near the end of each page.
	for path, marker := range map[string]string{
		"/benchmarks":                    "Loading runs",
		"/benchmarks/suites/" + suite.ID: "Runs of this suite",
	} {
		rec = do("GET", path, "", "")
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, "sweep") || !strings.Contains(body, marker) {
			t.Errorf("%s: %d, missing %q; body %.200s", path, rec.Code, marker, body)
		}
	}

	// Dataset upload and validation.
	rec = do("POST", "/api/v1/benchmarks/datasets?name=qa", "{\"prompt\":\"q\",\"expected\":\"a\"}\n", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload dataset: %d %s", rec.Code, rec.Body)
	}
	rec = do("POST", "/api/v1/benchmarks/datasets?name=bad", "{\"prompt\":\"q\"}\n", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad dataset: %d", rec.Code)
	}

	// Starting a run with an invalid eval spec fails before touching Nomad.
	rec = do("POST", "/api/v1/benchmarks/runs", `{"suite_id":"`+suite.ID+`","eval":{"accuracy":"lmeval"}}`, "application/json")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "task") {
		t.Errorf("run without tasks: %d %s", rec.Code, rec.Body)
	}

	// Unknown run and export format.
	if rec = do("GET", "/api/v1/benchmarks/runs/nope", "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing run: %d", rec.Code)
	}

	// Delete suite; the page redirects.
	rec = do("DELETE", "/benchmarks/suites/"+suite.ID, "", "")
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Redirect") != "/benchmarks" {
		t.Errorf("delete suite: %d %v", rec.Code, rec.Header())
	}
}

func TestBenchmarkRunPageAndExport(t *testing.T) {
	srv := newTestServer(t, fakeNomad(t, healthyNomadMux()).URL, nil)
	run := &bench.Run{SuiteName: "sweep", Status: bench.RunComplete, Eval: bench.EvalSpec{Accuracy: "none", Concurrency: []int{1}},
		Results: []bench.ConfigResult{
			{Label: "a", Status: bench.ConfigComplete, StartupSec: 20, Metrics: &bench.Metrics{Single: &bench.SingleStream{TokPerSec: 50}}},
			{Label: "b", Status: bench.ConfigFailed, Error: "deploy failed"},
		}}
	if err := srv.benchStore.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/benchmarks/runs/"+run.ID, nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Single-stream tok/s") || !strings.Contains(body, "deploy failed") {
		t.Errorf("run page: %d %.300s", rec.Code, body)
	}
	if !strings.Contains(body, "/export/csv") {
		t.Error("run page missing export link")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/benchmarks/runs/"+run.ID+"/export/csv", nil))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "metric,a,b\n") {
		t.Errorf("csv export: %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/benchmarks/runs/"+run.ID+"/export/xml", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown export format: %d", rec.Code)
	}
}
