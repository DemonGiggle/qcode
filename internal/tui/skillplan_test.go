package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type skillPlanTestRunner struct {
	enabled  bool
	cleared  bool
	name     string
	location string
	content  string
}

func (*skillPlanTestRunner) Run(context.Context, string) error { return nil }
func (r *skillPlanTestRunner) SkillPlanMode() bool             { return r.enabled }
func (r *skillPlanTestRunner) SetSkillPlanMode(enabled bool)   { r.enabled = enabled }
func (r *skillPlanTestRunner) ClearLatestSkillDraft()          { r.cleared = true }
func (r *skillPlanTestRunner) LatestSkillDraftText() (string, bool) {
	if r.name == "" {
		return "", false
	}
	return r.content, true
}
func (r *skillPlanTestRunner) LatestSkillDraftParts() (string, string, string, bool) {
	return r.name, r.location, r.content, r.name != ""
}

func TestSkillPlanCreateWritesExactReviewedDraft(t *testing.T) {
	root := t.TempDir()
	content := "---\ndescription: Review database migrations safely.\n---\n\n# Review migrations\n"
	runner := &skillPlanTestRunner{enabled: true, name: "review-migrations", location: ".qcode/skills", content: content}
	var output bytes.Buffer
	u := &UI{runner: runner, root: root, display: newHistoryWriter(&output)}

	u.handleSkillPlanCommand(context.Background(), []string{"/skillplan", "create"})

	path := filepath.Join(root, ".qcode", "skills", "review-migrations", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("created skill = %q, %v", data, err)
	}
	if runner.enabled {
		t.Fatal("Skill Plan mode remained enabled after creation")
	}
	if !strings.Contains(output.String(), "Skill created at") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestSkillPlanCreateDoesNotOverwriteExistingSkill(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".agents", "skills", "review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &skillPlanTestRunner{enabled: true, name: "review", location: ".agents/skills", content: "replacement"}
	var output bytes.Buffer
	u := &UI{runner: runner, root: root, display: newHistoryWriter(&output)}

	u.handleSkillPlanCommand(context.Background(), []string{"/skillplan", "create"})

	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing" {
		t.Fatalf("existing skill = %q, %v", data, err)
	}
	if !runner.enabled || !strings.Contains(output.String(), "already exists") {
		t.Fatalf("enabled=%v output=%q", runner.enabled, output.String())
	}
}
