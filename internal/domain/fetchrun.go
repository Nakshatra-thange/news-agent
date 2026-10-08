package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// RunTrigger records what started a fetch run.
type RunTrigger string

const (
	TriggerAPI       RunTrigger = "api"
	TriggerCLI       RunTrigger = "cli"
	TriggerScheduler RunTrigger = "scheduler" // started by the scheduler in `synergy serve`
)

// Valid reports whether t is a known trigger.
func (t RunTrigger) Valid() bool {
	switch t {
	case TriggerAPI, TriggerCLI, TriggerScheduler:
		return true
	}
	return false
}

// RunStatus is the state of a fetch run.
type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
)

// FetchStats counts what one fetch run did with the items it received.
// Fetched is the number of distinct candidates the adapter returned. Each is
// then either Rejected (failed normalization/validation) or stored as
// Inserted, Updated or Unchanged. Inserted includes items linked as
// cross-source duplicates; Duplicate is that subset.
type FetchStats struct {
	Fetched   int
	Inserted  int
	Updated   int
	Unchanged int
	Duplicate int
	Rejected  int
}

// Record adds one upsert result to the stats. Fetched is counted separately
// because items can be rejected before they are upserted.
func (s *FetchStats) Record(r UpsertResult) {
	switch r.Outcome {
	case UpsertInserted:
		s.Inserted++
		if r.DuplicateOf != nil {
			s.Duplicate++
		}
	case UpsertUpdated:
		s.Updated++
	case UpsertUnchanged:
		s.Unchanged++
	}
}

// FetchRun is one execution of a source fetch.
type FetchRun struct {
	ID         uuid.UUID
	SourceID   uuid.UUID
	Trigger    RunTrigger
	Status     RunStatus
	StartedAt  time.Time
	FinishedAt *time.Time
	Stats      FetchStats
	Error      string
}

// Duration is how long the run took, or zero while it is still running.
func (r FetchRun) Duration() time.Duration {
	if r.FinishedAt == nil {
		return 0
	}
	return r.FinishedAt.Sub(r.StartedAt)
}

// RunCompletion is the final outcome of a run, applied atomically to the run
// and to its source's health fields.
type RunCompletion struct {
	Stats FetchStats
	// Err is empty on success. On failure it is stored on the run and as the
	// source's last error.
	Err string
	// State is the adapter's new cursor; persisted only on success. Nil keeps
	// the existing state.
	State json.RawMessage
}

// Succeeded reports whether the completion represents a successful run.
func (c RunCompletion) Succeeded() bool { return c.Err == "" }
