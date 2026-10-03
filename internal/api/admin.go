package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/a-manraj-infrastream/island-venues-api/internal/store"
)

// Limits for staff-created venues. Generous, but bounded so one request
// cannot store megabytes of text.
const (
	maxNameLength        = 120
	maxTownLength        = 80
	maxDescriptionLength = 2000
	maxTags              = 20
	maxTagLength         = 40
	maxCapacity          = 100000
	maxPricePerDayMur    = 1_000_000_000
)

var validStatuses = map[string]bool{
	store.StatusPending: true, store.StatusApproved: true, store.StatusRejected: true,
	store.StatusCancelled: true, store.StatusPaid: true,
}

// adminListBookings returns every booking, optionally filtered by ?status=.
func (s *Server) adminListBookings(w http.ResponseWriter, r *http.Request, _ string) {
	status := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && !validStatuses[status] {
		s.writeError(w, r, http.StatusBadRequest, "unknown status filter")
		return
	}
	bookings, err := s.store.ListBookings(r.Context(), "")
	if err != nil {
		s.internalError(w, r, "failed to list all bookings", err)
		return
	}
	if status != "" {
		filtered := make([]store.Booking, 0, len(bookings))
		for _, b := range bookings {
			if b.Status == status {
				filtered = append(filtered, b)
			}
		}
		bookings = filtered
	}
	s.writeJSON(w, r, http.StatusOK, bookings)
}

// adminTransition approves or rejects a booking on behalf of staff.
func (s *Server) adminTransition(from []string, to string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, caller string) {
		s.logger.InfoContext(r.Context(), "staff booking decision", slog.String("staff", caller), slog.String("bookingId", r.PathValue("id")), slog.String("status", to))
		s.transition(w, r, r.PathValue("id"), from, to)
	}
}

// createVenueRequest is the POST /api/admin/venues body. The id is derived
// from town and name so it is stable and readable in URLs.
type createVenueRequest struct {
	Name           string   `json:"name"`
	Town           string   `json:"town"`
	Capacity       int      `json:"capacity"`
	PricePerDayMur int64    `json:"pricePerDayMur"`
	Description    string   `json:"description"`
	Tags           []string `json:"tags"`
}

func validateVenue(req *createVenueRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	req.Town = strings.TrimSpace(req.Town)
	req.Description = strings.TrimSpace(req.Description)
	switch {
	case req.Name == "" || utf8.RuneCountInString(req.Name) > maxNameLength:
		return invalid("name is required (max %d characters)", maxNameLength)
	case req.Town == "" || utf8.RuneCountInString(req.Town) > maxTownLength:
		return invalid("town is required (max %d characters)", maxTownLength)
	case req.Capacity < 1 || req.Capacity > maxCapacity:
		return invalid("capacity must be between 1 and %d", maxCapacity)
	case req.PricePerDayMur < 0 || req.PricePerDayMur > maxPricePerDayMur:
		return invalid("pricePerDayMur must be between 0 and %d", maxPricePerDayMur)
	case utf8.RuneCountInString(req.Description) > maxDescriptionLength:
		return invalid("description must be at most %d characters", maxDescriptionLength)
	case len(req.Tags) > maxTags:
		return invalid("at most %d tags are allowed", maxTags)
	}
	tags := make([]string, 0, len(req.Tags))
	for _, t := range req.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || utf8.RuneCountInString(t) > maxTagLength {
			return invalid("tags must be non-empty and at most %d characters", maxTagLength)
		}
		tags = append(tags, t)
	}
	req.Tags = tags
	return nil
}

func (s *Server) adminCreateVenue(w http.ResponseWriter, r *http.Request, caller string) {
	var req createVenueRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := validateVenue(&req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	id := slugify(req.Town + " " + req.Name)
	if id == "" {
		random, err := s.newID()
		if err != nil {
			s.internalError(w, r, "failed to generate venue id", err)
			return
		}
		id = random
	}
	v := store.Venue{
		ID:             id,
		Name:           req.Name,
		Town:           req.Town,
		Capacity:       req.Capacity,
		PricePerDayMur: req.PricePerDayMur,
		Description:    req.Description,
		Tags:           req.Tags,
	}
	err := s.store.CreateVenue(r.Context(), v)
	switch {
	case errors.Is(err, store.ErrConflict):
		s.writeError(w, r, http.StatusConflict, "a venue with this name already exists in this town")
	case err != nil:
		s.internalError(w, r, "failed to create venue", err)
	default:
		s.logger.InfoContext(r.Context(), "venue created", slog.String("venueId", v.ID), slog.String("staff", caller))
		s.writeJSON(w, r, http.StatusCreated, v)
	}
}

// slugify lower-cases s, folds common French accents and replaces every run
// of other characters with a single hyphen.
func slugify(s string) string {
	fold := strings.NewReplacer(
		"à", "a", "â", "a", "ä", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
		"î", "i", "ï", "i", "ô", "o", "ö", "o", "ù", "u", "û", "u", "ü", "u", "ç", "c",
	)
	s = fold.Replace(strings.ToLower(s))
	var b strings.Builder
	hyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			hyphen = false
			continue
		}
		if !hyphen && b.Len() > 0 {
			b.WriteByte('-')
			hyphen = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
