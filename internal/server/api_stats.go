package server

import (
	"net/http"

	"ai-model-scheduler/internal/hardware"
	"ai-model-scheduler/internal/nomadapi"
)

func (s *Server) apiNodeStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.nomad.NodeStats()
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "fetching node stats: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) apiAllocStats(w http.ResponseWriter, r *http.Request) {
	usage, err := s.nomad.AllocStats(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "fetching allocation stats: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (s *Server) apiGPUStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.hardware.Stats(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "fetching gpu stats: %v", err)
		return
	}
	if stats == nil {
		stats = []hardware.GPUStats{}
	}
	writeJSON(w, http.StatusOK, stats)
}

type nodeStatsData struct {
	Nodes   []nomadapi.NodeStats
	GPUs    []hardware.GPUStats
	GPUNote string // shown while the agent starts or when stats fail
}

func (s *Server) partialNodeStats(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.nomad.NodeStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	data := nodeStatsData{Nodes: nodes}
	if gpus, err := s.hardware.Stats(r.Context()); err != nil {
		data.GPUNote = err.Error()
	} else {
		data.GPUs = gpus
	}
	s.renderPartial(w, "partial:node-stats", data)
}
