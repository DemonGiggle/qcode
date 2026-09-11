package question

import "context"

// Question is a user-facing design question asked during Plan mode. Options
// are displayed as suggestions; AllowCustom controls whether text outside the
// listed options is accepted. An empty list always accepts free text.
type Question struct {
	Text        string
	Options     []string
	AllowCustom bool
}

// Questioner blocks the current planning request until the user answers the
// supplied questions or cancels the interaction.
type Questioner func(context.Context, []Question) ([]string, error)
