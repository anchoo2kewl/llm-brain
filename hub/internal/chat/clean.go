package chat

import (
	"html"
	"regexp"
	"strings"
)

// Slack's markup: <@U…|name>, <#C…|name>, <!here>, <!subteam^S…|@team>, <url|label>, <url>.
var angle = regexp.MustCompile(`<([^<>\s][^<>]*)>`)

// sentUsing is the footer some clients add ("*Sent using* Claude").
var sentUsing = regexp.MustCompile(`(?i)^[*_]?sent using[*_]?(\s|$)`)

// Clean turns Slack message markup into plain text. name looks up a user's display name ("" if unknown).
func Clean(text string, name func(id string) string) string {
	out := angle.ReplaceAllStringFunc(text, func(m string) string {
		inner := m[1 : len(m)-1]
		target, label, hasLabel := strings.Cut(inner, "|")
		switch {
		case strings.HasPrefix(target, "@"):
			id := target[1:]
			if hasLabel && label != "" {
				return "@" + strings.TrimPrefix(label, "@")
			}
			if name != nil {
				if n := name(id); n != "" {
					return "@" + n
				}
			}
			return "@" + id
		case strings.HasPrefix(target, "#"):
			if hasLabel && label != "" {
				return "#" + label
			}
			return target
		case strings.HasPrefix(target, "!"):
			if hasLabel && label != "" {
				return label // <!subteam^S…|@team>, <!date^…|fallback>
			}
			return "@" + strings.TrimPrefix(target, "!") // <!here>, <!channel>
		}
		if !hasLabel || label == "" {
			return target
		}
		if label == target || "mailto:"+label == target {
			return label
		}
		return label + " (" + target + ")"
	})
	out = html.UnescapeString(out)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if n := len(lines); n > 0 && sentUsing.MatchString(strings.TrimSpace(lines[n-1])) {
		lines = lines[:n-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// mentions reports whether raw (uncleaned) text mentions the user.
func mentions(raw, userID string) bool {
	return userID != "" && (strings.Contains(raw, "<@"+userID+">") || strings.Contains(raw, "<@"+userID+"|"))
}
