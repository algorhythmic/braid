package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/algorhythmic/braid/pkg/braid"
)

type reindexStatus struct {
	JobID  string               `json:"job_id"`
	State  string               `json:"state"`
	Result *braid.ReindexResult `json:"result,omitempty"`
	Error  *protocolError       `json:"error,omitempty"`
}
type reindexJob struct {
	mu     sync.Mutex
	status reindexStatus
	cancel context.CancelFunc
}

func (j *reindexJob) snapshot() reindexStatus { j.mu.Lock(); defer j.mu.Unlock(); return j.status }

type reindexJobs struct {
	current  *reindexJob
	sequence int
}

func (j *reindexJobs) close() {
	if j.current != nil {
		j.current.cancel()
	}
}
func (j *reindexJobs) handle(ctx context.Context, e *braid.Engine, r request) response {
	if r.Method == "reindex_start" {
		if j.current != nil && j.current.snapshot().State == "running" {
			return fail(r.ID, "job_running", "a reindex job is already running")
		}
		j.sequence++
		jobCtx, cancel := context.WithCancel(ctx)
		job := &reindexJob{cancel: cancel, status: reindexStatus{JobID: fmt.Sprintf("reindex:%d", j.sequence), State: "running"}}
		j.current = job
		go func() {
			defer cancel()
			result, err := e.ReindexWithOptions(jobCtx, braid.ReindexOptions{Force: r.Force})
			job.mu.Lock()
			defer job.mu.Unlock()
			if err == nil {
				job.status.State = "succeeded"
				job.status.Result = &result
				return
			}
			code := "operation_failed"
			job.status.State = "failed"
			if errors.Is(err, braid.ErrRevisionConflict) {
				code = "revision_conflict"
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				code = "cancelled"
				job.status.State = "cancelled"
			}
			job.status.Error = &protocolError{Code: code, Message: err.Error()}
		}()
		return response{Version: protocolVersion, ID: r.ID, Result: job.snapshot()}
	}
	if j.current == nil || j.current.snapshot().JobID != r.JobID {
		return fail(r.ID, "unknown_job", "job is not the current session reindex job")
	}
	if r.Method == "reindex_cancel" {
		j.current.cancel()
	}
	return response{Version: protocolVersion, ID: r.ID, Result: j.current.snapshot()}
}
