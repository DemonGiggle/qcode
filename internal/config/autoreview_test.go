package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistAutoreviewRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writer := newRuntimePreferenceWriter(path)
	if err := writer.PersistAutoreview(true); err != nil {
		t.Fatal(err)
	}
	if err := writer.PersistAutoreviewModel("ollama", "qwen2.5-coder:7b"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed Config
	if err := loadAndDecodeForTest(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.AutoReview == nil || !*parsed.AutoReview {
		t.Fatalf("saved autoreview = %v", parsed.AutoReview)
	}
	if parsed.AutoReviewProvider != "ollama" || parsed.AutoReviewModel != "qwen2.5-coder:7b" {
		t.Fatalf("saved reviewer = %q/%q", parsed.AutoReviewProvider, parsed.AutoReviewModel)
	}
	if err := writer.PersistAutoreview(false); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loadAndDecodeForTest(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.AutoReview == nil || *parsed.AutoReview {
		t.Fatalf("saved autoreview off = %v", parsed.AutoReview)
	}
}

func TestPersistAutoreviewModelClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writer := newRuntimePreferenceWriter(path)
	if err := writer.PersistAutoreviewModel("openai", "gpt-5"); err != nil {
		t.Fatal(err)
	}
	if err := writer.PersistAutoreviewModel("", ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed Config
	if err := loadAndDecodeForTest(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.AutoReviewProvider != "" || parsed.AutoReviewModel != "" {
		t.Fatalf("clear reviewer = %q/%q", parsed.AutoReviewProvider, parsed.AutoReviewModel)
	}
}
