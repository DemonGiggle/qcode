package redaction

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// oldPaymentSeparator is the pre-rewrite form kept here to prove the rewrite
// is exact. Old: A* (sep S*)? A* with A = ["'*_ ` \t], S = [ \t].
// New: A* (sep A*)?. Since S is a subset of A, S* A* == A* as languages.
const oldPaymentSeparatorConst = `["'*_` + "`" + ` \t]*(?:(?::|=|\||-|\bis\b|\bof\b|\bwas\b)[ \t]*)?["'*_` + "`" + ` \t]*`

var (
	oldLabelledCard     = regexp.MustCompile(`(?i)\b` + cardLabel + oldPaymentSeparatorConst + `(` + cardDigits + `)`)
	oldLabelledSecurity = regexp.MustCompile(`(?i)\b` + securityLabel + oldPaymentSeparatorConst + `([0-9]{3,4})\b`)
	oldLabelledExpiry   = regexp.MustCompile(`(?i)\b` + expiryLabel + oldPaymentSeparatorConst + `((?:[0-9]{4}[-/.](?:0?[1-9]|1[0-2])(?:[-/.][0-9]{1,2})?|(?:0?[1-9]|1[0-2])[-/.][0-9]{1,2}[-/.][0-9]{2,4}|(?:0?[1-9]|1[0-2])[ \t]*[-/.][ \t]*[0-9]{2,4}|(?:Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|Jul(?:y)?|Aug(?:ust)?|Sep(?:tember)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)[ \t]+[0-9]{4}))\b`)
	oldLabelledExpiryPt = regexp.MustCompile(`(?i)\b(?:card` + paymentGap + `)?(?:exp|expiry|expiration)` + paymentGap + `(?:month|year)` + oldPaymentSeparatorConst + `([0-9]{1,4})\b`)
)

func oldRedactPayments(line string) string {
	for _, pattern := range []*regexp.Regexp{oldLabelledCard, oldLabelledSecurity, oldLabelledExpiry, oldLabelledExpiryPt} {
		line = protect(line, func(piece string) string { return replacePaymentValueOld(piece, pattern) })
	}
	return protect(line, func(piece string) string {
		matches := cardCandidate.FindAllStringIndex(piece, -1)
		var out strings.Builder
		cursor := 0
		for _, match := range matches {
			if !cardBoundary(piece, match[0], match[1]) || !luhn(piece[match[0]:match[1]]) {
				continue
			}
			out.WriteString(piece[cursor:match[0]])
			out.WriteString(Marker)
			cursor = match[1]
		}
		out.WriteString(piece[cursor:])
		return out.String()
	})
}

func replacePaymentValueOld(text string, pattern *regexp.Regexp) string {
	matches := pattern.FindAllStringSubmatchIndex(text, -1)
	var out strings.Builder
	cursor := 0
	for _, match := range matches {
		start, end := match[2], match[3]
		if pattern == oldLabelledCard && !cardBoundary(text, start, end) {
			continue
		}
		out.WriteString(text[cursor:start])
		out.WriteString(Marker)
		cursor = end
	}
	out.WriteString(text[cursor:])
	return out.String()
}

func TestPaymentRewriteEquivalence(t *testing.T) {
	corpus := []string{
		"Card 4111111111111111",
		"- Number: 4111 1111 1111 1111",
		"5555-5555-5555-4444",
		"Credit card number: 4111 1111 1111 1112",
		`"card_number": "4111 1111 1111 1112"`,
		"**CVV:** `123`",
		"| CVC | 123 |",
		"Card security code is 1234.",
		"CVV2=123",
		"CVV is 123, expiration date is 04/29.",
		"- Thru: 04/29",
		"Valid Thru: 04/2029",
		"Expiry: 2029-04",
		"Expires: April 2029",
		"exp_month=04 exp_year=2029",
		"4111111111111112",
		"0000000000000000",
		"credit card number" + strings.Repeat(" ", 50),
		"credit card number" + strings.Repeat(" ", 50) + "x",
		"credit card number" + strings.Repeat("\"' * _` \t", 20) + "x",
		"CVV" + strings.Repeat(" ", 200) + "x",
		"CVV" + strings.Repeat("\"' * _` \t", 20) + "12",
		"expiry date" + strings.Repeat(" ", 200) + "x",
		"card number:" + strings.Repeat(" ", 500),
		strings.Repeat(" ", 2000),
		strings.Repeat("\"' * _` \t", 200),
		strings.Repeat("1 ", 500),
		strings.Repeat("1-", 500),
		"4111 1111 1111 1111 " + strings.Repeat("x", 1000),
		"password=secret " + strings.Repeat(" ", 500),
		"no secrets here just a log line",
		"ticket 123 on 04/29; 1234 records",
		"api_key = abcdef1234567890",
		"Authorization: Bearer abcdef.123456",
		"sk-abcdefghijklmnopqrst",
		"prefix " + strings.Repeat("a", 5000),
	}
	for _, in := range corpus {
		if got, want := redactPayments(in), oldRedactPayments(in); got != want {
			t.Fatalf("payment mismatch for %q:\n got %q\nwant %q", truncateForLog(in), got, want)
		}
		var p *Policy
		if got, want := p.Text(Persistence, in), oldText(p, in); got != want {
			t.Fatalf("text mismatch for %q:\n got %q\nwant %q", truncateForLog(in), got, want)
		}
	}
	// Deterministic fuzz over the separator-relevant alphabet.
	alphabet := []string{" ", "\t", `"`, "'", "*", "_", "`", ":", "=", "|", "-", "is", "of", "was", "card", "number", "1", "2", "x", "CVV"}
	x := uint64(0x12345678)
	next := func(n int) int {
		x = x*6364136223846793005 + 1442695040888963407
		return int((x >> 33) % uint64(n))
	}
	for i := 0; i < 2000; i++ {
		var b strings.Builder
		n := 1 + next(12)
		for j := 0; j < n; j++ {
			b.WriteString(alphabet[next(len(alphabet))])
		}
		in := b.String()
		if got, want := redactPayments(in), oldRedactPayments(in); got != want {
			t.Fatalf("fuzz mismatch for %q:\n got %q\nwant %q", in, got, want)
		}
	}
}

// oldText runs the pre-guard line path: assignment regex unconditionally.
// redactPayments here is already the new implementation, but the guard proof
// for payments is covered by oldRedactPayments above; this covers line-level
// composition (assignment guard + literals) by comparing against a direct
// unguarded assignment application.
func oldText(p *Policy, in string) string {
	p = effective(p)
	line := oldRedactPayments(in)
	line = p.assignment.ReplaceAllStringFunc(line, func(match string) string {
		separator := strings.IndexAny(match, ":=")
		prefix, value := match[:separator+1], strings.TrimSpace(match[separator+1:])
		if value == Marker || value == `"`+Marker+`"` || value == "'"+Marker+"'" {
			return match
		}
		if strings.HasPrefix(value, `"`) {
			return prefix + ` "` + Marker + `"`
		}
		if strings.HasPrefix(value, "'") {
			return prefix + " '" + Marker + "'"
		}
		return prefix + " " + Marker
	})
	return protect(line, func(part string) string {
		for _, literal := range p.literalSnapshot() {
			part = protect(part, func(piece string) string { return strings.ReplaceAll(piece, literal, Marker) })
		}
		part = tokens.ReplaceAllString(part, Marker)
		part = bearer.ReplaceAllString(part, Marker)
		for _, pattern := range p.patterns {
			part = protect(part, func(piece string) string { return pattern.ReplaceAllString(piece, Marker) })
		}
		return part
	})
}

func truncateForLog(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

func TestPaymentGoldenOutputsUnchanged(t *testing.T) {
	var p *Policy // nil selects the built-in policy, as in payment_test.go.
	for _, tt := range []struct{ in, want string }{
		{"Card 4111111111111111", "Card " + Marker},
		{"Credit card number: 4111 1111 1111 1112", "Credit card number: " + Marker},
		{"**CVV:** `123`", "**CVV:** `" + Marker + "`"},
		{"CVV is 123, expiration date is 04/29.", "CVV is " + Marker + ", expiration date is " + Marker + "."},
		{"exp_month=04 exp_year=2029", "exp_month=" + Marker + " exp_year=" + Marker},
		{"4111111111111112", "4111111111111112"},
		{"api_key = abcdef1234567890", "api_key = " + Marker},
		{"Authorization: Bearer abcdef.123456", "Authorization: " + Marker + " abcdef.123456"},
		{"sk-abcdefghijklmnopqrst", Marker},
		{"password=short", "password= " + Marker},
		{"public data and monkey=hello", "public data and monkey=hello"},
	} {
		if got := p.Text(Persistence, tt.in); got != tt.want {
			t.Fatalf("golden %q: got %q want %q", tt.in, got, tt.want)
		}
	}
	// Long pathological lines must not hang and must redact nothing new.
	pathological := []string{
		strings.Repeat(" ", 20000),
		strings.Repeat("\"' * _` \t", 2000),
		"credit card number" + strings.Repeat(" ", 8000) + "x",
		"expiry date" + strings.Repeat(" ", 8000) + "x",
		strings.Repeat("1 ", 5000),
		strings.Repeat("x", 64000),
	}
	for _, in := range pathological {
		start := time.Now()
		got := p.Text(Persistence, in)
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("pathological line len=%d took %v", len(in), elapsed)
		}
		if strings.Contains(got, Marker) && !strings.Contains(in, "4111") {
			// Only PAN-bearing inputs may gain a marker; padding alone must not.
			if !strings.ContainsAny(in, "0123456789") {
				t.Fatalf("false positive on padding len=%d: %q", len(in), truncateForLog(got))
			}
		}
		if p.Text(Persistence, got) != got {
			t.Fatalf("not idempotent for len=%d", len(in))
		}
	}
}

func TestLineCacheEquivalent(t *testing.T) {
	p, _ := New(Config{}, []string{"cache-golden-secret"})
	cp := p.Cached()
	inputs := []string{
		"hello cache-golden-secret world",
		"Card 4111111111111111",
		"password=hunter2",
		"-----BEGIN PRIVATE KEY-----\nbody\n-----END PRIVATE KEY-----",
		"a\n-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----\nb",
		"same line",
		"same line",
		"",
		strings.Repeat("x", 70000),
		"credit card number" + strings.Repeat(" ", 1000) + "x",
	}
	for _, in := range inputs {
		if got, want := cp.Text(Persistence, in), p.Text(Persistence, in); got != want {
			t.Fatalf("cache mismatch text %q", truncateForLog(in))
		}
		multi := in + "\n" + in
		if got, want := cp.Text(Persistence, multi), p.Text(Persistence, multi); got != want {
			t.Fatalf("cache mismatch multi %q", truncateForLog(in))
		}
	}
	// Literals registered after caching must still apply (no stale leak).
	p.RegisterSecrets("late-arriving-secret")
	if got := cp.Text(Persistence, "late-arriving-secret"); strings.Contains(got, "late-arriving-secret") {
		t.Fatal("stale cache leaked a newly registered secret")
	}
}

func BenchmarkPaymentPath(b *testing.B) {
	var p *Policy
	line := "some log output with no secrets at all 12345 hello world"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Text(Persistence, line)
	}
}

func BenchmarkPaymentPathLongLine(b *testing.B) {
	var p *Policy
	line := "credit card number" + strings.Repeat(" ", 2000) + "tail log output 12345"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Text(Persistence, line)
	}
}

func BenchmarkRepeatedLineHistory(b *testing.B) {
	var p *Policy
	cp := p.Cached()
	lines := make([]string, 2000)
	for i := range lines {
		lines[i] = "some log output with no secrets at all 12345 hello world"
	}
	text := strings.Join(lines, "\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cp.Text(Persistence, text)
	}
}
