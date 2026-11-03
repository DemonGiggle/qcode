package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"qcode/internal/chatgptoauth"
	"qcode/internal/config"
)

// loginExchange swaps an authorization code for tokens. It is a variable so
// tests can stub the token endpoint.
var loginExchange = chatgptoauth.Exchange

// runLogin handles `qcode login chatgpt`: console-only, terminal-required,
// manual paste-back with no localhost listener.
func runLogin(args []string, stdin io.Reader, stdout, stderr io.Writer, isTerminal bool) error {
	if len(args) != 1 || args[0] != "chatgpt" {
		return fmt.Errorf("usage: qcode login chatgpt")
	}
	if !isTerminal {
		return fmt.Errorf("login requires an interactive terminal")
	}
	cfg, _, _, err := config.Load()
	if err != nil {
		return err
	}
	oauthPath := cfg.ChatGPT.OAuthPath
	if oauthPath == "" {
		oauthPath = chatgptoauth.DefaultPath
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	return chatGPTLoginFlow(oauthPath, stdin, stdout, httpClient)
}

// chatGPTLoginFlow prints the authorize URL, reads the pasted callback URL,
// exchanges the code, and saves the tokens with 0600 file rules.
func chatGPTLoginFlow(oauthPath string, stdin io.Reader, stdout io.Writer, doer chatgptoauth.Doer) error {
	verifier, err := chatgptoauth.NewVerifier()
	if err != nil {
		return err
	}
	state, err := chatgptoauth.NewState()
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "ChatGPT login (console only).")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "1. Open this URL in your browser and approve:")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "   "+chatgptoauth.AuthorizeURL(verifier, state))
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "2. Your browser will try to reach http://localhost:1455/auth/callback")
	fmt.Fprintln(stdout, "   and fail to connect. That is expected.")
	fmt.Fprintln(stdout, "3. Paste the full URL from the address bar below.")
	fmt.Fprintln(stdout, "")
	fmt.Fprint(stdout, "Callback URL> ")
	pasted, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && len(strings.TrimSpace(pasted)) == 0 {
		return fmt.Errorf("read callback URL: %w", err)
	}
	code, err := chatgptoauth.ParseCallback(pasted, state)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tokens, err := loginExchange(ctx, doer, code, verifier)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	if err := chatgptoauth.Save(oauthPath, tokens); err != nil {
		return err
	}
	resolved, _ := chatgptoauth.ResolvePath(oauthPath)
	fmt.Fprintln(stdout, "")
	fmt.Fprintf(stdout, "Saved %s (0600).\n", resolved)
	fmt.Fprintln(stdout, "Next: qcode --provider chatgpt --model gpt-5.6-codex")
	return nil
}
