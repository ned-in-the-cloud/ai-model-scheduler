package server

import (
	"net/http"

	"ai-model-scheduler/web"
)

// Handler returns the root http.Handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// JSON API
	mux.HandleFunc("GET /api/v1/health", s.apiHealth)

	// UI pages
	mux.HandleFunc("GET /{$}", s.uiDashboard)

	// htmx fragments
	mux.HandleFunc("GET /partials/health", s.partialHealth)

	// static assets
	mux.Handle("GET /static/", http.FileServerFS(web.FS))

	var h http.Handler = mux
	if s.cfg.BasicAuthEnabled() {
		h = s.basicAuth(h)
	}
	return s.logRequests(h)
}
