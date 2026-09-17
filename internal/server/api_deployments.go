package server

import "net/http"

func (s *Server) apiListDeployments(w http.ResponseWriter, r *http.Request) {
	deps, err := s.nomad.ListManaged()
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "listing deployments: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, deps)
}

func (s *Server) partialDeployments(w http.ResponseWriter, r *http.Request) {
	deps, err := s.nomad.ListManaged()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.renderPartial(w, "partial:deployments", deps)
}
