package server

import (
	"encoding/json"
	"net/http"

	"ai-model-scheduler/internal/downloads"
)

func (s *Server) apiListDownloads(w http.ResponseWriter, r *http.Request) {
	list, err := s.downloads.List()
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "listing downloads: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) apiStartDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoID   string `json:"repo_id"`
		Revision string `json:"revision"`
		Include  string `json:"include"`
		Dest     string `json:"dest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "decoding request: %v", err)
		return
	}
	id, err := s.downloads.Start(req.RepoID, req.Revision, req.Include, req.Dest)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) apiDownloadLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	logs, err := s.downloads.Logs(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "logs": logs})
}

func (s *Server) apiDeleteDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.downloads.Remove(id); err != nil {
		writeJSONError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "removed"})
}

// UI handlers

func (s *Server) uiDownloads(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "downloads", pageData{Title: "Downloads", Active: "downloads"})
}

type downloadsPartialData struct {
	Downloads []downloads.Download
	Error     string
}

func (s *Server) partialDownloads(w http.ResponseWriter, r *http.Request) {
	data := downloadsPartialData{}
	list, err := s.downloads.List()
	if err != nil {
		data.Error = err.Error()
	}
	data.Downloads = list
	s.renderPartial(w, "partial:downloads", data)
}

func (s *Server) uiStartDownload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, err := s.downloads.Start(
		r.FormValue("repo_id"), r.FormValue("revision"),
		r.FormValue("include"), r.FormValue("dest"))
	if err != nil {
		s.renderPartial(w, "partial:download-error", err.Error())
		return
	}
	// Redirect back so the poller shows the new download.
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) uiRemoveDownload(w http.ResponseWriter, r *http.Request) {
	if err := s.downloads.Remove(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.partialDownloads(w, r)
}

type downloadLogsData struct {
	ID    string
	Logs  string
	Error string
}

func (s *Server) partialDownloadLogs(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	data := downloadLogsData{ID: id}
	logs, err := s.downloads.Logs(id)
	if err != nil {
		data.Error = err.Error()
	}
	data.Logs = logs
	s.renderPartial(w, "partial:download-logs", data)
}
