package api

import "net/http"

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// handleHealth is a liveness probe: it reports that the process is serving
// HTTP and deliberately does not touch dependencies such as the database.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Version: s.version})
}
