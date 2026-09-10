package agent

import (
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"qcode/internal/llm"
	"qcode/internal/trace"
)

const (
	maxActivityTargetRunes = 96
	// Keep the entire shell activity comfortably within a typical terminal row,
	// including its label and duration.
	maxShellCommandRunes = 48
)

// toolActivity turns an allowlisted subset of a tool call into safe terminal
// telemetry. It intentionally never returns arbitrary arguments or tool
// content: activities are output-only and may be visible in scrollback.
func toolActivity(call llm.ToolCall) trace.Activity {
	path := activityArgument(call.Arguments, "path")
	name := activityArgument(call.Arguments, "name")
	agentID := activityArgument(call.Arguments, "agent_id")
	pattern := activityArgument(call.Arguments, "pattern")
	query := activityArgument(call.Arguments, "query")
	address := activityURL(activityArgument(call.Arguments, "url"))
	target := func(value, fallback string) string {
		value = activityTarget(value)
		if value == "" {
			return fallback
		}
		return value
	}

	switch call.Name {
	case "read":
		readTarget := readActivityTarget(path, call.Arguments)
		return activity("read", "Reading "+target(readTarget, "file"), "Read "+target(readTarget, "file"), trace.ActivityRead)
	case "write":
		return activity("write", "Writing "+target(path, "file"), "Wrote "+target(path, "file"), trace.ActivityWrite)
	case "edit":
		return activity("edit", "Editing "+target(path, "file"), "Edited "+target(path, "file"), trace.ActivityWrite)
	case "list":
		return activity("list", "Listing "+target(path, "directory"), "Listed "+target(path, "directory"), trace.ActivityRead)
	case "search":
		if pattern != "" {
			value := activityTarget(pattern)
			return activity("search", "Searching workspace for "+quoteActivity(value), "Searched workspace for "+quoteActivity(value), trace.ActivityRead)
		}
		return activity("search", "Searching workspace", "Searched workspace", trace.ActivityRead)
	case "web_search":
		if query != "" {
			value := activityTarget(query)
			return activity("web_search", "Searching web for "+quoteActivity(value), "Searched web for "+quoteActivity(value), trace.ActivityRead)
		}
		return activity("web_search", "Searching web", "Searched web", trace.ActivityRead)
	case "web_fetch":
		return activity("web_fetch", "Fetching "+target(address, "web page"), "Fetched "+target(address, "web page"), trace.ActivityRead)
	case "shell":
		command := shellActivityCommand(activityArgument(call.Arguments, "command"))
		if command == "" {
			return activity("shell", "Running shell command", "Ran shell command", trace.ActivityWrite)
		}
		return activity("shell", "Running shell command: "+command, "Ran shell command: "+command, trace.ActivityWrite)
	case "view_image":
		return activity("view_image", "Viewing "+target(path, "image"), "Viewed "+target(path, "image"), trace.ActivityRead)
	case "skill":
		return activity("skill", "Loading skill "+target(name, "skill"), "Loaded skill "+target(name, "skill"), trace.ActivityAgent)
	case "request_directory_access":
		return activity("request_directory_access", "Requesting access to "+target(path, "directory"), "Requested access to "+target(path, "directory"), trace.ActivityWrite)
	case "list_agents":
		return activity("list_agents", "Listing agents", "Listed agents", trace.ActivityAgent)
	case "search_agent_work":
		return activity("search_agent_work", "Searching agent work history", "Searched agent work history", trace.ActivityAgent)
	case "consult_agents":
		return activity("consult_agents", "Waiting for agent consultations", "Collected agent consultation outcomes", trace.ActivityAgent)
	case "create_agent":
		return activity("create_agent", "Creating agent", "Created agent", trace.ActivityAgent)
	case "delegate_task":
		return activity("delegate_task", "Consulting "+target(agentID, "agent"), "Agent "+target(agentID, "agent")+" accepted the task", trace.ActivityAgent)
	case "get_agent_result":
		return activity("get_agent_result", "Checking "+target(agentID, "agent"), "Checked "+target(agentID, "agent"), trace.ActivityAgent)
	default:
		return activity(call.Name, "Using "+activityTarget(call.Name), "Used "+activityTarget(call.Name), trace.ActivityOther)
	}
}

// readActivityTarget adds the requested one-based line window when the call
// explicitly selects one. This makes separate reads of the same file
// distinguishable without exposing file content.
func readActivityTarget(path string, arguments json.RawMessage) string {
	offset, hasOffset := activityInteger(arguments, "offset")
	line, hasLine := activityInteger(arguments, "line")
	limit, hasLimit := activityInteger(arguments, "limit")
	if !hasOffset && !hasLine && !hasLimit {
		return path
	}
	if offset < 1 {
		offset = line
	}
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}
	end := offset + limit - 1
	if end < offset { // Prevent an overflow from producing a misleading range.
		end = int(^uint(0) >> 1)
	}
	suffix := ":" + strconv.Itoa(offset) + "-" + strconv.Itoa(end)
	// Reserve room for the range so a long path cannot make the useful part of
	// the activity title disappear when it is truncated for the terminal.
	return truncateActivityTarget(path, maxActivityTargetRunes-utf8.RuneCountInString(suffix)-1) + suffix
}

func activity(action, start, completed string, category trace.ActivityCategory) trace.Activity {
	return trace.Activity{Action: action, Start: start, Completed: completed, Category: category}
}

func activityArgument(arguments json.RawMessage, key string) string {
	var values map[string]json.RawMessage
	if json.Unmarshal(arguments, &values) != nil {
		return ""
	}
	var value string
	if json.Unmarshal(values[key], &value) != nil {
		return ""
	}
	return value
}

// activityInteger accepts the same integral number encodings as the read
// tool, including local models that serialize an integer as a decimal string.
func activityInteger(arguments json.RawMessage, key string) (int, bool) {
	var values map[string]json.RawMessage
	if json.Unmarshal(arguments, &values) != nil {
		return 0, false
	}
	data, ok := values[key]
	if !ok {
		return 0, false
	}
	value := strings.TrimSpace(string(data))
	if len(value) > 0 && value[0] == '"' {
		if json.Unmarshal(data, &value) != nil {
			return 0, false
		}
	}
	number, err := strconv.ParseFloat(value, 64)
	maxInt := int64(^uint(0) >> 1)
	minInt := -maxInt - 1
	if err != nil || math.IsInf(number, 0) || math.IsNaN(number) || math.Trunc(number) != number || number < float64(minInt) || number > float64(maxInt) {
		return 0, false
	}
	return int(number), true
}

func activityURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Host
}

func activityTarget(value string) string {
	return truncateActivityTarget(value, maxActivityTargetRunes)
}

// shellActivityCommand gives the command enough room to be recognizable while
// keeping the persistent activity event on one terminal row. It uses the same
// control-character sanitization as other activity targets.
func shellActivityCommand(command string) string {
	return truncateActivityTarget(command, maxShellCommandRunes)
}

func truncateActivityTarget(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	var out strings.Builder
	count := 0
	for _, character := range value {
		if unicode.IsControl(character) {
			out.WriteRune('?')
		} else {
			out.WriteRune(character)
		}
		count++
		if count == maxRunes {
			if utf8.RuneCountInString(value) > count {
				out.WriteRune('…')
			}
			break
		}
	}
	return out.String()
}

func quoteActivity(value string) string {
	return `"` + value + `"`
}
