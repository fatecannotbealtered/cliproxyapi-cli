package cmd

import (
	"strconv"
	"strings"

	cliproxyapicli "github.com/fatecannotbealtered/cliproxyapi-cli"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/output"
	"github.com/spf13/cobra"
)

type changelogEntry struct {
	Version string              `json:"version"`
	Date    string              `json:"date"`
	Changes map[string][]string `json:"changes"`
}

func (a *application) changelogCommand() *cobra.Command {
	var since string
	command := &cobra.Command{
		Use:   "changelog",
		Short: "Read version changes embedded from CHANGELOG.md",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if since != "" {
				if _, ok := parseSemver(since); !ok {
					return output.NewError("E_VALIDATION", "--since must be a semantic version", nil)
				}
			}
			entries := parseChangelog(cliproxyapicli.ChangelogMarkdown)
			filtered := make([]changelogEntry, 0, len(entries))
			for _, entry := range entries {
				if since == "" || compareSemver(entry.Version, since) > 0 {
					filtered = append(filtered, entry)
				}
			}
			data := map[string]any{"current_version": version, "entries": filtered, "count": len(filtered)}
			if since != "" {
				data["since"] = since
			}
			return a.success(data)
		},
	}
	command.Flags().StringVar(&since, "since", "", "Return releases strictly newer than this version")
	return command
}

func parseChangelog(markdown string) []changelogEntry {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	var entries []changelogEntry
	var current *changelogEntry
	category := ""
	for _, line := range lines {
		if strings.HasPrefix(line, "## [") && !strings.HasPrefix(strings.ToLower(line), "## [unreleased]") {
			end := strings.Index(line, "]")
			if end < 4 {
				continue
			}
			entry := changelogEntry{Version: line[4:end], Changes: emptyChangeSet()}
			if marker := strings.Index(line[end+1:], "-"); marker >= 0 {
				entry.Date = strings.TrimSpace(line[end+1+marker+1:])
			}
			entries = append(entries, entry)
			current = &entries[len(entries)-1]
			category = ""
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "### ") {
			category = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "### ")))
			continue
		}
		if strings.HasPrefix(line, "- ") {
			if _, ok := current.Changes[category]; ok {
				current.Changes[category] = append(current.Changes[category], strings.TrimSpace(strings.TrimPrefix(line, "- ")))
			}
		}
	}
	return entries
}

func emptyChangeSet() map[string][]string {
	return map[string][]string{"added": {}, "changed": {}, "fixed": {}, "deprecated": {}, "removed": {}, "security": {}}
}

func parseSemver(raw string) ([3]int, bool) {
	var out [3]int
	trimmed := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return out, false
	}
	for index, part := range parts {
		if suffix := strings.IndexAny(part, "-+"); suffix >= 0 {
			part = part[:suffix]
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return out, false
		}
		out[index] = value
	}
	return out, true
}

func compareSemver(left, right string) int {
	l, lok := parseSemver(left)
	r, rok := parseSemver(right)
	if !lok || !rok {
		return strings.Compare(left, right)
	}
	for index := range l {
		if l[index] < r[index] {
			return -1
		}
		if l[index] > r[index] {
			return 1
		}
	}
	return 0
}
