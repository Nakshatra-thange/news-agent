package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

// maxBodyBytes bounds request bodies; source configs are small.
const maxBodyBytes = 1 << 20

// decodeJSON strictly decodes a single JSON object from the request body into
// dst. On failure it writes an error response and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || mt != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "Content-Type must be application/json")
			return false
		}
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		status, msg := describeJSONError(err)
		code := codeInvalidJSON
		if status == http.StatusRequestEntityTooLarge {
			code = codePayloadTooLarge
		}
		writeError(w, status, code, msg)
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body must contain a single JSON object")
		return false
	}
	return true
}

func describeJSONError(err error) (int, string) {
	var (
		maxErr    *http.MaxBytesError
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &maxErr):
		return http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds %d bytes", maxErr.Limit)
	case errors.Is(err, io.EOF):
		return http.StatusBadRequest, "request body is required"
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		return http.StatusBadRequest, "request body is not valid JSON"
	case errors.As(err, &typeErr) && typeErr.Field == "":
		return http.StatusBadRequest, "request body must be a JSON object"
	case errors.As(err, &typeErr):
		return http.StatusBadRequest, fmt.Sprintf("field %q must be %s", typeErr.Field, typeErr.Type)
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		return http.StatusBadRequest, strings.TrimPrefix(err.Error(), "json: ")
	default:
		return http.StatusBadRequest, "invalid request body"
	}
}
