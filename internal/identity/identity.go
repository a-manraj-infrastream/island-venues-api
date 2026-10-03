// Package identity resolves the caller of a request.
//
// The load balancer authenticates users (IAP for staff and, in phase 1, for
// customers; Identity Platform behind IAP in phase 2), but the service still
// verifies the signed assertion itself: anything that reaches the container
// without passing the edge (a misconfigured ingress, a direct *.run.app URL)
// must not be able to claim an identity by setting a header.
package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"google.golang.org/api/idtoken"
)

const (
	// HeaderIAPAssertion carries the IAP-signed JWT.
	HeaderIAPAssertion = "X-Goog-IAP-JWT-Assertion"
	// HeaderDevUser names the caller in DEV_MODE (local development only).
	HeaderDevUser = "X-Dev-User"
	// IAPIssuer is the only issuer accepted for IAP assertions.
	IAPIssuer = "https://cloud.google.com/iap"
)

// ErrUnauthenticated is returned when the caller cannot be identified.
var ErrUnauthenticated = errors.New("unauthenticated")

// Verifier validates a Google-signed JWT for an audience. It matches
// idtoken.Validate so tests can substitute a fake without network access.
type Verifier interface {
	Validate(ctx context.Context, token, audience string) (*idtoken.Payload, error)
}

// VerifierFunc adapts a function to the Verifier interface.
type VerifierFunc func(ctx context.Context, token, audience string) (*idtoken.Payload, error)

// Validate calls f.
func (f VerifierFunc) Validate(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
	return f(ctx, token, audience)
}

// GoogleVerifier verifies tokens against Google's public keys via
// idtoken.Validate (ES256 keys for IAP, RS256 for Google ID tokens).
var GoogleVerifier Verifier = VerifierFunc(idtoken.Validate)

// Mode is how callers are identified.
type Mode int

const (
	// ModeDisabled rejects every signed-in request.
	ModeDisabled Mode = iota
	// ModeIAP verifies the IAP JWT assertion.
	ModeIAP
	// ModeDev trusts the X-Dev-User header. Local development only.
	ModeDev
)

func (m Mode) String() string {
	switch m {
	case ModeIAP:
		return "iap"
	case ModeDev:
		return "dev"
	default:
		return "disabled"
	}
}

// Options configure a Resolver.
type Options struct {
	// IAPAudience is IAP_AUDIENCE: one audience, or several separated by
	// commas when the load balancer fronts the service with more than one
	// IAP-protected backend service (each has its own audience
	// /projects/<number>/global/backendServices/<id>).
	IAPAudience string
	// StaffAudience is IAP_STAFF_AUDIENCE (optional, comma-separated). When
	// set, staff routes accept only assertions minted for these audiences,
	// i.e. only requests that crossed the staff backend whose IAP policy is
	// restricted to the venue-ops group. When unset, staff routes accept
	// IAP_AUDIENCE and group membership rests on the edge alone.
	StaffAudience string
	// DevMode is DEV_MODE: trust X-Dev-User. Ignored when IAP is configured.
	DevMode bool
	// Verifier checks JWTs; nil means GoogleVerifier.
	Verifier Verifier
}

// Resolver identifies callers according to the configured mode.
type Resolver struct {
	mode           Mode
	audiences      []string
	staffAudiences []string
	verifier       Verifier
}

// NewResolver picks the mode with the precedence the contract defines:
// an IAP audience always wins over DEV_MODE, so turning DEV_MODE on in an
// environment that also has IAP configured can never weaken verification.
func NewResolver(o Options) *Resolver {
	audiences := splitList(o.IAPAudience)
	switch {
	case len(audiences) > 0:
		verifier := o.Verifier
		if verifier == nil {
			verifier = GoogleVerifier
		}
		staff := splitList(o.StaffAudience)
		if len(staff) == 0 {
			staff = audiences
		}
		return &Resolver{mode: ModeIAP, audiences: audiences, staffAudiences: staff, verifier: verifier}
	case o.DevMode:
		return &Resolver{mode: ModeDev}
	default:
		return &Resolver{mode: ModeDisabled}
	}
}

// Mode reports the active mode.
func (r *Resolver) Mode() Mode {
	if r == nil {
		return ModeDisabled
	}
	return r.mode
}

// Caller returns the verified, lower-cased e-mail of a signed-in caller, or
// an error wrapping ErrUnauthenticated. X-Goog-Authenticated-User-Email is
// never read: it is unsigned and therefore spoofable by anything that
// bypasses the edge.
func (r *Resolver) Caller(req *http.Request) (string, error) {
	if r == nil {
		return "", fmt.Errorf("failed to resolve caller: no resolver: %w", ErrUnauthenticated)
	}
	return r.resolve(req, r.audiences)
}

// StaffCaller is Caller for staff routes: in IAP mode the assertion must be
// minted for a staff audience (see Options.StaffAudience).
func (r *Resolver) StaffCaller(req *http.Request) (string, error) {
	if r == nil {
		return "", fmt.Errorf("failed to resolve staff caller: no resolver: %w", ErrUnauthenticated)
	}
	return r.resolve(req, r.staffAudiences)
}

func (r *Resolver) resolve(req *http.Request, audiences []string) (string, error) {
	if req == nil {
		return "", fmt.Errorf("failed to resolve caller: no request: %w", ErrUnauthenticated)
	}
	switch r.mode {
	case ModeIAP:
		return r.fromIAP(req, audiences)
	case ModeDev:
		user := normalise(req.Header.Get(HeaderDevUser))
		if user == "" {
			return "", fmt.Errorf("failed to resolve caller: missing %s header: %w", HeaderDevUser, ErrUnauthenticated)
		}
		return user, nil
	default:
		return "", fmt.Errorf("failed to resolve caller: no identity provider configured: %w", ErrUnauthenticated)
	}
}

func (r *Resolver) fromIAP(req *http.Request, audiences []string) (string, error) {
	token := strings.TrimSpace(req.Header.Get(HeaderIAPAssertion))
	if token == "" {
		return "", fmt.Errorf("failed to resolve caller: missing %s header: %w", HeaderIAPAssertion, ErrUnauthenticated)
	}
	// With a single audience idtoken.Validate checks it too. With several,
	// pass "" (signature and expiry are still verified) and check
	// membership below, which runs in both cases.
	expected := ""
	if len(audiences) == 1 {
		expected = audiences[0]
	}
	payload, err := r.verifier.Validate(req.Context(), token, expected)
	if err != nil {
		return "", fmt.Errorf("failed to verify IAP assertion: %w: %w", ErrUnauthenticated, err)
	}
	if payload == nil {
		return "", fmt.Errorf("failed to verify IAP assertion: empty payload: %w", ErrUnauthenticated)
	}
	// idtoken.Validate checks signature, audience and expiry but not the
	// issuer. Any service account can mint a Google-signed ID token for an
	// arbitrary audience, so without this check such a token would be
	// accepted as an IAP assertion carrying the service account's e-mail.
	if payload.Issuer != IAPIssuer {
		return "", fmt.Errorf("failed to verify IAP assertion: unexpected issuer %q: %w", payload.Issuer, ErrUnauthenticated)
	}
	if !slices.Contains(audiences, payload.Audience) {
		return "", fmt.Errorf("failed to verify IAP assertion: audience %q not accepted on this route: %w", payload.Audience, ErrUnauthenticated)
	}
	email := emailFromClaims(payload.Claims)
	if email == "" {
		return "", fmt.Errorf("failed to verify IAP assertion: no email claim: %w", ErrUnauthenticated)
	}
	return email, nil
}

// splitList parses a comma-separated list, dropping blanks.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// emailFromClaims prefers the Identity Platform e-mail (gcip.email) when IAP
// fronts an Identity Platform tenant, then falls back to the top-level email
// claim IAP sets for Google identities.
func emailFromClaims(claims map[string]any) string {
	if gcip, ok := claims["gcip"].(map[string]any); ok {
		if email, ok := gcip["email"].(string); ok && normalise(email) != "" {
			return normalise(email)
		}
	}
	if email, ok := claims["email"].(string); ok {
		return normalise(email)
	}
	return ""
}

func normalise(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
