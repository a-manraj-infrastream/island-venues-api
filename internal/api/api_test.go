package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/idtoken"

	"github.com/a-manraj-infrastream/island-venues-api/internal/events"
	"github.com/a-manraj-infrastream/island-venues-api/internal/identity"
	"github.com/a-manraj-infrastream/island-venues-api/internal/store"
	"github.com/a-manraj-infrastream/island-venues-api/internal/webhook"
)

// fixedNow is 14:00 in Mauritius on 2026-10-03.
var fixedNow = time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)

const (
	venueID  = "grand-baie-lagoon-deck" // capacity 120
	alice    = "alice@example.mu"
	bob      = "bob@example.mu"
	staff    = "ops@example.mu"
	tomorrow = "2026-10-04"
)

type fakePublisher struct {
	mu     sync.Mutex
	events []events.BookingCreated
	err    error
	// ctxErrs records ctx.Err() as seen by each publish call.
	ctxErrs []error
}

func (p *fakePublisher) PublishBookingCreated(ctx context.Context, e events.BookingCreated) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ctxErrs = append(p.ctxErrs, ctx.Err())
	if p.err != nil {
		return p.err
	}
	p.events = append(p.events, e)
	return nil
}

func (p *fakePublisher) Close() error { return nil }

func (p *fakePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

type harness struct {
	t       *testing.T
	handler http.Handler
	store   *store.Memory
	pub     *fakePublisher
	ids     int
}

type harnessOpts struct {
	resolver *identity.Resolver
	now      time.Time
	pubErr   error
	secret   string
}

func newHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()
	if o.resolver == nil {
		o.resolver = identity.NewResolver(identity.Options{DevMode: true})
	}
	if o.now.IsZero() {
		o.now = fixedNow
	}
	h := &harness{t: t, store: store.NewMemory(), pub: &fakePublisher{err: o.pubErr}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(Deps{
		Store:     h.store,
		Publisher: h.pub,
		Identity:  o.resolver,
		Webhook:   webhook.NewHandler(o.secret, h.store, logger),
		Logger:    logger,
		Now:       func() time.Time { return o.now },
		NewID: func() (string, error) {
			h.ids++
			return fmt.Sprintf("bk-%03d", h.ids), nil
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	h.handler = srv.Handler()
	return h
}

// do sends a request as user (empty = anonymous) and returns the recorder.
func (h *harness) do(method, path, user string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			h.t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, r)
	if user != "" {
		req.Header.Set(identity.HeaderDevUser, user)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *harness) book(user, venue, date string, guests int) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPost, "/api/bookings", user, map[string]any{
		"venueId": venue, "date": date, "guests": guests, "contactEmail": "guest@example.mu",
	})
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, want, rec.Body.String())
	}
}

// expectError checks the {"error": "..."} shape on non-2xx responses.
func expectError(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	expectStatus(t, rec, want)
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	body := decode[map[string]string](t, rec)
	if body["error"] == "" {
		t.Fatalf("error body = %s, want non-empty error field", rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	rec := h.do(http.MethodGet, "/healthz", "", nil)
	expectStatus(t, rec, http.StatusOK)
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q, want ok", rec.Body.String())
	}
}

func TestCatalog(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	tests := []struct {
		name string
		path string
		want int
	}{
		{"list", "/api/catalog/venues", http.StatusOK},
		{"known venue", "/api/catalog/venues/" + venueID, http.StatusOK},
		{"unknown venue", "/api/catalog/venues/atlantis", http.StatusNotFound},
		{"unknown route", "/api/nope", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.do(http.MethodGet, tt.path, "", nil)
			if tt.want != http.StatusOK {
				expectError(t, rec, tt.want)
				return
			}
			expectStatus(t, rec, tt.want)
		})
	}

	venues := decode[[]store.Venue](t, h.do(http.MethodGet, "/api/catalog/venues", "", nil))
	if len(venues) != 12 {
		t.Fatalf("len(venues) = %d, want 12", len(venues))
	}
	v := decode[store.Venue](t, h.do(http.MethodGet, "/api/catalog/venues/"+venueID, "", nil))
	if v.Town != "Grand Baie" || v.Capacity != 120 || v.PricePerDayMur <= 0 {
		t.Fatalf("venue = %+v", v)
	}
}

func TestCreateBookingValidation(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{"venueId": venueID, "date": tomorrow, "guests": 50, "contactEmail": "alice@example.mu"}
	}
	with := func(k string, v any) map[string]any {
		b := valid()
		b[k] = v
		return b
	}
	tests := []struct {
		name string
		user string
		body any
		now  time.Time
		want int
	}{
		{"valid", alice, valid(), time.Time{}, http.StatusCreated},
		{"today is allowed", alice, with("date", "2026-10-03"), time.Time{}, http.StatusCreated},
		{"guests at capacity", alice, with("guests", 120), time.Time{}, http.StatusCreated},
		{"anonymous", "", valid(), time.Time{}, http.StatusUnauthorized},
		{"malformed json", alice, `{"venueId":`, time.Time{}, http.StatusBadRequest},
		{"trailing data", alice, `{"venueId":"x"} {}`, time.Time{}, http.StatusBadRequest},
		{"missing venue", alice, with("venueId", ""), time.Time{}, http.StatusBadRequest},
		{"unknown venue", alice, with("venueId", "atlantis"), time.Time{}, http.StatusBadRequest},
		{"bad date format", alice, with("date", "04/10/2026"), time.Time{}, http.StatusBadRequest},
		{"unpadded date", alice, with("date", "2026-10-4"), time.Time{}, http.StatusBadRequest},
		{"impossible date", alice, with("date", "2026-02-30"), time.Time{}, http.StatusBadRequest},
		{"past date", alice, with("date", "2026-10-02"), time.Time{}, http.StatusBadRequest},
		{"too far ahead", alice, with("date", "2030-01-01"), time.Time{}, http.StatusBadRequest},
		// 21:30 UTC on the 2nd is 01:30 on the 3rd in Mauritius: the 2nd is
		// already past for a venue on the island even though UTC disagrees.
		{"past in Mauritius time", alice, with("date", "2026-10-02"), time.Date(2026, 10, 2, 21, 30, 0, 0, time.UTC), http.StatusBadRequest},
		{"zero guests", alice, with("guests", 0), time.Time{}, http.StatusBadRequest},
		{"negative guests", alice, with("guests", -3), time.Time{}, http.StatusBadRequest},
		{"over capacity", alice, with("guests", 121), time.Time{}, http.StatusBadRequest},
		{"missing email", alice, with("contactEmail", ""), time.Time{}, http.StatusBadRequest},
		{"invalid email", alice, with("contactEmail", "not-an-email"), time.Time{}, http.StatusBadRequest},
		{"display name email", alice, with("contactEmail", "Alice <alice@example.mu>"), time.Time{}, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{now: tt.now})
			rec := h.do(http.MethodPost, "/api/bookings", tt.user, tt.body)
			if tt.want != http.StatusCreated {
				expectError(t, rec, tt.want)
				if h.pub.count() != 0 {
					t.Fatalf("published %d events for a rejected booking", h.pub.count())
				}
				return
			}
			expectStatus(t, rec, tt.want)
			b := decode[store.Booking](t, rec)
			if b.Status != store.StatusPending || b.Owner != alice || b.VenueName != "Lagoon Deck" || b.ID == "" {
				t.Fatalf("booking = %+v", b)
			}
			if h.pub.count() != 1 {
				t.Fatalf("published %d events, want 1", h.pub.count())
			}
			e := h.pub.events[0]
			if e.BookingID != b.ID || e.VenueID != venueID || e.VenueName != b.VenueName || e.Date != b.Date ||
				e.Guests != b.Guests || e.Owner != alice || e.ContactEmail != b.ContactEmail || !e.CreatedAt.Equal(b.CreatedAt) {
				t.Fatalf("event = %+v, booking = %+v", e, b)
			}
		})
	}
}

func TestEventPayloadFieldNames(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	expectStatus(t, h.book(alice, venueID, tomorrow, 10), http.StatusCreated)
	raw, err := json.Marshal(h.pub.events[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"bookingId", "venueId", "venueName", "date", "guests", "owner", "contactEmail", "createdAt"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("event payload missing %q: %s", k, raw)
		}
	}
	if len(fields) != 8 {
		t.Errorf("event payload has %d fields, want 8: %s", len(fields), raw)
	}
}

func TestPublishFailureStillCreatesBooking(t *testing.T) {
	h := newHarness(t, harnessOpts{pubErr: errors.New("pubsub down")})
	expectStatus(t, h.book(alice, venueID, tomorrow, 10), http.StatusCreated)
	all, _ := h.store.ListBookings(context.Background(), "")
	if len(all) != 1 {
		t.Fatalf("stored %d bookings, want 1", len(all))
	}
}

// TestPublishSurvivesClientDisconnect: once the booking is stored, a client
// that hangs up must not cancel the booking-created publish, or the booking
// would exist without a confirmation e-mail.
func TestPublishSurvivesClientDisconnect(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	raw, err := json.Marshal(map[string]any{
		"venueId": venueID, "date": "2026-12-01", "guests": 10, "contactEmail": "guest@example.mu",
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client is already gone when the handler publishes
	req := httptest.NewRequest(http.MethodPost, "/api/bookings", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set(identity.HeaderDevUser, "alice@example.mu")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	expectStatus(t, rec, http.StatusCreated)

	h.pub.mu.Lock()
	defer h.pub.mu.Unlock()
	if len(h.pub.ctxErrs) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(h.pub.ctxErrs))
	}
	if h.pub.ctxErrs[0] != nil {
		t.Fatalf("publish context error = %v, want nil (request cancellation must not reach the publisher)", h.pub.ctxErrs[0])
	}
}

// TestSignInBounce: the web app's Sign in button navigates here (an IAP path)
// and must land back on the app, never on a caller-chosen location.
func TestSignInBounce(t *testing.T) {
	h := newHarness(t, harnessOpts{resolver: identity.NewResolver(identity.Options{})})
	for _, path := range []string{"/api/bookings/sign-in", "/api/bookings/sign-in?next=https://evil.example"} {
		rec := h.do(http.MethodGet, path, "", nil)
		expectStatus(t, rec, http.StatusSeeOther)
		if got := rec.Header().Get("Location"); got != "/" {
			t.Fatalf("GET %s Location = %q, want /", path, got)
		}
	}
	// Other methods on the path are still the cancel route, which needs a caller.
	expectError(t, h.do(http.MethodDelete, "/api/bookings/sign-in", "", nil), http.StatusUnauthorized)
}

func TestDoubleBooking(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	// lastID is the id of the most recent successful booking; rejected
	// attempts also consume ids, so steps never hard-code them.
	var lastID string
	steps := []struct {
		name string
		run  func() *httptest.ResponseRecorder
		want int
	}{
		{"first booking", func() *httptest.ResponseRecorder { return h.book(alice, venueID, tomorrow, 10) }, http.StatusCreated},
		{"same venue same date", func() *httptest.ResponseRecorder { return h.book(bob, venueID, tomorrow, 10) }, http.StatusConflict},
		{"same owner same date", func() *httptest.ResponseRecorder { return h.book(alice, venueID, tomorrow, 10) }, http.StatusConflict},
		{"other venue same date", func() *httptest.ResponseRecorder { return h.book(bob, "curepipe-colonial-hall", tomorrow, 10) }, http.StatusCreated},
		{"same venue other date", func() *httptest.ResponseRecorder { return h.book(bob, venueID, "2026-10-05", 10) }, http.StatusCreated},
		{"approve first", func() *httptest.ResponseRecorder {
			return h.do(http.MethodPost, "/api/admin/bookings/bk-001/approve", staff, nil)
		}, http.StatusOK},
		{"approved still holds date", func() *httptest.ResponseRecorder { return h.book(bob, venueID, tomorrow, 10) }, http.StatusConflict},
		{"owner cancels first", func() *httptest.ResponseRecorder {
			return h.do(http.MethodDelete, "/api/bookings/bk-001", alice, nil)
		}, http.StatusOK},
		{"cancelled releases date", func() *httptest.ResponseRecorder { return h.book(bob, venueID, tomorrow, 10) }, http.StatusCreated},
		{"staff rejects it", func() *httptest.ResponseRecorder {
			return h.do(http.MethodPost, "/api/admin/bookings/"+lastID+"/reject", staff, nil)
		}, http.StatusOK},
		{"rejected releases date", func() *httptest.ResponseRecorder { return h.book(alice, venueID, tomorrow, 10) }, http.StatusCreated},
	}
	for _, s := range steps {
		rec := s.run()
		if rec.Code != s.want {
			t.Fatalf("%s: status = %d, want %d; body = %s", s.name, rec.Code, s.want, rec.Body.String())
		}
		if rec.Code == http.StatusCreated {
			lastID = decode[store.Booking](t, rec).ID
		}
	}
}

func TestOwnerChecks(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	expectStatus(t, h.book(alice, venueID, tomorrow, 10), http.StatusCreated)                 // bk-001
	expectStatus(t, h.book(bob, "tamarin-salt-pans-studio", tomorrow, 5), http.StatusCreated) // bk-002

	t.Run("each caller sees only their bookings", func(t *testing.T) {
		for _, tt := range []struct {
			user string
			want string
		}{{alice, "bk-001"}, {bob, "bk-002"}, {"carol@example.mu", ""}} {
			list := decode[[]store.Booking](t, h.do(http.MethodGet, "/api/bookings", tt.user, nil))
			if tt.want == "" {
				if len(list) != 0 {
					t.Fatalf("%s sees %d bookings, want 0", tt.user, len(list))
				}
				continue
			}
			if len(list) != 1 || list[0].ID != tt.want || list[0].Owner != tt.user {
				t.Fatalf("%s sees %+v, want only %s", tt.user, list, tt.want)
			}
		}
	})

	t.Run("owner identity is case-insensitive", func(t *testing.T) {
		list := decode[[]store.Booking](t, h.do(http.MethodGet, "/api/bookings", "ALICE@Example.MU", nil))
		if len(list) != 1 {
			t.Fatalf("got %d bookings, want 1", len(list))
		}
	})

	tests := []struct {
		name   string
		user   string
		id     string
		want   int
		status string
	}{
		{"anonymous cannot cancel", "", "bk-001", http.StatusUnauthorized, store.StatusPending},
		{"other user cannot cancel", bob, "bk-001", http.StatusForbidden, store.StatusPending},
		{"unknown booking", alice, "bk-999", http.StatusNotFound, ""},
		{"owner cancels", alice, "bk-001", http.StatusOK, store.StatusCancelled},
		{"cancel twice", alice, "bk-001", http.StatusConflict, store.StatusCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.do(http.MethodDelete, "/api/bookings/"+tt.id, tt.user, nil)
			if tt.want == http.StatusOK {
				expectStatus(t, rec, tt.want)
				if b := decode[store.Booking](t, rec); b.Status != store.StatusCancelled {
					t.Fatalf("status = %s, want CANCELLED", b.Status)
				}
			} else {
				expectError(t, rec, tt.want)
			}
			if tt.status != "" {
				b, err := h.store.GetBooking(context.Background(), tt.id)
				if err != nil || b.Status != tt.status {
					t.Fatalf("stored status = %q (err %v), want %q", b.Status, err, tt.status)
				}
			}
		})
	}
}

func TestListRequiresIdentity(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	for _, p := range []string{"/api/bookings", "/api/admin/bookings"} {
		expectError(t, h.do(http.MethodGet, p, "", nil), http.StatusUnauthorized)
	}
}

func TestAdminTransitions(t *testing.T) {
	tests := []struct {
		name       string
		setup      []string // actions applied to bk-001 first: approve, reject, cancel, pay
		user       string
		action     string
		id         string
		want       int
		wantStatus string
	}{
		{"approve pending", nil, staff, "approve", "bk-001", http.StatusOK, store.StatusApproved},
		{"reject pending", nil, staff, "reject", "bk-001", http.StatusOK, store.StatusRejected},
		{"approve twice", []string{"approve"}, staff, "approve", "bk-001", http.StatusConflict, store.StatusApproved},
		{"reject approved", []string{"approve"}, staff, "reject", "bk-001", http.StatusConflict, store.StatusApproved},
		{"approve rejected", []string{"reject"}, staff, "approve", "bk-001", http.StatusConflict, store.StatusRejected},
		{"approve cancelled", []string{"cancel"}, staff, "approve", "bk-001", http.StatusConflict, store.StatusCancelled},
		{"reject cancelled", []string{"cancel"}, staff, "reject", "bk-001", http.StatusConflict, store.StatusCancelled},
		{"approve paid", []string{"pay"}, staff, "approve", "bk-001", http.StatusConflict, store.StatusPaid},
		{"approve unknown", nil, staff, "approve", "bk-404", http.StatusNotFound, ""},
		{"anonymous approve", nil, "", "approve", "bk-001", http.StatusUnauthorized, store.StatusPending},
		{"anonymous reject", nil, "", "reject", "bk-001", http.StatusUnauthorized, store.StatusPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{})
			expectStatus(t, h.book(alice, venueID, tomorrow, 10), http.StatusCreated)
			ctx := context.Background()
			for _, a := range tt.setup {
				var err error
				switch a {
				case "approve":
					_, err = h.store.TransitionBooking(ctx, "bk-001", store.ApproveFrom, store.StatusApproved)
				case "reject":
					_, err = h.store.TransitionBooking(ctx, "bk-001", store.RejectFrom, store.StatusRejected)
				case "cancel":
					_, err = h.store.TransitionBooking(ctx, "bk-001", store.CancelFrom, store.StatusCancelled)
				case "pay":
					_, err = h.store.TransitionBooking(ctx, "bk-001", store.PayFrom, store.StatusPaid)
				}
				if err != nil {
					t.Fatalf("setup %s: %v", a, err)
				}
			}
			rec := h.do(http.MethodPost, "/api/admin/bookings/"+tt.id+"/"+tt.action, tt.user, nil)
			if tt.want == http.StatusOK {
				expectStatus(t, rec, tt.want)
			} else {
				expectError(t, rec, tt.want)
			}
			if tt.wantStatus != "" {
				b, _ := h.store.GetBooking(ctx, tt.id)
				if b.Status != tt.wantStatus {
					t.Fatalf("stored status = %s, want %s", b.Status, tt.wantStatus)
				}
			}
		})
	}
}

func TestAdminListBookings(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	expectStatus(t, h.book(alice, venueID, tomorrow, 10), http.StatusCreated)
	expectStatus(t, h.book(bob, "tamarin-salt-pans-studio", tomorrow, 5), http.StatusCreated)
	expectStatus(t, h.do(http.MethodPost, "/api/admin/bookings/bk-002/approve", staff, nil), http.StatusOK)

	tests := []struct {
		name  string
		query string
		want  int
		ids   int
	}{
		{"all", "", http.StatusOK, 2},
		{"pending", "?status=PENDING", http.StatusOK, 1},
		{"approved lower-case", "?status=approved", http.StatusOK, 1},
		{"paid", "?status=PAID", http.StatusOK, 0},
		{"unknown status", "?status=LOST", http.StatusBadRequest, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.do(http.MethodGet, "/api/admin/bookings"+tt.query, staff, nil)
			if tt.want != http.StatusOK {
				expectError(t, rec, tt.want)
				return
			}
			expectStatus(t, rec, tt.want)
			if list := decode[[]store.Booking](t, rec); len(list) != tt.ids {
				t.Fatalf("got %d bookings, want %d", len(list), tt.ids)
			}
		})
	}
}

func TestAdminCreateVenue(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{
			"name": "Rivière Noire Boathouse", "town": "Black River", "capacity": 40,
			"pricePerDayMur": 30000, "description": "Boathouse by the river mouth.", "tags": []string{"River", " heritage "},
		}
	}
	with := func(k string, v any) map[string]any {
		b := valid()
		b[k] = v
		return b
	}
	tests := []struct {
		name string
		user string
		body any
		want int
	}{
		{"valid", staff, valid(), http.StatusCreated},
		{"anonymous", "", valid(), http.StatusUnauthorized},
		{"malformed", staff, `{"name":`, http.StatusBadRequest},
		{"missing name", staff, with("name", "  "), http.StatusBadRequest},
		{"missing town", staff, with("town", ""), http.StatusBadRequest},
		{"zero capacity", staff, with("capacity", 0), http.StatusBadRequest},
		{"negative price", staff, with("pricePerDayMur", -1), http.StatusBadRequest},
		{"long name", staff, with("name", strings.Repeat("x", 121)), http.StatusBadRequest},
		{"empty tag", staff, with("tags", []string{""}), http.StatusBadRequest},
		{"too many tags", staff, with("tags", make([]string, 21)), http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{})
			rec := h.do(http.MethodPost, "/api/admin/venues", tt.user, tt.body)
			if tt.want != http.StatusCreated {
				expectError(t, rec, tt.want)
				return
			}
			expectStatus(t, rec, tt.want)
			v := decode[store.Venue](t, rec)
			if v.ID != "black-river-riviere-noire-boathouse" {
				t.Fatalf("id = %q", v.ID)
			}
			if len(v.Tags) != 2 || v.Tags[0] != "river" || v.Tags[1] != "heritage" {
				t.Fatalf("tags = %q", v.Tags)
			}
			// The new venue is public and bookable.
			expectStatus(t, h.do(http.MethodGet, "/api/catalog/venues/"+v.ID, "", nil), http.StatusOK)
			expectStatus(t, h.book(alice, v.ID, tomorrow, 40), http.StatusCreated)
			// The same name in the same town is a conflict.
			expectError(t, h.do(http.MethodPost, "/api/admin/venues", staff, tt.body), http.StatusConflict)
		})
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Grand Baie Lagoon Deck", "grand-baie-lagoon-deck"},
		{"Mahébourg  Old -- Boathouse!", "mahebourg-old-boathouse"},
		{"  ***  ", ""},
		{"Île aux Cerfs", "ile-aux-cerfs"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := slugify(tt.in); got != tt.want {
				t.Fatalf("slugify(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// fakeIAP accepts tokens of the form "valid:<email>" as IAP assertions.
func fakeIAP(_ context.Context, token, audience string) (*idtoken.Payload, error) {
	email, ok := strings.CutPrefix(token, "valid:")
	if !ok {
		return nil, errors.New("bad token")
	}
	return &idtoken.Payload{Issuer: identity.IAPIssuer, Audience: audience, Claims: map[string]any{"email": email}}, nil
}

// TestIdentityModesThroughHandlers drives the real routes with each identity
// mode, so a regression in how handlers consult the resolver is caught here
// and not only in the identity package's unit tests.
func TestIdentityModesThroughHandlers(t *testing.T) {
	const aud = "/projects/1/global/backendServices/2"
	iap := identity.NewResolver(identity.Options{IAPAudience: aud, Verifier: identity.VerifierFunc(fakeIAP)})
	iapAndDev := identity.NewResolver(identity.Options{IAPAudience: aud, DevMode: true, Verifier: identity.VerifierFunc(fakeIAP)})
	dev := identity.NewResolver(identity.Options{DevMode: true})
	off := identity.NewResolver(identity.Options{})

	tests := []struct {
		name     string
		resolver *identity.Resolver
		headers  map[string]string
		want     int
	}{
		{"iap valid", iap, map[string]string{identity.HeaderIAPAssertion: "valid:" + alice}, http.StatusOK},
		{"iap invalid", iap, map[string]string{identity.HeaderIAPAssertion: "forged"}, http.StatusUnauthorized},
		{"iap missing", iap, nil, http.StatusUnauthorized},
		{"iap ignores dev header", iapAndDev, map[string]string{identity.HeaderDevUser: alice}, http.StatusUnauthorized},
		{"iap ignores unsigned email header", iap, map[string]string{"X-Goog-Authenticated-User-Email": "accounts.google.com:" + alice}, http.StatusUnauthorized},
		{"dev header", dev, map[string]string{identity.HeaderDevUser: alice}, http.StatusOK},
		{"dev missing header", dev, nil, http.StatusUnauthorized},
		{"disabled", off, map[string]string{identity.HeaderDevUser: alice}, http.StatusUnauthorized},
		{"disabled ignores unsigned email header", off, map[string]string{"X-Goog-Authenticated-User-Email": "accounts.google.com:" + alice}, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{resolver: tt.resolver})
			for _, path := range []string{"/api/bookings", "/api/admin/bookings"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				for k, v := range tt.headers {
					req.Header.Set(k, v)
				}
				rec := httptest.NewRecorder()
				h.handler.ServeHTTP(rec, req)
				if rec.Code != tt.want {
					t.Fatalf("%s: status = %d, want %d", path, rec.Code, tt.want)
				}
			}
		})
	}

	t.Run("iap identity owns the booking", func(t *testing.T) {
		h := newHarness(t, harnessOpts{resolver: iap})
		body := `{"venueId":"` + venueID + `","date":"` + tomorrow + `","guests":3,"contactEmail":"a@example.mu"}`
		req := httptest.NewRequest(http.MethodPost, "/api/bookings", strings.NewReader(body))
		req.Header.Set(identity.HeaderIAPAssertion, "valid:Alice@Example.mu")
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		expectStatus(t, rec, http.StatusCreated)
		if b := decode[store.Booking](t, rec); b.Owner != alice {
			t.Fatalf("owner = %q, want %q", b.Owner, alice)
		}
	})
}

// fakeIAPAud accepts "valid|<aud>|<email>" and reports that audience, so a
// test can present a token minted for a specific IAP backend.
func fakeIAPAud(_ context.Context, token, _ string) (*idtoken.Payload, error) {
	parts := strings.SplitN(token, "|", 3)
	if len(parts) != 3 || parts[0] != "valid" {
		return nil, errors.New("bad token")
	}
	return &idtoken.Payload{Issuer: identity.IAPIssuer, Audience: parts[1], Claims: map[string]any{"email": parts[2]}}, nil
}

// TestStaffAudience proves staff routes, when IAP_STAFF_AUDIENCE is set,
// refuse a customer's assertion even though it is validly signed.
func TestStaffAudience(t *testing.T) {
	const public, staffAud = "/projects/1/global/backendServices/10", "/projects/1/global/backendServices/20"
	r := identity.NewResolver(identity.Options{IAPAudience: public, StaffAudience: staffAud, Verifier: identity.VerifierFunc(fakeIAPAud)})
	tests := []struct {
		name  string
		path  string
		token string
		want  int
	}{
		{"customer on customer route", "/api/bookings", "valid|" + public + "|" + alice, http.StatusOK},
		{"customer on staff route", "/api/admin/bookings", "valid|" + public + "|" + alice, http.StatusUnauthorized},
		{"staff on staff route", "/api/admin/bookings", "valid|" + staffAud + "|" + staff, http.StatusOK},
		{"staff token on customer route", "/api/bookings", "valid|" + staffAud + "|" + staff, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{resolver: r})
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set(identity.HeaderIAPAssertion, tt.token)
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestWebhookRoute(t *testing.T) {
	h := newHarness(t, harnessOpts{secret: "s3cret"})
	expectStatus(t, h.book(alice, venueID, tomorrow, 10), http.StatusCreated)
	body := []byte(`{"bookingId":"bk-001","status":"PAID"}`)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/payments", bytes.NewReader(body))
	req.Header.Set(webhook.HeaderSignature, webhook.Sign([]byte("s3cret"), body))
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	expectStatus(t, rec, http.StatusOK)

	mine := decode[[]store.Booking](t, h.do(http.MethodGet, "/api/bookings", alice, nil))
	if len(mine) != 1 || mine[0].Status != store.StatusPaid {
		t.Fatalf("bookings = %+v, want one PAID", mine)
	}
	// A paid booking cannot be cancelled by its owner.
	expectError(t, h.do(http.MethodDelete, "/api/bookings/bk-001", alice, nil), http.StatusConflict)
}

func TestNewRequiresDeps(t *testing.T) {
	r := identity.NewResolver(identity.Options{DevMode: true})
	tests := []struct {
		name string
		deps Deps
	}{
		{"no store", Deps{Publisher: &fakePublisher{}, Identity: r}},
		{"no publisher", Deps{Store: store.NewMemory(), Identity: r}},
		{"no identity", Deps{Store: store.NewMemory(), Publisher: &fakePublisher{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.deps); err == nil {
				t.Fatal("New() error = nil, want error")
			}
		})
	}
}
