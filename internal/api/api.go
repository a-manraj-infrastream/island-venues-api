// Package api implements the island-venues-api HTTP handlers.
//
// Path prefixes match the load balancer's routes one-to-one, and each prefix
// has a protection level enforced at the edge (public, signed-in, staff).
// Handlers still verify the caller themselves via identity.Resolver; the
// edge is a first line of defence, not the only one.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/a-manraj-infrastream/island-venues-api/internal/events"
	"github.com/a-manraj-infrastream/island-venues-api/internal/httpx"
	"github.com/a-manraj-infrastream/island-venues-api/internal/identity"
	"github.com/a-manraj-infrastream/island-venues-api/internal/store"
	"github.com/a-manraj-infrastream/island-venues-api/internal/telemetry"
)

// maxBodyBytes bounds JSON request bodies.
const maxBodyBytes = 64 << 10

// mauritius is Mauritius Time (UTC+4, no daylight saving). A fixed zone
// avoids depending on tzdata being present in the scratch container image.
var mauritius = time.FixedZone("MUT", 4*60*60)

// Deps are the collaborators the API needs. Store, Publisher and Identity
// are required; the rest default sensibly.
type Deps struct {
	Store     store.Store
	Publisher events.Publisher
	Identity  *identity.Resolver
	// Webhook serves POST /webhooks/payments.
	Webhook http.Handler
	Logger  *slog.Logger
	// Now returns the current time; tests pin it.
	Now func() time.Time
	// NewID returns a fresh random identifier; tests may pin it.
	NewID func() (string, error)
}

// Server holds the handlers.
type Server struct {
	store     store.Store
	publisher events.Publisher
	identity  *identity.Resolver
	webhook   http.Handler
	logger    *slog.Logger
	now       func() time.Time
	newID     func() (string, error)
}

// New validates deps and returns a Server.
func New(d Deps) (*Server, error) {
	if d.Store == nil {
		return nil, errors.New("failed to create api server: store is required")
	}
	if d.Publisher == nil {
		return nil, errors.New("failed to create api server: publisher is required")
	}
	if d.Identity == nil {
		return nil, errors.New("failed to create api server: identity resolver is required")
	}
	s := &Server{
		store:     d.Store,
		publisher: d.Publisher,
		identity:  d.Identity,
		webhook:   d.Webhook,
		logger:    d.Logger,
		now:       d.Now,
		newID:     d.NewID,
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.newID == nil {
		s.newID = randomID
	}
	if s.webhook == nil {
		s.webhook = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpx.WriteError(w, r, s.logger, http.StatusServiceUnavailable, "payment webhook not configured")
		})
	}
	return s, nil
}

// Handler returns the routed, instrumented HTTP handler. Every route is
// wrapped individually so its span is named after the route pattern rather
// than the raw URL (which would explode span-name cardinality with ids).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	routes := []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /healthz", s.healthz},
		{"GET /api/catalog/venues", s.listVenues},
		{"GET /api/catalog/venues/{id}", s.getVenue},
		{"POST /api/bookings", s.signedIn(s.createBooking)},
		{"GET /api/bookings", s.signedIn(s.listMyBookings)},
		{"DELETE /api/bookings/{id}", s.signedIn(s.cancelBooking)},
		{"GET /api/bookings/sign-in", s.signInBounce},
		{"GET /api/admin/bookings", s.staffOnly(s.adminListBookings)},
		{"POST /api/admin/bookings/{id}/approve", s.staffOnly(s.adminTransition(store.ApproveFrom, store.StatusApproved))},
		{"POST /api/admin/bookings/{id}/reject", s.staffOnly(s.adminTransition(store.RejectFrom, store.StatusRejected))},
		{"POST /api/admin/venues", s.staffOnly(s.adminCreateVenue)},
		{"POST /webhooks/payments", s.webhook.ServeHTTP},
		{"/", s.notFound},
	}
	for _, rt := range routes {
		mux.Handle(rt.pattern, telemetry.Handler(rt.pattern, rt.handler, s.logger))
	}
	return mux
}

// signedIn verifies the caller before running next.
func (s *Server) signedIn(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return s.authenticated(s.identity.Caller, next)
}

// staffOnly verifies a staff caller. Membership of the venue-ops group is
// enforced by IAP at the edge; the verified assertion proves the request
// crossed it, and with IAP_STAFF_AUDIENCE set it proves it crossed the
// staff backend specifically.
func (s *Server) staffOnly(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return s.authenticated(s.identity.StaffCaller, next)
}

func (s *Server) authenticated(resolve func(*http.Request) (string, error), next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, err := resolve(r)
		if err != nil {
			s.logger.WarnContext(r.Context(), "unauthenticated request", slog.String("path", r.URL.Path), slog.Any("error", err))
			s.writeError(w, r, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r, caller)
	}
}

// SignInLandingPath is where GET /api/bookings/sign-in sends the browser.
const SignInLandingPath = "/"

// signInBounce is the web app's "Sign in" target. The customer app itself is
// served on an open path, so reloading it never makes the edge start a
// sign-in; this path sits under the IAP-protected /api/bookings/ prefix, so a
// top-level navigation here makes IAP (Identity Platform in phase 2) sign the
// visitor in first, after which this handler sends them back to the app,
// which now carries the edge's session cookie.
//
// It verifies nothing on purpose: it only redirects to a fixed same-origin
// path (never to a caller-supplied URL, so it cannot be an open redirect),
// and every data route still verifies the caller itself.
func (s *Server) signInBounce(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, SignInLandingPath, http.StatusSeeOther)
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusNotFound, "not found")
}

func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	httpx.WriteJSON(w, r, s.logger, status, v)
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	httpx.WriteError(w, r, s.logger, status, msg)
}

// internalError logs the cause and returns a generic 500 so storage details
// never leak to clients.
func (s *Server) internalError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	s.logger.ErrorContext(r.Context(), msg, slog.Any("error", err))
	s.writeError(w, r, http.StatusInternalServerError, "internal error")
}

// decodeJSON reads a bounded JSON body into v.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("failed to decode request body: %w", err)
	}
	if dec.More() {
		return errors.New("failed to decode request body: unexpected data after JSON object")
	}
	return nil
}

// randomID returns 16 random bytes as hex.
func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("failed to generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
