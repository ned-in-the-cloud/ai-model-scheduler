// Package server implements the HTTP layer: a JSON API under /api/v1 and an
// htmx-based UI rendered from embedded templates.
package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"context"

	"ai-model-scheduler/internal/catalog"
	"ai-model-scheduler/internal/config"
	"ai-model-scheduler/internal/deploy"
	"ai-model-scheduler/internal/downloads"
	"ai-model-scheduler/internal/nomadapi"
	"ai-model-scheduler/web"
)

// Server holds the application's HTTP handlers and their dependencies.
type Server struct {
	cfg      config.Config
	nomad    *nomadapi.Client
	deploy    *deploy.Service
	catalog   *catalog.Catalog
	downloads *downloads.Service
	log       *slog.Logger
	pages    map[string]*template.Template
	partials *template.Template
}

// New builds a Server with all templates parsed.
func New(cfg config.Config, nomad *nomadapi.Client, log *slog.Logger) (*Server, error) {
	cat := catalog.New(nomad, cfg, log)
	s := &Server{
		cfg:     cfg,
		nomad:   nomad,
		deploy:  deploy.New(nomad, cfg),
		catalog: cat,
		downloads: downloads.New(nomad, cfg, log, func() {
			if err := cat.Refresh(context.Background()); err != nil {
				log.Warn("catalog refresh after download", "err", err)
			}
		}),
		log: log,
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

// parseTemplates builds one template set per page: layout + all partials +
// the page file itself (which defines "content").
func (s *Server) parseTemplates() error {
	pageFiles, err := fs.Glob(web.FS, "templates/pages/*.html")
	if err != nil {
		return err
	}
	s.pages = make(map[string]*template.Template, len(pageFiles))
	for _, pf := range pageFiles {
		name := strings.TrimSuffix(path.Base(pf), ".html")
		t, err := template.New("layout.html").Funcs(templateFuncs).ParseFS(
			web.FS, "templates/layout.html", "templates/partials/*.html", pf)
		if err != nil {
			return fmt.Errorf("parsing templates for page %s: %w", name, err)
		}
		s.pages[name] = t
	}
	p, err := template.New("partials").Funcs(templateFuncs).ParseFS(
		web.FS, "templates/partials/*.html")
	if err != nil {
		return fmt.Errorf("parsing partials: %w", err)
	}
	s.partials = p
	return nil
}

var templateFuncs = template.FuncMap{
	"humanBytes": humanBytes,
	"meterClass": meterClass,
	"meterWidth": meterWidth,
}

// meterClass maps a utilization percentage to a severity class per the meter
// spec: accent below 75%, warning to 90%, danger above.
func meterClass(pct float64) string {
	switch {
	case pct >= 90:
		return "err"
	case pct >= 75:
		return "warn"
	default:
		return "ok"
	}
}

// meterWidth clamps a percentage to [0,100] for use as a CSS width.
func meterWidth(pct float64) float64 {
	return min(max(pct, 0), 100)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// renderPage renders a full page (layout + content).
func (s *Server) renderPage(w http.ResponseWriter, page string, data any) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "unknown page "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout.html", data); err != nil {
		s.log.Error("render page", "page", page, "err", err)
	}
}

// renderPartial renders a named partial template (htmx fragment).
func (s *Server) renderPartial(w http.ResponseWriter, partial string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.partials.ExecuteTemplate(w, partial, data); err != nil {
		s.log.Error("render partial", "partial", partial, "err", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSONError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, apiError{Error: fmt.Sprintf(format, args...)})
}
