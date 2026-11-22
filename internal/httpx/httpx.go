// Package httpx has the small HTTP helpers shared by the JSON API and the web
// UI: JSON encoding and mapping domain errors to status codes.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// ErrorBody matches the Error schema in openapi.yaml.
type ErrorBody struct {
	Error    string   `json:"error"`
	Message  string   `json:"message,omitempty"`
	Problems []string `json:"problems,omitempty"`
}

// Status maps a service error to an HTTP status and error code.
func Status(err error) (int, string) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, domain.ErrUnauthorized):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, domain.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, domain.ErrValidation):
		return http.StatusUnprocessableEntity, "invalid_input"
	case errors.Is(err, domain.ErrConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, domain.ErrGeneration):
		return http.StatusBadGateway, "generation_failed"
	case errors.Is(err, domain.ErrNotImplemented):
		return http.StatusNotImplemented, "not_implemented"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json", "err", err)
	}
}

// WriteError writes err as an ErrorBody. Internal errors are logged and their
// message is hidden from the client.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := Status(err)
	body := ErrorBody{Error: code}
	if status == http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	} else {
		body.Message = err.Error()
	}
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		body.Problems = ve.Problems
	}
	WriteJSON(w, status, body)
}

// DecodeJSON reads a JSON body (max 1 MiB), rejecting unknown fields.
func DecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &domain.ValidationError{Problems: []string{"body: " + err.Error()}}
	}
	return nil
}
