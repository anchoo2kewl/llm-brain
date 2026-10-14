package parse

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RunLog is what a fleet worker log's file name says: logs/<key>.<YYYYmmdd-HHMMSS>.log.
type RunLog struct {
	Key       string
	Kind      string // issue | pr | review | other
	Started   time.Time
	Target    string // issue or PR URL when the key says which
	Repo      string
	Number    int
	StartedTS string // the name's own timestamp, as written
}

var runLogName = regexp.MustCompile(`^(.+)\.(\d{8}-\d{6})\.log$`)
var keyRepoNum = regexp.MustCompile(`^(?:pr|review)-(.+)-(\d+)$`)

// ParseRunLogName parses a worker log file name. loc is the zone of the machine that wrote it (the fleet
// stamps names with local time). ok is false for files that aren't worker logs (fleet.log, hub.log, ...).
func ParseRunLogName(base string, loc *time.Location, org, issueRepo string) (RunLog, bool) {
	m := runLogName.FindStringSubmatch(base)
	if m == nil {
		return RunLog{}, false
	}
	t, err := time.ParseInLocation("20060102-150405", m[2], loc)
	if err != nil {
		return RunLog{}, false
	}
	r := RunLog{Key: m[1], Started: t, StartedTS: m[2], Kind: "other"}
	switch {
	case strings.HasPrefix(r.Key, "issue-"):
		r.Kind = "issue"
		if n, err := strconv.Atoi(strings.TrimPrefix(r.Key, "issue-")); err == nil {
			r.Number, r.Repo = n, issueRepo
			r.Target = "https://github.com/" + issueRepo + "/issues/" + strconv.Itoa(n)
		}
	case strings.HasPrefix(r.Key, "pr-"), strings.HasPrefix(r.Key, "review-"):
		r.Kind = "pr"
		if strings.HasPrefix(r.Key, "review-") {
			r.Kind = "review"
		}
		if mm := keyRepoNum.FindStringSubmatch(r.Key); mm != nil {
			r.Repo = org + "/" + mm[1]
			r.Number, _ = strconv.Atoi(mm[2])
			r.Target = "https://github.com/" + r.Repo + "/pull/" + mm[2]
		}
	}
	return r, true
}

// FleetLine is one line of logs/fleet.log: "2026-10-14 16:20:06 [Host] message". Lines that don't match (gh
// errors written to stderr) have a zero time and level "error".
type FleetLine struct {
	At    time.Time
	Host  string
	Level string
	Text  string
}

var fleetLine = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) \[([^\]]+)\] (.*)$`)

func ParseFleetLine(s string, loc *time.Location) FleetLine {
	m := fleetLine.FindStringSubmatch(s)
	if m == nil {
		return FleetLine{Level: "error", Text: s}
	}
	t, _ := time.ParseInLocation("2006-01-02 15:04:05", m[1], loc)
	lv := "info"
	switch {
	case strings.HasPrefix(m[3], "ERROR"):
		lv = "error"
	case strings.HasPrefix(m[3], "started "), strings.HasPrefix(m[3], "notify:"):
		lv = "notice"
	}
	return FleetLine{At: t, Host: m[2], Level: lv, Text: m[3]}
}

// ReviewedLine finds the reviewer's "REVIEWED <url> <verdict> <detail>" line.
var reviewedLine = regexp.MustCompile(`(?m)^REVIEWED (\S+)(?: (\S+))?(.*)$`)

type Reviewed struct {
	URL, Verdict, Detail string
}

func FindReviewed(s string) (Reviewed, bool) {
	m := reviewedLine.FindStringSubmatch(s)
	if m == nil {
		return Reviewed{}, false
	}
	return Reviewed{URL: m[1], Verdict: m[2], Detail: strings.TrimSpace(m[3])}, true
}

// LastLine is the last non-empty line of s.
func LastLine(s string) string {
	s = strings.TrimRight(s, "\r\n\t ")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return Truncate(strings.TrimSpace(s), 500)
}

// LooksLikeCodex reports whether a worker log was written by `codex exec` rather than `claude -p`.
func LooksLikeCodex(head string) bool {
	return strings.Contains(head, "OpenAI Codex") || strings.Contains(head, "\nworkdir: ")
}
