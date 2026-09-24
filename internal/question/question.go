package question

import "context"

// Question is a user-facing question. Options are displayed as suggestions;
// OptionDescriptions are optional display text at matching indexes. Answers
// use option labels. An empty option list always accepts free text.
type Question struct {
	Text               string
	Options            []string
	OptionDescriptions []string
	AllowCustom        bool
}

// Questioner blocks the current request until the user answers the supplied
// questions or cancels the interaction.
type Questioner func(context.Context, []Question) ([]string, error)
