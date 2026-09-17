package server

import (
	"net/http"
	"time"

	"ai-model-scheduler/internal/hardware"
)

type gpusResponse struct {
	GPUs      []hardware.GPU `json:"gpus"`
	FetchedAt time.Time      `json:"fetched_at"`
}

func (s *Server) apiListGPUs(w http.ResponseWriter, r *http.Request) {
	gpus, fetched, err := s.hardware.Entries(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "probing gpus: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, gpusResponse{GPUs: gpus, FetchedAt: fetched})
}

func (s *Server) apiRefreshGPUs(w http.ResponseWriter, r *http.Request) {
	if err := s.hardware.Refresh(r.Context()); err != nil {
		writeJSONError(w, http.StatusBadGateway, "probing gpus: %v", err)
		return
	}
	s.apiListGPUs(w, r)
}

type gpusPartialData struct {
	GPUs     []hardware.GPU // deployable vendors only
	Selected string
	Error    string
}

func (s *Server) gpusPartialData(r *http.Request) gpusPartialData {
	data := gpusPartialData{Selected: r.FormValue("gpu")}
	gpus, _, err := s.hardware.Entries(r.Context())
	if err != nil {
		data.Error = err.Error()
	}
	for _, g := range gpus {
		if g.Deployable() {
			data.GPUs = append(data.GPUs, g)
		}
	}
	return data
}

func (s *Server) partialGPUs(w http.ResponseWriter, r *http.Request) {
	s.renderPartial(w, "partial:gpus", s.gpusPartialData(r))
}

func (s *Server) uiRescanGPUs(w http.ResponseWriter, r *http.Request) {
	if err := s.hardware.Refresh(r.Context()); err != nil {
		s.renderPartial(w, "partial:gpus", gpusPartialData{
			Selected: r.FormValue("gpu"), Error: err.Error(),
		})
		return
	}
	s.partialGPUs(w, r)
}
