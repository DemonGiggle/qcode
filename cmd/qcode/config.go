package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"qcode/internal/config"
)

func runConfig(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: qcode config check [--json]")
	}
	if args[0] != "check" {
		return fmt.Errorf("usage: qcode config check [--json]")
	}
	fs := flag.NewFlagSet("qcode config check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit issues as JSON")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: qcode config check [--json]")
	}
	issues, inspected, err := config.CheckDefault()
	if err != nil {
		return err
	}
	if *jsonOut {
		type payload struct {
			Files  []string       `json:"files"`
			Issues []config.Issue `json:"issues"`
		}
		files := inspected
		if files == nil {
			files = []string{}
		}
		if issues == nil {
			issues = []config.Issue{}
		}
		data, merr := json.MarshalIndent(payload{Files: files, Issues: issues}, "", "  ")
		if merr != nil {
			return merr
		}
		fmt.Fprintln(stdout, string(data))
	} else {
		if len(inspected) == 0 {
			fmt.Fprintln(stdout, "No config files found; using built-in defaults.")
		} else {
			fmt.Fprintln(stdout, "Checked:")
			for _, path := range inspected {
				fmt.Fprintln(stdout, "  "+path)
			}
		}
		if len(issues) == 0 {
			fmt.Fprintln(stdout, "OK: no errors or warnings.")
		} else {
			for _, issue := range issues {
				fmt.Fprintln(stdout, issue.String())
			}
		}
	}
	if config.HasErrors(issues) {
		return fmt.Errorf("config check found %d error(s)", countErrors(issues))
	}
	return nil
}

func countErrors(issues []config.Issue) int {
	n := 0
	for _, issue := range issues {
		if issue.Severity == config.SeverityError {
			n++
		}
	}
	return n
}
