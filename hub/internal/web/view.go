package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Two ways to read a conversation: Terminal (the default) looks like the CLI's own screen (❯ prompts, ⏺
// answers and tool calls, ⎿ results cut to a few lines, markdown left as text with a little colour), and Chat
// is cards with rendered markdown. The viewer's choice is the hub_view cookie (static/view.js keeps it, and a
// copy in localStorage); ?view= on the URL wins, so a workspace pane can differ from the rest.

const viewCookie = "hub_view"

// viewOf: the view this request asks for, "term" or "chat".
func viewOf(r *http.Request) string {
	if v := r.URL.Query().Get("view"); v == "term" || v == "chat" {
		return v
	}
	if c, err := r.Cookie(viewCookie); err == nil && (c.Value == "term" || c.Value == "chat") {
		return c.Value
	}
	return "term"
}

// viewSeg is what the "viewseg" template needs: the page's URL without view, and the view shown.
type viewSeg struct{ Base, View string }

// viewLink is the page's own URL with view set to each choice (other query values kept).
func viewLink(r *http.Request) string {
	q := r.URL.Query()
	q.Del("view")
	q.Del("err")
	u := r.URL.Path
	if e := q.Encode(); e != "" {
		u += "?" + e + "&"
	} else {
		u += "?"
	}
	return u
}

// tcall is a tool call on one line, as the CLI shows it: Bash(git status), Read(main.go), Ran git status (Codex).
func tcall(name, input string) string {
	arg := toolArg(name, input)
	arg = strings.Join(strings.Fields(arg), " ")
	if len([]rune(arg)) > 160 {
		arg = string([]rune(arg)[:159]) + "…"
	}
	switch name {
	case "shell", "exec_command", "local_shell", "container.exec":
		return "Ran " + arg
	}
	if arg == "" {
		return name
	}
	return name + "(" + arg + ")"
}

// toolArg picks the part of a tool's input a person would want to see.
func toolArg(name, input string) string {
	var m map[string]any
	if json.Unmarshal([]byte(input), &m) != nil {
		if name == "apply_patch" { // Codex: the patch itself; its files are on the "*** Update File:" lines
			var files []string
			for _, l := range strings.Split(input, "\n") {
				for _, p := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
					if strings.HasPrefix(l, p) {
						files = append(files, strings.TrimPrefix(l, p))
					}
				}
			}
			return strings.Join(files, ", ")
		}
		return firstLine(input, 200)
	}
	for _, k := range []string{"command", "cmd", "file_path", "path", "pattern", "url", "query", "description", "prompt", "skill", "notebook_path"} {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case []any: // ["bash","-lc","git status"]: the script
			if len(v) > 0 {
				if s, ok := v[len(v)-1].(string); ok {
					return s
				}
			}
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// tlines splits a tool result into the first few lines and the rest, for "… +N lines".
type tlinesT struct {
	Head, Rest string
	More       int
}

func tlines(text string, n int) tlinesT {
	text = strings.TrimRight(text, "\n")
	ls := strings.Split(text, "\n")
	if len(ls) <= n+1 { // never hide a single line behind "+1 lines"
		return tlinesT{Head: text}
	}
	return tlinesT{Head: strings.Join(ls[:n], "\n"), Rest: strings.Join(ls[n:], "\n"), More: len(ls) - n}
}

var (
	tBold = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	tCode = regexp.MustCompile("`([^`\n]+)`")
)

// tmd is markdown left as text, with the light colouring a terminal gives it: headings and **bold** bold,
// `code` tinted, diff lines green and red, fences dim.
func tmd(text string) template.HTML {
	var b strings.Builder
	fence := false
	for i, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		e := template.HTMLEscapeString(l)
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "```"):
			fence = !fence
			b.WriteString(`<span class="t-dim">` + e + `</span>`)
			continue
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") && fence:
			b.WriteString(`<span class="t-add">` + e + `</span>`)
			continue
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---") && fence:
			b.WriteString(`<span class="t-del">` + e + `</span>`)
			continue
		case strings.HasPrefix(l, "@@") && fence:
			b.WriteString(`<span class="t-hunk">` + e + `</span>`)
			continue
		case fence:
			b.WriteString(`<span class="t-code">` + e + `</span>`)
			continue
		case strings.HasPrefix(t, "#"):
			b.WriteString(`<b>` + e + `</b>`)
			continue
		}
		e = tBold.ReplaceAllString(e, `<b>$1</b>`)
		e = tCode.ReplaceAllString(e, `<span class="t-code">$1</span>`)
		b.WriteString(e)
	}
	return template.HTML(b.String()) //nolint:gosec // every piece of text was escaped above
}

// tdiff colours a tool result that is a diff or patch (+/- lines); other output stays plain.
func tdiff(text string) template.HTML {
	var b strings.Builder
	for i, l := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		e := template.HTMLEscapeString(l)
		switch {
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			b.WriteString(`<span class="t-add">` + e + `</span>`)
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
			b.WriteString(`<span class="t-del">` + e + `</span>`)
		default:
			b.WriteString(e)
		}
	}
	return template.HTML(b.String()) //nolint:gosec // escaped above
}

func init() {
	funcs["tcall"] = tcall
	funcs["tlines"] = tlines
	funcs["tmd"] = tmd
	funcs["tdiff"] = tdiff
	funcs["viewSeg"] = func(base, view string) viewSeg { return viewSeg{base, view} }
}
