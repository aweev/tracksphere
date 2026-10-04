package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/tracksphere/tracksphere/internal/model"
	"github.com/tracksphere/tracksphere/internal/shipments"
)

// writeJSON writes a response with the standard envelope.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.Envelope{Data: data})
}

// writeJSONMeta writes a paginated response.
func writeJSONMeta(w http.ResponseWriter, status int, data any, meta *model.Meta) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.Envelope{Data: data, Meta: meta})
}

// writeError writes a machine-readable error body.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.Envelope{
		Error: &model.APIError{Code: code, Message: message},
	})
}

// decodeJSON reads a bounded JSON body and rejects unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "Invalid request body: "+err.Error())
		return false
	}
	return true
}

// domainError maps domain errors onto HTTP status codes + API error codes.
func (s *Server) domainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, shipments.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Shipment not found")
	case errors.Is(err, shipments.ErrDuplicateTracking):
		writeError(w, http.StatusConflict, "duplicate_tracking", "Tracking number already exists in your organization")
	case errors.Is(err, shipments.ErrAmbiguousTracking):
		writeError(w, http.StatusConflict, "ambiguous_tracking", err.Error())
	case errors.Is(err, shipments.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
	default:
		s.log.Error("internal error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Internal server error")
	}
}

// requestLogger emits one structured log line per request.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		level := slog.LevelInfo
		if ww.Status() >= 500 {
			level = slog.LevelError
		} else if ww.Status() >= 400 {
			level = slog.LevelWarn
		}
		s.log.Log(r.Context(), level, "http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()),
			"remote", r.RemoteAddr,
		)
	})
}