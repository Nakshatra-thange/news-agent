package api

import (
	"context"
	"net/http"

	"synergy/internal/domain"
	"synergy/internal/sources"
)

// PreferenceStore is what the preference endpoints and the personalized
// feed need.
type PreferenceStore interface {
	GetPreferences(ctx context.Context) (domain.Preferences, error)
	SetPreferences(ctx context.Context, p domain.Preferences) (domain.Preferences, error)
}

type preferencesJSON struct {
	Topics  []string         `json:"topics"`
	Sources []itemSourceJSON `json:"sources"`
}

// putPreferencesRequest replaces all preferences; an absent list means
// none. Sources are slugs or UUIDs.
type putPreferencesRequest struct {
	Topics  []string `json:"topics"`
	Sources []string `json:"sources"`
}

// handleGetPreferences returns the topics and sources of interest.
func (s *Server) handleGetPreferences(w http.ResponseWriter, r *http.Request) {
	p, err := s.prefs.GetPreferences(r.Context())
	if err != nil {
		s.writeServiceError(w, r, "preferences", err)
		return
	}
	s.writePreferences(w, r, p)
}

// handlePutPreferences replaces the preferences. Unknown sources and
// out-of-bounds lists are a 422; nothing is changed then.
func (s *Server) handlePutPreferences(w http.ResponseWriter, r *http.Request) {
	var req putPreferencesRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Sources) > domain.MaxPreferredSources {
		writeValidationError(w, []domain.FieldError{{Field: "sources", Message: "too many sources"}})
		return
	}
	fq := feedQuery{sourceRefs: req.Sources}
	if fields := s.resolveSources(r.Context(), &fq); len(fields) > 0 {
		for i := range fields {
			fields[i].Field = "sources"
		}
		writeValidationError(w, fields)
		return
	}
	p, err := s.prefs.SetPreferences(r.Context(), domain.Preferences{Topics: req.Topics, SourceIDs: fq.filter.SourceIDs})
	if err != nil {
		s.writeServiceError(w, r, "preferences", err)
		return
	}
	s.writePreferences(w, r, p)
}

// writePreferences renders preferences with their sources' details, found
// with one source listing.
func (s *Server) writePreferences(w http.ResponseWriter, r *http.Request, p domain.Preferences) {
	all, err := s.sources.List(r.Context(), sources.ListFilter{})
	if err != nil {
		s.writeServiceError(w, r, "source", err)
		return
	}
	byID := make(map[string]domain.Source, len(all))
	for _, src := range all {
		byID[src.ID.String()] = src
	}
	out := preferencesJSON{Topics: orEmpty(p.Topics), Sources: []itemSourceJSON{}}
	for _, id := range p.SourceIDs {
		if src, ok := byID[id.String()]; ok {
			out.Sources = append(out.Sources, itemSourceJSON{ID: src.ID, Slug: src.Slug, Name: src.Name, Type: string(src.Type)})
		}
	}
	writeJSON(w, http.StatusOK, out)
}
