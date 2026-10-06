package arxiv

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"synergy/internal/canon"
	"synergy/internal/domain"
)

const (
	nsAtom       = "http://www.w3.org/2005/Atom"
	nsOpenSearch = "http://a9.com/-/spec/opensearch/1.1/"
	nsArxiv      = "http://arxiv.org/schemas/atom"
)

// feed is an arXiv API response (Atom with OpenSearch and arXiv extensions).
type feed struct {
	XMLName      xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
	TotalResults int      `xml:"http://a9.com/-/spec/opensearch/1.1/ totalResults"`
	StartIndex   int      `xml:"http://a9.com/-/spec/opensearch/1.1/ startIndex"`
	Entries      []entry  `xml:"http://www.w3.org/2005/Atom entry"`
}

type entry struct {
	ID        string `xml:"http://www.w3.org/2005/Atom id"`
	Title     string `xml:"http://www.w3.org/2005/Atom title"`
	Summary   string `xml:"http://www.w3.org/2005/Atom summary"`
	Published string `xml:"http://www.w3.org/2005/Atom published"`
	Updated   string `xml:"http://www.w3.org/2005/Atom updated"`
	Authors   []struct {
		Name string `xml:"http://www.w3.org/2005/Atom name"`
	} `xml:"http://www.w3.org/2005/Atom author"`
	Links []struct {
		Href  string `xml:"href,attr"`
		Rel   string `xml:"rel,attr"`
		Title string `xml:"title,attr"`
	} `xml:"http://www.w3.org/2005/Atom link"`
	Categories []struct {
		Term string `xml:"term,attr"`
	} `xml:"http://www.w3.org/2005/Atom category"`
	PrimaryCategory struct {
		Term string `xml:"term,attr"`
	} `xml:"http://arxiv.org/schemas/atom primary_category"`
	Comment    string `xml:"http://arxiv.org/schemas/atom comment"`
	JournalRef string `xml:"http://arxiv.org/schemas/atom journal_ref"`
	DOI        string `xml:"http://arxiv.org/schemas/atom doi"`
}

// parseFeed decodes a response. arXiv reports query errors as a feed whose
// single entry has an id under /api/errors; those become errors.
func parseFeed(body []byte) (*feed, error) {
	var f feed
	if err := xml.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("malformed arXiv feed: %w", err)
	}
	if len(f.Entries) == 1 && strings.Contains(f.Entries[0].ID, "/api/errors") {
		msg := canon.Text(f.Entries[0].Summary)
		if msg == "" {
			msg = "unspecified error"
		}
		return nil, errors.New("arXiv API error: " + msg)
	}
	return &f, nil
}

var versionRE = regexp.MustCompile(`v(\d+)$`)

// toCandidate maps an entry. It is deliberately lenient: an entry lacking an
// ID or title still becomes a candidate, which the ingestion pipeline then
// rejects and counts, so malformed upstream data is visible in fetch stats.
// The returned time is the submission date (nil if unparseable).
func toCandidate(e entry) (domain.Candidate, *time.Time) {
	id, _ := canon.ParseArxivID(e.ID)
	version := ""
	if m := versionRE.FindStringSubmatch(strings.TrimSpace(e.ID)); m != nil {
		version = "v" + m[1]
	}
	published := parseTime(e.Published)
	updated := parseTime(e.Updated)

	var authors []string
	for _, a := range e.Authors {
		authors = append(authors, a.Name)
	}
	// Primary category first, then the cross-lists.
	tags := []string{}
	if p := strings.TrimSpace(e.PrimaryCategory.Term); p != "" {
		tags = append(tags, p)
	}
	for _, c := range e.Categories {
		if t := strings.TrimSpace(c.Term); t != "" && !contains(tags, t) {
			tags = append(tags, t)
		}
	}

	pdf := ""
	for _, l := range e.Links {
		if l.Title == "pdf" {
			pdf = l.Href
		}
	}
	meta := map[string]any{"primary_category": e.PrimaryCategory.Term}
	for k, v := range map[string]string{
		"version": version, "pdf_url": pdf, "comment": canon.Text(e.Comment),
		"journal_ref": canon.Text(e.JournalRef), "doi": strings.TrimSpace(e.DOI),
	} {
		if v != "" {
			meta[k] = v
		}
	}
	if updated != nil {
		meta["updated_at"] = updated.Format(time.RFC3339)
	}
	metaJSON, _ := json.Marshal(meta)

	url := ""
	if id != "" {
		url = "https://arxiv.org/abs/" + id
	}
	return domain.Candidate{
		ExternalID:  id,
		Kind:        domain.ItemKindPaper,
		Title:       e.Title,
		Description: e.Summary,
		URL:         url,
		Authors:     authors,
		Tags:        tags,
		Metadata:    metaJSON,
		PublishedAt: published,
	}, published
}

func parseTime(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
