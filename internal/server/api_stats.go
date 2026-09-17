package server

import "net/http"

func (s *Server) apiNodeStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.nomad.NodeStats()
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "fetching node stats: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) partialNodeStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.nomad.NodeStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.renderPartial(w, "partial:node-stats", stats)
}
