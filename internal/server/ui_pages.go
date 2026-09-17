package server

import (
	"net/http"
	"strconv"

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
	// Allow prefilling from links on the models page.
	data.Params.Model = r.URL.Query().Get("model")
	data.Params.Runtime = r.URL.Query().Get("runtime")
	s.renderPage(w, "deploy", pageData{Title: "Deploy", Active: "deploy", Data: data})
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
		GPU:       r.FormValue("gpu") == "on",
		CtxSize:   formInt(r, "ctx_size"),
		ExtraArgs: r.FormValue("extra_args"),
		CPUMHz:    formInt(r, "cpu_mhz"),
		MemMB:     formInt(r, "mem_mb"),
	}
	if err := s.deploy.Deploy(p); err != nil {
		data := s.deployFormData(r)
		data.Params = p
		data.Error = err.Error()
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
	s.renderPage(w, "deployment", pageData{Title: d.Name, Active: "dashboard", Data: d})
}

// uiStopDeployment stops a deployment and returns the refreshed deployments
// table so htmx can swap it in place.
func (s *Server) uiStopDeployment(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.deploy.Stop(name, false); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.log.Info("deployment stopped", "name", name)
	s.partialDeployments(w, r)
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
