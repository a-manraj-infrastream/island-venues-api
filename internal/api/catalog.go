package api

import (
	"errors"
	"net/http"

	"github.com/a-manraj-infrastream/island-venues-api/internal/store"
)

func (s *Server) listVenues(w http.ResponseWriter, r *http.Request) {
	venues, err := s.store.ListVenues(r.Context())
	if err != nil {
		s.internalError(w, r, "failed to list venues", err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, venues)
}

func (s *Server) getVenue(w http.ResponseWriter, r *http.Request) {
	v, err := s.store.GetVenue(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		s.writeJSON(w, r, http.StatusOK, v)
	case errors.Is(err, store.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "venue not found")
	default:
		s.internalError(w, r, "failed to get venue", err)
	}
}
