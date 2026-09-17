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
	mux.HandleFunc("GET /api/v1/deployments", s.apiListDeployments)
	mux.HandleFunc("GET /api/v1/stats/node", s.apiNodeStats)

	// UI pages
	mux.HandleFunc("GET /{$}", s.uiDashboard)

	// htmx fragments
	mux.HandleFunc("GET /partials/health", s.partialHealth)
	mux.HandleFunc("GET /partials/deployments", s.partialDeployments)
	mux.HandleFunc("GET /partials/node-stats", s.partialNodeStats)

	// static assets
	mux.Handle("GET /static/", http.FileServerFS(web.FS))

	var h http.Handler = mux
	if s.cfg.BasicAuthEnabled() {
		h = s.basicAuth(h)
	}
	return s.logRequests(h)
}
