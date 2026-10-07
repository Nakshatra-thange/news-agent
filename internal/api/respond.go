package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"synergy/internal/domain"
	"synergy/internal/ingest"
)

// Error codes returned in the "code" field of error responses. Clients should
// branch on these, never on the human-readable message.
const (
	codeNotFound             = "not_found"
	codeMethodNotAllowed     = "method_not_allowed"
	codeInternal             = "internal"
	codeInvalidJSON          = "invalid_json"
	codeInvalidQuery         = "invalid_query"
	codeInvalidID            = "invalid_id"
	codeValidationFailed     = "validation_failed"
	codeConflict             = "conflict"
	codeUnsupportedMediaType = "unsupported_media_type"
	codePayloadTooLarge      = "payload_too_large"
	codeTooManyRequests      = "too_many_requests"
	codeNotImplemented       = "not_implemented"
	codeUnavailable          = "unavailable"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details lists per-field problems for validation errors.
	Details []domain.FieldError `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent; the client most likely went away.
		slog.Debug("write json response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

func writeValidationError(w http.ResponseWriter, fields []domain.FieldError) {
	writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: errorDetail{
		Code: codeValidationFailed, Message: "request validation failed", Details: fields,
	}})
}

// writeServiceError maps errors from services onto HTTP responses. resource
// names the thing being acted on ("source") for not-found messages.
// Unexpected errors are logged with the request ID and reported as a generic
// 500 so internals never leak to clients.
func (s *Server) writeServiceError(w http.ResponseWriter, r *http.Request, resource string, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidationError(w, ve.Fields)
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, resource+" not found")
	case errors.Is(err, domain.ErrTooSoon):
		var ce *domain.CooldownError
		if errors.As(err, &ce) {
			secs := int64((ce.Remaining + time.Second - 1) / time.Second) // round up
			w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
		}
		writeError(w, http.StatusTooManyRequests, codeTooManyRequests, err.Error()+"; add ?force=true to override")
	case errors.Is(err, domain.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, codeNotImplemented, strings.TrimPrefix(err.Error(), domain.ErrUnsupported.Error()+": "))
	case errors.Is(err, ingest.ErrClosed):
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "the server is shutting down")
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrFetchInProgress):
		writeError(w, http.StatusConflict, codeConflict, strings.TrimPrefix(err.Error(), domain.ErrConflict.Error()+": "))
	case errors.Is(err, domain.ErrInvalid):
		// Rejected by a database constraint that Go validation did not catch.
		s.logger.WarnContext(r.Context(), "value rejected by database", "request_id", RequestIDFromContext(r.Context()), "err", err)
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "a value was rejected by the database")
	default:
		s.logger.ErrorContext(r.Context(), "request failed",
			"request_id", RequestIDFromContext(r.Context()), "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal server error")
	}
}
