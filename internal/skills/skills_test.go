package skills

import (
	"os"
	"path/filepath"
	"strings"
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

func TestDiscoverIncludesCustomSkillsAndKeepsMissingLocations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	custom := filepath.Join(t.TempDir(), "custom-skills")
	writeSkill(t, custom, "custom/SKILL.md", "# Custom skill\n")
	missing := filepath.Join(t.TempDir(), "missing-skills")

	catalog, err := Discover(root, custom, missing)
	if err != nil {
		t.Fatal(err)
	}
	items := catalog.Skills()
	if len(items) != 1 || items[0].Name != "custom" || items[0].Path() != filepath.Join(custom, "custom", "SKILL.md") {
		t.Fatalf("skills = %#v, want custom skill", items)
	}
	locations := catalog.Locations()
	if len(locations) < 5 || locations[len(locations)-2] != custom || locations[len(locations)-1] != missing {
		t.Fatalf("locations = %#v, want custom and missing paths retained", locations)
	}
}

func TestDiscoverReadsOnlySkillDescriptionPrefix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	large := "# Large skill\n" + strings.Repeat("x", maxFileSize)
	writeSkill(t, root, ".qcode/skills/large/SKILL.md", large)

	catalog, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover() rejected large skill: %v", err)
	}
	if items := catalog.Skills(); len(items) != 1 || items[0].Description != "Large skill" {
		t.Fatalf("skills = %#v", items)
	}
	if _, err := catalog.Load("large"); err == nil || !strings.Contains(err.Error(), "exceeds the 64 KiB limit") {
		t.Fatalf("Load(large) error = %v", err)
	}
}

func TestLazySelectionDefersDiscoveryUntilLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	selection := NewLazySelection(root)
	selection.Set([]string{"review"})
	writeSkill(t, root, ".qcode/skills/review/SKILL.md", "Review instructions")

	if got, err := selection.Load("review"); err != nil || got != "Review instructions" {
		t.Fatalf("Load(review) = %q, %v", got, err)
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
