package autodl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/iyear/tdl/internal/transfer"
)

// JobCounts uses explicit message, post, file and byte units across all modes.
type JobCounts = transfer.Counts

// JobResult is emitted after every job, after its workers and state commits
// settle. Err retains the original cause; Error is suitable for JSON reports.
type JobResult struct {
	Job     int       `json:"job"`
	Source  string    `json:"source"`
	Dir     string    `json:"dir"`
	Kind    string    `json:"kind"`
	Status  string    `json:"status"`
	Targets int       `json:"target_messages,omitempty"`
	Counts  JobCounts `json:"counts"`
	Error   string    `json:"error,omitempty"`
	Err     error     `json:"-"`
}

type jobReport struct {
	mu     sync.Mutex
	result JobResult
}

func (r *Runner) beginJob(index int, job *Job) {
	kind := "messages"
	if job.FollowLinks {
		kind = "linked"
	} else if job.IsTagJob() {
		kind = "tag"
	}
	r.report = &jobReport{result: JobResult{Job: index, Source: job.ChatURL, Dir: job.Dir(), Kind: kind}}
}

func (r *Runner) observeCounts(counts transfer.Counts) {
	if r.report == nil {
		return
	}
	r.report.mu.Lock()
	defer r.report.mu.Unlock()
	r.report.result.Counts.Add(counts)
}

func (r *Runner) setTargets(count int) {
	if r.report == nil {
		return
	}
	r.report.mu.Lock()
	defer r.report.mu.Unlock()
	r.report.result.Targets = count
}

func (r *Runner) finishJob(err error) JobResult {
	report := r.report
	if report == nil {
		return JobResult{Err: err}
	}
	report.mu.Lock()
	result := report.result
	report.mu.Unlock()
	result.Err = err
	switch {
	case err == nil && r.opts.CheckOnly:
		result.Status = "planned"
	case err == nil:
		result.Status = "complete"
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		result.Status = "canceled"
		result.Error = err.Error()
	default:
		result.Status = "failed"
		result.Error = err.Error()
	}
	if r.opts.OnJobResult != nil {
		r.opts.OnJobResult(result)
	}
	r.report = nil
	return result
}

func formatJobSummary(result JobResult) string {
	c := result.Counts
	summary := fmt.Sprintf("Job %d (%s): %d messages, %d posts, %d files; downloaded %d, existing %d, filtered %d, failed %d; committed %d bytes", result.Job, result.Status, c.Messages, c.Posts, c.Files, c.FilesDownloaded, c.FilesExisting, c.FilesFiltered, c.FilesFailed, c.Bytes)
	var reasons []string
	for _, entry := range []struct {
		count int64
		label string
	}{{c.MessagesUnavailable, "unavailable messages"}, {c.MessagesNoMedia, "messages without media"}, {c.PostsUnavailable, "unavailable posts"}, {c.PostsNoLinks, "posts without links"}} {
		if entry.count > 0 {
			reasons = append(reasons, fmt.Sprintf("%d %s", entry.count, entry.label))
		}
	}
	if len(reasons) > 0 {
		summary += "; " + strings.Join(reasons, ", ")
	}
	return summary
}
