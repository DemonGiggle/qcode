package redaction

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestRegisterSecretsConcurrentConsumers(t *testing.T) {
	p, err := New(Config{}, []string{"initial-credential"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 12; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				secret := fmt.Sprintf("opaque-%d-%d-quote\"slash\\end", worker, i)
				encoded, _ := json.Marshal(secret)
				escaped := string(encoded[1 : len(encoded)-1])
				stream := p.Stream(Terminal)
				stream.Feed(secret[:8])
				p.RegisterSecrets(secret, secret, "", Marker)
				check := func(label, got string) {
					if strings.Contains(got, secret) || strings.Contains(got, escaped) || !strings.Contains(got, Marker) {
						t.Errorf("%s did not redact registered secret", label)
					}
				}
				check("existing stream", stream.Feed(secret[8:])+stream.Flush())
				check("text", p.Text(Terminal, secret+" "+escaped))
				check("redact", p.Redact(secret))
				if !p.ContainsSecret(secret) {
					t.Error("ContainsSecret missed secret")
				}
				check("JSON", string(p.JSON(Persistence, encoded)))
				check("Replay", string(p.Replay(Remote, encoded)))
				check("Copy", Copy(p, Exports, struct{ Content string }{secret}).Content)
				check("Value", p.Value(JSONEvents, secret).(string))
				check("Error", p.Error(Terminal, fmt.Errorf("echo %s", secret)).Error())
			}
		}(worker)
	}
	wg.Wait()
	values := p.literalSnapshot()
	seen := map[string]bool{}
	for i, value := range values {
		if seen[value] {
			t.Fatal("duplicate literal")
		}
		seen[value] = true
		if i > 0 && (len(values[i-1]) < len(value) || len(values[i-1]) == len(value) && values[i-1] > value) {
			t.Fatal("literals not deterministically sorted longest-first")
		}
	}
}

func TestRegisterSecretsKeepsConfiguration(t *testing.T) {
	disabled := false
	p, _ := New(Config{Terminal: &disabled, CustomPatterns: []string{"custom-match"}}, nil)
	stream := p.Stream(Persistence)
	stream.Feed("new-")
	p.RegisterSecrets("new-credential-long", "new-credential", "new-credential-long")
	if got := stream.Feed("credential-long\n"); got != Marker+"\n" {
		t.Fatalf("stream = %q", got)
	}
	if got := p.Text(Terminal, "new-credential"); got != "new-credential" {
		t.Fatal("sink switch changed")
	}
	if got := p.Text(Persistence, "custom-match new-credential-long"); got != Marker+" "+Marker {
		t.Fatalf("text = %q", got)
	}
	if p.Redact("new-credential") != Marker {
		t.Fatal("Redact lost shared registry")
	}
	var absent *Policy
	absent.RegisterSecrets("nil-policy-credential")
	if absent.Text(Terminal, "nil-policy-credential") != "nil-policy-credential" {
		t.Fatal("nil registration mutated default")
	}
}
