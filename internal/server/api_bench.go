package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"ai-model-scheduler/internal/bench"
)

// JSON API

func (s *Server) apiListSuites(w http.ResponseWriter, r *http.Request) {
	suites, err := s.benchStore.ListSuites()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "listing suites: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, suites)
}

// apiCreateSuite accepts a suite as JSON, or as YAML when the Content-Type
// says so.
func (s *Server) apiCreateSuite(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "reading body: %v", err)
		return
	}
	var suite *bench.Suite
	if strings.Contains(r.Header.Get("Content-Type"), "yaml") {
		suite, err = bench.ParseSuiteYAML(body)
	} else {
		suite = &bench.Suite{}
		err = json.Unmarshal(body, suite)
	}
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "%v", err)
		return
	}
	suite.ID = ""
	if err := s.benchStore.SaveSuite(suite); err != nil {
		writeJSONError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusCreated, suite)
}

func (s *Server) apiGetSuite(w http.ResponseWriter, r *http.Request) {
	suite, err := s.benchStore.GetSuite(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, notFoundStatus(err), "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, suite)
}

func (s *Server) apiDeleteSuite(w http.ResponseWriter, r *http.Request) {
	if err := s.benchStore.DeleteSuite(r.PathValue("id")); err != nil {
		writeJSONError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "status": "deleted"})
}

func (s *Server) apiListRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.benchStore.ListRuns()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "listing runs: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) apiStartRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SuiteID string         `json:"suite_id"`
		Eval    bench.EvalSpec `json:"eval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "decoding request: %v", err)
		return
	}
	run, err := s.benchRunner.Start(req.SuiteID, req.Eval)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, bench.ErrRunActive) {
			status = http.StatusConflict
		}
		writeJSONError(w, status, "%v", err)
		return
	}
	s.log.Info("benchmark run started", "id", run.ID, "suite", run.SuiteName)
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) apiGetRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.benchStore.GetRun(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, notFoundStatus(err), "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) apiCancelRun(w http.ResponseWriter, r *http.Request) {
	if err := s.benchRunner.Cancel(r.PathValue("id")); err != nil {
		writeJSONError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "status": "cancelling"})
}

func (s *Server) apiDeleteRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.benchRunner.ActiveID() == id {
		writeJSONError(w, http.StatusConflict, "run %s is in progress; cancel it first", id)
		return
	}
	if err := s.benchStore.DeleteRun(id); err != nil {
		writeJSONError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
}

func (s *Server) apiExportRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.benchStore.GetRun(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, notFoundStatus(err), "%v", err)
		return
	}
	switch r.PathValue("format") {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="benchmark-%s.csv"`, run.ID))
		_, _ = w.Write(bench.CSV(*run))
	case "json":
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="benchmark-%s.json"`, run.ID))
		writeJSON(w, http.StatusOK, run)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown export format %q (csv or json)", r.PathValue("format"))
	}
}

func (s *Server) apiListDatasets(w http.ResponseWriter, r *http.Request) {
	list, err := s.benchStore.ListDatasets()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "listing datasets: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// apiUploadDataset takes raw JSONL in the body; ?name= labels it.
func (s *Server) apiUploadDataset(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, bench.MaxDatasetBytes+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "reading body: %v", err)
		return
	}
	ds, err := s.benchStore.SaveDataset(r.URL.Query().Get("name"), body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusCreated, ds)
}

func (s *Server) apiDeleteDataset(w http.ResponseWriter, r *http.Request) {
	if err := s.benchStore.DeleteDataset(r.PathValue("id")); err != nil {
		writeJSONError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "status": "deleted"})
}

func notFoundStatus(err error) int {
	if errors.Is(err, bench.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// UI pages

type benchmarksPageData struct {
	Suites   []bench.Suite
	Datasets []bench.Dataset
	Error    string
}

func (s *Server) uiBenchmarks(w http.ResponseWriter, r *http.Request) {
	data := benchmarksPageData{Error: r.URL.Query().Get("error")}
	data.Suites, _ = s.benchStore.ListSuites()
	data.Datasets, _ = s.benchStore.ListDatasets()
	s.renderPage(w, "benchmarks", pageData{Title: "Benchmarks", Active: "benchmarks", Data: data})
}

func benchRedirectError(w http.ResponseWriter, r *http.Request, to string, err error) {
	sep := "?"
	if strings.Contains(to, "?") {
		sep = "&"
	}
	http.Redirect(w, r, to+sep+"error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
}

// uiCreateSuite creates an empty-named suite from the form. A suite needs at
// least one config to validate, so the form carries the first config too.
func (s *Server) uiCreateSuite(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(2 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var suite *bench.Suite
	if f, _, err := r.FormFile("yaml"); err == nil {
		defer f.Close()
		body, err := io.ReadAll(io.LimitReader(f, 1<<20))
		if err == nil {
			suite, err = bench.ParseSuiteYAML(body)
		}
		if err != nil {
			benchRedirectError(w, r, "/benchmarks", err)
			return
		}
	} else {
		cfg, err := configFromForm(r)
		if err != nil {
			benchRedirectError(w, r, "/benchmarks", err)
			return
		}
		suite = &bench.Suite{Name: r.FormValue("name"), Configs: []bench.Config{cfg}}
	}
	suite.ID = ""
	if err := s.benchStore.SaveSuite(suite); err != nil {
		benchRedirectError(w, r, "/benchmarks", err)
		return
	}
	http.Redirect(w, r, "/benchmarks/suites/"+suite.ID, http.StatusSeeOther)
}

func (s *Server) uiDeleteSuite(w http.ResponseWriter, r *http.Request) {
	if err := s.benchStore.DeleteSuite(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("HX-Redirect", "/benchmarks")
	w.WriteHeader(http.StatusNoContent)
}

type suitePageData struct {
	Suite    bench.Suite
	YAML     string
	Datasets []bench.Dataset
	Tasks    []bench.LMEvalTask
	Runs     []bench.Run
	Defaults bench.EvalSpec
	Error    string
}

func (s *Server) uiSuitePage(w http.ResponseWriter, r *http.Request) {
	suite, err := s.benchStore.GetSuite(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), notFoundStatus(err))
		return
	}
	y, _ := bench.SuiteYAML(*suite)
	data := suitePageData{
		Suite: *suite, YAML: string(y), Tasks: bench.CuratedTasks,
		Error: r.URL.Query().Get("error"),
		Defaults: bench.EvalSpec{
			Concurrency: bench.DefaultConcurrency, Prompts: bench.DefaultPrompts,
			MaxTokens: bench.DefaultMaxTokens, ReadyTimeoutSec: bench.DefaultReadyTimeout, Limit: 100,
		},
	}
	data.Datasets, _ = s.benchStore.ListDatasets()
	if runs, err := s.benchStore.ListRuns(); err == nil {
		for _, run := range runs {
			if run.SuiteID == suite.ID {
				data.Runs = append(data.Runs, run)
			}
		}
	}
	s.renderPage(w, "benchmark-suite", pageData{Title: suite.Name, Active: "benchmarks", Data: data})
}

// configFromForm reads the config fields shared by the deploy form.
func configFromForm(r *http.Request) (bench.Config, error) {
	env, err := parseEnvLines(r.FormValue("env"))
	if err != nil {
		return bench.Config{}, err
	}
	cfg := bench.Config{
		Label:     strings.TrimSpace(r.FormValue("label")),
		Runtime:   r.FormValue("runtime"),
		Model:     strings.TrimSpace(r.FormValue("model")),
		GPU:       r.FormValue("gpu"),
		CtxSize:   formInt(r, "ctx_size"),
		Threads:   formInt(r, "threads"),
		GPULayers: formInt(r, "gpu_layers"),
		Env:       env,
		ExtraArgs: strings.TrimSpace(r.FormValue("extra_args")),
		CPUMHz:    formInt(r, "cpu_mhz"),
		MemMB:     formInt(r, "mem_mb"),
	}
	return cfg, cfg.Validate()
}

func (s *Server) uiAddConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	suite, err := s.benchStore.GetSuite(id)
	if err != nil {
		http.Error(w, err.Error(), notFoundStatus(err))
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg, err := configFromForm(r)
	if err == nil {
		suite.Configs = append(suite.Configs, cfg)
		err = s.benchStore.SaveSuite(suite)
	}
	if err != nil {
		benchRedirectError(w, r, "/benchmarks/suites/"+id, err)
		return
	}
	http.Redirect(w, r, "/benchmarks/suites/"+id, http.StatusSeeOther)
}

func (s *Server) uiRemoveConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	suite, err := s.benchStore.GetSuite(id)
	if err != nil {
		http.Error(w, err.Error(), notFoundStatus(err))
		return
	}
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil || idx < 0 || idx >= len(suite.Configs) {
		http.Error(w, "no such config", http.StatusNotFound)
		return
	}
	if len(suite.Configs) == 1 {
		http.Error(w, "a suite needs at least one config; delete the suite instead", http.StatusBadRequest)
		return
	}
	suite.Configs = append(suite.Configs[:idx], suite.Configs[idx+1:]...)
	if err := s.benchStore.SaveSuite(suite); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) uiUploadDataset(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(bench.MaxDatasetBytes * 2); err != nil {
		benchRedirectError(w, r, "/benchmarks", err)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		benchRedirectError(w, r, "/benchmarks", fmt.Errorf("choose a JSONL file"))
		return
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, bench.MaxDatasetBytes+1))
	if err != nil {
		benchRedirectError(w, r, "/benchmarks", err)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = hdr.Filename
	}
	if _, err := s.benchStore.SaveDataset(name, body); err != nil {
		benchRedirectError(w, r, "/benchmarks", err)
		return
	}
	http.Redirect(w, r, "/benchmarks", http.StatusSeeOther)
}

func (s *Server) uiDeleteDataset(w http.ResponseWriter, r *http.Request) {
	if err := s.benchStore.DeleteDataset(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusNoContent)
}

// uiStartRun starts a run from the suite page's form.
func (s *Server) uiStartRun(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	suiteID := r.FormValue("suite_id")
	eval := bench.EvalSpec{
		Accuracy:        r.FormValue("accuracy"),
		DatasetID:       r.FormValue("dataset_id"),
		DatasetPath:     strings.TrimSpace(r.FormValue("dataset_path")),
		Tasks:           r.Form["tasks"],
		Limit:           formInt(r, "limit"),
		Prompts:         formInt(r, "prompts"),
		MaxTokens:       formInt(r, "max_tokens"),
		ReadyTimeoutSec: formInt(r, "ready_timeout_sec"),
	}
	if extra := strings.TrimSpace(r.FormValue("extra_tasks")); extra != "" {
		for _, t := range strings.FieldsFunc(extra, func(c rune) bool { return c == ',' || c == ' ' }) {
			eval.Tasks = append(eval.Tasks, t)
		}
	}
	if eval.DatasetPath != "" {
		eval.DatasetID = "" // a path takes precedence over the picker
	}
	for _, f := range strings.FieldsFunc(r.FormValue("concurrency"), func(c rune) bool { return c == ',' || c == ' ' }) {
		if n, err := strconv.Atoi(f); err == nil {
			eval.Concurrency = append(eval.Concurrency, n)
		}
	}
	run, err := s.benchRunner.Start(suiteID, eval)
	if err != nil {
		benchRedirectError(w, r, "/benchmarks/suites/"+suiteID, err)
		return
	}
	s.log.Info("benchmark run started", "id", run.ID, "suite", run.SuiteName)
	http.Redirect(w, r, "/benchmarks/runs/"+run.ID, http.StatusSeeOther)
}

type runPageData struct {
	Run   bench.Run
	Table bench.Table
	Error string
}

func (s *Server) runPageData(id string) (runPageData, error) {
	run, err := s.benchStore.GetRun(id)
	if err != nil {
		return runPageData{}, err
	}
	return runPageData{Run: *run, Table: bench.BuildTable(*run)}, nil
}

func (s *Server) uiRunPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.runPageData(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), notFoundStatus(err))
		return
	}
	s.renderPage(w, "benchmark-run", pageData{Title: "Run " + data.Run.ID, Active: "benchmarks", Data: data})
}

func (s *Server) partialRun(w http.ResponseWriter, r *http.Request) {
	data, err := s.runPageData(r.URL.Query().Get("id"))
	if err != nil {
		data.Error = err.Error()
	}
	s.renderPartial(w, "partial:bench-run", data)
}

func (s *Server) uiCancelRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.benchRunner.Cancel(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.log.Info("benchmark run cancel requested", "id", id)
	data, _ := s.runPageData(id)
	s.renderPartial(w, "partial:bench-run", data)
}

func (s *Server) uiDeleteRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.benchRunner.ActiveID() == id {
		http.Error(w, "run is in progress; cancel it first", http.StatusConflict)
		return
	}
	if err := s.benchStore.DeleteRun(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("HX-Redirect", "/benchmarks")
	w.WriteHeader(http.StatusNoContent)
}

type runsPartialData struct {
	Runs   []bench.Run
	Active string
}

func (s *Server) partialRuns(w http.ResponseWriter, r *http.Request) {
	data := runsPartialData{Active: s.benchRunner.ActiveID()}
	data.Runs, _ = s.benchStore.ListRuns()
	s.renderPartial(w, "partial:bench-runs", data)
}
