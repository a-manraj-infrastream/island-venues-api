package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/a-manraj-infrastream/island-venues-api/internal/events"
	"github.com/a-manraj-infrastream/island-venues-api/internal/store"
)

const (
	dateLayout      = "2006-01-02"
	maxEmailLength  = 254
	maxBookingYears = 3
)

// createBookingRequest is the POST /api/bookings body.
type createBookingRequest struct {
	VenueID      string `json:"venueId"`
	Date         string `json:"date"`
	Guests       int    `json:"guests"`
	ContactEmail string `json:"contactEmail"`
}

// validationError is a client error with a user-facing message.
type validationError struct{ msg string }

func (e validationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return validationError{msg: fmt.Sprintf(format, args...)}
}

// validateBooking checks the request against the venue and today's date in
// Mauritius (where every venue is). It returns a validationError on failure.
func validateBooking(req createBookingRequest, venue store.Venue, now time.Time) error {
	day, err := time.ParseInLocation(dateLayout, req.Date, mauritius)
	if err != nil || day.Format(dateLayout) != req.Date {
		return invalid("date must be a valid calendar date in YYYY-MM-DD format")
	}
	today := now.In(mauritius)
	todayStart := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, mauritius)
	if day.Before(todayStart) {
		return invalid("date must not be in the past")
	}
	if day.After(todayStart.AddDate(maxBookingYears, 0, 0)) {
		return invalid("date must be within %d years", maxBookingYears)
	}
	if req.Guests < 1 || req.Guests > venue.Capacity {
		return invalid("guests must be between 1 and %d for this venue", venue.Capacity)
	}
	if !validEmail(req.ContactEmail) {
		return invalid("contactEmail must be a valid e-mail address")
	}
	return nil
}

// validEmail accepts a bare address only (no display name, no comments).
func validEmail(s string) bool {
	if s == "" || len(s) > maxEmailLength {
		return false
	}
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Address == s && addr.Name == ""
}

func (s *Server) createBooking(w http.ResponseWriter, r *http.Request, caller string) {
	ctx := r.Context()
	var req createBookingRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.VenueID = strings.TrimSpace(req.VenueID)
	req.ContactEmail = strings.TrimSpace(req.ContactEmail)
	if req.VenueID == "" {
		s.writeError(w, r, http.StatusBadRequest, "venueId is required")
		return
	}

	venue, err := s.store.GetVenue(ctx, req.VenueID)
	if errors.Is(err, store.ErrNotFound) {
		s.writeError(w, r, http.StatusBadRequest, "venue does not exist")
		return
	}
	if err != nil {
		s.internalError(w, r, "failed to load venue for booking", err)
		return
	}

	now := s.now()
	if err := validateBooking(req, venue, now); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	id, err := s.newID()
	if err != nil {
		s.internalError(w, r, "failed to generate booking id", err)
		return
	}
	b := store.Booking{
		ID:           id,
		VenueID:      venue.ID,
		VenueName:    venue.Name,
		Date:         req.Date,
		Guests:       req.Guests,
		Owner:        caller,
		ContactEmail: req.ContactEmail,
		Status:       store.StatusPending,
		// PostgreSQL stores microseconds; truncating here keeps responses
		// identical whichever store is used.
		CreatedAt: now.UTC().Truncate(time.Microsecond),
	}
	err = s.store.CreateBooking(ctx, b)
	switch {
	case errors.Is(err, store.ErrConflict):
		s.writeError(w, r, http.StatusConflict, "venue is already booked on that date")
		return
	case errors.Is(err, store.ErrNotFound):
		// The venue was deleted between the read and the insert.
		s.writeError(w, r, http.StatusBadRequest, "venue does not exist")
		return
	case err != nil:
		s.internalError(w, r, "failed to create booking", err)
		return
	}

	// The booking is committed; a publish failure must not turn a successful
	// booking into an error the client would retry (creating a conflict).
	// The failure is logged at ERROR with the booking id so it can be
	// replayed. A transactional outbox would close this gap; see README.
	//
	// WithoutCancel keeps the trace context but not the request's
	// cancellation: a client that disconnects right after the insert must not
	// cancel the publish, or the booking would exist with no confirmation.
	// The publisher still bounds the call with its own timeout.
	if err := s.publisher.PublishBookingCreated(context.WithoutCancel(ctx), events.NewBookingCreated(b)); err != nil {
		s.logger.ErrorContext(ctx, "failed to publish booking-created", slog.String("bookingId", b.ID), slog.Any("error", err))
	}
	s.logger.InfoContext(ctx, "booking created", slog.String("bookingId", b.ID), slog.String("venueId", b.VenueID), slog.String("date", b.Date))
	s.writeJSON(w, r, http.StatusCreated, b)
}

func (s *Server) listMyBookings(w http.ResponseWriter, r *http.Request, caller string) {
	bookings, err := s.store.ListBookings(r.Context(), caller)
	if err != nil {
		s.internalError(w, r, "failed to list bookings", err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, bookings)
}

func (s *Server) cancelBooking(w http.ResponseWriter, r *http.Request, caller string) {
	ctx := r.Context()
	id := r.PathValue("id")
	b, err := s.store.GetBooking(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.writeError(w, r, http.StatusNotFound, "booking not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "failed to load booking", err)
		return
	}
	// Owner is immutable, so checking it before the transition is race-free.
	if b.Owner != caller {
		s.logger.WarnContext(ctx, "cancel refused: caller does not own booking", slog.String("bookingId", id))
		s.writeError(w, r, http.StatusForbidden, "you can only cancel your own bookings")
		return
	}
	s.transition(w, r, id, store.CancelFrom, store.StatusCancelled)
}

// transition applies a status change and maps store errors to responses.
func (s *Server) transition(w http.ResponseWriter, r *http.Request, id string, from []string, to string) {
	b, err := s.store.TransitionBooking(r.Context(), id, from, to)
	switch {
	case err == nil:
		s.logger.InfoContext(r.Context(), "booking status changed", slog.String("bookingId", id), slog.String("status", to))
		s.writeJSON(w, r, http.StatusOK, b)
	case errors.Is(err, store.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "booking not found")
	case errors.Is(err, store.ErrInvalidTransition):
		s.writeError(w, r, http.StatusConflict, fmt.Sprintf("booking in status %s cannot become %s", b.Status, to))
	default:
		s.internalError(w, r, "failed to update booking", err)
	}
}
