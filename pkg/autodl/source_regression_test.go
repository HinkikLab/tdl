package autodl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func sourceManager(t *testing.T, api *tg.Client) *peers.Manager {
	t.Helper()
	m := peers.Options{Cache: &peers.InmemoryCache{}}.Build(api)
	require.NoError(t, m.Apply(context.Background(), nil, []tg.ChatClass{&tg.Channel{ID: 50, AccessHash: 500}, &tg.Channel{ID: 60, AccessHash: 600}}))
	return m
}

func TestIncrementalConflictingPeerFailsBeforeScanOrStateMutation(t *testing.T) {
	requests := 0
	api := tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		requests++
		return fmt.Errorf("unexpected RPC %T", in)
	}))
	m := sourceManager(t, api)
	dir := filepath.Join(t.TempDir(), "not-created")
	job := &Job{ChatURL: "https://t.me/c/50", Chat: "60", Incremental: ptr(true), dir: dir}
	r := &Runner{cfg: &Config{}, opts: Options{}, pool: batchPool{api}, manager: m}
	err := r.runJob(context.Background(), job, 1, 1, nil)
	require.ErrorContains(t, err, "scan and download sources must match")
	require.Zero(t, requests)
	require.NoDirExists(t, dir)
}

func TestIncrementalSourceOverrideMustMatchDirectOrDiscussionPeer(t *testing.T) {
	api := tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		return fmt.Errorf("unexpected RPC %T", in)
	}))
	m := sourceManager(t, api)
	for _, comment := range []bool{false, true} {
		t.Run(fmt.Sprint(comment), func(t *testing.T) {
			expected := int64(50)
			if comment {
				expected = 60
			}
			peer := m.Channel(&tg.Channel{ID: expected})
			r := &Runner{manager: m, dialogs: map[string]peers.Peer{fmt.Sprintf("%t:50", comment): peer}}
			job := &Job{Comment: comment, Chat: fmt.Sprint(expected)}
			p, err := r.scanPeer(context.Background(), job, Link{Chat: "50"})
			require.NoError(t, err)
			require.Equal(t, peerKey(peer), peerKey(p))
			job.Chat = fmt.Sprint(110 - expected)
			_, err = r.scanPeer(context.Background(), job, Link{Chat: "50"})
			require.ErrorContains(t, err, "sources must match")
		})
	}
}

func TestIncrementalCheckOnlyRetrySkippedHasNoLocalWrite(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "planned-output")
	statePath := filepath.Join(root, "state.json")
	api := tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.MessagesGetHistoryRequest:
			var msgs []tg.MessageClass
			if req.OffsetID == 0 {
				msg := &tg.Message{ID: 7, Date: int(time.Now().Unix()), PeerID: &tg.PeerChannel{ChannelID: 50}}
				msg.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: 42, Size: 11, DCID: 2, MimeType: "video/mp4"}})
				msgs = []tg.MessageClass{msg}
			}
			return encodeResult(&tg.MessagesChannelMessages{Count: len(msgs), Messages: msgs}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	m := sourceManager(t, api)
	job := &Job{ChatURL: "https://t.me/c/50", Incremental: ptr(true), dir: dir}
	r := &Runner{cfg: &Config{}, opts: Options{CheckOnly: true, RetrySkipped: true, StateFile: statePath}, pool: batchPool{api}, manager: m}
	peer := m.Channel(&tg.Channel{ID: 50})
	state := NewState()
	state.Scope = r.peerStateScope(job, Link{Chat: "50"}, dir, peer)
	state.Skip(7)
	state.SetLastTS(10)
	require.NoError(t, state.Save(statePath))
	before, err := os.ReadFile(statePath)
	require.NoError(t, err)
	require.NoError(t, r.runJob(context.Background(), job, 1, 1, nil))
	after, err := os.ReadFile(statePath)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoDirExists(t, dir)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestStateIdentityIncludesEffectiveModeFilterNamespaceAndNumericPeer(t *testing.T) {
	r := &Runner{opts: Options{Namespace: "account"}, cfg: &Config{}}
	job := &Job{mode: ModeDirect}
	link := Link{Chat: "source", MessageID: 10}
	base := r.stateScope(job, link, "downloads")
	link.Comment = 20
	require.NotEqual(t, base, r.stateScope(job, link, "downloads"))
	link.Comment = 0
	job.ExportFilter = "ID > 10"
	require.NotEqual(t, base, r.stateScope(job, link, "downloads"))
	job.ExportFilter = ""
	job.ExportAll = true
	require.NotEqual(t, base, r.stateScope(job, link, "downloads"))
	job.ExportAll = false
	r.opts.Namespace = "other"
	require.NotEqual(t, base, r.stateScope(job, link, "downloads"))
	r.opts.Namespace = "account"
	r.account = "account:user:1"
	accountScope := r.stateScope(job, link, "downloads")
	r.account = "account:user:2"
	require.NotEqual(t, accountScope, r.stateScope(job, link, "downloads"))
	m := peers.Options{}.Build(nil)
	peer := m.Channel(&tg.Channel{ID: 50})
	a := r.peerStateScope(job, Link{Chat: "old_alias"}, "downloads", peer)
	b := r.peerStateScope(job, Link{Chat: "new_alias"}, "downloads", peer)
	require.Equal(t, a, b)
	other := m.Chat(&tg.Chat{ID: 50})
	require.NotEqual(t, a, r.peerStateScope(job, link, "downloads", other))
}

func TestArchiveIdentityNormalizesTagsButExcludesRetryPerformance(t *testing.T) {
	r := &Runner{cfg: &Config{}}
	job := &Job{ChatURL: "https://t.me/course", FollowLinks: true, Tag: "#Notes", Tags: []string{"#EXTRA", "#notes"}}
	require.NoError(t, job.LinkOptions.Normalize())
	base, err := r.archiveStateScope(job, "downloads")
	require.NoError(t, err)
	job.Tag = "#notes"
	job.Tags = []string{"extra", "#NOTES"}
	job.LinkOptions.BotTimeout = 120
	job.LinkOptions.ReRequestLimit = ptr(8)
	job.LinkOptions.PollInterval = 1000
	changed, err := r.archiveStateScope(job, "downloads")
	require.NoError(t, err)
	require.Equal(t, base, changed)
	job.LinkOptions.MaxTopicMessages++
	changed, err = r.archiveStateScope(job, "downloads")
	require.NoError(t, err)
	require.NotEqual(t, base, changed, "resource topic window changes must not reuse an incremental selection identity")
	job.LinkOptions.MaxTopicMessages--
	job.Tags = []string{"notes", "other"}
	changed, err = r.archiveStateScope(job, "downloads")
	require.NoError(t, err)
	require.NotEqual(t, base, changed)
}
