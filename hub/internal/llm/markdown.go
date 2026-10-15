package llm

import (
	"html"
	"html/template"
	"regexp"
	"strings"
)

// Markdown renders a model's reply as HTML. Everything is escaped first; only the markup made here (code
// blocks, headings, lists, quotes, bold, italics, inline code, http(s) links) comes out as tags. Raw HTML
// in a reply shows as text.
func Markdown(src string) template.HTML {
	var b strings.Builder
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var para []string
	list := "" // "ul" | "ol" while in a list
	flushPara := func() {
		if len(para) > 0 {
			b.WriteString("<p>" + strings.Join(para, "<br>") + "</p>\n")
			para = nil
		}
	}
	closeList := func() {
		if list != "" {
			b.WriteString("</" + list + ">\n")
			list = ""
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			flushPara()
			closeList()
			lang := strings.TrimSpace(strings.TrimPrefix(t, "```"))
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, lines[i])
			}
			cls := ""
			if lang != "" && langRe.MatchString(lang) {
				cls = ` class="lang-` + lang + `"`
			}
			b.WriteString("<pre><code" + cls + ">" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
			continue
		}
		if t == "" {
			flushPara()
			closeList()
			continue
		}
		if m := headRe.FindStringSubmatch(t); m != nil {
			flushPara()
			closeList()
			lvl := min(len(m[1])+2, 6) // # -> h3: replies sit inside a page
			tag := "h" + string(rune('0'+lvl))
			b.WriteString("<" + tag + ">" + inline(m[2]) + "</" + tag + ">\n")
			continue
		}
		if m := ulRe.FindStringSubmatch(line); m != nil {
			flushPara()
			if list != "ul" {
				closeList()
				b.WriteString("<ul>\n")
				list = "ul"
			}
			b.WriteString("<li>" + inline(m[1]) + "</li>\n")
			continue
		}
		if m := olRe.FindStringSubmatch(line); m != nil {
			flushPara()
			if list != "ol" {
				closeList()
				b.WriteString("<ol>\n")
				list = "ol"
			}
			b.WriteString("<li>" + inline(m[1]) + "</li>\n")
			continue
		}
		if strings.HasPrefix(t, ">") {
			flushPara()
			closeList()
			b.WriteString("<blockquote>" + inline(strings.TrimSpace(strings.TrimPrefix(t, ">"))) + "</blockquote>\n")
			continue
		}
		if t == "---" || t == "***" {
			flushPara()
			closeList()
			b.WriteString("<hr>\n")
			continue
		}
		closeList()
		para = append(para, inline(t))
	}
	flushPara()
	closeList()
	return template.HTML(b.String()) //nolint:gosec // built from escaped text only
}

var (
	langRe   = regexp.MustCompile(`^[A-Za-z0-9_+-]{1,20}$`)
	headRe   = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	ulRe     = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)
	olRe     = regexp.MustCompile(`^\s*\d+[.)]\s+(.*)$`)
	boldRe   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	italRe   = regexp.MustCompile(`(^|[^\w*])\*([^*\s][^*]*?)\*`)
	linkRe   = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^\s)]+)\)`)
	tickSpan = regexp.MustCompile("`[^`]+`")
)

// inline renders one line: code spans verbatim (escaped), the rest escaped and then marked up.
func inline(s string) string {
	var b strings.Builder
	last := 0
	for _, loc := range tickSpan.FindAllStringIndex(s, -1) {
		b.WriteString(markup(s[last:loc[0]]))
		b.WriteString("<code>" + html.EscapeString(s[loc[0]+1:loc[1]-1]) + "</code>")
		last = loc[1]
	}
	b.WriteString(markup(s[last:]))
	return b.String()
}

func markup(s string) string {
	s = html.EscapeString(s)
	s = linkRe.ReplaceAllString(s, `<a href="$2" rel="noreferrer noopener" target="_blank">$1</a>`)
	s = boldRe.ReplaceAllString(s, `<strong>$1</strong>`)
	s = italRe.ReplaceAllString(s, `$1<em>$2</em>`)
	return s
}
