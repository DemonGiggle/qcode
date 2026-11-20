package agent

import (
	"slices"

	"qcode/internal/llm"
)

// Context updates are interleaved with the request's copy of the conversation.
// They live only on the running agent. Resume and compaction start from the
// currently available context because they rebuild the underlying history.
type chatGPTContextFields struct {
	cwd, dynamic, work string
}

type contextInsertion struct {
	before int // index in durable history, or its length for a tool continuation
	text   string
}

type chatGPTContextHistory struct {
	last     chatGPTContextFields
	updates  []contextInsertion
	learning contextInsertion // only the currently retrieved learning may be sent
}

func changedContext(previous, current, cleared string) string {
	if previous == current {
		return ""
	}
	if current == "" {
		return "\n\n" + cleared
	}
	return current
}

// prepare returns a candidate history. The caller commits it only when it
// accepts the assistant response, so failed requests and pre-compaction
// estimates never introduce context that the assistant didn't see.
func (h chatGPTContextHistory) prepare(messages []llm.Message, fields chatGPTContextFields, learning string) ([]llm.Message, chatGPTContextHistory) {
	before := len(messages)
	last := messages[before-1]
	if last.Role == "user" && !last.Untrusted {
		before-- // Include context within the new user's cacheable prefix.
	}
	delta := changedContext(h.last.cwd, fields.cwd, "The working directory is no longer available.") +
		changedContext(h.last.dynamic, fields.dynamic, "There is no current additional context.") +
		changedContext(h.last.work, fields.work, "There are no current work history excerpts.")
	if delta != "" {
		h.updates = append(slices.Clip(h.updates), contextInsertion{before: before, text: delta})
		h.last = fields
	}
	if h.learning.text != learning {
		h.learning = contextInsertion{before: before, text: learning}
	}
	if len(h.updates) == 0 && learning == "" {
		return messages, h
	}

	withContext := make([]llm.Message, 0, len(messages)+len(h.updates)+1)
	update := 0
	for i := 0; i <= len(messages); i++ {
		if update < len(h.updates) && h.updates[update].before == i {
			withContext = append(withContext, llm.Message{Role: "developer", Content: h.updates[update].text})
			update++
		}
		if learning != "" && h.learning.before == i {
			withContext = append(withContext, llm.Message{Role: "developer", Content: learning})
		}
		if i < len(messages) {
			withContext = append(withContext, messages[i])
		}
	}
	return withContext, h
}
