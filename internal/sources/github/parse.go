package github

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"synergy/internal/domain"
)

// searchResponse is GitHub's repository search response.
type searchResponse struct {
	TotalCount        int    `json:"total_count"`
	IncompleteResults bool   `json:"incomplete_results"`
	Items             []repo `json:"items"`
}

type repo struct {
	ID          int64   `json:"id"`
	FullName    string  `json:"full_name"`
	HTMLURL     string  `json:"html_url"`
	Description *string `json:"description"`
	Homepage    *string `json:"homepage"`
	Owner       struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
	Stars      int        `json:"stargazers_count"`
	Forks      int        `json:"forks_count"`
	OpenIssues int        `json:"open_issues_count"`
	Language   *string    `json:"language"`
	Topics     []string   `json:"topics"`
	CreatedAt  *time.Time `json:"created_at"`
	UpdatedAt  *time.Time `json:"updated_at"`
	PushedAt   *time.Time `json:"pushed_at"`
	License    *struct {
		SPDXID string `json:"spdx_id"`
	} `json:"license"`
	Archived bool `json:"archived"`
	Fork     bool `json:"fork"`
}

func parseSearch(body []byte) (*searchResponse, error) {
	var r searchResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("malformed search response: %w", err)
	}
	return &r, nil
}

// toCandidate maps a repository. Stars and other fast-changing numbers go to
// metadata, so a re-fetch with new star counts is an update, not a new item.
func toCandidate(r repo, matched []string) domain.Candidate {
	meta := map[string]any{
		"stars":           r.Stars,
		"forks":           r.Forks,
		"open_issues":     r.OpenIssues,
		"matched_queries": matched,
		"owner_type":      r.Owner.Type,
		"archived":        r.Archived,
	}
	if r.Language != nil && *r.Language != "" {
		meta["language"] = *r.Language
	}
	if r.License != nil && r.License.SPDXID != "" && r.License.SPDXID != "NOASSERTION" {
		meta["license"] = r.License.SPDXID
	}
	if r.Homepage != nil && *r.Homepage != "" {
		meta["homepage"] = *r.Homepage
	}
	for k, t := range map[string]*time.Time{"updated_at": r.UpdatedAt, "pushed_at": r.PushedAt} {
		if t != nil {
			meta[k] = t.UTC().Format(time.RFC3339)
		}
	}
	metaJSON, _ := json.Marshal(meta)

	desc := ""
	if r.Description != nil {
		desc = *r.Description
	}
	var authors []string
	if r.Owner.Login != "" {
		authors = []string{r.Owner.Login}
	}
	var created *time.Time
	if r.CreatedAt != nil {
		c := r.CreatedAt.UTC()
		created = &c
	}
	externalID := ""
	if r.ID > 0 {
		externalID = strconv.FormatInt(r.ID, 10) // stable across renames
	}
	return domain.Candidate{
		ExternalID:  externalID,
		Kind:        domain.ItemKindRepository,
		Title:       r.FullName,
		Description: desc,
		URL:         r.HTMLURL,
		Authors:     authors,
		Tags:        r.Topics,
		Metadata:    metaJSON,
		PublishedAt: created,
	}
}
