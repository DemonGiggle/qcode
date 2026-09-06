package learning

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

var stopWords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "this": true, "that": true, "from": true, "please": true, "have": true, "what": true, "how": true, "can": true, "you": true, "use": true, "our": true, "into": true, "not": true, "are": true, "was": true, "want": true, "help": true}

func keywords(s string) map[string]bool {
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 2 && !stopWords[w] {
			words[w] = true
		}
	}
	return words
}

// Context serializes only reference content, excluding timestamps and provenance.
func Context(items []Learning) string {
	if len(items) == 0 {
		return ""
	}
	type reference struct {
		Topic   string   `json:"topic"`
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
	}
	refs := make([]reference, 0, len(items))
	for _, l := range items {
		refs = append(refs, reference{l.Topic, l.Content, l.Tags})
	}
	data, _ := json.Marshal(refs)
	return string(data)
}
func EstimatedTokens(s string) int { return (len(s) + 3) / 4 }
func (s *FileStore) Search(ctx context.Context, query string, budget int) ([]Learning, error) {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return Rank(snapshot.Items, query, budget), nil
}

// Rank uses topic/tag matches plus content overlap. Two distinct meaningful
// terms must match and at least one must be a topic/tag term. Tags act as
// applicability hints: if supplied, at least one tag must overlap the query.
func Rank(items []Learning, query string, budget int) []Learning {
	if budget <= 0 {
		return nil
	}
	terms := keywords(query)
	type scored struct {
		item  Learning
		score int
	}
	var candidates []scored
	for _, item := range items {
		primary := keywords(item.Topic + " " + strings.Join(item.Tags, " "))
		body := keywords(item.Content)
		tags := keywords(strings.Join(item.Tags, " "))
		score, matches, anchors, tagMatches := 0, 0, 0, 0
		for word := range terms {
			if tags[word] {
				tagMatches++
			}
			if primary[word] {
				score += 3
				anchors++
				matches++
			} else if body[word] {
				score++
				matches++
			}
		}
		if matches >= 2 && anchors > 0 && (len(tags) == 0 || tagMatches > 0) {
			candidates = append(candidates, scored{item, score})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].item.ID < candidates[j].item.ID
		}
		return candidates[i].score > candidates[j].score
	})
	var selected []Learning
	for _, c := range candidates {
		trial := append(append([]Learning(nil), selected...), c.item)
		if EstimatedTokens(Context(trial)) > budget {
			continue
		}
		selected = trial
		if len(selected) == 3 {
			break
		}
	}
	return selected
}
