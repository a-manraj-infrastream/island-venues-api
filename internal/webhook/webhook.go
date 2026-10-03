// Package webhook receives payment notifications.
//
// The route is public at the edge (the payment provider cannot sign in), so
// authenticity is established in code: the provider signs the raw request
// body with HMAC-SHA256 using a shared secret and sends the hex digest in
// X-Signature.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-manraj-infrastream/island-venues-api/internal/httpx"
	"github.com/a-manraj-infrastream/island-venues-api/internal/store"
)

const (
	// HeaderSignature carries the hex HMAC-SHA256 of the raw body.
	HeaderSignature = "X-Signature"
	// maxBodyBytes bounds how much is read before the signature is checked.
	maxBodyBytes = 64 << 10
)

// Payment is the webhook payload.
type Payment struct {
	BookingID string `json:"bookingId"`
	Status    string `json:"status"`
}

// Handler serves POST /webhooks/payments.
type Handler struct {
	secret []byte
	store  store.Store
	logger *slog.Logger
}

// NewHandler returns a webhook handler. An empty secret is allowed so the
// service can start, but every request is then refused with 503: accepting
// unsigned payment notifications would let anyone mark bookings paid.
func NewHandler(secret string, s store.Store, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{secret: []byte(secret), store: s, logger: logger}
}

// Sign returns the hex HMAC-SHA256 of body under secret.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether signature is the hex HMAC-SHA256 of body. The
// comparison is constant-time to avoid leaking the expected digest.
func Verify(secret, body []byte, signature string) bool {
	if len(secret) == 0 {
		return false
	}
	got, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// ServeHTTP verifies the signature, then marks the booking PAID.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if len(h.secret) == 0 {
		h.logger.ErrorContext(ctx, "payment webhook called but PAYMENT_WEBHOOK_SECRET is not set")
		httpx.WriteError(w, r, h.logger, http.StatusServiceUnavailable, "payment webhook not configured")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.WriteError(w, r, h.logger, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		httpx.WriteError(w, r, h.logger, http.StatusBadRequest, "failed to read request body")
		return
	}

	// Authenticate before parsing: nothing from an unsigned body is trusted
	// or even decoded.
	if !Verify(h.secret, body, r.Header.Get(HeaderSignature)) {
		h.logger.WarnContext(ctx, "payment webhook signature rejected")
		httpx.WriteError(w, r, h.logger, http.StatusUnauthorized, "invalid signature")
		return
	}

	var p Payment
	if err := json.Unmarshal(body, &p); err != nil {
		httpx.WriteError(w, r, h.logger, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(p.BookingID) == "" {
		httpx.WriteError(w, r, h.logger, http.StatusBadRequest, "bookingId is required")
		return
	}
	if p.Status != store.StatusPaid {
		httpx.WriteError(w, r, h.logger, http.StatusBadRequest, `status must be "PAID"`)
		return
	}

	b, err := h.store.TransitionBooking(ctx, p.BookingID, store.PayFrom, store.StatusPaid)
	switch {
	case err == nil:
		h.logger.InfoContext(ctx, "booking marked paid", slog.String("bookingId", b.ID))
		httpx.WriteJSON(w, r, h.logger, http.StatusOK, b)
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, r, h.logger, http.StatusNotFound, "booking not found")
	case errors.Is(err, store.ErrInvalidTransition) && b.Status == store.StatusPaid:
		// Providers retry deliveries; a repeat of an applied payment is a
		// success, not an error, or the provider would retry forever.
		httpx.WriteJSON(w, r, h.logger, http.StatusOK, b)
	case errors.Is(err, store.ErrInvalidTransition):
		httpx.WriteError(w, r, h.logger, http.StatusConflict, "booking cannot be paid in status "+b.Status)
	default:
		h.logger.ErrorContext(ctx, "failed to mark booking paid", slog.String("bookingId", p.BookingID), slog.Any("error", err))
		httpx.WriteError(w, r, h.logger, http.StatusInternalServerError, "internal error")
	}
}
