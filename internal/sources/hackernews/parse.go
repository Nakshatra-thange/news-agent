package hackernews

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"synergy/internal/domain"
)

// item is a Hacker News API item. Every field may be absent.
type item struct {
	ID          int64  `json:"id"`
	Deleted     bool   `json:"deleted"`
	Dead        bool   `json:"dead"`
	Type        string `json:"type"`
	By          string `json:"by"`
	Time        int64  `json:"time"`
	Text        string `json:"text"`
	URL         string `json:"url"`
	Score       int    `json:"score"`
	Title       string `json:"title"`
	Descendants int    `json:"descendants"`
}

// parseList decodes a story-ID list such as topstories.json.
func parseList(body []byte) ([]int64, error) {
	var ids []int64
	if err := json.Unmarshal(body, &ids); err != nil {
		return nil, fmt.Errorf("malformed story list: %w", err)
	}
	return ids, nil
}

// parseItem decodes one item. A JSON null (unknown or purged ID) yields nil.
func parseItem(body []byte) (*item, error) {
	var it *item
	if err := json.Unmarshal(body, &it); err != nil {
		return nil, fmt.Errorf("malformed item: %w", err)
	}
	return it, nil
}

// skipReason explains why an item is not a candidate, or "" if it is.
func skipReason(it *item, cfg Config, cutoff time.Time) string {
	switch {
	case it == nil:
		return "missing"
	case it.Deleted:
		return "deleted"
	case it.Dead:
		return "dead"
	case it.Type != "story":
		return "not a story (" + it.Type + ")" // comment, job, poll, pollopt
	case strings.TrimSpace(it.Title) == "":
		return "no title"
	case it.Time == 0 || time.Unix(it.Time, 0).Before(cutoff):
		return "too old"
	case it.Score < cfg.MinPoints:
		return "too few points"
	case !matchesAny(it.Title, cfg.Queries):
		return "no keyword match"
	}
	return ""
}

// itemURL is the story's page on Hacker News.
func itemURL(id int64) string {
	return "https://news.ycombinator.com/item?id=" + strconv.FormatInt(id, 10)
}

// toCandidate maps a story. Link posts point at their target; text posts
// (Ask HN and the like) point at their discussion page. The discussion page
// is always kept, so the HN signal is never lost when the link is a
// duplicate of another source's item.
func toCandidate(it *item, lists []string) domain.Candidate {
	disc := itemURL(it.ID)
	link := strings.TrimSpace(it.URL)
	if link == "" {
		link = disc
	}
	published := time.Unix(it.Time, 0).UTC()
	meta, _ := json.Marshal(map[string]any{
		"points":       it.Score,
		"num_comments": it.Descendants,
		"hn_lists":     lists,
	})
	var authors []string
	if it.By != "" {
		authors = []string{it.By}
	}
	return domain.Candidate{
		ExternalID:    strconv.FormatInt(it.ID, 10),
		Kind:          domain.ItemKindDiscussion,
		Title:         html.UnescapeString(it.Title),
		Description:   htmlToText(it.Text),
		URL:           link,
		DiscussionURL: disc,
		Authors:       authors,
		Tags:          postTags(it.Title),
		Metadata:      meta,
		PublishedAt:   &published,
	}
}

// postTags labels HN's conventional post types.
func postTags(title string) []string {
	t := strings.ToLower(strings.TrimSpace(title))
	for prefix, tag := range map[string]string{"ask hn:": "ask_hn", "show hn:": "show_hn", "launch hn:": "launch_hn", "tell hn:": "tell_hn"} {
		if strings.HasPrefix(t, prefix) {
			return []string{tag}
		}
	}
	return nil
}

// matchesAny reports whether any keyword occurs in title, case-insensitively,
// starting at a word boundary.
func matchesAny(title string, keywords []string) bool {
	lt := strings.ToLower(title)
	for _, kw := range keywords {
		k := strings.ToLower(strings.TrimSpace(kw))
		if k == "" {
			continue
		}
		for from := 0; from < len(lt); {
			i := strings.Index(lt[from:], k)
			if i < 0 {
				break
			}
			pos := from + i
			if pos == 0 {
				return true
			}
			prev, _ := utf8.DecodeLastRuneInString(lt[:pos])
			if !unicode.IsLetter(prev) && !unicode.IsDigit(prev) {
				return true
			}
			from = pos + 1
		}
	}
	return false
}

var (
	tagRE       = regexp.MustCompile(`<[^>]*>`)
	paragraphRE = regexp.MustCompile(`(?i)<p\s*/?>|<br\s*/?>`)
)

// htmlToText turns HN's limited HTML (p, a, i, pre, code) into plain text.
func htmlToText(s string) string {
	if s == "" {
		return ""
	}
	s = paragraphRE.ReplaceAllString(s, "\n")
	s = tagRE.ReplaceAllString(s, "")
	return html.UnescapeString(s)
}
