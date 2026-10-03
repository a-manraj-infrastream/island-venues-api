package store

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Memory is an in-process Store used for local runs and tests. A single mutex
// guards everything so the double-booking check and the insert are atomic,
// matching the partial unique index the PostgreSQL store relies on.
type Memory struct {
	mu       sync.Mutex
	venues   map[string]Venue
	bookings map[string]Booking
}

var _ Store = (*Memory)(nil)

// NewMemory returns a Memory store seeded with SeedVenues.
func NewMemory() *Memory {
	m := &Memory{
		venues:   make(map[string]Venue),
		bookings: make(map[string]Booking),
	}
	for _, v := range SeedVenues() {
		m.venues[v.ID] = cloneVenue(v)
	}
	return m
}

// ListVenues returns all venues ordered by town then name.
func (m *Memory) ListVenues(_ context.Context) ([]Venue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Venue, 0, len(m.venues))
	for _, v := range m.venues {
		out = append(out, cloneVenue(v))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Town != out[j].Town {
			return out[i].Town < out[j].Town
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// GetVenue returns one venue or ErrNotFound.
func (m *Memory) GetVenue(_ context.Context, id string) (Venue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.venues[id]
	if !ok {
		return Venue{}, fmt.Errorf("failed to get venue %q: %w", id, ErrNotFound)
	}
	return cloneVenue(v), nil
}

// CreateVenue inserts a venue or returns ErrConflict if the id exists.
func (m *Memory) CreateVenue(_ context.Context, v Venue) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.venues[v.ID]; ok {
		return fmt.Errorf("failed to create venue %q: %w", v.ID, ErrConflict)
	}
	m.venues[v.ID] = cloneVenue(v)
	return nil
}

// CreateBooking inserts a booking unless the venue/date is already held.
func (m *Memory) CreateBooking(_ context.Context, b Booking) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.venues[b.VenueID]; !ok {
		return fmt.Errorf("failed to create booking for venue %q: %w", b.VenueID, ErrNotFound)
	}
	if _, ok := m.bookings[b.ID]; ok {
		return fmt.Errorf("failed to create booking %q: %w", b.ID, ErrConflict)
	}
	for _, existing := range m.bookings {
		if existing.VenueID == b.VenueID && existing.Date == b.Date && IsLive(existing.Status) {
			return fmt.Errorf("failed to create booking for venue %q on %s: %w", b.VenueID, b.Date, ErrConflict)
		}
	}
	m.bookings[b.ID] = b
	return nil
}

// GetBooking returns one booking or ErrNotFound.
func (m *Memory) GetBooking(_ context.Context, id string) (Booking, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bookings[id]
	if !ok {
		return Booking{}, fmt.Errorf("failed to get booking %q: %w", id, ErrNotFound)
	}
	return b, nil
}

// ListBookings returns all bookings, or one owner's, newest first.
func (m *Memory) ListBookings(_ context.Context, owner string) ([]Booking, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Booking, 0)
	for _, b := range m.bookings {
		if owner == "" || b.Owner == owner {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// TransitionBooking atomically checks the current status and updates it.
func (m *Memory) TransitionBooking(_ context.Context, id string, from []string, to string) (Booking, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bookings[id]
	if !ok {
		return Booking{}, fmt.Errorf("failed to transition booking %q: %w", id, ErrNotFound)
	}
	if !contains(from, b.Status) {
		return b, fmt.Errorf("failed to move booking %q from %s to %s: %w", id, b.Status, to, ErrInvalidTransition)
	}
	b.Status = to
	m.bookings[id] = b
	return b, nil
}

// cloneVenue copies the tags slice so callers cannot mutate stored state.
func cloneVenue(v Venue) Venue {
	tags := make([]string, len(v.Tags))
	copy(tags, v.Tags)
	v.Tags = tags
	return v
}
