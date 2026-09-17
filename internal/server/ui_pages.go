package server

import "net/http"

type pageData struct {
	Title  string
	Active string // nav highlight: dashboard | deploy | models | downloads
	Data   any
}

func (s *Server) uiDashboard(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "dashboard", pageData{Title: "Dashboard", Active: "dashboard"})
}
