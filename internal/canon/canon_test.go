package canon

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCanonicalize(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		// Generic web URLs.
		{"already canonical", "https://example.com/post", "https://example.com/post"},
		{"http becomes https", "http://example.com/post", "https://example.com/post"},
		{"scheme and host case", "HTTPS://Example.COM/Post", "https://example.com/Post"},
		{"www stripped", "https://www.example.com/a", "https://example.com/a"},
		{"trailing dot host", "https://example.com./a", "https://example.com/a"},
		{"default https port", "https://example.com:443/a", "https://example.com/a"},
		{"default http port", "http://example.com:80/a", "https://example.com/a"},
		{"custom port kept", "https://example.com:8443/a", "https://example.com:8443/a"},
		{"credentials removed", "https://user:pw@example.com/a", "https://example.com/a"},
		{"fragment removed", "https://example.com/a#section-2", "https://example.com/a"},
		{"trailing slash", "https://example.com/a/", "https://example.com/a"},
		{"root slash", "https://example.com/", "https://example.com"},
		{"bare host", "https://example.com", "https://example.com"},
		{"duplicate slashes", "https://example.com//a///b", "https://example.com/a/b"},
		{"dot segments", "https://example.com/a/./b/../c", "https://example.com/a/c"},
		{"whitespace trimmed", "  https://example.com/a  ", "https://example.com/a"},
		{"path case kept", "https://example.com/CaseSensitive", "https://example.com/CaseSensitive"},
		{"encoded space", "https://example.com/a%20b", "https://example.com/a%20b"},
		{"unicode path", "https://example.com/über", "https://example.com/%C3%BCber"},

		// Query parameters.
		{"utm removed", "https://example.com/a?utm_source=hn&utm_medium=social", "https://example.com/a"},
		{"utm case-insensitive", "https://example.com/a?UTM_Campaign=x&id=1", "https://example.com/a?id=1"},
		{"click ids removed", "https://example.com/a?fbclid=x&gclid=y&msclkid=z", "https://example.com/a"},
		{"ref removed", "https://example.com/a?ref=producthunt", "https://example.com/a"},
		{"query sorted", "https://example.com/a?b=2&a=1", "https://example.com/a?a=1&b=2"},
		{"meaningful kept", "https://news.ycombinator.com/item?id=42&utm_source=x", "https://news.ycombinator.com/item?id=42"},
		{"repeated values keep order", "https://example.com/s?t=b&t=a", "https://example.com/s?t=b&t=a"},
		{"empty query dropped", "https://example.com/a?", "https://example.com/a"},
		{"youtube video id kept", "https://www.youtube.com/watch?v=abc123&feature=share", "https://youtube.com/watch?feature=share&v=abc123"},

		// arXiv.
		{"arxiv abs", "https://arxiv.org/abs/2410.12345", "https://arxiv.org/abs/2410.12345"},
		{"arxiv version stripped", "https://arxiv.org/abs/2410.12345v3", "https://arxiv.org/abs/2410.12345"},
		{"arxiv pdf", "https://arxiv.org/pdf/2410.12345v2", "https://arxiv.org/abs/2410.12345"},
		{"arxiv pdf extension", "http://arxiv.org/pdf/2410.12345v2.pdf", "https://arxiv.org/abs/2410.12345"},
		{"arxiv html", "https://arxiv.org/html/2410.12345v1/", "https://arxiv.org/abs/2410.12345"},
		{"arxiv www", "https://www.arxiv.org/abs/2410.12345", "https://arxiv.org/abs/2410.12345"},
		{"arxiv export host", "http://export.arxiv.org/abs/2410.12345v1", "https://arxiv.org/abs/2410.12345"},
		{"arxiv 4-digit sequence", "https://arxiv.org/abs/1706.03762", "https://arxiv.org/abs/1706.03762"},
		{"arxiv old style", "https://arxiv.org/abs/hep-th/9901001v2", "https://arxiv.org/abs/hep-th/9901001"},
		{"arxiv old style subject", "https://arxiv.org/pdf/math.GT/0309136", "https://arxiv.org/abs/math.GT/0309136"},
		{"arxiv query ignored", "https://arxiv.org/abs/2410.12345?context=cs.LG", "https://arxiv.org/abs/2410.12345"},
		{"arxiv non-paper page generic", "https://arxiv.org/list/cs.LG/recent", "https://arxiv.org/list/cs.LG/recent"},
		{"arxiv lookalike host not collapsed", "https://notarxiv.org/abs/2410.12345", "https://notarxiv.org/abs/2410.12345"},

		// GitHub.
		{"github repo", "https://github.com/vllm-project/vllm", "https://github.com/vllm-project/vllm"},
		{"github case", "https://github.com/Vllm-Project/VLLM", "https://github.com/vllm-project/vllm"},
		{"github www and slash", "https://www.github.com/a/b/", "https://github.com/a/b"},
		{"github .git", "https://github.com/a/b.git", "https://github.com/a/b"},
		{"github tree", "https://github.com/a/b/tree/main/src", "https://github.com/a/b"},
		{"github blob", "https://github.com/a/b/blob/main/README.md#install", "https://github.com/a/b"},
		{"github query dropped", "https://github.com/a/b?tab=readme-ov-file", "https://github.com/a/b"},
		{"github issue distinct", "https://github.com/A/B/issues/42", "https://github.com/a/b/issues/42"},
		{"github pull distinct", "https://github.com/a/b/pull/7/files", "https://github.com/a/b/pull/7/files"},
		{"github release distinct", "https://github.com/a/b/releases/tag/v1.0", "https://github.com/a/b/releases/tag/v1.0"},
		{"github profile generic", "https://github.com/someone", "https://github.com/someone"},
		{"github topics page generic", "https://github.com/topics/llm", "https://github.com/topics/llm"},
		{"github trending generic", "https://github.com/trending/python?since=daily", "https://github.com/trending/python?since=daily"},
		{"gist not a repo", "https://gist.github.com/a/b", "https://gist.github.com/a/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalize(tt.in)
			if err != nil {
				t.Fatalf("Canonicalize(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Canonicalize(%q)\n got  %q\n want %q", tt.in, got, tt.want)
			}
			// Canonical form is a fixed point.
			again, err := Canonicalize(got)
			if err != nil || again != got {
				t.Errorf("not idempotent: %q -> %q (%v)", got, again, err)
			}
		})
	}
}

func TestCanonicalizeEquivalentURLsMatch(t *testing.T) {
	groups := [][]string{
		{
			"https://arxiv.org/abs/2410.12345",
			"http://arxiv.org/abs/2410.12345v2",
			"https://arxiv.org/pdf/2410.12345v1.pdf",
			"https://www.arxiv.org/html/2410.12345v3/",
			"https://export.arxiv.org/abs/2410.12345?utm_source=x",
		},
		{
			"https://github.com/Org/Repo",
			"https://github.com/org/repo.git",
			"http://www.github.com/org/repo/tree/main/docs",
			"https://github.com/org/repo/blob/main/README.md#readme",
		},
		{
			"https://blog.example.com/post?utm_source=hn",
			"http://www.blog.example.com/post/#comments",
			"https://BLOG.example.com:443/post?fbclid=abc",
		},
	}
	for _, g := range groups {
		want, _ := Canonicalize(g[0])
		for _, in := range g[1:] {
			if got, err := Canonicalize(in); err != nil || got != want {
				t.Errorf("Canonicalize(%q) = %q (%v), want %q", in, got, err, want)
			}
		}
	}
}

func TestCanonicalizeInvalid(t *testing.T) {
	for _, in := range []string{
		"", "   ", "not a url", "/relative/path", "example.com/no-scheme",
		"ftp://example.com/file", "mailto:a@example.com", "javascript:alert(1)",
		"https://", "http://:80/x", "https://exa mple.com",
	} {
		if got, err := Canonicalize(in); !errors.Is(err, ErrInvalidURL) {
			t.Errorf("Canonicalize(%q) = %q, %v; want ErrInvalidURL", in, got, err)
		}
	}
}

func TestParseArxivID(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"2410.12345", "2410.12345", true},
		{"2410.12345v2", "2410.12345", true},
		{"arXiv:2410.12345v2", "2410.12345", true},
		{"1706.03762", "1706.03762", true},
		{"hep-th/9901001v1", "hep-th/9901001", true},
		{"math.GT/0309136", "math.GT/0309136", true},
		{"https://arxiv.org/abs/2410.12345v4", "2410.12345", true},
		{"http://arxiv.org/pdf/2410.12345.pdf", "2410.12345", true},
		{"https://example.com/abs/2410.12345", "", false},
		{"2410.123", "", false},
		{"not-an-id", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := ParseArxivID(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseArxivID(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestText(t *testing.T) {
	tests := []struct{ in, want string }{
		{"  Hello   world  ", "Hello world"},
		{"line one\nline two\r\n\tthree", "line one line two three"},
		{"nul\x00byte", "nulbyte"},
		{"bell\x07 and \x1b escape", "bell and escape"},
		{"\uFEFFbom", "bom"},
		{"bad \xff utf8", "bad utf8"},
		{"non breaking", "non breaking"},
		{"émoji 🚀 ok", "émoji 🚀 ok"},
		{"", ""},
		{" \n\t ", ""},
	}
	for _, tt := range tests {
		if got := Text(tt.in); got != tt.want {
			t.Errorf("Text(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHashes(t *testing.T) {
	if len(URLHash("https://a")) != 32 || len(ContentHash("t", "d")) != 32 {
		t.Fatal("hashes must be 32 bytes (SHA-256)")
	}
	if !bytes.Equal(URLHash("https://a"), URLHash("https://a")) {
		t.Error("URLHash not deterministic")
	}
	if bytes.Equal(URLHash("https://a"), URLHash("https://b")) {
		t.Error("URLHash collision on different input")
	}
	if !bytes.Equal(ContentHash("t", "d"), ContentHash("t", "d")) {
		t.Error("ContentHash not deterministic")
	}
	if bytes.Equal(ContentHash("ab", "c"), ContentHash("a", "bc")) {
		t.Error("ContentHash must separate title and description")
	}
	if bytes.Equal(ContentHash("Title", "x"), ContentHash("title", "x")) {
		t.Error("ContentHash should be case-sensitive (case edits are content changes)")
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("héllo", 2); got != "h" {
		t.Errorf("Truncate split a rune: %q", got)
	}
	if got := Truncate("abc", 10); got != "abc" {
		t.Errorf("Truncate = %q", got)
	}
	long := strings.Repeat("é", 10)
	if got := Truncate(long, 7); len(got) != 6 {
		t.Errorf("Truncate len = %d, want 6", len(got))
	}
}
