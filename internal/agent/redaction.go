package agent

import (
	"encoding/json"

	"qcode/internal/redaction"
)

// FilterSavedState works on a decoded, independently owned checkpoint. Tool
// configuration contains operational directory grants and must remain usable.
func FilterSavedState(p *redaction.Policy, data json.RawMessage) (json.RawMessage, error) {
	var s SavedState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if !p.Enabled(redaction.Persistence) {
		return append(json.RawMessage(nil), data...), nil
	}
	text := func(value string) string { return p.Text(redaction.Persistence, value) }
	s.System = text(s.System)
	s.LastResponse = text(s.LastResponse)
	s.LearningContext = text(s.LearningContext)
	s.Endpoint = text(s.Endpoint)
	for i := range s.Messages {
		m := &s.Messages[i]
		m.Content = text(m.Content)
		m.Thinking = text(m.Thinking)
		m.ReasoningDetails = p.Replay(redaction.Persistence, m.ReasoningDetails)
		for j := range m.ToolCalls {
			m.ToolCalls[j].Arguments = p.JSON(redaction.Persistence, m.ToolCalls[j].Arguments)
		}
	}
	for i := range s.Skills {
		s.Skills[i].Description = text(s.Skills[i].Description)
	}
	if s.LatestPlan != nil {
		filtered := redaction.Copy(p, redaction.Persistence, *s.LatestPlan)
		s.LatestPlan = &filtered
	}
	if s.LatestSkillDraft != nil {
		s.LatestSkillDraft.Name = text(s.LatestSkillDraft.Name)
		s.LatestSkillDraft.Content = text(s.LatestSkillDraft.Content)
	}
	return json.Marshal(s)
}
