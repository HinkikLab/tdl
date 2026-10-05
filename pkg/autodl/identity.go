package autodl

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/gotd/td/telegram/peers"

	"github.com/iyear/tdl/core/util/fsutil"
)

func (r *Runner) template() string {
	if r.opts.Template != "" {
		return r.opts.Template
	}

	return `{{ .DialogID }}_{{ .MessageID }}_{{ filenamify .FileName }}`
}

// statePath returns the state file of a job. Every namespace and download
// directory gets its own file so two configs can't clobber each other.
func (r *Runner) statePath(job *Job) string {
	if r.opts.StateFile != "" {
		return canonicalPath(r.opts.StateFile)
	}

	if r.cfg.StateFile != "" {
		return canonicalPath(r.cfg.StateFile)
	}

	dir := job.Dir()
	if r.opts.Dir != "" {
		dir = filepath.Join(r.opts.Dir, job.Subdir)
	}
	return canonicalPath(filepath.Join(dir, DefaultStateFile))
}

// stateScope prevents one state file from silently reusing message IDs from a
// different Telegram dialog or interpretation mode.
func (r *Runner) stateScope(job *Job, link Link, dir string) string {
	dir = canonicalPath(dir)
	if runtime.GOOS == "windows" {
		dir = strings.ToLower(dir)
	}
	identity := strings.Join([]string{
		r.opts.Namespace,
		r.account,
		strings.ToLower(strings.TrimSpace(link.Chat)),
		fmt.Sprint(commentDialog(job, link)),
		strings.ToLower(strings.TrimSpace(job.Chat)),
		fmt.Sprint(num(job.TopicID)),
		fmt.Sprint(num(job.ReplyPostID)),
		dir,
		r.template(),
		normalizeScopeExtensions(r.opts.Include),
		normalizeScopeExtensions(r.opts.Exclude),
		strings.TrimSpace(job.ExportFilter),
		fmt.Sprint(job.ExportAll),
	}, "\x00")
	return fmt.Sprintf("v3|%x", sha256.Sum256([]byte(identity)))
}

func (r *Runner) peerStateScope(job *Job, link Link, dir string, peer peers.Peer) string {
	// Bind identity to numeric peer kind/ID; usernames and aliases can change.
	link.Chat = peerKey(peer)
	copyJob := *job
	copyJob.Chat = ""
	return r.stateScope(&copyJob, link, dir)
}

func normalizeScopeExtensions(exts []string) string {
	normalized := make([]string, 0, len(exts))
	for _, ext := range exts {
		normalized = append(normalized, strings.ToLower(fsutil.AddPrefixDot(ext)))
	}
	sort.Strings(normalized)
	normalized = slices.Compact(normalized)
	return strings.Join(normalized, ",")
}
