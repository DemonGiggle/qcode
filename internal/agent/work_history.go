package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/session"
)

type WorkMatch struct {
	RequestID string    `json:"request_id"`
	AgentID   string    `json:"agent_id"`
	AgentName string    `json:"agent_name"`
	Available bool      `json:"available"`
	Status    string    `json:"status"`
	Task      string    `json:"task"`
	Findings  string    `json:"findings,omitempty"`
	Files     string    `json:"files,omitempty"`
	Created   time.Time `json:"created"`
	Finished  time.Time `json:"finished,omitempty"`
}

type WorkSearch struct {
	Matches    []WorkMatch `json:"matches"`
	Total      int         `json:"total"`
	NextOffset *int        `json:"next_offset,omitempty"`
}

// AgentKnowledge returns the latest completed finding recorded for an agent.
// This is the same persisted final-answer material that can be injected into
// the main agent's temporary historical context before a consultation.
func (m *AgentManager) AgentKnowledge(agentID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for index := len(m.work) - 1; index >= 0; index-- {
		work := m.work[index]
		if work.AgentID == agentID && strings.TrimSpace(work.Response) != "" {
			return work.Response
		}
	}
	return ""
}

// SearchWork searches every retained task, including closed agents and records
// evicted from the in-memory result cache. Only excerpts enter model context.
func (m *AgentManager) SearchWork(query, agentID string, offset int) WorkSearch {
	m.mu.RLock()
	defer m.mu.RUnlock()
	indices := m.matchWorkLocked(query, agentID)
	result := WorkSearch{Matches: []WorkMatch{}, Total: len(indices)}
	offset = max(0, min(offset, len(indices)))
	end := min(offset+10, len(indices))
	for _, index := range indices[offset:end] {
		result.Matches = append(result.Matches, m.workMatchLocked(index, query))
	}
	if end < len(indices) {
		result.NextOffset = &end
	}
	return result
}

func queryTerms(query string) []string {
	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	const stop = " a an and are as at be by can could do for from has have how i in is it me of on or please that the their them these this to was we were what when which with would you "
	seen := map[string]bool{}
	var terms []string
	for _, word := range words {
		if !seen[word] && !strings.Contains(stop, " "+word+" ") {
			terms = append(terms, word)
			seen[word] = true
		}
	}
	return terms
}

func (m *AgentManager) matchWorkLocked(query, agentID string) []int {
	terms := queryTerms(query)
	type scored struct{ index, score int }
	var matches []scored
	for i, work := range m.work {
		if work.AgentID == "main" || agentID != "" && work.AgentID != agentID {
			continue
		}
		text := strings.ToLower(work.Prompt + "\n" + work.Response + "\n" + strings.Join(work.ChangedFiles, " ") + "\n" + work.AgentName)
		score := 0
		for _, term := range terms {
			if strings.Contains(text, term) {
				score++
			}
		}
		if score > 0 || strings.TrimSpace(query) == "" {
			matches = append(matches, scored{i, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].index > matches[j].index
	})
	indices := make([]int, len(matches))
	for i, match := range matches {
		indices[i] = match.index
	}
	return indices
}

func excerpt(text, query string, limit int) string {
	if len(text) <= limit {
		return text
	}
	// Locate against the original bytes: Unicode lowercasing can change byte
	// offsets, so case-insensitive matching is performed rune by rune here.
	lower := strings.ToLower(text)
	for _, term := range queryTerms(query) {
		at := strings.Index(lower, term)
		if at < 0 {
			continue
		}
		runesBefore := utf8.RuneCountInString(lower[:at])
		byteAt := 0
		for n := 0; n < runesBefore && byteAt < len(text); n++ {
			_, size := utf8.DecodeRuneInString(text[byteAt:])
			byteAt += size
		}
		start := max(0, byteAt-limit/3)
		for start > 0 && !utf8.RuneStart(text[start]) {
			start--
		}
		if start > 0 {
			return "..." + truncateUTF8(text[start:], limit-3)
		}
		break
	}
	return truncateUTF8(text, limit)
}

func (m *AgentManager) workMatchLocked(index int, query string) WorkMatch {
	w := m.work[index]
	_, available := m.sessions[w.AgentID]
	return WorkMatch{RequestID: w.RequestID, AgentID: w.AgentID, AgentName: truncateUTF8(w.AgentName, 64), Available: available,
		Status: w.Status, Task: excerpt(w.Prompt, query, 256), Findings: excerpt(w.Response, query, 768), Files: truncateUTF8(strings.Join(w.ChangedFiles, ", "), 256), Created: w.Created, Finished: w.Finished}
}

// TaskContext is computed once before each new main request, on the agent's
// owning goroutine. It searches the full journal and suggests one best match
// per agent, with pagination available through search_agent_work.
func (t *managedToolset) TaskContext(query string) string {
	if !t.main {
		return ""
	}
	m := t.manager
	m.mu.RLock()
	defer m.mu.RUnlock()
	indices := m.matchWorkLocked(query, "")
	seen := map[string]bool{}
	matches := []WorkMatch{}
	for _, index := range indices {
		id := m.work[index].AgentID
		if seen[id] {
			continue
		}
		seen[id] = true
		if len(matches) < DefaultMaxAgents {
			matches = append(matches, m.workMatchLocked(index, query))
		}
	}
	data, _ := json.Marshal(struct {
		Matches       []WorkMatch `json:"matches"`
		RelatedAgents int         `json:"related_agents"`
	}{matches, len(seen)})
	return prompt.AgentHistoryReference + string(data)
}

func (t *managedToolset) searchWork(arguments json.RawMessage) (llm.ToolResult, error) {
	var args struct {
		Query   string `json:"query"`
		AgentID string `json:"agent_id"`
		Offset  int    `json:"offset"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return llm.ToolResult{}, fmt.Errorf("invalid search_agent_work arguments: %w", err)
	}
	if args.Offset < 0 {
		return llm.ToolResult{}, fmt.Errorf("offset must be non-negative")
	}
	data, _ := json.Marshal(t.manager.SearchWork(args.Query, args.AgentID, args.Offset))
	return llm.ToolResult{Output: string(data)}, nil
}

func (m *AgentManager) SaveWorkHistory() *session.WorkHistory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.saveWorkHistoryLocked()
}

func (m *AgentManager) saveWorkHistoryLocked() *session.WorkHistory {
	state := &session.WorkHistory{NextRequestID: m.nextRequestID, Events: append([]session.ConsultationEvent(nil), m.consultationEvents...)}
	for _, work := range m.work {
		work.ChangedFiles = append([]string(nil), work.ChangedFiles...)
		state.Records = append(state.Records, work)
	}
	return state
}

// RestoreWorkHistory runs on a detached, restored manager before any requests.
// Old snapshots have no journal. Unfinished requests are recorded as interrupted
// and are never resubmitted implicitly.
func (m *AgentManager) RestoreWorkHistory(saved *session.WorkHistory) error {
	if saved == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shutdown {
		return ErrManagerClosed
	}
	if len(m.work) != 0 || m.nextRequestID != 0 {
		return fmt.Errorf("work history already initialized")
	}
	seen := map[string]bool{}
	records := make([]session.WorkRecord, 0, len(saved.Records))
	for _, work := range saved.Records {
		n, err := strconv.ParseUint(strings.TrimPrefix(work.RequestID, "request-"), 10, 64)
		if err != nil || n == 0 || n > saved.NextRequestID || work.RequestID != fmt.Sprintf("request-%d", n) || seen[work.RequestID] || work.AgentID == "" {
			return fmt.Errorf("invalid work history request")
		}
		if work.AgentID != "main" {
			id, err := strconv.Atoi(strings.TrimPrefix(work.AgentID, "agent-"))
			if err != nil || id < 1 || id > m.nextID || work.AgentID != fmt.Sprintf("agent-%d", id) {
				return fmt.Errorf("invalid work history agent")
			}
		}
		switch work.Status {
		case "queued", "running", "completed", "failed", "cancelled", "timed_out", "interrupted":
		default:
			return fmt.Errorf("invalid work history status")
		}
		seen[work.RequestID] = true
		work.ChangedFiles = append([]string(nil), work.ChangedFiles...)
		records = append(records, work)
	}
	for i, event := range saved.Events {
		if event.Sequence != uint64(i+1) {
			return fmt.Errorf("invalid consultation event sequence")
		}
	}
	m.work, m.nextRequestID = records, saved.NextRequestID
	m.consultationEvents = append([]session.ConsultationEvent(nil), saved.Events...)
	for i := range m.work {
		work := &m.work[i]
		if _, exists := m.sessions[work.AgentID]; !exists {
			m.closed[work.AgentID] = struct{}{}
		}
		if work.Status == "queued" || work.Status == "running" {
			work.Status, work.Error, work.Finished = "interrupted", "Interrupted when the previous process ended", time.Now().UTC()
			work.Response = ""
			if work.Consultation {
				m.recordConsultationEventLocked(session.ConsultationEvent{AgentID: work.AgentID, RequestID: work.RequestID, Status: work.Status, Error: work.Error})
			}
		}
	}
	return nil
}
