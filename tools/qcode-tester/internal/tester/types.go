package tester

import "time"

type Options struct {
	QcodeBinary        string
	ScenarioDirectory  string
	Scenarios          []string
	ArtifactsDirectory string
	KeepArtifacts      bool
	JSON               bool
}

type Scenario struct {
	Version    int               `json:"version"`
	Name       string            `json:"name"`
	Prompt     string            `json:"prompt"`
	Timeout    string            `json:"timeout"`
	SeedFiles  map[string]string `json:"seed_files"`
	Assertions Assertions        `json:"assertions"`
}

type Assertions struct {
	ExitCode       int               `json:"exit_code"`
	ExpectTimeout  bool              `json:"expect_timeout"`
	StdoutContains []string          `json:"stdout_contains"`
	Files          map[string]string `json:"files"`
	FileContains   map[string]string `json:"file_contains"`
	AbsentFiles    []string          `json:"absent_files"`
}

type Report struct {
	Results []Result `json:"results"`
	Passed  int      `json:"passed"`
	Failed  int      `json:"failed"`
}

type Result struct {
	Name      string        `json:"name"`
	Passed    bool          `json:"passed"`
	TimedOut  bool          `json:"timed_out"`
	ExitCode  int           `json:"exit_code"`
	Duration  time.Duration `json:"duration"`
	Failures  []string      `json:"failures,omitempty"`
	Artifacts string        `json:"artifacts,omitempty"`
}
