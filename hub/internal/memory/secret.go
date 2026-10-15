package memory

import (
	"math"
	"regexp"
	"strings"
)

// Secret says what credential a text seems to hold ("" when none): a memory is shared with every agent and
// copied to TaskAI, so the hub refuses a write rather than store one. It errs on the side of refusing; the
// writer can rephrase ("the key is in 1Password item X" is a fine memory, the key is not).
func Secret(texts ...string) string {
	for _, t := range texts {
		if t == "" {
			continue
		}
		for _, p := range secretPatterns {
			if p.re.MatchString(t) {
				return p.name
			}
		}
		if assignedSecret(t) {
			return "a password or key assignment"
		}
		if highEntropy(t) {
			return "a long random token"
		}
	}
	return ""
}

var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"a private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY`)},
	{"an AWS access key", regexp.MustCompile(`\b(AKIA|ASIA|AGPA|AIDA|AROA)[0-9A-Z]{16}\b`)},
	{"a GitHub token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})`)},
	{"a Slack token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
	{"a Slack webhook", regexp.MustCompile(`hooks\.slack\.com/services/T[A-Za-z0-9]+/B[A-Za-z0-9]+/[A-Za-z0-9]+`)},
	{"an Anthropic or OpenAI key", regexp.MustCompile(`\bsk-(ant-|proj-|live-)?[A-Za-z0-9_-]{20,}`)},
	{"a Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"a Stripe key", regexp.MustCompile(`\b[rs]k_(live|test)_[0-9A-Za-z]{16,}`)},
	{"a JWT", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{"a bearer token", regexp.MustCompile(`(?i)\b(bearer|apikey|token)\s+[A-Za-z0-9._~+/=-]{24,}`)},
	{"a password in a URL", regexp.MustCompile(`[a-z][a-z0-9+.-]*://[^\s/:@]+:[^\s/@]{3,}@`)},
	{"an npm token", regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
}

// assignRe: NAME=value or "name": "value" where the name says it is a secret.
var assignRe = regexp.MustCompile("(?i)\\b([a-z0-9_.-]*(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|auth)[a-z0-9_.-]*)[\"'`]?\\s*[:=]\\s*[\"'`]?([^\\s\"',;}`]+)")

// wordRe: a plain word or identifier ("author: required", "token_type=bearer", "HttpTokens=optional"), or a
// line range, not a value.
var wordRe = regexp.MustCompile(`^([a-z]+([_-][a-z]+)*|[A-Z][a-z]+([A-Z][a-z]+)*|true|false|null|none|\d+(-\d+)?)$`)

// envNameRe: an environment variable's name (FLAGTGL_TOKEN): where the secret is, not the secret.
var envNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)+$`)

// fileKeyRe: the "name" is a file (internal_auth.rs: 86-120), not a setting.
var fileKeyRe = regexp.MustCompile(`\.(rs|go|ts|tsx|js|jsx|py|rb|java|kt|swift|md|sql|sh|yaml|yml|json|toml|tf)$`)

func assignedSecret(t string) bool {
	for _, m := range assignRe.FindAllStringSubmatch(t, -1) {
		key, v := strings.ToLower(m[1]), strings.TrimRight(m[2], ".:)]>")
		if strings.Contains(key, "author") || fileKeyRe.MatchString(key) {
			continue
		}
		if len(v) < 8 || placeholder(v) || wordRe.MatchString(v) || envNameRe.MatchString(v) || strings.Contains(v, "@") {
			continue
		}
		return true
	}
	return false
}

// placeholder: a value that names where the secret is rather than being it.
func placeholder(v string) bool {
	l := strings.ToLower(v)
	if strings.HasPrefix(v, "$") || strings.HasPrefix(v, "<") || strings.HasPrefix(v, "{{") || strings.HasPrefix(v, "%") ||
		strings.Contains(l, "xxxx") || strings.Contains(l, "redacted") || strings.Contains(l, "example") ||
		strings.Contains(l, "changeme") || strings.Contains(l, "placeholder") || strings.Contains(l, "1password") ||
		strings.HasPrefix(l, "op://") || strings.HasPrefix(l, "env:") {
		return true
	}
	// a path or a reference to an env file / setting, not a value
	return strings.ContainsAny(v, "/~") || strings.HasSuffix(l, ".env") || strings.HasSuffix(l, ".json") || strings.HasSuffix(l, ".yaml")
}

var tokenRe = regexp.MustCompile(`[A-Za-z0-9+_=-]{32,}`) // path segments, not whole paths

// highEntropy: a long run of random-looking characters (a key with no recognizable prefix). Hex digests up to
// 64 chars (git SHAs, sha256 sums) are allowed, and a slug or identifier with several - or _ (an encoded
// folder name, a_long_function_name) is judged piece by piece.
func highEntropy(t string) bool {
	for _, tok := range tokenRe.FindAllString(t, -1) {
		if randomToken(tok) {
			return true
		}
	}
	return false
}

var sepRe = regexp.MustCompile(`[-_]`)

func randomToken(tok string) bool {
	if len(tok) < 32 || (isHex(tok) && len(tok) <= 64) {
		return false
	}
	if len(sepRe.FindAllStringIndex(tok, -1)) >= 3 {
		for _, p := range sepRe.Split(tok, -1) {
			if randomToken(p) {
				return true
			}
		}
		return false
	}
	return mixedDigits(tok) && entropy(tok) >= 4.0
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// mixedDigits: has upper, lower and digits, as random tokens do.
func mixedDigits(s string) bool {
	var up, lo, dg bool
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z':
			up = true
		case c >= 'a' && c <= 'z':
			lo = true
		case c >= '0' && c <= '9':
			dg = true
		}
	}
	return up && lo && dg
}

func entropy(s string) float64 {
	n := map[rune]int{}
	for _, c := range s {
		n[c]++
	}
	var h float64
	for _, k := range n {
		p := float64(k) / float64(len(s))
		h -= p * math.Log2(p)
	}
	return h
}
