package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"ai-model-scheduler/internal/catalog"
	"ai-model-scheduler/internal/jobspec"
	"ai-model-scheduler/internal/nomadapi"
)

type pageData struct {
	Title  string
	Active string // nav highlight: dashboard | deploy | models | downloads
	Data   any
}

func (s *Server) uiDashboard(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "dashboard", pageData{Title: "Dashboard", Active: "dashboard"})
}

type deployFormData struct {
	Params        jobspec.Params
	EnvText       string // raw textarea content on validation errors
	SuggestedPort int
	Models        []catalog.Entry
	Error         string
}

func (s *Server) deployFormData(r *http.Request) deployFormData {
	data := deployFormData{}
	if port, err := s.deploy.SuggestPort(); err == nil {
		data.SuggestedPort = port
	}
	// Cached-only: the deploy form must render instantly, never block on an
	// indexer dispatch. The models page is where refreshes happen.
	data.Models, _ = s.catalog.Cached()
	return data
}

func (s *Server) uiDeployPage(w http.ResponseWriter, r *http.Request) {
	data := s.deployFormData(r)
	if from := r.URL.Query().Get("from"); from != "" {
		// Relaunch: prefill the form from a stopped deployment.
		data.Params = s.relaunchParams(from)
	} else {
		// Allow prefilling from links on the models page.
		data.Params.Model = r.URL.Query().Get("model")
		data.Params.Runtime = r.URL.Query().Get("runtime")
	}
	s.renderPage(w, "deploy", pageData{Title: "Deploy", Active: "deploy", Data: data})
}

// relaunchParams reconstructs deployment parameters from an existing job:
// the full set from the params meta when present, else what the job listing
// carries (jobs deployed before params were stored in meta).
func (s *Server) relaunchParams(name string) jobspec.Params {
	p := jobspec.Params{Name: name}
	if d, ok := s.findDeploymentQuiet(name); ok {
		p = jobspec.Params{
			Name: d.Name, Runtime: d.Runtime, Model: d.Model,
			GPU: d.GPU, Port: d.Port, CPUMHz: d.CPUMHz, MemMB: d.MemMB,
		}
	}
	if meta, err := s.nomad.JobMeta(nomadapi.JobPrefix + name); err == nil {
		if raw := meta[nomadapi.ParamsKey]; raw != "" {
			var stored jobspec.Params
			if json.Unmarshal([]byte(raw), &stored) == nil && stored.Name != "" {
				p = stored
			}
		}
	}
	return p
}

func (s *Server) uiDeploySubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := jobspec.Params{
		Name:      r.FormValue("name"),
		Runtime:   r.FormValue("runtime"),
		Model:     r.FormValue("model"),
		Port:      formInt(r, "port"),
		GPU:       r.FormValue("gpu"),
		CtxSize:   formInt(r, "ctx_size"),
		Threads:   formInt(r, "threads"),
		GPULayers: formInt(r, "gpu_layers"),
		ExtraArgs: r.FormValue("extra_args"),
		CPUMHz:    formInt(r, "cpu_mhz"),
		MemMB:     formInt(r, "mem_mb"),
	}
	env, envErr := parseEnvLines(r.FormValue("env"))
	p.Env = env

	deployErr := envErr
	if deployErr == nil {
		deployErr = s.deploy.Deploy(p)
	}
	if deployErr != nil {
		data := s.deployFormData(r)
		data.Params = p
		data.EnvText = r.FormValue("env") // preserve exactly what was typed
		data.Error = deployErr.Error()
		s.renderPage(w, "deploy", pageData{Title: "Deploy", Active: "deploy", Data: data})
		return
	}
	s.log.Info("deployment created", "name", p.Name, "runtime", p.Runtime)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) uiDeploymentDetail(w http.ResponseWriter, r *http.Request) {
	d, ok := s.findDeployment(w, r.PathValue("name"))
	if !ok {
		return
	}
	s.renderPage(w, "deployment", pageData{Title: d.Name, Active: "dashboard", Data: s.deploymentDetail(d)})
}

// uiStopDeployment stops (or, with ?purge=1, permanently removes) a
// deployment and returns the refreshed deployments panel so htmx can swap
// it in place, preserving the active tab via ?filter.
func (s *Server) uiStopDeployment(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	purge := r.URL.Query().Get("purge") == "1"
	if err := s.deploy.Stop(name, purge); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.log.Info("deployment stopped", "name", name, "purge", purge)
	s.renderDeployments(w, r.URL.Query().Get("filter"))
}

type logsData struct {
	Name   string
	Stream string
	Logs   string
	Error  string
}

func (s *Server) partialLogs(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	stream := r.URL.Query().Get("stream")
	if stream != "stdout" {
		stream = "stderr"
	}
	data := logsData{Name: name, Stream: stream}
	if d, ok := s.findDeploymentQuiet(name); !ok {
		data.Error = "deployment not found"
	} else if d.AllocID == "" {
		data.Error = "no allocation yet"
	} else {
		logs, err := s.nomad.TailLogs(d.AllocID, nomadapi.TaskName, stream, 16*1024)
		data.Logs = logs
		if err != nil && logs == "" {
			data.Error = err.Error()
		}
	}
	s.renderPartial(w, "partial:logs", data)
}

// findDeploymentQuiet is findDeployment without writing an HTTP error.
func (s *Server) findDeploymentQuiet(name string) (nomadapi.Deployment, bool) {
	deps, err := s.nomad.ListManaged()
	if err != nil {
		return nomadapi.Deployment{}, false
	}
	for _, d := range deps {
		if d.Name == name {
			return d, true
		}
	}
	return nomadapi.Deployment{}, false
}

func (s *Server) uiModels(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "models", pageData{Title: "Models", Active: "models"})
}

func formInt(r *http.Request, key string) int {
	n, _ := strconv.Atoi(r.FormValue(key))
	return n
}

// parseEnvLines turns "KEY=VALUE" lines from the deploy form's textarea into
// an environment map. Blank lines are skipped; anything else malformed is an
// error rather than silently dropped.
func parseEnvLines(text string) (map[string]string, error) {
	env := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("environment line %q is not KEY=VALUE", line)
		}
		env[k] = strings.TrimSpace(v)
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
}
