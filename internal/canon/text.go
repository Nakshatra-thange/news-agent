package canon

import (
	"crypto/sha256"
	"strings"
	"unicode"
)

// URLHash is the dedup key for a canonical URL.
func URLHash(canonicalURL string) []byte {
	h := sha256.Sum256([]byte(canonicalURL))
	return h[:]
}

// ContentHash fingerprints an item's textual content for change detection.
// Inputs should already be normalized with Text. A NUL separator keeps
// ("ab", "c") and ("a", "bc") distinct.
func ContentHash(title, description string) []byte {
	h := sha256.New()
	h.Write([]byte(title))
	h.Write([]byte{0})
	h.Write([]byte(description))
	return h.Sum(nil)
}

// Text normalizes free text deterministically: invalid UTF-8 and control
// characters (including NUL, which PostgreSQL rejects) are removed, and every
// run of whitespace becomes a single space, trimmed at both ends.
func Text(s string) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), r == '\uFEFF':
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Truncate shortens s to at most n bytes without splitting a UTF-8 sequence.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
