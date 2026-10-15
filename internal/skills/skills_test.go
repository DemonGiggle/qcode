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
	if err != nil {
		t.Fatalf("Load(review) = %q, %v", content, err)
	}
	assertLoadedSkill(t, content, "review", filepath.Join(root, ".qcode/skills/review/SKILL.md"), "---\ndescription: Focused review\n---\npreferred instructions")
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
	escape := filepath.Join(root, ".qcode/skills/escape/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(escape), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, escape); err != nil {
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
	got, err := selection.Load("review")
	if err != nil {
		t.Fatalf("Load(review) = %q, %v", got, err)
	}
	assertLoadedSkill(t, got, "review", filepath.Join(root, ".qcode/skills/review/SKILL.md"), "Review instructions")
	selection.Set(nil)
	if _, err := selection.Load("review"); err == nil {
		t.Fatal("deselected skill loaded")
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
	got, err := catalog.Load("global")
	if err != nil {
		t.Fatalf("Load(global) = %q, %v", got, err)
	}
	assertLoadedSkill(t, got, "global", filepath.Join(home, ".qcode/skills/global/SKILL.md"), "Global instructions")
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

func TestSkillsInPathsUsesWinningDefinitionAndResolvedRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	writeSkill(t, root, ".qcode/skills/review/SKILL.md", "# Built-in review")
	writeSkill(t, root, "shared/review/SKILL.md", "# Shared review")
	writeSkill(t, root, "shared/check/SKILL.md", "# Shared check")
	writeSkill(t, root, "override/review/SKILL.md", "# Override review")
	writeSkill(t, home, "personal/format/SKILL.md", "# Personal format")
	alias := filepath.Join(root, "shared-link")
	if err := os.Symlink(filepath.Join(root, "shared"), alias); err != nil {
		t.Fatal(err)
	}
	catalog, err := Discover(root, "shared-link", "override", "~/personal", "missing")
	if err != nil {
		t.Fatal(err)
	}
	selected := catalog.SkillsInPaths("shared", "~/personal", "missing", " ")
	if len(selected) != 2 || selected[0].Name != "check" || selected[1].Name != "format" {
		t.Fatalf("selected = %#v, want check and format", selected)
	}
	if got := catalog.SkillsInPaths("override"); len(got) != 1 || got[0].Name != "review" {
		t.Fatalf("override selection = %#v", got)
	}
	got, err := catalog.Load("review")
	if err != nil {
		t.Fatal(err)
	}
	assertLoadedSkill(t, got, "review", filepath.Join(root, "override/review/SKILL.md"), "# Override review")
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

	got, err := selection.Load("review")
	if err != nil {
		t.Fatalf("Load(review) = %q, %v", got, err)
	}
	assertLoadedSkill(t, got, "review", filepath.Join(root, ".qcode/skills/review/SKILL.md"), "Review instructions")
}

func TestLoadContextForSkillLocationsWithSpaces(t *testing.T) {
	for _, location := range []string{".agents/skills", ".qcode/skills", "user", "custom-relative", "custom-absolute"} {
		t.Run(location, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "workspace with spaces")
			home := filepath.Join(base, "home with spaces")
			t.Setenv("HOME", home)
			skillRoot := filepath.Join(root, location)
			var customPaths []string
			switch location {
			case "user":
				skillRoot = filepath.Join(home, ".qcode/skills")
			case "custom-relative":
				skillRoot = filepath.Join(root, "team skills")
				customPaths = []string{"team skills"}
			case "custom-absolute":
				skillRoot = filepath.Join(base, "external skills")
				customPaths = []string{skillRoot}
			}
			const markdown = "\n---\ndescription: Query a bundled database.\n---\n\n# Query\n\nUse `foo.db`.\n  \t"
			writeSkill(t, skillRoot, "query/SKILL.md", markdown)
			catalog, err := Discover(root, customPaths...)
			if err != nil {
				t.Fatal(err)
			}
			got, err := catalog.Load("query")
			if err != nil {
				t.Fatal(err)
			}
			assertLoadedSkill(t, got, "query", filepath.Join(skillRoot, "query/SKILL.md"), markdown)
		})
	}
}

func TestLoadContextUsesValidatedSymlinkTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	const markdown = "# Query\r\n\r\nRead `foo.db`.\r\n"
	writeSkill(t, root, ".qcode/skills/query/SKILL.md", markdown)
	catalog, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the discovered document with a link inside the same root. The
	// returned context must describe the target actually validated at load time.
	target := filepath.Join(root, ".qcode/skills/references/query.md")
	writeSkill(t, root, ".qcode/skills/references/query.md", markdown)
	path := filepath.Join(root, ".qcode/skills/query/SKILL.md")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	got, err := catalog.Load("query")
	if err != nil {
		t.Fatal(err)
	}
	assertLoadedSkill(t, got, "query", target, markdown)
	// Discovery must also use the resolved target rather than the link's parent.
	catalog, err = Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err = catalog.Load("query")
	if err != nil {
		t.Fatal(err)
	}
	assertLoadedSkill(t, got, "query", target, markdown)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(outside, []byte("outside instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if got, err := catalog.Load("query"); err == nil || !strings.Contains(err.Error(), "outside its skill directory") || got != "" {
		t.Fatalf("Load(query) after symlink escape = %q, %v", got, err)
	}
}

func TestLoadAllowsDocumentAtSizeLimit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	markdown := strings.Repeat("x", maxFileSize)
	writeSkill(t, root, ".qcode/skills/large/SKILL.md", markdown)
	catalog, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := catalog.Load("large")
	if err != nil {
		t.Fatal(err)
	}
	assertLoadedSkill(t, got, "large", filepath.Join(root, ".qcode/skills/large/SKILL.md"), markdown)
}

func TestCreateWritesReviewedSkillWithoutOverwriting(t *testing.T) {
	root := t.TempDir()
	content := "---\ndescription: Review migrations safely.\n---\n\n# Review migrations\n"
	path, err := Create(root, ".qcode/skills", "review-migrations", content)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".qcode", "skills", "review-migrations", "SKILL.md")
	if path != want {
		t.Fatalf("Create() path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("created content = %q, %v", data, err)
	}
	if _, err := Create(root, ".qcode/skills", "review-migrations", "replacement"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite error = %v", err)
	}
}

func TestCreateRejectsUnsupportedDrafts(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		location string
		name     string
		content  string
	}{
		{location: ".qcode/skills", name: "Not Valid", content: "instructions"},
		{location: "custom/skills", name: "valid", content: "instructions"},
		{location: ".agents/skills", name: "valid", content: ""},
		{location: ".agents/skills", name: "valid", content: strings.Repeat("x", maxFileSize+1)},
	}
	for _, test := range tests {
		if _, err := Create(root, test.location, test.name, test.content); err == nil {
			t.Fatalf("Create(%q, %q) unexpectedly succeeded", test.location, test.name)
		}
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

func assertLoadedSkill(t *testing.T, got, name, path, markdown string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	header, body, ok := strings.Cut(got, "\n\n")
	if !ok || body != markdown {
		t.Fatalf("loaded skill Markdown changed: got %q, want %q", body, markdown)
	}
	want := strings.Join([]string{"Skill context:", "Name: " + name, "SKILL.md path: " + resolved, "Base directory: " + filepath.Dir(resolved)}, "\n")
	if header != want {
		t.Fatalf("skill context = %q, want %q", header, want)
	}
}
