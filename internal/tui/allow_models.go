package tui

import (
	"context"
	"fmt"
	"io"
	"qcode/internal/llm"
	"sort"
	"strings"
	"time"

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
	header := fmt.Sprintf("%s · %d selected | Space: role | Enter: save | Esc: back", provider, allowed)
	if width >= 110 {
		header += fmt.Sprintf(" | %d/%d | off/smart/balanced/cheap", matched, total)
	}
	if query != "" {
		header += " | " + query
	} else if width >= 90 {
		header += " | Type to filter"
	}
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
	if len(fields) == 2 {
		u.chooseAllowProvider(ctx)
		return
	}
	if len(fields) < 3 || len(fields) > 4 {
		u.agentUsage()
		return
	}
	provider := ""
	if len(fields) == 4 {
		if fields[3] == "--all" && fields[2] == "clear" {
			provider = "*"
		} else {
			var ok bool
			provider, ok = resolveProviderID(fields[3], llm.Names())
			if !ok {
				u.agentUsage()
				return
			}
		}
	}
	switch fields[2] {
	case "list":
		u.listAllowModels(provider)
	case "clear":
		if provider == "" {
			provider = u.currentAllowProvider()
		}
		u.clearAllowModels(provider)
	default:
		u.agentUsage()
	}
}

func (u *UI) chooseAllowProvider(ctx context.Context) {
	if !u.activeAgentConfigurable() {
		return
	}
	for {
		names := llm.Names()
		current := u.currentAllowProvider()
		isAvailable := func(name string) bool { return u.isProviderSelectable(name, current) }
		labels := make([]string, len(names))
		disabled := make(map[string]bool, len(names))
		for i, name := range names {
			labels[i] = fmt.Sprintf("%s · %d selected", providerDisplayName(name), len(u.currentAllowedForProvider(name)))
			if !isAvailable(name) {
				labels[i] += providerNeedsSetupSuffix
				disabled[labels[i]] = true
			}
		}
		u.input.setRaw(true)
		u.beginRawSelector()
		label, accepted, err := selectOptionWithDisabled(u.input, u.themedSelectorWriter(), labels, disabled, "", min(12, max(3, u.height-6)), u.width, ColorEnabled(u.out), "Allowed sub-agent models | Enter: edit | Esc: close", false)
		u.input.setRaw(false)
		u.endRawSelector()
		if err != nil || !accepted {
			return
		}
		for i, candidate := range labels {
			if candidate == label {
				u.chooseAllowModels(ctx, names[i])
				break
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
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

func (u *UI) listAllowModels(provider string) {
	var entries []config.AllowedAgentModel
	if provider != "" {
		entries = u.currentAllowedForProvider(provider)
	} else if reader, ok := u.manager.(allowedModelsReader); ok {
		entries = reader.AllowedAgentModels()
	}
	label := providerDisplayName(provider)
	if label == "" {
		label = "all providers"
	}
	if len(entries) == 0 {
		u.printSystemMessage(dim + "No allowed models for " + sanitizeDiffLine(label, "<ESC>") + ". When the complete roster is empty, sub-agents use main’s current provider and model." + reset)
		return
	}
	var lines []string
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("  %s  %s", roleBadge(entry.Role), sanitizeDiffLine(entry.Provider+"/"+entry.Model, "<ESC>")))
	}
	u.printSystemMessage(dim + "Allowed models for " + sanitizeDiffLine(label, "<ESC>") + " (" + fmt.Sprint(len(entries)) + "):" + reset + "\n" + strings.Join(lines, "\n"))
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

func (u *UI) clearAllowModels(provider string) {
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
		if provider != "*" && entry.Provider != provider {
			kept = append(kept, entry)
		}
	}
	if kept == nil {
		kept = []config.AllowedAgentModel{}
	}
	writer.SetAllowedAgentModels(kept)
	u.persistAllowModelsPreference(kept)
	u.printSystemMessage(green + "Cleared allowed models for " + sanitizeDiffLine(providerDisplayName(provider), "<ESC>") + ". When the complete roster is empty, sub-agents use main’s current provider and model." + reset)
}

func (u *UI) chooseAllowModels(ctx context.Context, provider string) {
	if !u.activeAgentConfigurable() {
		return
	}
	u.screenMu.Lock()
	runner := u.runner
	u.screenMu.Unlock()
	if provider == "" {
		u.printSystemMessage(yellow + "Provider is unknown." + reset)
		return
	}
	var models []string
	var err error
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	u.input.setCancel(cancel)
	if available, hint := u.providerSetupStatus(provider); !available {
		err = fmt.Errorf("%s", hint)
	} else if provider == u.currentAllowProvider() {
		if r, ok := runner.(modelRunner); ok {
			models, err = r.ListModels(fetchCtx)
		} else {
			err = fmt.Errorf("model discovery unavailable")
		}
	} else if u.providerFactory != nil {
		var prepared llm.Provider
		prepared, _, err = u.providerFactory(fetchCtx, provider)
		if err == nil {
			if lister, ok := prepared.(llm.ModelLister); ok {
				models, err = lister.Models(fetchCtx)
			} else {
				err = fmt.Errorf("model discovery unavailable")
			}
		}
	} else {
		err = fmt.Errorf("provider preparation unavailable")
	}
	u.input.setCancel(nil)
	cancel()
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		u.printSystemMessage(yellow + "Unable to discover models: " + sanitizeDiffLine(err.Error(), "<ESC>") + ". Saved selections are preserved." + reset)
	}
	missing := map[string]bool{}
	for _, entry := range u.currentAllowedForProvider(provider) {
		if !containsOption(models, entry.Model) {
			missing[entry.Model] = true
			models = append(models, entry.Model)
		}
	}
	if len(models) == 0 {
		u.printSystemMessage(dim + "No models available for " + providerDisplayName(provider) + reset)
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
	displayModels := append([]string(nil), sorted...)
	for i, name := range sorted {
		if missing[name] {
			displayModels[i] += " (not listed)"
		}
	}
	selected, accepted, selErr := selectAllowModels(u.input, u.themedSelectorWriter(), displayModels, initial, visible, u.width, ColorEnabled(u.out), provider)
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
			if provider != "*" && entry.Provider != provider {
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
		u.printSystemMessage(green + "Allowed models for " + sanitizeDiffLine(providerDisplayName(provider), "<ESC>") + " cleared. When the complete roster is empty, sub-agents use main’s current provider and model." + reset)
		return
	}
	sort.Slice(chosen, func(i, j int) bool { return chosen[i].Model < chosen[j].Model })
	parts := make([]string, 0, len(chosen))
	for _, entry := range chosen {
		parts = append(parts, entry.Model+"("+entry.Role+")")
	}
	u.printSystemMessage(green + "Allowed models for " + sanitizeDiffLine(providerDisplayName(provider), "<ESC>") + ": " + sanitizeDiffLine(strings.Join(parts, ", "), "<ESC>") + reset)
	u.printSystemMessage(dim + "main will call list_allow_agent_models to pick by hardness: hard->smart, routine->balanced, trivial->cheap" + reset)
}

func (u *UI) persistAllowModelsPreference(entries []config.AllowedAgentModel) {
	writer, main := u.runtimePreferenceTarget()
	if writer == nil || !main {
		return
	}
	if err := writer.PersistAllowedAgentModels(entries); err != nil {
		u.printSystemMessage(yellow + "Changes apply to this session only; unable to persist allowed models: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
	}
}
