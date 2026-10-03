package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a-manraj-infrastream/island-venues-api/internal/store"
)

const secret = "whsec-test"

func seedBooking(t *testing.T, s *store.Memory, id, status string) {
	t.Helper()
	ctx := context.Background()
	b := store.Booking{
		ID: id, VenueID: "grand-baie-lagoon-deck", VenueName: "Lagoon Deck", Date: "2026-11-0" + id[len(id)-1:],
		Guests: 10, Owner: "alice@example.mu", ContactEmail: "alice@example.mu", Status: store.StatusPending,
		CreatedAt: time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC),
	}
	if err := s.CreateBooking(ctx, b); err != nil {
		t.Fatalf("seed booking: %v", err)
	}
	if status != store.StatusPending {
		all := []string{store.StatusPending, store.StatusApproved, store.StatusPaid}
		if _, err := s.TransitionBooking(ctx, id, all, status); err != nil {
			t.Fatalf("seed status: %v", err)
		}
	}
}

func TestWebhook(t *testing.T) {
	paid := `{"bookingId":"bk-1","status":"PAID"}`
	tests := []struct {
		name       string
		secret     string
		body       string
		sig        string // "" = sign body with secret; "-" = no header
		bookingSt  string
		want       int
		wantStatus string
	}{
		{"valid payment", secret, paid, "", store.StatusPending, http.StatusOK, store.StatusPaid},
		{"valid payment after approval", secret, paid, "", store.StatusApproved, http.StatusOK, store.StatusPaid},
		{"duplicate delivery is idempotent", secret, paid, "", store.StatusPaid, http.StatusOK, store.StatusPaid},
		{"secret unset", "", paid, "-", store.StatusPending, http.StatusServiceUnavailable, store.StatusPending},
		{"secret unset ignores signature", "", paid, Sign([]byte(""), []byte(paid)), store.StatusPending, http.StatusServiceUnavailable, store.StatusPending},
		{"missing signature", secret, paid, "-", store.StatusPending, http.StatusUnauthorized, store.StatusPending},
		{"wrong secret", secret, paid, Sign([]byte("other"), []byte(paid)), store.StatusPending, http.StatusUnauthorized, store.StatusPending},
		{"signature of other body", secret, paid, Sign([]byte(secret), []byte(`{"bookingId":"bk-2","status":"PAID"}`)), store.StatusPending, http.StatusUnauthorized, store.StatusPending},
		{"non-hex signature", secret, paid, "zz-not-hex", store.StatusPending, http.StatusUnauthorized, store.StatusPending},
		{"truncated signature", secret, paid, Sign([]byte(secret), []byte(paid))[:32], store.StatusPending, http.StatusUnauthorized, store.StatusPending},
		{"upper-case hex accepted", secret, paid, strings.ToUpper(Sign([]byte(secret), []byte(paid))), store.StatusPending, http.StatusOK, store.StatusPaid},
		{"signed malformed json", secret, `{"bookingId":`, "", store.StatusPending, http.StatusBadRequest, store.StatusPending},
		{"signed missing booking id", secret, `{"status":"PAID"}`, "", store.StatusPending, http.StatusBadRequest, store.StatusPending},
		{"signed other status", secret, `{"bookingId":"bk-1","status":"REFUNDED"}`, "", store.StatusPending, http.StatusBadRequest, store.StatusPending},
		{"signed unknown booking", secret, `{"bookingId":"bk-9","status":"PAID"}`, "", store.StatusPending, http.StatusNotFound, store.StatusPending},
		{"signed but booking cancelled", secret, paid, "", store.StatusCancelled, http.StatusConflict, store.StatusCancelled},
		{"signed but booking rejected", secret, paid, "", store.StatusRejected, http.StatusConflict, store.StatusRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := store.NewMemory()
			seedBooking(t, s, "bk-1", tt.bookingSt)
			h := NewHandler(tt.secret, s, slog.New(slog.NewTextHandler(io.Discard, nil)))

			req := httptest.NewRequest(http.MethodPost, "/webhooks/payments", strings.NewReader(tt.body))
			switch tt.sig {
			case "":
				req.Header.Set(HeaderSignature, Sign([]byte(tt.secret), []byte(tt.body)))
			case "-":
			default:
				req.Header.Set(HeaderSignature, tt.sig)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.want, rec.Body.String())
			}
			if tt.want != http.StatusOK {
				var e map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e["error"] == "" {
					t.Fatalf("error body = %s", rec.Body.String())
				}
			}
			b, err := s.GetBooking(context.Background(), "bk-1")
			if err != nil {
				t.Fatal(err)
			}
			if b.Status != tt.wantStatus {
				t.Fatalf("booking status = %s, want %s", b.Status, tt.wantStatus)
			}
		})
	}
}

func TestWebhookBodyTooLarge(t *testing.T) {
	h := NewHandler(secret, store.NewMemory(), nil)
	body := bytes.Repeat([]byte("a"), maxBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/payments", bytes.NewReader(body))
	req.Header.Set(HeaderSignature, Sign([]byte(secret), body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestVerify(t *testing.T) {
	body := []byte("payload")
	good := Sign([]byte("k"), body)
	tests := []struct {
		name   string
		secret string
		sig    string
		want   bool
	}{
		{"good", "k", good, true},
		{"good with whitespace", "k", " " + good + "\n", true},
		{"empty secret never verifies", "", Sign(nil, body), false},
		{"empty signature", "k", "", false},
		{"wrong key", "k2", good, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Verify([]byte(tt.secret), body, tt.sig); got != tt.want {
				t.Fatalf("Verify() = %v, want %v", got, tt.want)
			}
		})
	}
}
