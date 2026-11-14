package config

import "testing"

func TestProviderKind(t *testing.T) {
	for _, tc := range []struct {
		name, kind, want string
		setType          bool
	}{
		{"ollama", "", "ollama", false},
		{"gemini", "", "gemini", false},
		{"lab", "ollama", "ollama", true},
		{"second", "openai", "openai", true},
		{"lab", "", "", false},
		{"lab", "", "", true},
		{"lab", "other-instance", "", true},
		{"ollama", "openai", "", true},
		{"custom", "ollama", "", true},
		{"bad.name", "ollama", "", true},
		{"Bad", "ollama", "", true},
	} {
		t.Run(tc.name+"/"+tc.kind, func(t *testing.T) {
			p := ProviderProfile{}
			if tc.setType {
				p.Type = stringPointer(tc.kind)
			}
			cfg := Config{Providers: map[string]ProviderProfile{tc.name: p}}
			got, err := cfg.ProviderKind(tc.name)
			if (err != nil) != (tc.want == "") || got != tc.want {
				t.Fatalf("ProviderKind = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestInstanceTypeLayering(t *testing.T) {
	low, err := decodeConfig([]byte("[providers.lab]\ntype = 'ollama'\nbase_url = 'http://lab'\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	high, err := decodeConfig([]byte("[providers.lab]\nmodel = 'coder'\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	merge(&low, high)
	if kind, err := low.ProviderKind("lab"); err != nil || kind != "ollama" {
		t.Fatalf("merged kind = %q, %v", kind, err)
	}
	if *low.Providers["lab"].BaseURL != "http://lab" || *low.Providers["lab"].Model != "coder" {
		t.Fatal("lost profile fields")
	}
}
