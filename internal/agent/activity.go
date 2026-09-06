package agent

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"qcode/internal/llm"
	"qcode/internal/trace"
)

const maxActivityTargetRunes = 96

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
		return activity("read", "Reading "+target(path, "file"), "Read "+target(path, "file"), trace.ActivityRead)
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
		return activity("shell", "Running shell command", "Ran shell command", trace.ActivityWrite)
	case "view_image":
		return activity("view_image", "Viewing "+target(path, "image"), "Viewed "+target(path, "image"), trace.ActivityRead)
	case "skill":
		return activity("skill", "Loading skill "+target(name, "skill"), "Loaded skill "+target(name, "skill"), trace.ActivityAgent)
	case "request_directory_access":
		return activity("request_directory_access", "Requesting access to "+target(path, "directory"), "Requested access to "+target(path, "directory"), trace.ActivityWrite)
	case "list_agents":
		return activity("list_agents", "Listing agents", "Listed agents", trace.ActivityAgent)
	case "delegate_task":
		return activity("delegate_task", "Consulting "+target(agentID, "agent"), "Agent "+target(agentID, "agent")+" accepted the task", trace.ActivityAgent)
	case "get_agent_result":
		return activity("get_agent_result", "Checking "+target(agentID, "agent"), "Checked "+target(agentID, "agent"), trace.ActivityAgent)
	default:
		return activity(call.Name, "Using "+activityTarget(call.Name), "Used "+activityTarget(call.Name), trace.ActivityOther)
	}
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

func activityURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Host
}

func activityTarget(value string) string {
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
		if count == maxActivityTargetRunes {
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
