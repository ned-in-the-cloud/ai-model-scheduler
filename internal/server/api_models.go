package server

import (
	"net/http"
	"slices"
	"strings"
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

type modelSelectData struct {
	GGUF, HFDirs []catalog.Entry
	Error        string
}

// partialModelSelect renders the model drop-down for the benchmark config
// form. The first load after an app restart waits for a share scan, which
// is why the form fetches it separately; ?refresh=1 forces a rescan.
func (s *Server) partialModelSelect(w http.ResponseWriter, r *http.Request) {
	data := modelSelectData{}
	var entries []catalog.Entry
	var err error
	if r.URL.Query().Get("refresh") != "" {
		if err = s.catalog.Refresh(r.Context()); err == nil {
			entries, _ = s.catalog.Cached()
		}
	} else {
		entries, _, err = s.catalog.Entries(r.Context())
	}
	if err != nil {
		data.Error = err.Error()
	}
	for _, e := range entries {
		if e.Kind == "gguf" {
			data.GGUF = append(data.GGUF, e)
		} else {
			data.HFDirs = append(data.HFDirs, e)
		}
	}
	byPath := func(a, b catalog.Entry) int { return strings.Compare(a.Path, b.Path) }
	slices.SortFunc(data.GGUF, byPath)
	slices.SortFunc(data.HFDirs, byPath)
	s.renderPartial(w, "partial:model-select", data)
}
