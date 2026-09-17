package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"ai-model-scheduler/internal/jobspec"
	"ai-model-scheduler/internal/nomadapi"
)

func (s *Server) apiListDeployments(w http.ResponseWriter, r *http.Request) {
	deps, err := s.nomad.ListManaged()
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "listing deployments: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, deps)
}

func (s *Server) partialDeployments(w http.ResponseWriter, r *http.Request) {
	deps, err := s.nomad.ListManaged()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.renderPartial(w, "partial:deployments", deps)
}

// findDeployment resolves a deployment by name, or writes a 404/502 and
// returns false.
func (s *Server) findDeployment(w http.ResponseWriter, name string) (nomadapi.Deployment, bool) {
	deps, err := s.nomad.ListManaged()
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "listing deployments: %v", err)
		return nomadapi.Deployment{}, false
	}
	for _, d := range deps {
		if d.Name == name {
			return d, true
		}
	}
	writeJSONError(w, http.StatusNotFound, "deployment %q not found", name)
	return nomadapi.Deployment{}, false
}

func (s *Server) apiCreateDeployment(w http.ResponseWriter, r *http.Request) {
	var p jobspec.Params
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSONError(w, http.StatusBadRequest, "decoding request: %v", err)
		return
	}
	if err := s.deploy.Deploy(p); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "already used") {
			status = http.StatusConflict
		}
		writeJSONError(w, status, "%v", err)
		return
	}
	s.log.Info("deployment created", "name", p.Name, "runtime", p.Runtime, "model", p.Model)
	writeJSON(w, http.StatusCreated, map[string]string{"name": p.Name})
}

func (s *Server) apiGetDeployment(w http.ResponseWriter, r *http.Request) {
	if d, ok := s.findDeployment(w, r.PathValue("name")); ok {
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) apiDeleteDeployment(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	purge := r.URL.Query().Get("purge") == "1"
	if _, ok := s.findDeployment(w, name); !ok {
		return
	}
	if err := s.deploy.Stop(name, purge); err != nil {
		writeJSONError(w, http.StatusBadGateway, "%v", err)
		return
	}
	s.log.Info("deployment stopped", "name", name, "purge", purge)
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "status": "stopped"})
}

func (s *Server) apiDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	d, ok := s.findDeployment(w, r.PathValue("name"))
	if !ok {
		return
	}
	if d.AllocID == "" {
		writeJSONError(w, http.StatusNotFound, "deployment %q has no allocation yet", d.Name)
		return
	}
	stream := r.URL.Query().Get("stream")
	if stream != "stdout" {
		stream = "stderr"
	}
	logs, err := s.nomad.TailLogs(d.AllocID, nomadapi.TaskName, stream, 16*1024)
	if err != nil && logs == "" {
		writeJSONError(w, http.StatusBadGateway, "reading logs: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"alloc_id": d.AllocID, "stream": stream, "logs": logs})
}
