package search

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/blevesearch/bleve/v2/analysis"
	"github.com/blevesearch/bleve/v2/registry"
)

// AnalyzerName is the analyzer for every full-text field. It keeps code tokens searchable:
//
//	"see uiv3#835 in blue-stack-api/internal/x.go"
//	  → see | uiv3#835 uiv3 | 835 | in | blue-stack-api/internal/x.go blue | stack | api | internal | x | go
//
// A word is a run of characters between separators (whitespace, quotes, brackets, ',;|=<>'), trimmed of
// leading and trailing punctuation. Each alphanumeric part of a word is a token at its own position, and a
// word with more than one part is also indexed whole, at its first part's position, so a phrase over the
// parts and a term or prefix on the whole word both match. Everything is lowercased; nothing is stemmed.
const AnalyzerName = "hubcode"

const (
	maxPart  = 64  // longer alphanumeric runs are blobs (base64, hashes of hashes): not indexed
	maxWhole = 160 // longer whole words still index their parts
)

func init() {
	if err := registry.RegisterAnalyzer(AnalyzerName, func(map[string]interface{}, *registry.Cache) (analysis.Analyzer, error) {
		return &analysis.DefaultAnalyzer{Tokenizer: codeTokenizer{}}, nil
	}); err != nil {
		panic(err)
	}
}

type codeTokenizer struct{}

func (codeTokenizer) Tokenize(in []byte) analysis.TokenStream {
	var out analysis.TokenStream
	pos := 0
	eachWord(in, func(w word) {
		first := true
		for _, p := range w.parts {
			if p.end-p.start > maxPart {
				continue
			}
			pos++
			if first && w.whole && w.end-w.start <= maxWhole {
				out = append(out, &analysis.Token{Start: w.start, End: w.end, Term: lowerBytes(in[w.start:w.end]),
					Position: pos, Type: analysis.AlphaNumeric})
			}
			first = false
			out = append(out, &analysis.Token{Start: p.start, End: p.end, Term: lowerBytes(in[p.start:p.end]),
				Position: pos, Type: analysis.AlphaNumeric})
		}
	})
	return out
}

type span struct{ start, end int }

type word struct {
	span
	parts []span
	whole bool // the word is more than its single part (uiv3#835, snake_case, a/b.go)
}

func isSep(r rune) bool {
	if unicode.IsSpace(r) {
		return true
	}
	switch r {
	case '"', '\'', '`', '(', ')', '[', ']', '{', '}', '<', '>', ',', ';', '|', '=', '\\':
		return true
	}
	return false
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// eachWord calls f for every word of s that has at least one alphanumeric part.
func eachWord(s []byte, f func(word)) {
	i := 0
	for i < len(s) {
		r, n := utf8.DecodeRune(s[i:])
		if isSep(r) {
			i += n
			continue
		}
		start := i
		for i < len(s) {
			r, n := utf8.DecodeRune(s[i:])
			if isSep(r) {
				break
			}
			i += n
		}
		w := splitWord(s, start, i)
		if len(w.parts) > 0 {
			f(w)
		}
	}
}

func splitWord(s []byte, start, end int) word {
	var w word
	inPart := false
	ps := 0
	for j := start; j < end; {
		r, n := utf8.DecodeRune(s[j:end])
		if isAlnum(r) {
			if !inPart {
				ps, inPart = j, true
			}
		} else if inPart {
			w.parts = append(w.parts, span{ps, j})
			inPart = false
		}
		j += n
	}
	if inPart {
		w.parts = append(w.parts, span{ps, end})
	}
	if len(w.parts) == 0 {
		return w
	}
	// The whole word, trimmed of leading/trailing punctuation: "(see", "done.", "path:" are just their part.
	w.start, w.end = w.parts[0].start, w.parts[len(w.parts)-1].end
	w.whole = len(w.parts) > 1
	return w
}

func lowerBytes(b []byte) []byte {
	for _, c := range b {
		if c >= utf8.RuneSelf || ('A' <= c && c <= 'Z') {
			return []byte(strings.ToLower(string(b)))
		}
	}
	return append([]byte(nil), b...)
}

// Analyzed is a query word as the index sees it.
type Analyzed struct {
	Whole string   // the whole word, when it has more than one part ("blue-stack-api")
	Parts []string // its alphanumeric parts ("blue", "stack", "api")
}

// AnalyzeWord tokenizes one query word the way the index does.
func AnalyzeWord(s string) Analyzed {
	var a Analyzed
	b := []byte(s)
	eachWord(b, func(w word) {
		if w.whole && a.Whole == "" && len(a.Parts) == 0 && w.end-w.start <= maxWhole {
			a.Whole = strings.ToLower(s[w.start:w.end])
		} else if len(a.Parts) > 0 {
			a.Whole = "" // several separate words: no single whole token
		}
		for _, p := range w.parts {
			if p.end-p.start <= maxPart {
				a.Parts = append(a.Parts, strings.ToLower(s[p.start:p.end]))
			}
		}
	})
	return a
}

// AnalyzePhrase tokenizes a phrase into positions; each position lists the terms that can stand there.
func AnalyzePhrase(s string) [][]string {
	var out [][]string
	for _, t := range (codeTokenizer{}).Tokenize([]byte(s)) {
		for len(out) < t.Position {
			out = append(out, nil)
		}
		out[t.Position-1] = append(out[t.Position-1], string(t.Term))
	}
	return out
}
