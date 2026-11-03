package session

import "qcode/internal/llm"

// CreationBackend identifies a connection without carrying credentials or a live
// provider instance. Each new agent receives an independent provider instance.
type CreationBackend struct {
	Provider string
	Endpoint string
}

// BackendSwitch is prepared outside orchestration locks. ContextCapacity is zero
// when unknown; provider thinking capabilities must already be locally available.
type BackendSwitch struct {
	Provider        llm.Provider
	Endpoint        string
	Model           string
	Thinking        string
	ContextCapacity int
}
