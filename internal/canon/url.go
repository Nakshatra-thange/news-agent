// Package canon turns URLs and text into deterministic canonical forms used
// for deduplication. Everything here is pure: no I/O, no network lookups
// (redirects are never followed), and the same input always yields the same
// output.
//
// Canonical URLs are identity keys, not display links: items keep the URL
// the source gave them for display, and the canonical form is only compared.
package canon

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
)

// ErrInvalidURL reports a URL that cannot be canonicalized.
var ErrInvalidURL = errors.New("invalid url")

// trackingParams are query parameters that identify a referral or campaign,
// never the content. Matched case-insensitively; any "utm_" prefix also
// matches.
var trackingParams = map[string]bool{
	"fbclid": true, "gclid": true, "dclid": true, "gbraid": true, "wbraid": true,
	"msclkid": true, "yclid": true, "twclid": true, "igshid": true,
	"mc_cid": true, "mc_eid": true, "_hsenc": true, "_hsmi": true, "mkt_tok": true,
	"_ga": true, "_gl": true, "oly_anon_id": true, "oly_enc_id": true, "vero_id": true,
	"__s": true, "ref": true, "ref_src": true, "ref_url": true,
}

// Canonicalize returns the canonical form of an http(s) URL:
//
//   - scheme becomes https (http and https identify the same content)
//   - host is lowercased; a leading "www.", a trailing dot, credentials and
//     default ports are removed
//   - the fragment is removed
//   - the path is cleaned (dot segments, duplicate and trailing slashes)
//   - tracking parameters are removed and the remaining query is sorted
//   - arXiv abstract/PDF/HTML links collapse to https://arxiv.org/abs/<id>
//     without a version suffix
//   - GitHub repository and code-browsing links collapse to
//     https://github.com/<owner>/<repo> (lowercased); issue, pull request,
//     release and other sub-pages stay distinct
func Canonicalize(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidURL)
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("%w: %q cannot be parsed", ErrInvalidURL, truncate(s, 100))
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("%w: %q is not an absolute http(s) URL", ErrInvalidURL, truncate(s, 100))
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	host = strings.TrimPrefix(host, "www.")
	if host == "" {
		return "", fmt.Errorf("%w: %q has no host", ErrInvalidURL, truncate(s, 100))
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		host += ":" + port
	}

	p := cleanPath(u.Path)
	if c, ok := arxivCanonical(host, p); ok {
		return c, nil
	}
	if c, ok := githubCanonical(host, p); ok {
		return c, nil
	}

	out := "https://" + host + (&url.URL{Path: p}).EscapedPath()
	if q := cleanQuery(u.RawQuery); q != "" {
		out += "?" + q
	}
	return out, nil
}

// cleanPath resolves dot segments and removes duplicate and trailing
// slashes. The root path becomes empty.
func cleanPath(p string) string {
	if p == "" || p == "/" {
		return ""
	}
	p = path.Clean("/" + p) // also collapses "//"
	if p == "/" {
		return ""
	}
	return strings.TrimSuffix(p, "/")
}

// cleanQuery drops tracking parameters and sorts the rest by key; repeated
// values keep their order, which can be meaningful.
func cleanQuery(raw string) string {
	if raw == "" {
		return ""
	}
	// ParseQuery returns what it could parse even on error.
	vals, _ := url.ParseQuery(raw)
	for k := range vals {
		lk := strings.ToLower(k)
		if trackingParams[lk] || strings.HasPrefix(lk, "utm_") {
			delete(vals, k)
		}
	}
	return vals.Encode() // sorted by key
}

// ---- arXiv ----

var (
	arxivHosts    = []string{"arxiv.org", "export.arxiv.org"}
	arxivPath     = regexp.MustCompile(`^/(?:abs|pdf|html|format|ps)/(.+)$`)
	arxivNewStyle = regexp.MustCompile(`^(\d{4}\.\d{4,5})(?:v\d+)?$`)
	// Pre-2007 identifiers: archive[.subject]/YYMMNNN, e.g. hep-th/9901001.
	arxivOldStyle = regexp.MustCompile(`^([a-z][a-z-]*(?:\.[A-Z]{2})?/\d{7})(?:v\d+)?$`)
)

// ParseArxivID extracts a versionless arXiv identifier from a bare ID
// ("2410.12345v2"), a prefixed one ("arXiv:2410.12345") or an arXiv URL.
func ParseArxivID(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		if !slices.Contains(arxivHosts, host) {
			return "", false
		}
		m := arxivPath.FindStringSubmatch(cleanPath(u.Path))
		if m == nil {
			return "", false
		}
		s = m[1]
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "arXiv:"), "arxiv:")
	s = strings.TrimSuffix(s, ".pdf")
	if m := arxivNewStyle.FindStringSubmatch(s); m != nil {
		return m[1], true
	}
	if m := arxivOldStyle.FindStringSubmatch(s); m != nil {
		return m[1], true
	}
	return "", false
}

func arxivCanonical(host, p string) (string, bool) {
	if !slices.Contains(arxivHosts, host) {
		return "", false
	}
	m := arxivPath.FindStringSubmatch(p)
	if m == nil {
		return "", false
	}
	id, ok := ParseArxivID(m[1])
	if !ok {
		return "", false
	}
	return "https://arxiv.org/abs/" + id, true
}

// ---- GitHub ----

// githubReserved are first path segments that are GitHub pages, not owners.
var githubReserved = map[string]bool{
	"about": true, "account": true, "apps": true, "codespaces": true, "collections": true,
	"contact": true, "copilot": true, "customer-stories": true, "dashboard": true,
	"enterprise": true, "events": true, "explore": true, "features": true, "git-guides": true,
	"home": true, "issues": true, "join": true, "login": true, "logout": true,
	"marketplace": true, "mobile": true, "new": true, "notifications": true, "orgs": true,
	"organizations": true, "pricing": true, "pulls": true, "readme": true, "resources": true,
	"search": true, "security": true, "settings": true, "signup": true, "site": true,
	"solutions": true, "sponsors": true, "team": true, "topics": true, "trending": true,
	"users": true,
}

// githubCodeViews are repository sub-pages that show the repository's code
// and so identify the repository itself.
var githubCodeViews = map[string]bool{"tree": true, "blob": true}

func githubCanonical(host, p string) (string, bool) {
	if host != "github.com" {
		return "", false
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(segs) < 2 || segs[0] == "" || githubReserved[strings.ToLower(segs[0])] {
		return "", false
	}
	owner := strings.ToLower(segs[0])
	repo := strings.ToLower(strings.TrimSuffix(segs[1], ".git"))
	if repo == "" {
		return "", false
	}
	base := "https://github.com/" + owner + "/" + repo
	if len(segs) == 2 || githubCodeViews[segs[2]] {
		return base, true
	}
	// Issues, pull requests, releases, discussions...: distinct content.
	// Owner and repo are case-insensitive; the rest of the path is not.
	return base + (&url.URL{Path: "/" + strings.Join(segs[2:], "/")}).EscapedPath(), true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
