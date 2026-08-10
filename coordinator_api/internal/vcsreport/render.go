package vcsreport

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const RootMarker = "<!-- reactorcide:report:v1 -->"

type Entry struct {
	Key        string
	Title      string
	Status     string
	Body       string
	Generation int64
}

var markerUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func MarkerKey(value string) string {
	value = markerUnsafe.ReplaceAllString(strings.TrimSpace(value), "-")
	value = strings.Trim(value, "-.")
	if value == "" {
		return "unknown"
	}
	return value
}

func Render(entries []Entry, generation int64, generationComplete bool) string {
	selected := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if generationComplete && entry.Generation != generation {
			continue
		}
		selected = append(selected, entry)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Key < selected[j].Key })
	var out strings.Builder
	out.WriteString(RootMarker + "\n\n## Reactorcide\n")
	for _, entry := range selected {
		key := MarkerKey(entry.Key)
		title := strings.TrimSpace(entry.Title)
		if title == "" {
			title = entry.Key
		}
		fmt.Fprintf(&out, "\n<!-- reactorcide:workflow:%s:begin -->\n", key)
		fmt.Fprintf(&out, "### %s — %s\n", title, entry.Status)
		if body := strings.TrimSpace(entry.Body); body != "" {
			out.WriteString("\n" + body + "\n")
		}
		fmt.Fprintf(&out, "<!-- reactorcide:workflow:%s:end -->\n", key)
	}
	return out.String()
}

// Merge replaces the database-owned report region and preserves text before
// its root marker. A malformed old region cannot delete structured state,
// because the replacement is always rebuilt from entries.
func Merge(existing string, entries []Entry, generation int64, generationComplete bool) (body string, rebuilt bool) {
	rendered := Render(entries, generation, generationComplete)
	index := strings.Index(existing, RootMarker)
	if index < 0 {
		return rendered, false
	}
	prefix := existing[:index]
	managed := existing[index:]
	malformed := strings.Count(managed, ":begin -->") != strings.Count(managed, ":end -->")
	if malformed {
		rendered += "\n> Warning: Reactorcide rebuilt a malformed managed report region.\n"
		return prefix + rendered, true
	}
	suffix := ""
	if lastEnd := strings.LastIndex(managed, ":end -->"); lastEnd >= 0 {
		lastEnd += len(":end -->")
		suffix = managed[lastEnd:]
	}
	return prefix + rendered + suffix, false
}
