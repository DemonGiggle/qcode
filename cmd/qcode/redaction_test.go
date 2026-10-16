package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"qcode/internal/agent"
	"qcode/internal/llm"
	"qcode/internal/redaction"
	"qcode/internal/session"
)

const cliRedactionSecret = "synthetic-cli-api-credential"

type cliRedactionProvider struct{ request llm.Request }

func (*cliRedactionProvider) Name() string { return "redaction-cli-test" }
func (p *cliRedactionProvider) Complete(_ context.Context, r llm.Request, cb llm.StreamCallback) (llm.Response, error) {
	p.request = r
	for _, b := range []byte(cliRedactionSecret) {
		cb(llm.StreamEvent{Text: string(b)})
	}
	return llm.Response{}, errors.New("provider failed: " + cliRedactionSecret)
}
func TestCLIRegistersEffectiveKeyAndFiltersRedirectedErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("QCODE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	provider := &cliRedactionProvider{}
	llm.Register(provider.Name(), func(llm.Config) (llm.Provider, error) { return provider, nil })
	for _, events := range []bool{false, true} {
		out, _ := os.CreateTemp(t.TempDir(), "out")
		defer out.Close()
		errOut, _ := os.CreateTemp(t.TempDir(), "err")
		defer errOut.Close()
		in, _ := os.CreateTemp(t.TempDir(), "in")
		defer in.Close()
		args := []string{"--provider", provider.Name(), "--model", "test", "--cwd", t.TempDir(), "--api-key", cliRedactionSecret, "prompt " + cliRedactionSecret}
		if events {
			args = append([]string{"--json-events"}, args...)
		}
		err := run(args, in, out, errOut)
		if err == nil || strings.Contains(err.Error(), cliRedactionSecret) {
			t.Fatal("returned diagnostic leaked")
		}
		stdout, _ := os.ReadFile(out.Name())
		stderr, _ := os.ReadFile(errOut.Name())
		if bytes.Contains(stdout, []byte(cliRedactionSecret)) || bytes.Contains(stderr, []byte(cliRedactionSecret)) || !bytes.Contains(stdout, []byte(redaction.Marker)) {
			t.Fatal("CLI output leaked")
		}
		if provider.request.Messages[1].Content != "prompt "+cliRedactionSecret {
			t.Fatal("provider prompt changed")
		}
		if events {
			for _, line := range strings.Split(strings.TrimSpace(string(stderr)), "\n") {
				if !json.Valid([]byte(line)) {
					t.Fatalf("JSON event corrupted: %q", line)
				}
			}
		}
	}
}
func TestSnapshotStorageFiltersAllOwnedPayloads(t *testing.T) {
	p, _ := redaction.New(redaction.Config{}, []string{cliRedactionSecret})
	state := agent.SavedState{Provider: "test", Model: "test", System: "system", LastResponse: cliRedactionSecret, Messages: []agent.SavedMessage{{Message: llm.Message{Role: "user", Content: cliRedactionSecret}, ToolCalls: []agent.SavedToolCall{{ID: "call-1", Name: "shell", Arguments: []byte(`{"command":"` + cliRedactionSecret + `"}`)}}}}}
	data, _ := json.Marshal(state)
	dir, root := t.TempDir(), t.TempDir()
	store, _ := session.Open(dir, root)
	snap, _ := store.New()

	snap.Preview = cliRedactionSecret
	snap.Agents = []session.SavedAgent{{Summary: session.Summary{ID: "main", Name: "Main", CurrentTask: cliRedactionSecret, Error: cliRedactionSecret}, State: data}}
	snap.Presentation = json.RawMessage(`{"Active":"main","Drafts":{"main":"` + cliRedactionSecret + `"},"Views":[{"ID":"main","History":{"Lines":["` + cliRedactionSecret + `"],"Archive":["` + cliRedactionSecret + `"]},"Buffer":"` + cliRedactionSecret + `","Diffs":["` + cliRedactionSecret + `"]}]}`)
	snap.Work = &session.WorkHistory{NextRequestID: 5, Records: []session.WorkRecord{{AgentID: "main", RequestID: "request-1", Prompt: cliRedactionSecret, Response: cliRedactionSecret, Steers: []session.SteeringInput{{ID: "steer-1", Text: cliRedactionSecret}}}}, Events: []session.ConsultationEvent{{AgentID: "main", Error: cliRedactionSecret}}, SteeringEvents: []session.SteeringEvent{{Sequence: 3, AgentID: "main", Input: session.SteeringInput{ID: "steer-1", Text: cliRedactionSecret}}}}
	store.SetSnapshotFilter(func(s session.Snapshot) (session.Snapshot, error) { return filterSessionSnapshot(p, s) })
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*.json"))
	if len(files) != 1 {
		t.Fatal("missing snapshot")
	}
	disk, _ := os.ReadFile(files[0])
	if bytes.Contains(disk, []byte(cliRedactionSecret)) || bytes.Contains(disk, []byte(base64.StdEncoding.EncodeToString(state.Messages[0].ToolCalls[0].Arguments))) {
		t.Fatal("persisted snapshot leaked")
	}
	saved, err := store.Load(snap.ID)
	if err != nil || saved.ID != snap.ID || saved.Workspace != snap.Workspace || saved.Work.NextRequestID != 5 || saved.Work.SteeringEvents[0].Sequence != 3 {
		t.Fatal("operational state changed")
	}
	var decoded agent.SavedState
	json.Unmarshal(saved.Agents[0].State, &decoded)
	if bytes.Contains(decoded.Messages[0].ToolCalls[0].Arguments, []byte(cliRedactionSecret)) || !json.Valid(decoded.Messages[0].ToolCalls[0].Arguments) {
		t.Fatal("decoded arguments leaked")
	}
	if state.Messages[0].Content != cliRedactionSecret || snap.Work.Records[0].Prompt != cliRedactionSecret {
		t.Fatal("save mutated runtime")
	}
}

func TestSnapshotListingIsolatesMalformedOwnedPayloads(t *testing.T) {
	for _, payload := range []string{"presentation", "agent checkpoint"} {
		t.Run(payload, func(t *testing.T) {
			store, err := session.Open(t.TempDir(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			healthy, _ := store.New()
			healthy.Preview = "healthy conversation"
			broken, _ := store.New()
			broken.Preview = cliRedactionSecret
			if payload == "presentation" {
				broken.Presentation = json.RawMessage(`{"Views":"wrong type"}`)
			} else {
				broken.Agents = []session.SavedAgent{{State: json.RawMessage(`{"Messages":"wrong type"}`)}}
			}
			for _, snap := range []session.Snapshot{healthy, broken} {
				if err := store.Save(snap); err != nil {
					t.Fatal(err)
				}
			}
			p, err := redaction.New(redaction.Config{}, []string{cliRedactionSecret})
			if err != nil {
				t.Fatal(err)
			}
			store.SetSnapshotFilter(func(s session.Snapshot) (session.Snapshot, error) {
				return filterSessionSnapshot(p, s)
			})
			entries, err := store.List()
			if err != nil || len(entries) != 2 {
				t.Fatalf("malformed payload blocked listing: %+v, %v", entries, err)
			}
			for _, entry := range entries {
				if entry.ID == healthy.ID && entry.Problem != "" {
					t.Fatalf("healthy conversation unavailable: %+v", entry)
				}
				if entry.ID == broken.ID && (entry.Problem == "" || entry.Preview != "" || len(entry.Agents) != 0 || len(entry.Presentation) != 0) {
					t.Fatalf("rejected payload escaped through listing: %+v", entry)
				}
			}
			if _, err := store.LoadForResume(healthy.ID); err != nil {
				t.Fatal("healthy conversation could not resume", err)
			}
		})
	}
}

func TestInvalidRedactionConfigStopsCLIStartup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux configuration lookup fixture")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".local", "etc", "qcode", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[redaction]\ncustom_patterns = ['private-regex[']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, _ := os.CreateTemp(t.TempDir(), "output")
	defer out.Close()
	err := run([]string{"hello"}, out, out, out)
	if err == nil || !strings.Contains(err.Error(), "redaction.custom_patterns[0]") || strings.Contains(err.Error(), "private-regex") {
		t.Fatalf("startup diagnostic: %v", err)
	}
}
