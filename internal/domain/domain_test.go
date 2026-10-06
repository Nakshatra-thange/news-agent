package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

func validNewSource() NewSource {
	return NewSource{Slug: "hn-ai", Name: "Hacker News AI", Type: SourceTypeHackerNews}
}

func TestNewSourceNormalizeAppliesDefaults(t *testing.T) {
	n := NewSource{Slug: "  hn-ai ", Name: " HN ", Type: SourceTypeHackerNews}
	n.Normalize()

	if n.Slug != "hn-ai" || n.Name != "HN" {
		t.Errorf("not trimmed: slug=%q name=%q", n.Slug, n.Name)
	}
	if n.Status != SourceStatusActive {
		t.Errorf("Status = %q, want active", n.Status)
	}
	if string(n.Config) != "{}" {
		t.Errorf("Config = %s, want {}", n.Config)
	}
	if n.MinFetchInterval == nil || *n.MinFetchInterval != DefaultMinFetchInterval {
		t.Errorf("MinFetchInterval = %v, want %v", n.MinFetchInterval, DefaultMinFetchInterval)
	}
	if err := n.Validate(); err != nil {
		t.Errorf("Validate after Normalize: %v", err)
	}
}

func TestNewSourceValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NewSource)
		field  string // expected invalid field, "" for valid
	}{
		{"valid", func(*NewSource) {}, ""},
		{"valid with url", func(n *NewSource) { n.URL = "https://news.ycombinator.com" }, ""},
		{"slug uppercase", func(n *NewSource) { n.Slug = "HN" }, "slug"},
		{"slug leading hyphen", func(n *NewSource) { n.Slug = "-hn" }, "slug"},
		{"slug too long", func(n *NewSource) { n.Slug = strings.Repeat("a", 64) }, "slug"},
		{"slug empty", func(n *NewSource) { n.Slug = "" }, "slug"},
		{"name empty", func(n *NewSource) { n.Name = "" }, "name"},
		{"unknown type", func(n *NewSource) { n.Type = "reddit" }, "type"},
		{"relative url", func(n *NewSource) { n.URL = "/foo" }, "url"},
		{"ftp url", func(n *NewSource) { n.URL = "ftp://example.com" }, "url"},
		{"bad status", func(n *NewSource) { n.Status = "deleted" }, "status"},
		{"config array", func(n *NewSource) { n.Config = json.RawMessage(`[]`) }, "config"},
		{"config null", func(n *NewSource) { n.Config = json.RawMessage(`null`) }, "config"},
		{"config malformed", func(n *NewSource) { n.Config = json.RawMessage(`{`) }, "config"},
		{"negative interval", func(n *NewSource) { n.MinFetchInterval = ptr(-time.Second) }, "min_fetch_interval"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := validNewSource()
			n.Normalize()
			tt.mutate(&n)
			err := n.Validate()
			if tt.field == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			assertInvalidField(t, err, tt.field)
		})
	}
}

func TestValidationErrorReportsAllFields(t *testing.T) {
	err := NewSource{Config: json.RawMessage(`{}`)}.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error %v is not a ValidationError", err)
	}
	if len(ve.Fields) < 4 {
		t.Errorf("got %d field errors, want slug, name, type and status: %v", len(ve.Fields), ve)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Error("ValidationError does not match ErrInvalid")
	}
}

func TestSourcePatch(t *testing.T) {
	if !(SourcePatch{}).IsEmpty() {
		t.Error("zero patch should be empty")
	}
	p := SourcePatch{Name: ptr("  New name  "), URL: ptr(" ")}
	p.Normalize()
	if *p.Name != "New name" || *p.URL != "" {
		t.Errorf("Normalize: name=%q url=%q", *p.Name, *p.URL)
	}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	assertInvalidField(t, SourcePatch{Name: ptr("")}.Validate(), "name")
	assertInvalidField(t, SourcePatch{Status: ptr(SourceStatus("gone"))}.Validate(), "status")
	assertInvalidField(t, SourcePatch{Config: ptr(json.RawMessage(`"x"`))}.Validate(), "config")
	assertInvalidField(t, SourcePatch{MinFetchInterval: ptr(-time.Minute)}.Validate(), "min_fetch_interval")
}

func TestSourceHealth(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		src  Source
		want Health
	}{
		{"never fetched", Source{}, HealthUnknown},
		{"succeeded", Source{LastSuccessAt: &now}, HealthHealthy},
		{"one failure", Source{LastSuccessAt: &now, ConsecutiveFailures: 1}, HealthDegraded},
		{"below threshold", Source{ConsecutiveFailures: FailingThreshold - 1}, HealthDegraded},
		{"at threshold", Source{ConsecutiveFailures: FailingThreshold}, HealthFailing},
	}
	for _, tt := range tests {
		if got := tt.src.Health(); got != tt.want {
			t.Errorf("%s: Health() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestEnums(t *testing.T) {
	for _, st := range SourceTypes {
		if !st.Valid() {
			t.Errorf("SourceType %q should be valid", st)
		}
	}
	for _, k := range ItemKinds {
		if !k.Valid() {
			t.Errorf("ItemKind %q should be valid", k)
		}
	}
	if SourceType("twitter").Valid() || ItemKind("tweet").Valid() || RunTrigger("cron").Valid() {
		t.Error("unknown enum value reported valid")
	}
	for _, tr := range []RunTrigger{TriggerAPI, TriggerCLI, TriggerScheduler} {
		if !tr.Valid() {
			t.Errorf("RunTrigger %q should be valid", tr)
		}
	}
}

func validNewItem() NewItem {
	h := make([]byte, HashSize)
	return NewItem{
		SourceID: uuid.New(), ExternalID: "123", Kind: ItemKindDiscussion, Title: "Title",
		URL: "https://example.com", CanonicalURL: "https://example.com", URLHash: h, ContentHash: h,
	}
}

func TestNewItemValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NewItem)
		field  string
	}{
		{"valid", func(*NewItem) {}, ""},
		{"valid with metadata", func(n *NewItem) { n.Metadata = json.RawMessage(`{"points":10}`) }, ""},
		{"missing source", func(n *NewItem) { n.SourceID = uuid.Nil }, "source_id"},
		{"blank external id", func(n *NewItem) { n.ExternalID = " " }, "external_id"},
		{"bad kind", func(n *NewItem) { n.Kind = "Paper!" }, "kind"},
		{"blank title", func(n *NewItem) { n.Title = "" }, "title"},
		{"blank url", func(n *NewItem) { n.URL = "" }, "url"},
		{"blank canonical", func(n *NewItem) { n.CanonicalURL = "" }, "canonical_url"},
		{"short url hash", func(n *NewItem) { n.URLHash = []byte{1} }, "url_hash"},
		{"missing content hash", func(n *NewItem) { n.ContentHash = nil }, "content_hash"},
		{"metadata array", func(n *NewItem) { n.Metadata = json.RawMessage(`[1]`) }, "metadata"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := validNewItem()
			tt.mutate(&n)
			err := n.Validate()
			if tt.field == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			assertInvalidField(t, err, tt.field)
		})
	}
}

func TestFetchStatsRecord(t *testing.T) {
	var s FetchStats
	other := uuid.New()
	s.Record(UpsertResult{Outcome: UpsertInserted})
	s.Record(UpsertResult{Outcome: UpsertInserted, DuplicateOf: &other})
	s.Record(UpsertResult{Outcome: UpsertUpdated})
	s.Record(UpsertResult{Outcome: UpsertUnchanged})
	s.Record(UpsertResult{Outcome: UpsertUnchanged})

	want := FetchStats{Inserted: 2, Duplicate: 1, Updated: 1, Unchanged: 2}
	if s != want {
		t.Errorf("stats = %+v, want %+v", s, want)
	}
}

func TestFetchRunDuration(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if d := (FetchRun{StartedAt: start}).Duration(); d != 0 {
		t.Errorf("running Duration = %v, want 0", d)
	}
	end := start.Add(1500 * time.Millisecond)
	if d := (FetchRun{StartedAt: start, FinishedAt: &end}).Duration(); d != 1500*time.Millisecond {
		t.Errorf("Duration = %v, want 1.5s", d)
	}
}

func assertInvalidField(t *testing.T, err error, field string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want ValidationError for %q", err, field)
	}
	for _, f := range ve.Fields {
		if f.Field == field {
			return
		}
	}
	t.Errorf("error %v does not mention field %q", err, field)
}
