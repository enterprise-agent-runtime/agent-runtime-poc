package secrets

import (
	"bytes"
	"encoding/base64"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// pattern is one redaction type (design A15 §6.1). group means only capture
// group 1 is replaced.
type pattern struct {
	typ      string
	res      []*regexp.Regexp
	group    bool
	validate func(string) bool
}

var placeholder = regexp.MustCompile(`(?i)^(changeme|your[_-].*|example.*|dummy.*|fake.*|placeholder.*|redacted.*|none|null|undefined)$`)
var dottedIdent = regexp.MustCompile(`^[A-Za-z_$][\w$]*(\.[A-Za-z_$][\w$]*)+$`)

// notPlaceholder is the placeholder validator of types 14–16.
func notPlaceholder(v string) bool {
	switch {
	case strings.HasPrefix(v, "${") || strings.HasPrefix(v, "$(") || strings.HasPrefix(v, "{{") || strings.HasPrefix(v, "<") || strings.HasPrefix(v, "%("):
		return false
	case strings.Trim(v, "*") == "" || strings.Trim(strings.ToLower(v), "x") == "":
		return false
	case placeholder.MatchString(v):
		return false
	}
	return true
}

func entropy(s string) float64 {
	counts := map[rune]int{}
	for _, r := range s {
		counts[r]++
	}
	var h float64
	n := float64(len([]rune(s)))
	for _, c := range counts {
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// genericValue is the type 16 validator (A15 §6.1).
func genericValue(v string) bool {
	if !notPlaceholder(v) || strings.Contains(v, "[REDACTED:") {
		return false
	}
	hasDigit, hasLetter := false, false
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		}
	}
	if !hasDigit || !hasLetter || entropy(v) < 3.0 {
		return false
	}
	lv := strings.ToLower(v)
	if strings.HasPrefix(lv, "process.env.") || strings.HasPrefix(lv, "os.environ") || strings.HasPrefix(lv, "env.") || strings.Contains(v, "(") {
		return false
	}
	return !dottedIdent.MatchString(v)
}

func openAIValidator(v string) bool {
	body := strings.TrimPrefix(v, "sk-")
	if strings.HasPrefix(body, "ant-") {
		return false
	}
	return strings.ContainsAny(body, "0123456789") && strings.ContainsAny(body, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && strings.ContainsAny(body, "abcdefghijklmnopqrstuvwxyz")
}

func mustAll(exprs ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(exprs))
	for i, e := range exprs {
		out[i] = regexp.MustCompile(e)
	}
	return out
}

// patterns in priority order; known_secret (type 2) is handled separately
// but ranks second. Type 15(b) has (?m) and type 16 is an interpreted
// string (CONFLICTS C-52).
var patterns = []pattern{
	{typ: "private_key", res: mustAll(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----.*?-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)},
	{typ: "known_secret"}, // placeholder for ordering
	{typ: "jwt", res: mustAll(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]*`)},
	{typ: "anthropic_key", res: mustAll(`\bsk-ant-[a-z]{2,8}[0-9]{2}-[A-Za-z0-9_-]{32,}`)},
	{typ: "openai_key", res: mustAll(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{32,}`), validate: openAIValidator},
	{typ: "github_token", res: mustAll(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,255}\b`, `\bgithub_pat_[A-Za-z0-9_]{22,255}\b`)},
	{typ: "gitlab_token", res: mustAll(`\bglpat-[A-Za-z0-9_-]{20,}`)},
	{typ: "aws_access_key_id", res: mustAll(`\b(?:AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}\b`)},
	{typ: "aws_secret_access_key", group: true, res: mustAll(`(?i)\baws_?secret_?access_?key\b["']?\s*[:=]\s*["']?([A-Za-z0-9/+=]{40})`)},
	{typ: "google_api_key", res: mustAll(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{typ: "slack_token", res: mustAll(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{typ: "stripe_key", res: mustAll(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}\b`)},
	{typ: "npm_token", res: mustAll(`\bnpm_[A-Za-z0-9]{36}\b`)},
	{typ: "auth_header", group: true, validate: notPlaceholder, res: mustAll(
		`(?i)\b(?:proxy-)?authorization\s*:\s*(?:bearer|basic|token)\s+([A-Za-z0-9._~+/=-]{8,})`,
		`(?i)\b(?:x-api-key|api-key)\s*:\s*([A-Za-z0-9._~+/=-]{8,})`)},
	{typ: "conn_string_password", group: true, validate: notPlaceholder, res: mustAll(
		`\b[a-zA-Z][a-zA-Z0-9+.-]{1,20}://[^\s:/@'"]{1,256}:([^\s@/'"]{1,256})@`,
		`(?im)(?:^|;)\s*(?:password|pwd)=([^;'"\s]{1,256})(?:;|$)`)},
	{typ: "generic_assignment", group: true, validate: genericValue, res: mustAll(
		"(?i)\\b[A-Za-z0-9_.-]*(?:api[_-]?key|apikey|secret|token|passwd|password|access[_-]?key|private[_-]?key|client[_-]?secret|auth[_-]?key)[\"']?\\s*[:=]\\s*[\"'`]?([^\\s\"'`,;)}\\]]{16,})")},
}

// Redactor replaces secrets with [REDACTED:<type>] (design A15 §6). It
// also knows every value the broker resolved in this process (type
// known_secret). It is safe for concurrent use.
type Redactor struct {
	mu    sync.RWMutex
	known [][]byte
}

// NewRedactor returns a redactor with no known values.
func NewRedactor() *Redactor { return &Redactor{} }

// AddKnown registers a resolved secret value (≥ 8 bytes) and its base64
// encoding, so it is redacted wherever it reappears.
func (r *Redactor) AddKnown(v []byte) {
	if len(v) < 8 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.known {
		if bytes.Equal(k, v) {
			return
		}
	}
	c := append([]byte(nil), v...)
	r.known = append(r.known, c, []byte(base64.StdEncoding.EncodeToString(c)))
}

type span struct {
	start, end int
	typ        string
	prio       int
}

// Redact implements store.Redactor: it returns the redacted text and the
// type of each replacement made.
func (r *Redactor) Redact(s string) (string, []string) {
	out, types := r.redact([]byte(s))
	return string(out), types
}

// RedactBytes redacts b and returns counts per type.
func (r *Redactor) RedactBytes(b []byte) ([]byte, map[string]int) {
	out, types := r.redact(b)
	counts := map[string]int{}
	for _, t := range types {
		counts[t]++
	}
	return out, counts
}

func (r *Redactor) redact(b []byte) ([]byte, []string) {
	var cands []span
	for prio, p := range patterns {
		if p.typ == "known_secret" {
			r.mu.RLock()
			for _, k := range r.known {
				for off := 0; ; {
					i := bytes.Index(b[off:], k)
					if i < 0 {
						break
					}
					cands = append(cands, span{off + i, off + i + len(k), p.typ, prio})
					off += i + len(k)
				}
			}
			r.mu.RUnlock()
			continue
		}
		for _, re := range p.res {
			for _, m := range re.FindAllSubmatchIndex(b, -1) {
				start, end := m[0], m[1]
				if p.group {
					if len(m) < 4 || m[2] < 0 {
						continue
					}
					start, end = m[2], m[3]
				}
				if p.validate != nil && !p.validate(string(b[start:end])) {
					continue
				}
				if bytes.Contains(b[start:end], []byte("[REDACTED:")) {
					continue
				}
				cands = append(cands, span{start, end, p.typ, prio})
			}
		}
	}
	if len(cands) == 0 {
		return b, nil
	}
	// Earlier types win on overlapping text (A15 §6.1).
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].prio != cands[j].prio {
			return cands[i].prio < cands[j].prio
		}
		return cands[i].start < cands[j].start
	})
	var accepted []span
	for _, c := range cands {
		overlap := false
		for _, a := range accepted {
			if c.start < a.end && a.start < c.end {
				overlap = true
				break
			}
		}
		if !overlap {
			accepted = append(accepted, c)
		}
	}
	sort.Slice(accepted, func(i, j int) bool { return accepted[i].start < accepted[j].start })
	var out bytes.Buffer
	types := make([]string, 0, len(accepted))
	pos := 0
	for _, a := range accepted {
		out.Write(b[pos:a.start])
		out.WriteString("[REDACTED:" + a.typ + "]")
		pos = a.end
		types = append(types, a.typ)
	}
	out.Write(b[pos:])
	return out.Bytes(), types
}
