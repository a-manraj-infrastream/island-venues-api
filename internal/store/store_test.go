package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

// contractTest runs the same behavioural checks against any Store, so the
// memory store used by handler tests cannot drift from PostgreSQL.
func contractTest(t *testing.T, newStore func(t *testing.T) Store) {
	ctx := context.Background()
	created := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	booking := func(id, venue, date, owner string) Booking {
		return Booking{ID: id, VenueID: venue, VenueName: "n", Date: date, Guests: 2, Owner: owner,
			ContactEmail: owner, Status: StatusPending, CreatedAt: created}
	}

	t.Run("seeded venues", func(t *testing.T) {
		s := newStore(t)
		venues, err := s.ListVenues(ctx)
		if err != nil || len(venues) != 12 {
			t.Fatalf("ListVenues() = %d venues, %v; want 12", len(venues), err)
		}
		expectVenueOrder(t, venues)
		v, err := s.GetVenue(ctx, "chamarel-highland-lodge")
		if err != nil || v.Town != "Chamarel" || len(v.Tags) == 0 {
			t.Fatalf("GetVenue() = %+v, %v", v, err)
		}
		if _, err := s.GetVenue(ctx, "nowhere"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetVenue(unknown) error = %v, want ErrNotFound", err)
		}
	})

	t.Run("create venue", func(t *testing.T) {
		s := newStore(t)
		v := Venue{ID: "x-new", Name: "New", Town: "X", Capacity: 5, PricePerDayMur: 1}
		if err := s.CreateVenue(ctx, v); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateVenue(ctx, v); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate CreateVenue() error = %v, want ErrConflict", err)
		}
		got, err := s.GetVenue(ctx, "x-new")
		if err != nil || got.Tags == nil {
			t.Fatalf("GetVenue() = %+v, %v (tags must be non-nil so JSON is [])", got, err)
		}
		// Locale collation sorts "aaa" before "Zzz"; byte order does not.
		// This pair makes the two orders disagree.
		for _, town := range []string{"aaa", "Zzz"} {
			if err := s.CreateVenue(ctx, Venue{ID: "order-" + town, Name: "n", Town: town, Capacity: 1}); err != nil {
				t.Fatal(err)
			}
		}
		venues, err := s.ListVenues(ctx)
		if err != nil {
			t.Fatal(err)
		}
		expectVenueOrder(t, venues)
	})

	t.Run("bookings and double booking", func(t *testing.T) {
		s := newStore(t)
		v := "tamarin-salt-pans-studio"
		if err := s.CreateBooking(ctx, booking("b1", v, "2026-10-10", "a@x")); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateBooking(ctx, booking("b2", v, "2026-10-10", "b@x")); !errors.Is(err, ErrConflict) {
			t.Fatalf("double booking error = %v, want ErrConflict", err)
		}
		if err := s.CreateBooking(ctx, booking("b3", "nowhere", "2026-10-10", "b@x")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown venue error = %v, want ErrNotFound", err)
		}
		got, err := s.GetBooking(ctx, "b1")
		if err != nil || got.Date != "2026-10-10" || !got.CreatedAt.Equal(created) || got.Status != StatusPending {
			t.Fatalf("GetBooking() = %+v, %v", got, err)
		}
		if _, err := s.TransitionBooking(ctx, "b1", CancelFrom, StatusCancelled); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateBooking(ctx, booking("b2", v, "2026-10-10", "b@x")); err != nil {
			t.Fatalf("booking after cancel: %v", err)
		}
		if _, err := s.TransitionBooking(ctx, "b2", RejectFrom, StatusRejected); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateBooking(ctx, booking("b4", v, "2026-10-10", "a@x")); err != nil {
			t.Fatalf("booking after reject: %v", err)
		}
	})

	t.Run("list by owner", func(t *testing.T) {
		s := newStore(t)
		for i, owner := range []string{"a@x", "b@x", "a@x"} {
			b := booking(fmt.Sprintf("o%d", i), "curepipe-colonial-hall", fmt.Sprintf("2026-11-%02d", i+1), owner)
			b.CreatedAt = created.Add(time.Duration(i) * time.Minute)
			if err := s.CreateBooking(ctx, b); err != nil {
				t.Fatal(err)
			}
		}
		mine, err := s.ListBookings(ctx, "a@x")
		if err != nil || len(mine) != 2 || mine[0].ID != "o2" || mine[1].ID != "o0" {
			t.Fatalf("ListBookings(a) = %+v, %v; want o2, o0", mine, err)
		}
		all, err := s.ListBookings(ctx, "")
		if err != nil || len(all) != 3 {
			t.Fatalf("ListBookings(all) = %d, %v", len(all), err)
		}
		none, err := s.ListBookings(ctx, "nobody@x")
		if err != nil || none == nil || len(none) != 0 {
			t.Fatalf("ListBookings(nobody) = %#v, %v; want empty non-nil", none, err)
		}
	})

	t.Run("transitions", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateBooking(ctx, booking("t1", "blue-bay-marine-park-cove", "2026-12-01", "a@x")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.TransitionBooking(ctx, "missing", ApproveFrom, StatusApproved); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing error = %v", err)
		}
		b, err := s.TransitionBooking(ctx, "t1", ApproveFrom, StatusApproved)
		if err != nil || b.Status != StatusApproved {
			t.Fatalf("approve = %+v, %v", b, err)
		}
		b, err = s.TransitionBooking(ctx, "t1", RejectFrom, StatusRejected)
		if !errors.Is(err, ErrInvalidTransition) || b.Status != StatusApproved {
			t.Fatalf("reject approved = %+v, %v; want ErrInvalidTransition with current status", b, err)
		}
	})

	t.Run("concurrent double booking admits one", func(t *testing.T) {
		s := newStore(t)
		var wg sync.WaitGroup
		var mu sync.Mutex
		ok, conflicts := 0, 0
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				err := s.CreateBooking(ctx, booking("c"+strconv.Itoa(i), "le-morne-brabant-pavilion", "2027-01-01", "a@x"))
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					ok++
				case errors.Is(err, ErrConflict):
					conflicts++
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}(i)
		}
		wg.Wait()
		if ok != 1 || conflicts != 19 {
			t.Fatalf("ok = %d, conflicts = %d; want 1 and 19", ok, conflicts)
		}
	})
}

// expectVenueOrder checks both stores order venues byte-wise by town then
// name, so the catalogue looks the same whichever store serves it.
func expectVenueOrder(t *testing.T, venues []Venue) {
	t.Helper()
	sorted := sort.SliceIsSorted(venues, func(i, j int) bool {
		if venues[i].Town != venues[j].Town {
			return venues[i].Town < venues[j].Town
		}
		return venues[i].Name < venues[j].Name
	})
	if !sorted {
		t.Fatalf("ListVenues() not ordered by town, name: %v", venues)
	}
}

func TestMemoryStore(t *testing.T) {
	contractTest(t, func(*testing.T) Store { return NewMemory() })
}

func TestMemoryReturnsCopies(t *testing.T) {
	m := NewMemory()
	v, _ := m.GetVenue(context.Background(), "grand-baie-lagoon-deck")
	v.Tags[0] = "mutated"
	again, _ := m.GetVenue(context.Background(), "grand-baie-lagoon-deck")
	if again.Tags[0] == "mutated" {
		t.Fatal("caller mutation leaked into the store")
	}
}

// TestPostgresStore runs the contract against a real database when
// ISLAND_VENUES_TEST_DATABASE_URL points at a disposable PostgreSQL. Each
// subtest gets a clean schema. Skipped otherwise, so CI needs no database.
func TestPostgresStore(t *testing.T) {
	url := os.Getenv("ISLAND_VENUES_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ISLAND_VENUES_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	reset := func(t *testing.T) {
		t.Helper()
		p, err := OpenPostgres(ctx, url)
		if err != nil {
			t.Fatalf("OpenPostgres() error = %v", err)
		}
		defer p.Close()
		if _, err := p.pool.Exec(ctx, "DROP TABLE IF EXISTS bookings; DROP TABLE IF EXISTS venues"); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("concurrent start-ups migrate safely", func(t *testing.T) {
		reset(t)
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p, err := OpenPostgres(ctx, url)
				if err != nil {
					errs <- err
					return
				}
				p.Close()
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("concurrent OpenPostgres() error = %v", err)
		}
	})

	contractTest(t, func(t *testing.T) Store {
		p, err := OpenPostgres(ctx, url)
		if err != nil {
			t.Fatalf("OpenPostgres() error = %v", err)
		}
		if _, err := p.pool.Exec(ctx, "DROP TABLE IF EXISTS bookings; DROP TABLE IF EXISTS venues"); err != nil {
			t.Fatal(err)
		}
		p.Close()
		// Open twice: the second run proves the migrations are idempotent.
		for i := 0; i < 2; i++ {
			if p, err = OpenPostgres(ctx, url); err != nil {
				t.Fatalf("OpenPostgres() run %d error = %v", i, err)
			}
			if i == 0 {
				p.Close()
			}
		}
		t.Cleanup(p.Close)
		return p
	})
}

// TestSeedMatchesMigration guards against the Go seed and the SQL seed
// drifting apart: both stores must start with identical venues.
func TestSeedMatchesMigration(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/002_seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile(`\('([^']+)', '((?:[^']|'')+)', '([^']+)', (\d+), (\d+), '((?:[^']|'')*)', ARRAY\[([^\]]*)\]\)`)
	matches := row.FindAllStringSubmatch(string(raw), -1)
	seed := SeedVenues()
	if len(matches) != len(seed) {
		t.Fatalf("SQL seed has %d rows, Go seed has %d", len(matches), len(seed))
	}
	unquote := func(s string) string { return regexp.MustCompile(`''`).ReplaceAllString(s, "'") }
	for i, m := range matches {
		v := seed[i]
		capacity, _ := strconv.Atoi(m[4])
		price, _ := strconv.ParseInt(m[5], 10, 64)
		var tags []string
		for _, tm := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[7], -1) {
			tags = append(tags, tm[1])
		}
		if m[1] != v.ID || unquote(m[2]) != v.Name || m[3] != v.Town || capacity != v.Capacity ||
			price != v.PricePerDayMur || unquote(m[6]) != v.Description || fmt.Sprint(tags) != fmt.Sprint(v.Tags) {
			t.Errorf("row %d differs:\nSQL: %q\nGo:  %+v", i, m[0], v)
		}
	}
}
