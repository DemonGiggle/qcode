package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverListsSkillsAndQcodeTakesPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeSkill(t, root, ".agents/skills/review/SKILL.md", "---\ndescription: General review\n---\nagent instructions")
	writeSkill(t, root, ".qcode/skills/review/SKILL.md", "---\ndescription: Focused review\n---\npreferred instructions")
	writeSkill(t, root, ".agents/skills/test/SKILL.md", "# Test skill\nDo the tests.")

	catalog, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	items := catalog.Skills()
	if len(items) != 2 || items[0].Name != "review" || items[0].Description != "Focused review" || items[1].Name != "test" {
		t.Fatalf("skills = %#v", items)
	}
	content, err := catalog.Load("review")
	if err != nil || content != "---\ndescription: Focused review\n---\npreferred instructions" {
		t.Fatalf("Load(review) = %q, %v", content, err)
	}
}

func TestDiscoverIgnoresInvalidNamesAndSkillOutsideWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeSkill(t, root, ".qcode/skills/Not-valid/SKILL.md", "ignored")
	outside := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".qcode/skills/linked")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), link); err != nil {
		t.Fatal(err)
	}
	catalog, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills()) != 0 {
		t.Fatalf("skills = %#v, want none", catalog.Skills())
	}
}

func TestSelectionLoadsOnlyEnabledSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeSkill(t, root, ".qcode/skills/review/SKILL.md", "Review instructions")
	catalog, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	selection := NewSelection(catalog)
	if _, err := selection.Load("review"); err == nil {
		t.Fatal("disabled skill loaded")
	}
	selection.Set([]string{"review"})
	if got, err := selection.Load("review"); err != nil || got != "Review instructions" {
		t.Fatalf("Load(review) = %q, %v", got, err)
	}
}

func TestDiscoverIncludesUserSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	writeSkill(t, home, ".qcode/skills/global/SKILL.md", "Global instructions")
	catalog, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := catalog.Load("global"); err != nil || got != "Global instructions" {
		t.Fatalf("Load(global) = %q, %v", got, err)
	}
}

func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
