package question

import "context"

// Question is a user-facing design question asked during Plan mode. Options
// turn the question into a multiple-choice prompt; an empty list accepts free
// text.
type Question struct {
	Text    string
	Options []string
}

// Questioner blocks the current planning request until the user answers the
// supplied questions or cancels the interaction.
type Questioner func(context.Context, []Question) ([]string, error)
