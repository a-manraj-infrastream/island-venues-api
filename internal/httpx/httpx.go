// Package httpx holds the JSON response helpers shared by the API and the
// webhook so every error has the same {"error": "..."} shape.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ErrorBody is the JSON shape of every error response.
type ErrorBody struct {
	Error string `json:"error"`
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil && logger != nil {
		logger.WarnContext(r.Context(), "failed to write response body", slog.Any("error", err))
	}
}

// WriteError writes {"error": msg} with the given status.
func WriteError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, msg string) {
	WriteJSON(w, r, logger, status, ErrorBody{Error: msg})
}
