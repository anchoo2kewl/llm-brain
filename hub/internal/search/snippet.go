package search

import (
	"html"
	"html/template"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// HTerm is a term to highlight; a Prefix term may continue into a longer word.
type HTerm struct {
	Text   string
	Prefix bool
}

const snippetWidth = 280

// lowerASCII lowercases ASCII letters only, so byte offsets in the result are offsets in s.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func alnumBefore(s string, i int) bool {
	if i <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func alnumAt(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// matches finds where the terms occur in s, on word boundaries, as sorted, merged byte ranges.
func matches(s string, terms []HTerm, limit int) []span {
	low := lowerASCII(s)
	var out []span
	for _, t := range terms {
		tl := lowerASCII(t.Text)
		if tl == "" {
			continue
		}
		n := 0
		for from := 0; from < len(low) && n < limit; {
			k := strings.Index(low[from:], tl)
			if k < 0 {
				break
			}
			st := from + k
			en := st + len(tl)
			from = en
			if alnumBefore(low, st) || (!t.Prefix && alnumAt(low, en)) {
				continue
			}
			if t.Prefix {
				for en < len(low) && alnumAt(low, en) {
					_, sz := utf8.DecodeRuneInString(low[en:])
					en += sz
				}
			}
			out = append(out, span{st, en})
			n++
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	merged := out[:0]
	for _, m := range out {
		if len(merged) > 0 && m.start <= merged[len(merged)-1].end {
			if m.end > merged[len(merged)-1].end {
				merged[len(merged)-1].end = m.end
			}
			continue
		}
		merged = append(merged, m)
	}
	return merged
}

// Snippet is the stretch of s where the most query terms occur together, HTML-escaped, with the terms in
// <mark>. With no match it is the start of s.
func Snippet(s string, terms []HTerm) template.HTML {
	return snippetN(s, terms, snippetWidth)
}

func snippetN(s string, terms []HTerm, width int) template.HTML {
	ms := matches(s, terms, 200)
	start := 0
	if len(ms) > 0 {
		// The window that covers the most distinct terms, starting a third of a window before a match.
		best, bestScore := 0, -1
		low := lowerASCII(s)
		for i, m := range ms {
			distinct := map[string]bool{}
			for j := i; j < len(ms) && ms[j].start < m.start+width*2/3; j++ {
				distinct[low[ms[j].start:ms[j].end]] = true
			}
			if len(distinct) > bestScore {
				best, bestScore = i, len(distinct)
			}
		}
		start = max(0, ms[best].start-width/3)
	}
	end := min(len(s), start+width)
	// Snap to rune boundaries.
	for start > 0 && !utf8.RuneStart(s[start]) {
		start--
	}
	for end < len(s) && !utf8.RuneStart(s[end]) {
		end++
	}
	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	at := start
	for _, m := range ms {
		if m.end <= start || m.start >= end {
			continue
		}
		a, z := max(m.start, start), min(m.end, end)
		b.WriteString(html.EscapeString(squash(s[at:a])))
		b.WriteString("<mark>")
		b.WriteString(html.EscapeString(s[a:z]))
		b.WriteString("</mark>")
		at = z
	}
	b.WriteString(html.EscapeString(squash(s[at:end])))
	if end < len(s) {
		b.WriteString("…")
	}
	return template.HTML(b.String()) // every byte of s went through html.EscapeString
}

// squash collapses runs of whitespace (keeping one newline), so a snippet isn't mostly indentation.
func squash(s string) string {
	if !strings.ContainsAny(s, "\n\t\r  ") {
		return s
	}
	var b strings.Builder
	space, nl := false, false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			if r == '\n' {
				nl = true
			}
			continue
		}
		if space {
			if nl {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
			space, nl = false, false
		}
		b.WriteRune(r)
	}
	if space {
		if nl {
			b.WriteByte('\n')
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}
