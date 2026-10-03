// Package store holds the Island Venues domain model and the persistence
// contract shared by the in-memory and PostgreSQL implementations.
//
// Handlers depend only on the Store interface so the same behaviour (and the
// same tests) apply whichever backend main wires in.
package store

import (
	"context"
	"errors"
	"time"
)

// Booking statuses. A booking starts PENDING; staff approve or reject it, the
// payment webhook marks it PAID and its owner may cancel it.
const (
	StatusPending   = "PENDING"
	StatusApproved  = "APPROVED"
	StatusRejected  = "REJECTED"
	StatusCancelled = "CANCELLED"
	StatusPaid      = "PAID"
)

var (
	// ErrNotFound is returned when a venue or booking does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a write would break a uniqueness rule:
	// a duplicate venue id, or a second live booking of a venue on one date.
	ErrConflict = errors.New("conflict")
	// ErrInvalidTransition is returned when a status change is not allowed
	// from the booking's current status.
	ErrInvalidTransition = errors.New("invalid status transition")
)

// Venue is a bookable event space.
type Venue struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Town           string   `json:"town"`
	Capacity       int      `json:"capacity"`
	PricePerDayMur int64    `json:"pricePerDayMur"`
	Description    string   `json:"description"`
	Tags           []string `json:"tags"`
}

// Booking is a request to hold a venue for one day.
type Booking struct {
	ID           string    `json:"id"`
	VenueID      string    `json:"venueId"`
	VenueName    string    `json:"venueName"`
	Date         string    `json:"date"`
	Guests       int       `json:"guests"`
	Owner        string    `json:"owner"`
	ContactEmail string    `json:"contactEmail"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Store is the persistence contract. Implementations must be safe for
// concurrent use and must enforce the double-booking rule atomically
// (see CreateBooking), because two requests can race for the same date.
type Store interface {
	ListVenues(ctx context.Context) ([]Venue, error)
	GetVenue(ctx context.Context, id string) (Venue, error)
	// CreateVenue returns ErrConflict when the id is already taken.
	CreateVenue(ctx context.Context, v Venue) error

	// CreateBooking returns ErrConflict when another booking of the same
	// venue on the same date is still live (not CANCELLED or REJECTED).
	CreateBooking(ctx context.Context, b Booking) error
	GetBooking(ctx context.Context, id string) (Booking, error)
	// ListBookings returns every booking when owner is empty, otherwise only
	// that owner's bookings, newest first.
	ListBookings(ctx context.Context, owner string) ([]Booking, error)
	// TransitionBooking moves a booking to status "to" only if its current
	// status is one of "from"; otherwise it returns ErrInvalidTransition.
	// The check and the write are atomic so concurrent transitions cannot
	// both succeed from the same starting state.
	TransitionBooking(ctx context.Context, id string, from []string, to string) (Booking, error)
}

// Allowed source statuses per action. Keeping the state machine here (rather
// than in each handler) means both stores and every caller agree on it.
var (
	// ApproveFrom lists the statuses staff can approve from.
	ApproveFrom = []string{StatusPending}
	// RejectFrom lists the statuses staff can reject from.
	RejectFrom = []string{StatusPending}
	// CancelFrom lists the statuses an owner can cancel from. A PAID booking
	// is not cancellable here because refunds are out of scope for the demo.
	CancelFrom = []string{StatusPending, StatusApproved}
	// PayFrom lists the statuses the payment webhook can mark PAID from.
	PayFrom = []string{StatusPending, StatusApproved}
)

// IsLive reports whether a booking in this status still holds its date.
func IsLive(status string) bool {
	return status != StatusCancelled && status != StatusRejected
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
