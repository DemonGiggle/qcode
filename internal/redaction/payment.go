package redaction

import (
	"regexp"
	"strings"
)

// A checksum avoids treating every long timestamp or counter as a card number.
// Explicit payment-card labels also protect test/invalid numbers without one.
// Tabs delimit columns, including the read tool's line-number column. Keeping
// them out of PAN grouping prevents a line number from hiding the card number.
const cardDigits = `[0-9](?:[ -]*[0-9]){12,18}`

var cardCandidate = regexp.MustCompile(cardDigits)

const paymentGap = `[ \t_-]*`
const cardLabel = `(?:credit` + paymentGap + `card(?:` + paymentGap + `(?:number|no\.?))?|debit` + paymentGap + `card(?:` + paymentGap + `(?:number|no\.?))?|payment` + paymentGap + `card(?:` + paymentGap + `number)?|card` + paymentGap + `(?:number|no\.?|num)|cc` + paymentGap + `(?:number|num)|pan)`
const securityLabel = `(?:cvv2?|cvc2?|cid|card` + paymentGap + `(?:security` + paymentGap + `code|verification` + paymentGap + `(?:code|value))|security` + paymentGap + `code)`
const expiryLabel = `(?:(?:card` + paymentGap + `)?(?:expir(?:y|ation|es|e)|exp\.)(?:` + paymentGap + `date)?|(?:valid|validate|validation|validity)` + paymentGap + `(?:date|thru|through|until)|thru)`

// Labels may be Markdown headings/table cells or natural-language mentions.
// Capture only the value so formatting and useful labels survive filtering.
//
// Separator form: an optional run of quote/space padding, an optional single
// separator token (punctuation or is/of/was), then padding again. The previous
// form wrote this as A* (sep S*)? A* where A = ["'*_ ` \t] and S = [ \t].
// Since S is a subset of A, S* A* and A* denote the same language, so
// A* (sep S*)? A* accepts exactly the same strings as A* (sep A*)?. The old
// form forced the engine to partition every padding run between two adjacent
// stars (quadratic backtracking on long quote/space runs when the trailing
// value failed); the new form has a single star when no separator is present
// and a separator token disjoint from A otherwise (":=|-" and is/of/was
// letters never appear in A), giving a linear scan with identical matches.
const paymentSeparator = `["'*_` + "`" + ` \t]*(?:(?::|=|\||-|\bis\b|\bof\b|\bwas\b)["'*_` + "`" + ` \t]*)?`

var labelledCard = regexp.MustCompile(`(?i)\b` + cardLabel + paymentSeparator + `(` + cardDigits + `)`)
var labelledSecurity = regexp.MustCompile(`(?i)\b` + securityLabel + paymentSeparator + `([0-9]{3,4})\b`)
var labelledExpiry = regexp.MustCompile(`(?i)\b` + expiryLabel + paymentSeparator + `((?:[0-9]{4}[-/.](?:0?[1-9]|1[0-2])(?:[-/.][0-9]{1,2})?|(?:0?[1-9]|1[0-2])[-/.][0-9]{1,2}[-/.][0-9]{2,4}|(?:0?[1-9]|1[0-2])[ \t]*[-/.][ \t]*[0-9]{2,4}|(?:Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|Jul(?:y)?|Aug(?:ust)?|Sep(?:tember)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)[ \t]+[0-9]{4}))\b`)
var labelledExpiryPart = regexp.MustCompile(`(?i)\b(?:card` + paymentGap + `)?(?:exp|expiry|expiration)` + paymentGap + `(?:month|year)` + paymentSeparator + `([0-9]{1,4})\b`)

var paymentFields = []string{
	"card_number", "credit_card", "credit_card_number", "debit_card_number", "payment_card_number", "cc_number", "cc_num", "pan",
	"cvv", "cvv2", "cvc", "cvc2", "cid", "security_code", "card_security_code", "card_verification_code", "card_verification_value",
	"expiry", "expiry_date", "expiration", "expiration_date", "expires", "expire_date", "card_expiry", "card_expiry_date", "card_expiration", "card_expiration_date", "valid_thru", "valid_through", "valid_until", "valid_date", "validate_date", "validation_date", "thru", "exp_month", "exp_year", "expiry_month", "expiry_year", "expiration_month", "expiration_year",
}

func redactPayments(line string) string {
	// Match labelled grouped numbers before generic assignments, which would
	// otherwise remove just their first digit group.
	// Fast paths below skip regexes whose values are impossible: labelledCard
	// and the generic PAN scan need 13+ ASCII digits, labelledSecurity needs
	// 3+. Counting digits is a single cheap pass; the guards change no match
	// because cardDigits requires 13-19 digits and the security value needs
	// 3-4 digits.
	line = protect(line, func(piece string) string {
		if countDigits(piece) < 13 {
			return piece
		}
		return replacePaymentValue(piece, labelledCard)
	})
	line = protect(line, func(piece string) string {
		if countDigits(piece) < 3 {
			return piece
		}
		return replacePaymentValue(piece, labelledSecurity)
	})
	for _, pattern := range []*regexp.Regexp{labelledExpiry, labelledExpiryPart} {
		line = protect(line, func(piece string) string { return replacePaymentValue(piece, pattern) })
	}
	return protect(line, func(piece string) string {
		if countDigits(piece) < 13 {
			return piece
		}
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
func replacePaymentValue(text string, pattern *regexp.Regexp) string {
	matches := pattern.FindAllStringSubmatchIndex(text, -1)
	var out strings.Builder
	cursor := 0
	for _, match := range matches {
		start, end := match[2], match[3]
		if pattern == labelledCard && !cardBoundary(text, start, end) {
			continue
		}
		out.WriteString(text[cursor:start])
		out.WriteString(Marker)
		cursor = end
	}
	out.WriteString(text[cursor:])
	return out.String()
}
func cardBoundary(text string, start, end int) bool {
	// A candidate must not be a substring of a longer digit sequence, including
	// longer sequences written with the same space/hyphen grouping.
	for i := start - 1; i >= 0; i-- {
		if text[i] == ' ' || text[i] == '-' {
			continue
		}
		if text[i] >= '0' && text[i] <= '9' {
			return false
		}
		break
	}
	for i := end; i < len(text); i++ {
		if text[i] == ' ' || text[i] == '-' {
			continue
		}
		if text[i] >= '0' && text[i] <= '9' {
			return false
		}
		break
	}
	return true
}
func luhn(value string) bool {
	sum, count := 0, 0
	nonzero := false
	for i := len(value) - 1; i >= 0; i-- {
		digit := value[i]
		if digit < '0' || digit > '9' {
			continue
		}
		number := int(digit - '0')
		nonzero = nonzero || number != 0
		if count%2 == 1 {
			number *= 2
			if number > 9 {
				number -= 9
			}
		}
		sum += number
		count++
	}
	return count >= 13 && count <= 19 && nonzero && sum%10 == 0
}

// countDigits reports the number of ASCII digits in s. Card values need
// 13-19 digits and security codes need 3-4, so callers skip the corresponding
// regexes when fewer digits are present. The guard is exact: a match requires
// at least that many digit characters in the same Marker-delimited piece.
func countDigits(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n++
		}
	}
	return n
}
