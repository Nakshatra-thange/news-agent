package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

// FeedbackStore is what the feedback endpoints and the personalized feed
// need.
type FeedbackStore interface {
	SetFeedback(ctx context.Context, itemID uuid.UUID, v domain.FeedbackValue) (domain.Feedback, error)
	DeleteFeedback(ctx context.Context, itemID uuid.UUID) error
	ListFeedback(ctx context.Context, limit int) ([]domain.Feedback, error)
	FeedbackSignals(ctx context.Context) (domain.FeedbackSignals, error)
}

type feedbackJSON struct {
	ItemID    uuid.UUID `json:"item_id"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toFeedbackJSON(f domain.Feedback) feedbackJSON {
	return feedbackJSON{ItemID: f.ItemID, Value: string(f.Value), CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt}
}

type putFeedbackRequest struct {
	Value string `json:"value"`
}

// Feedback listing limits.
const (
	defaultFeedbackLimit = 50
	maxFeedbackLimit     = 500
)

// handlePutFeedback sets the feedback on an item ("more" or "less"),
// replacing any earlier one. Repeating it is a no-op.
func (s *Server) handlePutFeedback(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidID, "item id must be a UUID")
		return
	}
	var req putFeedbackRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	v := domain.FeedbackValue(req.Value)
	if !v.Valid() {
		writeValidationError(w, []domain.FieldError{{Field: "value", Message: `must be "more" or "less"`}})
		return
	}
	f, err := s.feedback.SetFeedback(r.Context(), id, v)
	if err != nil {
		s.writeServiceError(w, r, "item", err)
		return
	}
	writeJSON(w, http.StatusOK, toFeedbackJSON(f))
}

// handleDeleteFeedback removes the feedback on an item. It succeeds whether
// or not there was any.
func (s *Server) handleDeleteFeedback(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidID, "item id must be a UUID")
		return
	}
	if err := s.feedback.DeleteFeedback(r.Context(), id); err != nil {
		s.writeServiceError(w, r, "item", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListFeedback lists feedback, most recently changed first.
// ?limit= 1-500, default 50.
func (s *Server) handleListFeedback(w http.ResponseWriter, r *http.Request) {
	limit := defaultFeedbackLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxFeedbackLimit {
			writeQueryError(w, "limit", "must be an integer between 1 and 500")
			return
		}
		limit = n
	}
	list, err := s.feedback.ListFeedback(r.Context(), limit)
	if err != nil {
		s.writeServiceError(w, r, "feedback", err)
		return
	}
	out := struct {
		Feedback []feedbackJSON `json:"feedback"`
	}{Feedback: make([]feedbackJSON, len(list))}
	for i, f := range list {
		out.Feedback[i] = toFeedbackJSON(f)
	}
	writeJSON(w, http.StatusOK, out)
}
