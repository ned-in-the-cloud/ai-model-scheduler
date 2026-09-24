package server

import (
	"net/http"
	"time"

	"ai-model-scheduler/internal/catalog"
)

type modelsResponse struct {
	Models    []catalog.Entry `json:"models"`
	FetchedAt time.Time       `json:"fetched_at"`
}

func (s *Server) apiListModels(w http.ResponseWriter, r *http.Request) {
	entries, fetched, err := s.catalog.Entries(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "listing models: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, modelsResponse{Models: entries, FetchedAt: fetched})
}

func (s *Server) apiRefreshModels(w http.ResponseWriter, r *http.Request) {
	if err := s.catalog.Refresh(r.Context()); err != nil {
		writeJSONError(w, http.StatusBadGateway, "refreshing models: %v", err)
		return
	}
	s.apiListModels(w, r)
}

type modelsPartialData struct {
	Models     []catalog.Entry
	FetchedAt  time.Time
	Error      string
	Refreshing bool // a background rescan is running; the partial polls until it finishes
}

func (s *Server) partialModels(w http.ResponseWriter, r *http.Request) {
	data := modelsPartialData{}
	entries, fetched, err := s.catalog.Entries(r.Context())
	if err != nil {
		data.Error = err.Error()
	}
	data.Models, data.FetchedAt = entries, fetched
	refreshing, lastErr := s.catalog.Status()
	data.Refreshing = refreshing
	if data.Error == "" && lastErr != nil && !refreshing {
		data.Error = "last rescan failed, showing cached results: " + lastErr.Error()
	}
	s.renderPartial(w, "partial:models", data)
}

func (s *Server) uiRefreshModels(w http.ResponseWriter, r *http.Request) {
	if err := s.catalog.Refresh(r.Context()); err != nil {
		s.renderPartial(w, "partial:models", modelsPartialData{Error: err.Error()})
		return
	}
	s.partialModels(w, r)
}
