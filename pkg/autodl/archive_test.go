package autodl

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/app/chat"
)

func TestArchiveRangeAndIncrementalState(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, scenario := range []string{"range", "success", "failure", "limit", "check", "cancel"} {
			t.Run(fmtArchiveCase(linked, scenario), func(t *testing.T) {
				job := &Job{
					ChatURL: "https://t.me/course", FollowLinks: linked, Tag: "#notes", dir: t.TempDir(),
					StartComment: ptr(10), EndComment: ptr(20), Incremental: ptr(scenario != "range"), Overlap: ptr(100),
				}
				r := &Runner{cfg: &Config{}, opts: Options{CheckOnly: scenario == "check"}}
				scope, err := r.archiveStateScope(job, job.Dir())
				require.NoError(t, err)
				state := NewState()
				state.Scope = scope
				last := time.Now().Unix() - 200
				state.SetLastTS(last)
				require.NoError(t, state.Save(r.statePath(job)))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failure := errors.New("download failed")
				var end int64
				err = r.runArchiveWindow(ctx, job, job.Dir(), func(window chat.ArchiveWindow) error {
					if scenario == "range" {
						require.Equal(t, chat.ArchiveWindow{StartID: 10, EndID: 20}, window)
					} else {
						require.Zero(t, window.StartID, "incremental must override ID ranges")
						require.Zero(t, window.EndID)
						require.Equal(t, last-100, window.Since)
						require.Greater(t, window.Until, last)
						end = window.Until
					}
					switch scenario {
					case "failure", "limit":
						return failure
					case "cancel":
						cancel()
					}
					return nil
				})
				switch scenario {
				case "failure", "limit":
					require.ErrorIs(t, err, failure)
				case "cancel":
					require.ErrorIs(t, err, context.Canceled)
				default:
					require.NoError(t, err)
				}
				saved, err := LoadState(r.statePath(job))
				require.NoError(t, err)
				if scenario == "success" {
					require.Equal(t, end, saved.GetLastTS())
				} else {
					require.Equal(t, last, saved.GetLastTS())
				}
			})
		}
	}
}

func fmtArchiveCase(linked bool, scenario string) string {
	if linked {
		return "linked/" + scenario
	}
	return "tag/" + scenario
}

func TestArchiveCheckOnlyDoesNotCreateStateAndScopeRejectsChangedSelection(t *testing.T) {
	job := &Job{ChatURL: "https://t.me/course", Tag: "#notes", dir: t.TempDir(), Incremental: ptr(true)}
	r := &Runner{cfg: &Config{}, opts: Options{CheckOnly: true}}
	require.NoError(t, r.runArchiveWindow(context.Background(), job, job.Dir(), func(w chat.ArchiveWindow) error {
		require.Zero(t, w.Since)
		return nil
	}))
	_, err := os.Stat(r.statePath(job))
	require.True(t, os.IsNotExist(err))
	r.opts.CheckOnly = false
	require.NoError(t, r.runArchiveWindow(context.Background(), job, job.Dir(), func(chat.ArchiveWindow) error { return nil }))
	job.Tag = "#other"
	require.ErrorContains(t, r.runArchiveWindow(context.Background(), job, job.Dir(), func(chat.ArchiveWindow) error {
		t.Fatal("changed selection must not reuse last_ts")
		return nil
	}), "belongs to")
}
