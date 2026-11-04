package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"qcode/internal/chatgptoauth"
	"qcode/internal/config"
	"qcode/internal/redaction"
)

// loginExchange swaps a code for tokens. Variable so tests can stub the token
// endpoint. clientID is the issued client from the callback.
var loginExchange = chatgptoauth.Exchange

// loginVerify checks the ID token against the published JWKS. Variable so
// tests can stub network verification.
var loginVerify = chatgptoauth.VerifyIDTokenWithFetch

// runLogin handles `qcode login chatgpt`: terminal-required loopback flow per
// the open-source Sign in with ChatGPT page.
func runLogin(args []string, stdin io.Reader, stdout, stderr io.Writer, isTerminal bool) error {
	if len(args) != 1 || args[0] != "chatgpt" {
		return fmt.Errorf("usage: qcode login chatgpt")
	}
	if !isTerminal {
		return fmt.Errorf("login requires an interactive terminal")
	}
	cfg, _, diagnostics, err := config.Load()
	if writer, ok := stderr.(*redaction.DiagnosticWriter); ok {
		writer.Policy.RegisterSecrets(cfg.Secrets()...)
	}
	writeConfigDiagnostics(stderr, diagnostics, err)
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

// writeConfigDiagnostics reports skipped files; a fatal diagnostic is returned
// to the caller and printed separately by the CLI entry point.
func writeConfigDiagnostics(out io.Writer, diagnostics []error, fatal error) {
	for _, diagnostic := range diagnostics {
		if fatal != nil && errors.Is(diagnostic, fatal) {
			continue
		}
		fmt.Fprintln(out, "WARNING:", diagnostic, "Skipping file.")
	}
}

// chatGPTLoginFlow performs dynamic registration (or reauthorization when a
// registration already exists), exchanges the code, validates the ID token and
// plan scope, and saves the doc-shaped credential record.
func chatGPTLoginFlow(oauthPath string, stdin io.Reader, stdout io.Writer, doer chatgptoauth.Doer) error {
	hostPath, err := chatgptoauth.HostPathForOAuth(oauthPath)
	if err != nil {
		return err
	}
	hostID, err := chatgptoauth.GetOrCreateHostID(hostPath)
	if err != nil {
		return err
	}
	var existing chatgptoauth.Tokens
	hasRegistration := false
	if t, err := chatgptoauth.Load(oauthPath); err == nil && !t.IsLegacy() && t.ClientID != "" {
		existing = t
		hasRegistration = true
	}

	fmt.Fprintln(stdout, "ChatGPT login (Sign in with ChatGPT).")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Continue with ChatGPT in the browser to connect your ChatGPT plan.")
	fmt.Fprintln(stdout, "")

	// Up to two attempts: a stale code returns invalid_grant and must be
	// discarded for a fresh authorization with the issued client retained.
	var lastErr error
	attempts := 2
	reauth := hasRegistration
	pendingExisting := existing
	for attempt := 0; attempt < attempts; attempt++ {
		verifier, err := chatgptoauth.NewVerifier()
		if err != nil {
			return err
		}
		state, err := chatgptoauth.NewState()
		if err != nil {
			return err
		}
		nonce, err := chatgptoauth.NewNonce()
		if err != nil {
			return err
		}
		preferred := chatgptoauth.DefaultRedirectPort
		if attempt > 0 {
			preferred = 0
		}
		ln, redirectURI, err := chatgptoauth.StartLoopbackListener(preferred)
		if err != nil {
			return err
		}
		var authURL string
		var pendingClient string
		if reauth {
			authURL = chatgptoauth.ReauthAuthorizeURLForTokens(pendingExisting, hostID, verifier, state, nonce, redirectURI)
			pendingClient = pendingExisting.ClientID
		} else {
			authURL = chatgptoauth.RegistrationAuthorizeURL(verifier, state, nonce, redirectURI, hostID)
		}
		fmt.Fprintln(stdout, "1. Open this URL in your browser and approve:")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "   "+authURL)
		fmt.Fprintln(stdout, "")
		fmt.Fprintf(stdout, "2. Waiting for the callback on %s ...\n", redirectURI)
		fmt.Fprintln(stdout, "   If your browser is on another machine, paste the full callback URL below.")
		fmt.Fprintln(stdout, "")
		tryBrowser(authURL)
		fmt.Fprint(stdout, "Callback URL> ")

		cb, err := awaitCallback(ln, stdin, state)
		_ = ln.Close()
		if err != nil {
			return err
		}
		issued := pendingClient
		if !reauth {
			if cb.ClientID == "" || cb.ClientID == chatgptoauth.DynamicClientID {
				return fmt.Errorf("registration incomplete: callback did not return an issued client_id")
			}
			issued = cb.ClientID
		} else {
			if cb.ClientID != "" && cb.ClientID != pendingExisting.ClientID {
				return fmt.Errorf("callback client_id does not match the saved ChatGPT account; refusing to overwrite it")
			}
			issued = pendingExisting.ClientID
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		tokens, err := loginExchange(ctx, doer, cb.Code, verifier, redirectURI, issued)
		cancel()
		if err != nil {
			if chatgptoauth.IsInvalidGrant(err) && attempt+1 < attempts {
				lastErr = err
				// Retain the issued client and try a fresh authorization.
				if !reauth {
					pendingExisting = chatgptoauth.Tokens{ClientID: issued, ExtAgentHostID: hostID}
				}
				reauth = true
				fmt.Fprintln(stdout, "That code expired; starting a fresh authorization ...")
				continue
			}
			return fmt.Errorf("login failed: %w", err)
		}
		tokens.ClientID = issued
		tokens.ExtAgentHostID = hostID

		ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
		claims, err := loginVerify(ctx2, doer, tokens.IDToken, nonce, issued)
		cancel2()
		if err != nil {
			return fmt.Errorf("login failed: ID token validation: %w", err)
		}
		if reauth && pendingExisting.Subject != "" && claims.Subject != pendingExisting.Subject {
			return fmt.Errorf("login failed: returned account does not match the saved ChatGPT account")
		}
		tokens.Issuer = claims.Issuer
		tokens.Subject = claims.Subject
		if claims.Email != "" {
			tokens.Email = claims.Email
		}
		if !tokens.HasPlanScope() {
			return fmt.Errorf("login failed: ChatGPT plan use isn't enabled (missing %s scope); re-approve with plan access", chatgptoauth.PlanScope)
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
	if lastErr != nil {
		return fmt.Errorf("login failed: %w", lastErr)
	}
	return fmt.Errorf("login failed")
}

// awaitCallback races the loopback listener against a pasted callback URL so
// same-machine browsers complete automatically while remote browsers can still
// paste the 127.0.0.1 URL back.
func awaitCallback(ln net.Listener, stdin io.Reader, wantState string) (chatgptoauth.CallbackResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	type res struct {
		cb  chatgptoauth.CallbackResult
		err error
	}
	ch := make(chan res, 2)
	go func() {
		cb, err := chatgptoauth.WaitForCallback(ctx, ln, wantState)
		select {
		case ch <- res{cb, err}:
		case <-ctx.Done():
		}
	}()
	go func() {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && len(strings.TrimSpace(line)) == 0 {
			select {
			case ch <- res{err: fmt.Errorf("read callback URL: %w", err)}:
			case <-ctx.Done():
			}
			return
		}
		cb, perr := chatgptoauth.ParseCallbackFull(line, wantState)
		select {
		case ch <- res{cb, perr}:
		case <-ctx.Done():
		}
	}()
	select {
	case <-ctx.Done():
		return chatgptoauth.CallbackResult{}, ctx.Err()
	case out := <-ch:
		return out.cb, out.err
	}
}

func tryBrowser(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	_ = cmd.Start()
}
