package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"qcode/internal/chatgptoauth"
	"qcode/internal/redaction"
)

// All HTTP is intercepted; credentials are synthetic and stored only in TempDir.
func TestChatGPTRegistersCredentialsBeforeEchoes(t *testing.T) {
	for _, models := range []bool{false, true} {
		for _, mode := range []string{"fresh", "expired", "retry", "refresh-error", "provider-error"} {
			t.Run(fmt.Sprintf("models=%t/%s", models, mode), func(t *testing.T) {
				old := chatgptoauth.Tokens{AccessToken: "opaque-old-access", RefreshToken: "opaque-old-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}
				if mode == "expired" || mode == "refresh-error" {
					old.ExpiresAt = time.Now().Add(-time.Hour).Unix()
				}
				path := writeChatGPTOAuth(t, old)
				policy, _ := redaction.New(redaction.Config{}, nil)
				// Created before any credential registration, like an existing UI stream.
				stream := policy.Stream(redaction.Terminal)
				requireMasked := func(text string) {
					t.Helper()
					got := policy.Text(redaction.Terminal, text)
					for _, secret := range []string{old.AccessToken, old.RefreshToken, "opaque-new-access", "opaque-new-refresh"} {
						if strings.Contains(got, secret) {
							t.Fatalf("credential escaped registration: %q", got)
						}
					}
				}
				calls := 0
				client := doerFunc(func(r *http.Request) (*http.Response, error) {
					reply := func(status int, body string) (*http.Response, error) {
						return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d status", status), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
					}
					if r.URL.Host == "auth.openai.com" {
						// Loaded credentials must already be registered before refresh starts.
						requireMasked(old.AccessToken + " " + old.RefreshToken)
						if mode == "refresh-error" {
							return reply(400, old.AccessToken+" "+old.RefreshToken)
						}
						return reply(200, `{"access_token":"opaque-new-access","refresh_token":"opaque-new-refresh","expires_in":3600}`)
					}
					calls++
					access, refresh := old.AccessToken, old.RefreshToken
					if mode == "expired" || mode == "retry" && calls > 1 {
						access, refresh = "opaque-new-access", "opaque-new-refresh"
					}
					echo := access + " " + refresh
					// Registration precedes provider requests, not merely their return.
					requireMasked(echo)
					if mode == "retry" && calls == 1 {
						return reply(401, echo)
					}
					if mode == "provider-error" {
						return reply(500, echo)
					}
					if models {
						return reply(200, `{"models":[{"slug":"`+echo+`"}]}`)
					}
					return reply(200, responsesStream(echo))
				})
				provider, err := newChatGPT(Config{HTTP: client, ChatGPTOAuthPath: path, RegisterSecrets: policy.RegisterSecrets})
				if err != nil {
					t.Fatal(err)
				}
				if models {
					result, callErr := provider.(ModelLister).Models(context.Background())
					err = callErr
					for _, model := range result {
						requireMasked(model)
					}
				} else {
					result, callErr := provider.Complete(context.Background(), Request{Model: "fake"}, func(event StreamEvent) {
						requireMasked(event.Text)
						if got := stream.Feed(event.Text + "\n"); strings.Contains(got, "opaque-") {
							t.Fatalf("stream leaked: %q", got)
						}
					})
					err = callErr
					if err == nil {
						requireMasked(result.Message.Content)
					}
				}
				wantError := mode == "refresh-error" || mode == "provider-error"
				if (err != nil) != wantError {
					t.Fatalf("error = %v; wantError = %t", err, wantError)
				}
				if err != nil {
					requireMasked(err.Error())
				}
			})
		}
	}
}
