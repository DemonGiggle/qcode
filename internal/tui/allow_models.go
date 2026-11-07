package tui

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"qcode/internal/config"
)

// Allow-model selector roles for the Space cycle:
// off -> smart -> balanced -> cheap -> off.
const (
	allowRoleOff = iota
	allowRoleSmart
	allowRoleBalanced
	allowRoleCheap
)

func allowRoleName(mode int) string {
	switch mode {
	case allowRoleSmart:
		return "smart"
	case allowRoleBalanced:
		return "balanced"
	case allowRoleCheap:
		return "cheap"
	default:
		return "-"
	}
}

func allowRoleMode(role string) int {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "smart":
		return allowRoleSmart
	case "balanced":
		return allowRoleBalanced
	case "cheap":
		return allowRoleCheap
	default:
		return allowRoleOff
	}
}

func matchingAllowModelIndices(models []string, query string) []int {
	query = strings.ToLower(query)
	matches := make([]int, 0, len(models))
	for i, model := range models {
		if strings.Contains(strings.ToLower(model), query) {
			matches = append(matches, i)
		}
	}
	return matches
}

func allowModelsSelectorRows(visible int) int { return visible + 1 }

func renderAllowModelsHeader(total, matched, allowed int, query string, width int, color bool, provider string) string {
	header := fmt.Sprintf("Allow models for %s (%d allowed) | %d/%d | Type to filter", provider, allowed, matched, total)
	if query != "" {
		header += " | Search: " + query
	}
	header += " | Space: - -> smart -> balanced -> cheap -> - | Enter: save"
	header = selectorHeader(header, width)
	if color {
		return dim + header + reset
	}
	return header
}

func renderAllowModelLine(model string, mode int, current bool, width int, color bool) string {
	marker := "  "
	if current {
		marker = "> "
	}
	role := allowRoleName(mode)
	check := " "
	if mode != allowRoleOff {
		check = "✓"
	}
	name := sanitizeDiffLine(model, "<ESC>")
	// Keep role column stable: "✓ smart    <model>"
	lineBody := fmt.Sprintf("%s %-8s %s", check, role, name)
	if width > 2 {
		lineBody = truncateDiffLine(lineBody, width-2, false)
	}
	line := marker + lineBody
	if color && current {
		line = cyan + bold + line + reset
	}
	return line
}

func renderAllowModelsSelector(out io.Writer, models []string, matches []int, selected map[int]int, current, start, visible, width int, query string, color bool, provider string) {
	allowed := 0
	for _, mode := range selected {
		if mode != allowRoleOff {
			allowed++
		}
	}
	printSelectorRow(out, renderAllowModelsHeader(len(models), len(matches), allowed, query, width, color, provider))
	for row := 0; row < visible; row++ {
		matchIndex := start + row
		var line string
		if matchIndex >= len(matches) {
			if row == 0 && len(matches) == 0 {
				line = "  No matching models"
			} else {
				line = ""
			}
		} else {
			i := matches[matchIndex]
			line = renderAllowModelLine(models[i], selected[i], matchIndex == current, width, color)
		}
		printSelectorRow(out, line)
	}
}

func selectAllowModels(in io.Reader, out io.Writer, models []string, initial map[int]int, visible, width int, color bool, provider string) (map[int]int, bool, error) {
	if len(models) == 0 {
		return nil, false, nil
	}
	visible = selectorVisible(len(models), visible)
	selected := make(map[int]int, len(initial))
	for index, mode := range initial {
		if mode == allowRoleSmart || mode == allowRoleBalanced || mode == allowRoleCheap {
			selected[index] = mode
		}
	}
	current := 0
	start := 0
	query := ""
	matches := matchingAllowModelIndices(models, query)
	rows := allowModelsSelectorRows(visible)
	renderAllowModelsSelector(out, models, matches, selected, current, start, visible, width, query, color, provider)
	for {
		key, err := readSelectorKey(in)
		if err != nil {
			clearSelector(out, rows)
			return nil, false, err
		}
		switch key {
		case string([]byte{ctrlC}), "\x1b":
			clearSelector(out, rows)
			return nil, false, nil
		case "\r", "\n":
			clearSelector(out, rows)
			return selected, true, nil
		case " ":
			if len(matches) == 0 {
				continue
			}
			index := matches[current]
			switch selected[index] {
			case allowRoleOff:
				selected[index] = allowRoleSmart
			case allowRoleSmart:
				selected[index] = allowRoleBalanced
			case allowRoleBalanced:
				selected[index] = allowRoleCheap
			default:
				delete(selected, index)
			}
			replaceSelectorRow(out, rows, 0, renderAllowModelsHeader(len(models), len(matches), countAllowSelected(selected), query, width, color, provider))
			line := renderAllowModelLine(models[index], selected[index], true, width, color)
			replaceSelectorRow(out, rows, 1+current-start, line)
			continue
		case arrowUpSequence, arrowDownSequence, selectorPageUp, selectorPageDown:
			if len(matches) == 0 {
				continue
			}
			oldCurrent, oldStart := current, start
			switch key {
			case arrowUpSequence:
				current = (current - 1 + len(matches)) % len(matches)
				start = selectorStart(current, len(matches), visible, start)
			case arrowDownSequence:
				current = (current + 1) % len(matches)
				start = selectorStart(current, len(matches), visible, start)
			case selectorPageUp:
				current, start = selectorPage(current, start, len(matches), visible, -1)
			case selectorPageDown:
				current, start = selectorPage(current, start, len(matches), visible, 1)
			}
			if start != oldStart {
				clearSelector(out, rows)
				renderAllowModelsSelector(out, models, matches, selected, current, start, visible, width, query, color, provider)
			} else if current != oldCurrent {
				oldIndex, index := matches[oldCurrent], matches[current]
				oldLine := renderAllowModelLine(models[oldIndex], selected[oldIndex], false, width, color)
				newLine := renderAllowModelLine(models[index], selected[index], true, width, color)
				replaceSelectorRow(out, rows, 1+oldCurrent-start, oldLine)
				replaceSelectorRow(out, rows, 1+current-start, newLine)
			}
			continue
		case string([]byte{8}), string([]byte{127}):
			if query == "" {
				continue
			}
			query = query[:len(query)-1]
			matches = matchingAllowModelIndices(models, query)
			current, start = 0, 0
		case string([]byte{ctrlU}):
			query = ""
			matches = matchingAllowModelIndices(models, query)
			current, start = 0, 0
		default:
			if len(key) != 1 || key[0] < 32 || key[0] > 126 {
				continue
			}
			query += key
			matches = matchingAllowModelIndices(models, query)
			current, start = 0, 0
		}
		clearSelector(out, rows)
		renderAllowModelsSelector(out, models, matches, selected, current, start, visible, width, query, color, provider)
	}
}

func countAllowSelected(selected map[int]int) int {
	n := 0
	for _, mode := range selected {
		if mode != allowRoleOff {
			n++
		}
	}
	return n
}

// allowedModelsReader is the optional manager capability for the roster.
// control.Host implements it; test doubles may not.
type allowedModelsReader interface {
	AllowedAgentModels() []config.AllowedAgentModel
}

// allowedModelsWriter is the optional manager capability to update the roster.
type allowedModelsWriter interface {
	SetAllowedAgentModels([]config.AllowedAgentModel)
}

func (u *UI) handleAllowModelsCommand(ctx context.Context, fields []string) {
	if len(fields) >= 3 {
		switch fields[2] {
		case "list":
			if len(fields) != 3 {
				u.agentUsage()
				return
			}
			u.listAllowModels()
			return
		case "clear":
			if len(fields) != 3 {
				u.agentUsage()
				return
			}
			u.clearAllowModels()
			return
		default:
			u.agentUsage()
			return
		}
	}
	u.chooseAllowModels(ctx)
}

func (u *UI) currentAllowProvider() string {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	return strings.TrimSpace(u.provider)
}

func (u *UI) currentAllowedForProvider(provider string) []config.AllowedAgentModel {
	if u.manager == nil {
		return nil
	}
	reader, ok := u.manager.(allowedModelsReader)
	if !ok {
		return nil
	}
	var out []config.AllowedAgentModel
	for _, entry := range reader.AllowedAgentModels() {
		if entry.Provider == provider {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Role < out[j].Role
	})
	return out
}

func (u *UI) listAllowModels() {
	provider := u.currentAllowProvider()
	if provider == "" {
		u.printSystemMessage(yellow + "Provider is unknown." + reset)
		return
	}
	entries := u.currentAllowedForProvider(provider)
	if len(entries) == 0 {
		u.printSystemMessage(dim + "No allowed models for " + sanitizeDiffLine(provider, "<ESC>") + ". Empty means no restriction." + reset)
		return
	}
	var lines []string
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("  %s  %s", roleBadge(entry.Role), sanitizeDiffLine(entry.Model, "<ESC>")))
	}
	u.printSystemMessage(dim + "Allowed models for " + sanitizeDiffLine(provider, "<ESC>") + " (" + fmt.Sprint(len(entries)) + "):" + reset + "\n" + strings.Join(lines, "\n"))
}

func roleBadge(role string) string {
	switch role {
	case "smart":
		return "✓ smart   "
	case "balanced":
		return "✓ balanced"
	case "cheap":
		return "✓ cheap   "
	default:
		return "  -       "
	}
}

func (u *UI) clearAllowModels() {
	provider := u.currentAllowProvider()
	if provider == "" {
		u.printSystemMessage(yellow + "Provider is unknown." + reset)
		return
	}
	if u.manager == nil {
		u.printSystemMessage(yellow + "Agent tabs are unavailable." + reset)
		return
	}
	reader, ok := u.manager.(allowedModelsReader)
	if !ok {
		u.printSystemMessage(yellow + "Allowed models are unavailable in this mode." + reset)
		return
	}
	writer, ok := u.manager.(allowedModelsWriter)
	if !ok {
		u.printSystemMessage(yellow + "Allowed models are unavailable in this mode." + reset)
		return
	}
	var kept []config.AllowedAgentModel
	for _, entry := range reader.AllowedAgentModels() {
		if entry.Provider != provider {
			kept = append(kept, entry)
		}
	}
	if kept == nil {
		kept = []config.AllowedAgentModel{}
	}
	writer.SetAllowedAgentModels(kept)
	u.persistAllowModelsPreference(kept)
	u.printSystemMessage(green + "Cleared allowed models for " + sanitizeDiffLine(provider, "<ESC>") + ". Empty means no restriction." + reset)
}

func (u *UI) chooseAllowModels(ctx context.Context) {
	if !u.activeAgentConfigurable() {
		return
	}
	u.screenMu.Lock()
	provider := strings.TrimSpace(u.provider)
	runner := u.runner
	u.screenMu.Unlock()
	if provider == "" {
		u.printSystemMessage(yellow + "Provider is unknown." + reset)
		return
	}
	modelRunner, ok := runner.(modelRunner)
	if !ok {
		u.printSystemMessage(yellow + "Model selection is unavailable." + reset)
		return
	}
	fetchCtx, cancel := context.WithCancel(ctx)
	u.input.setCancel(cancel)
	models, err := modelRunner.ListModels(fetchCtx)
	u.input.setCancel(nil)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		u.printSystemMessage(yellow + "Unable to list models: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
		return
	}
	if len(models) == 0 {
		u.printSystemMessage(yellow + "Provider " + sanitizeDiffLine(provider, "<ESC>") + " returned no models." + reset)
		return
	}
	sorted := append([]string(nil), models...)
	sort.Strings(sorted)
	// Build initial selection from current roster for this provider.
	initial := make(map[int]int)
	if reader, ok := u.manager.(allowedModelsReader); ok {
		byModel := map[string]string{}
		for _, entry := range reader.AllowedAgentModels() {
			if entry.Provider == provider {
				byModel[entry.Model] = entry.Role
			}
		}
		for i, name := range sorted {
			if role, exists := byModel[name]; exists {
				initial[i] = allowRoleMode(role)
			}
		}
	}
	visible := min(12, max(3, u.height-6))
	u.input.setRaw(true)
	u.beginRawSelector()
	selected, accepted, selErr := selectAllowModels(u.input, u.themedSelectorWriter(), sorted, initial, visible, u.width, ColorEnabled(u.out), provider)
	u.input.setRaw(false)
	u.endRawSelector()
	if selErr != nil {
		if ctx.Err() == nil {
			u.printSystemMessage(yellow + "Allowed model selection failed: " + sanitizeDiffLine(selErr.Error(), "<ESC>") + reset)
		}
		return
	}
	if !accepted {
		u.printSystemMessage(dim + "Allowed model selection cancelled." + reset)
		return
	}
	var chosen []config.AllowedAgentModel
	for index, mode := range selected {
		if mode == allowRoleOff {
			continue
		}
		if index < 0 || index >= len(sorted) {
			continue
		}
		chosen = append(chosen, config.AllowedAgentModel{
			Provider: provider,
			Model:    sorted[index],
			Role:     allowRoleName(mode),
		})
	}
	// Merge with other providers' entries so saving for one provider keeps the rest.
	var merged []config.AllowedAgentModel
	if reader, ok := u.manager.(allowedModelsReader); ok {
		for _, entry := range reader.AllowedAgentModels() {
			if entry.Provider != provider {
				merged = append(merged, entry)
			}
		}
	}
	merged = append(merged, chosen...)
	if merged == nil {
		merged = []config.AllowedAgentModel{}
	}
	if writer, ok := u.manager.(allowedModelsWriter); ok {
		writer.SetAllowedAgentModels(merged)
	}
	u.persistAllowModelsPreference(merged)
	if len(chosen) == 0 {
		u.printSystemMessage(green + "Allowed models for " + sanitizeDiffLine(provider, "<ESC>") + " cleared. Empty means no restriction." + reset)
		return
	}
	sort.Slice(chosen, func(i, j int) bool { return chosen[i].Model < chosen[j].Model })
	parts := make([]string, 0, len(chosen))
	for _, entry := range chosen {
		parts = append(parts, entry.Model+"("+entry.Role+")")
	}
	u.printSystemMessage(green + "Allowed models for " + sanitizeDiffLine(provider, "<ESC>") + ": " + sanitizeDiffLine(strings.Join(parts, ", "), "<ESC>") + reset)
	u.printSystemMessage(dim + "main will call list_allow_agent_models to pick by hardness: hard->smart, routine->balanced, trivial->cheap" + reset)
}

func (u *UI) persistAllowModelsPreference(entries []config.AllowedAgentModel) {
	writer, main := u.runtimePreferenceTarget()
	if writer == nil || !main {
		return
	}
	if err := writer.PersistAllowedAgentModels(entries); err != nil {
		u.printSystemMessage(yellow + "Warning: unable to persist allowed models: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
	}
}
