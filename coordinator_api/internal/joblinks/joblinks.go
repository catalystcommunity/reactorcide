// Package joblinks reads the named links that a job publishes in its
// workflow output (plan item C1, docs/builds-and-caches.md).
//
// The workflow output file (/job/workflow-output.json, at most 1 MiB) can
// have a "links" key:
//
//	{"links": [{"name": "Build results", "url": "https://builds.example/1"}]}
//
// A link needs a name (1 to 100 characters) and an absolute http or https
// URL (at most 2048 characters). A job has at most MaxLinks links. The
// coordinator drops an invalid link and keeps the valid ones.
package joblinks

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

const (
	MaxLinks      = 20
	MaxNameLength = 100
	MaxURLLength  = 2048
)

type outputDocument struct {
	Links json.RawMessage `json:"links"`
}

type rawLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ValidateLink checks one link.
func ValidateLink(name, rawURL string) error {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxNameLength {
		return fmt.Errorf("link name must have 1 to %d characters", MaxNameLength)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("link name %q has a control character", name)
		}
	}
	rawURL = strings.TrimSpace(rawURL)
	if len(rawURL) > MaxURLLength {
		return fmt.Errorf("link %q: the URL has more than %d characters", name, MaxURLLength)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("link %q: the URL is not valid", name)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("link %q: the URL must use https or http", name)
	}
	if parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("link %q: the URL must have a host and no user information", name)
	}
	return nil
}

// Parse reads the links of a workflow output document. A document with no
// "links" key has no links. It returns the valid links, in order, and a
// reason for each dropped link.
func Parse(jobID string, data []byte) ([]models.JobLink, []string, error) {
	if len(data) == 0 {
		return nil, nil, nil
	}
	var doc outputDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse workflow output: %w", err)
	}
	if len(doc.Links) == 0 || string(doc.Links) == "null" {
		return nil, nil, nil
	}
	var raw []rawLink
	if err := json.Unmarshal(doc.Links, &raw); err != nil {
		return nil, []string{"links must be a list of {name, url} objects"}, nil
	}
	links := []models.JobLink{}
	dropped := []string{}
	seen := map[string]bool{}
	for _, item := range raw {
		name := strings.TrimSpace(item.Name)
		if err := ValidateLink(name, item.URL); err != nil {
			dropped = append(dropped, err.Error())
			continue
		}
		if seen[name] {
			dropped = append(dropped, fmt.Sprintf("link %q: the name is used two times", name))
			continue
		}
		if len(links) >= MaxLinks {
			dropped = append(dropped, fmt.Sprintf("link %q: a job has at most %d links", name, MaxLinks))
			continue
		}
		seen[name] = true
		links = append(links, models.JobLink{JobID: jobID, Position: len(links), Name: name, URL: strings.TrimSpace(item.URL)})
	}
	return links, dropped, nil
}
