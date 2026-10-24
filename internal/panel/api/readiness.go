package api

import (
	"net/http"
	"proxypanel/internal/panel/store"
)

// Registered by mountAdmin; no request parameter can choose a local file.
func (s *Server) handleGetUpgradeReadiness(w http.ResponseWriter, r *http.Request) {
	result, err := s.Store.CheckUpgradeReadiness(r.Context(), false)
	if err != nil {
		result = store.UnknownUpgradeReadiness(false)
	}
	s.writeData(w, http.StatusOK, result)
}
