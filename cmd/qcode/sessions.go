package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"qcode/internal/config"
	"qcode/internal/redaction"
	"qcode/internal/session"
	"qcode/internal/tui"
)

func runSessions(args []string, in, out *os.File, diagnostics *redaction.DiagnosticWriter) (selection *tui.SavedSessionSelection, runErr error) {
	flags := flag.NewFlagSet("sessions", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.Usage = func() {
		fmt.Fprintln(diagnostics, "Usage: qcode sessions\n\nManage saved sessions across workspaces in an interactive terminal.")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil
		}
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, fmt.Errorf("usage: qcode sessions")
	}
	cfg, _, warnings, err := config.Load()
	diagnostics.Policy.RegisterSecrets(cfg.Secrets()...)
	writeConfigDiagnostics(diagnostics, warnings, err)
	if err != nil {
		return nil, err
	}
	policy, err := redaction.New(cfg.Redaction, append(redaction.EnvironmentValues(), cfg.Secrets()...))
	if err != nil {
		return nil, err
	}
	defer func() { runErr = policy.Error(redaction.Terminal, runErr) }()
	directory, err := session.DefaultDirectory()
	if err != nil {
		return nil, err
	}
	catalog := session.NewCatalog(directory, func(snap session.Snapshot) (session.Snapshot, error) {
		return filterSessionSnapshot(policy, snap)
	}, func(name string) string {
		return policy.Text(redaction.Persistence, name)
	})
	return tui.ManageSavedSessions(in, out, catalog, policy)
}
