package server

import "net/http"

func (s *Server) apiHealth(w http.ResponseWriter, r *http.Request) {
	h := s.nomad.Health()
	status := http.StatusOK
	if !h.Reachable {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, h)
}

func (s *Server) partialHealth(w http.ResponseWriter, r *http.Request) {
	s.renderPartial(w, "partial:health", s.nomad.Health())
}
