package enrich

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"unicode"

	"synergy/internal/domain"
)

// FakeProvider is a deterministic, offline Provider. It derives a valid
// answer from the prompt alone (tags become topics, capitalized title words
// become entities; summaries restate the title and first sentence), so the
// same item always gets the same result. It is
// for tests and for exercising the pipeline before a real provider exists;
// its output is labelled provider "fake" wherever it is stored.
type FakeProvider struct{}

// Name implements Provider.
func (FakeProvider) Name() string { return "fake" }

// Model implements Provider.
func (FakeProvider) Model() string { return "fake-1" }

var fakeCategories = map[string]string{
	"paper": "research", "repository": "tool", "discussion": "industry", "release": "model_release",
}

// Complete implements Provider.
func (FakeProvider) Complete(ctx context.Context, p Prompt) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.System == summarySystemPrompt {
		return fakeSummary(p.User), nil
	}
	fields := map[string]string{}
	for line := range strings.Lines(p.User) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ": "); ok {
			if _, seen := fields[k]; !seen {
				fields[k] = v
			}
		}
	}

	var topics []string
	for t := range strings.SplitSeq(fields["Tags"], ",") {
		if t = strings.TrimSpace(t); t != "" && len([]rune(t)) <= domain.MaxTopicLen && len(topics) < 5 {
			topics = append(topics, t)
		}
	}
	if len(topics) == 0 {
		topics = []string{"ai"}
	}

	type entity struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	entities := []entity{}
	for _, w := range strings.Fields(fields["Title"]) {
		w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if len([]rune(w)) > 2 && unicode.IsUpper([]rune(w)[0]) && len(entities) < 5 {
			entities = append(entities, entity{Name: w, Type: "other"})
		}
	}

	h := fnv.New32a()
	h.Write([]byte(fields["Title"]))
	category, ok := fakeCategories[fields["Kind"]]
	if !ok {
		category = "other"
	}
	out, err := json.Marshal(map[string]any{
		"topics":     topics,
		"entities":   entities,
		"importance": 1 + int(h.Sum32()%5),
		"category":   category,
	})
	return string(out), err
}

var fakeKindNouns = map[string]string{
	"paper": "A paper", "repository": "A repository", "discussion": "A discussion", "release": "A release",
}

// fakeSummary restates the item: its kind and title, then the first
// sentence of its description, bounded to the summary limits.
func fakeSummary(user string) string {
	var kind, title string
	var desc []string
	inDesc := false
	for line := range strings.Lines(user) {
		line = strings.TrimSpace(line)
		switch {
		case line == "</item>":
			inDesc = false
		case inDesc:
			desc = append(desc, line)
		case line == "Description:":
			inDesc = true
		case strings.HasPrefix(line, "Kind: ") && kind == "":
			kind = strings.TrimPrefix(line, "Kind: ")
		case strings.HasPrefix(line, "Title: ") && title == "":
			title = strings.TrimPrefix(line, "Title: ")
		}
	}
	noun, ok := fakeKindNouns[kind]
	if !ok {
		noun = "An item"
	}
	out := fmt.Sprintf("%s titled “%s”.", noun, title)
	if first, _, _ := strings.Cut(strings.Join(desc, " "), ". "); strings.TrimSpace(first) != "" {
		out += " " + strings.TrimSuffix(strings.TrimSpace(first), ".") + "."
	}
	if r := []rune(out); len(r) > domain.MaxSummaryLen {
		out = string(r[:domain.MaxSummaryLen-1]) + "…"
	}
	for len([]rune(out)) < domain.MinSummaryLen {
		out += " No further details were given."
	}
	return out
}
