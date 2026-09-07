package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"qcode-tester/internal/tester"
)

type repeatedString []string

func (values *repeatedString) String() string { return strings.Join(*values, ",") }
func (values *repeatedString) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "qcode-tester:", err)
		os.Exit(2)
	}
}

func run(arguments []string) error {
	var options tester.Options
	var scenarios repeatedString
	flags := flag.NewFlagSet("qcode-tester", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&options.QcodeBinary, "qcode-bin", "", "path to the qcode executable (required)")
	flags.StringVar(&options.ScenarioDirectory, "scenarios", "testdata/scenarios", "scenario directory")
	flags.Var(&scenarios, "scenario", "scenario name to run (repeatable)")
	flags.StringVar(&options.ArtifactsDirectory, "artifacts-dir", "artifacts", "directory for retained failure artifacts")
	flags.BoolVar(&options.KeepArtifacts, "keep-artifacts", false, "retain artifacts for successful scenarios")
	flags.BoolVar(&options.JSON, "json", false, "write the aggregate report as JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if options.QcodeBinary == "" {
		return fmt.Errorf("--qcode-bin is required")
	}
	options.Scenarios = scenarios
	report, err := tester.Run(context.Background(), options)
	if options.JSON {
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		fmt.Println(string(data))
	} else {
		for _, result := range report.Results {
			status := "PASS"
			if !result.Passed {
				status = "FAIL"
			}
			fmt.Printf("%s %s (%s)\n", status, result.Name, result.Duration)
			for _, failure := range result.Failures {
				fmt.Printf("  %s\n", failure)
			}
			if result.Artifacts != "" {
				fmt.Printf("  artifacts: %s\n", result.Artifacts)
			}
		}
		fmt.Printf("%d passed, %d failed\n", report.Passed, report.Failed)
	}
	if err != nil {
		return err
	}
	if report.Failed > 0 {
		return fmt.Errorf("%d scenario(s) failed", report.Failed)
	}
	return nil
}
