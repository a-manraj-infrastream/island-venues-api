package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/api/idtoken"
)

const aud = "/projects/123/global/backendServices/456"

// fakeVerifier returns a fixed payload or error and records the audience
// it was asked to check.
type fakeVerifier struct {
	payload *idtoken.Payload
	err     error
	gotAud  string
	calls   int
}

func (f *fakeVerifier) Validate(_ context.Context, _ string, audience string) (*idtoken.Payload, error) {
	f.calls++
	f.gotAud = audience
	return f.payload, f.err
}

func iapPayload(claims map[string]any) *idtoken.Payload {
	return &idtoken.Payload{Issuer: IAPIssuer, Audience: aud, Claims: claims}
}

func TestResolverModes(t *testing.T) {
	tests := []struct {
		name     string
		audience string
		dev      bool
		wantMode Mode
	}{
		{"none", "", false, ModeDisabled},
		{"dev", "", true, ModeDev},
		{"iap", aud, false, ModeIAP},
		{"iap wins over dev", aud, true, ModeIAP},
		{"blank list is not iap", " , ", true, ModeDev},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewResolver(Options{IAPAudience: tt.audience, DevMode: tt.dev, Verifier: &fakeVerifier{}}).Mode(); got != tt.wantMode {
				t.Fatalf("Mode() = %v, want %v", got, tt.wantMode)
			}
		})
	}
}

func TestCaller(t *testing.T) {
	tests := []struct {
		name     string
		audience string
		dev      bool
		verifier *fakeVerifier
		headers  map[string]string
		want     string
		wantErr  bool
	}{
		{
			name: "iap email claim", audience: aud,
			verifier: &fakeVerifier{payload: iapPayload(map[string]any{"email": "Alice@Example.MU"})},
			headers:  map[string]string{HeaderIAPAssertion: "tok"},
			want:     "alice@example.mu",
		},
		{
			name: "identity platform gcip.email wins", audience: aud,
			verifier: &fakeVerifier{payload: iapPayload(map[string]any{
				"email": "securetoken.google.com/proj:uid", "gcip": map[string]any{"email": "bob@example.mu"},
			})},
			headers: map[string]string{HeaderIAPAssertion: "tok"},
			want:    "bob@example.mu",
		},
		{
			name: "gcip without email falls back to email", audience: aud,
			verifier: &fakeVerifier{payload: iapPayload(map[string]any{"email": "carol@example.mu", "gcip": map[string]any{"sub": "x"}})},
			headers:  map[string]string{HeaderIAPAssertion: "tok"},
			want:     "carol@example.mu",
		},
		{
			name: "iap missing header", audience: aud,
			verifier: &fakeVerifier{payload: iapPayload(map[string]any{"email": "a@b.c"})},
			wantErr:  true,
		},
		{
			name: "iap invalid token", audience: aud,
			verifier: &fakeVerifier{err: errors.New("idtoken: invalid signature")},
			headers:  map[string]string{HeaderIAPAssertion: "tok"},
			wantErr:  true,
		},
		{
			// A service account can mint a Google-signed ID token for any
			// audience; it must not pass as an IAP assertion.
			name: "google id token with matching audience is not IAP", audience: aud,
			verifier: &fakeVerifier{payload: &idtoken.Payload{Issuer: "https://accounts.google.com", Audience: aud, Claims: map[string]any{"email": "sa@proj.iam.gserviceaccount.com"}}},
			headers:  map[string]string{HeaderIAPAssertion: "tok"},
			wantErr:  true,
		},
		{
			name: "audience mismatch", audience: aud,
			verifier: &fakeVerifier{payload: &idtoken.Payload{Issuer: IAPIssuer, Audience: "/projects/9/global/backendServices/9", Claims: map[string]any{"email": "a@b.c"}}},
			headers:  map[string]string{HeaderIAPAssertion: "tok"},
			wantErr:  true,
		},
		{
			name: "no email claim", audience: aud,
			verifier: &fakeVerifier{payload: iapPayload(map[string]any{"sub": "123"})},
			headers:  map[string]string{HeaderIAPAssertion: "tok"},
			wantErr:  true,
		},
		{
			name: "nil payload", audience: aud,
			verifier: &fakeVerifier{},
			headers:  map[string]string{HeaderIAPAssertion: "tok"},
			wantErr:  true,
		},
		{
			name: "iap mode ignores dev header", audience: aud, dev: true,
			verifier: &fakeVerifier{err: errors.New("no token")},
			headers:  map[string]string{HeaderDevUser: "mallory@example.mu"},
			wantErr:  true,
		},
		{
			name: "dev header", dev: true,
			headers: map[string]string{HeaderDevUser: " Dev@Example.MU "},
			want:    "dev@example.mu",
		},
		{
			name: "dev missing header", dev: true,
			wantErr: true,
		},
		{
			name:    "disabled ignores every header",
			headers: map[string]string{HeaderDevUser: "a@b.c", "X-Goog-Authenticated-User-Email": "accounts.google.com:a@b.c", HeaderIAPAssertion: "tok"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v Verifier
			if tt.verifier != nil {
				v = tt.verifier
			}
			r := NewResolver(Options{IAPAudience: tt.audience, DevMode: tt.dev, Verifier: v})
			req := httptest.NewRequest(http.MethodGet, "/api/bookings", nil)
			for k, val := range tt.headers {
				req.Header.Set(k, val)
			}
			got, err := r.Caller(req)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Caller() = %q, want error", got)
				}
				if !errors.Is(err, ErrUnauthenticated) {
					t.Fatalf("error %v does not wrap ErrUnauthenticated", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Caller() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Caller() = %q, want %q", got, tt.want)
			}
			if tt.audience != "" && tt.verifier.gotAud != tt.audience {
				t.Fatalf("verifier audience = %q, want %q", tt.verifier.gotAud, tt.audience)
			}
		})
	}
}

// TestAudiences covers several IAP backends in front of one service and the
// optional staff audience that pins staff routes to the staff backend.
func TestAudiences(t *testing.T) {
	const (
		public = "/projects/1/global/backendServices/10"
		staff  = "/projects/1/global/backendServices/20"
		other  = "/projects/1/global/backendServices/99"
	)
	tests := []struct {
		name      string
		opts      Options
		tokenAud  string
		staffCall bool
		wantErr   bool
		wantPass  string // audience handed to the verifier
	}{
		{"single audience passed to verifier", Options{IAPAudience: public}, public, false, false, public},
		{"list accepts first", Options{IAPAudience: public + "," + staff}, public, false, false, ""},
		{"list accepts second", Options{IAPAudience: public + ", " + staff}, staff, false, false, ""},
		{"list rejects others", Options{IAPAudience: public + "," + staff}, other, false, true, ""},
		{"staff defaults to iap audience", Options{IAPAudience: public}, public, true, false, public},
		{"staff audience accepted on staff route", Options{IAPAudience: public, StaffAudience: staff}, staff, true, false, staff},
		{"customer token refused on staff route", Options{IAPAudience: public, StaffAudience: staff}, public, true, true, staff},
		{"staff token refused on customer route", Options{IAPAudience: public, StaffAudience: staff}, staff, false, true, public},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &fakeVerifier{payload: &idtoken.Payload{Issuer: IAPIssuer, Audience: tt.tokenAud, Claims: map[string]any{"email": "a@b.c"}}}
			tt.opts.Verifier = v
			r := NewResolver(tt.opts)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(HeaderIAPAssertion, "tok")
			resolve := r.Caller
			if tt.staffCall {
				resolve = r.StaffCaller
			}
			_, err := resolve(req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if v.gotAud != tt.wantPass {
				t.Fatalf("verifier got audience %q, want %q", v.gotAud, tt.wantPass)
			}
		})
	}
}

func TestDevModeStaffCaller(t *testing.T) {
	r := NewResolver(Options{DevMode: true})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderDevUser, "ops@example.mu")
	if got, err := r.StaffCaller(req); err != nil || got != "ops@example.mu" {
		t.Fatalf("StaffCaller() = %q, %v", got, err)
	}
}

func TestNilResolver(t *testing.T) {
	var r *Resolver
	if _, err := r.Caller(httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("nil resolver error = %v", err)
	}
	if _, err := r.StaffCaller(httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("nil resolver staff error = %v", err)
	}
	if r.Mode() != ModeDisabled {
		t.Fatalf("nil resolver mode = %v", r.Mode())
	}
}
