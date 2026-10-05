package autodl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/internal/transfer"
)

func resultMedia(id int, name string, size int64) *tg.Message {
	m := &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 50}}
	m.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: int64(40 + id), Size: size, DCID: 2, MimeType: "application/octet-stream", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: name}}}})
	return m
}

func TestJobResultCountsUseExplicitUnitsAndSkipReasons(t *testing.T) {
	dir := t.TempDir()
	job := &Job{ChatURL: "https://t.me/c/50", dir: dir}
	api := tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.ChannelsGetMessagesRequest:
			return encodeResult(&tg.MessagesChannelMessages{Messages: []tg.MessageClass{resultMedia(1, "first.mp4", 11), resultMedia(2, "notes.txt", 5), resultMedia(3, "existing.mp4", 3), &tg.Message{ID: 4, PeerID: &tg.PeerChannel{ChannelID: 50}, Message: "plain text"}, &tg.MessageEmpty{ID: 5}}}, out)
		case *tg.UploadGetFileRequest:
			return encodeResult(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: []byte("hello world")}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	m := peers.Options{}.Build(api)
	peer := m.Channel(&tg.Channel{ID: 50})
	var emitted []JobResult
	r := &Runner{cfg: &Config{}, opts: Options{Template: "{{.MessageID}}.bin", Include: []string{"mp4"}, OnJobResult: func(result JobResult) { emitted = append(emitted, result) }}, pool: batchPool{api}, manager: m, dialogs: map[string]peers.Peer{"false:50": peer}}
	store, state, err := LoadStateStore(filepath.Join(dir, "state.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "3.bin"), []byte("yes"), 0o600))
	r.beginJob(1, job)
	r.setTargets(5)
	err = r.download(context.Background(), job, Link{Chat: "50"}, dir, []int{1, 2, 3, 4, 5}, store, state, 1, 1)
	require.NoError(t, err)
	result := r.finishJob(err)
	require.Equal(t, "complete", result.Status)
	require.Equal(t, 5, result.Targets)
	require.Equal(t, JobCounts{Messages: 5, Files: 3, Bytes: 11, FilesDownloaded: 1, FilesExisting: 1, FilesFiltered: 1, MessagesNoMedia: 1, MessagesUnavailable: 1}, result.Counts)
	summary := formatJobSummary(result)
	require.Contains(t, summary, "5 messages, 0 posts, 3 files")
	require.Contains(t, summary, "downloaded 1, existing 1, filtered 1, failed 0")
	require.Contains(t, summary, "committed 11 bytes")
	require.Contains(t, summary, "1 unavailable messages, 1 messages without media")
	require.Len(t, emitted, 1)
	require.Equal(t, result, emitted[0])
	_, recorded := state.Media(1)
	require.True(t, recorded)
	require.True(t, state.IsTerminalWithoutMedia(2))
}

func TestJobResultRetainsFailureAndPlanningStatus(t *testing.T) {
	r := &Runner{opts: Options{CheckOnly: true}}
	job := &Job{ChatURL: "https://t.me/course", FollowLinks: true}
	r.beginJob(2, job)
	r.observeCounts(transfer.Counts{Posts: 2, PostsUnavailable: 1})
	result := r.finishJob(nil)
	require.Equal(t, "planned", result.Status)
	require.Equal(t, "linked", result.Kind)
	failure := fmt.Errorf("rename output: destination occupied")
	r.beginJob(3, job)
	result = r.finishJob(failure)
	require.ErrorIs(t, result.Err, failure)
	require.Contains(t, result.Error, "destination occupied")
	require.Equal(t, "failed", result.Status)
	r.beginJob(4, job)
	result = r.finishJob(context.Canceled)
	require.Equal(t, "canceled", result.Status)
}

func TestStateOutputCollisionFailsBeforeWritingMediaOrState(t *testing.T) {
	for _, suffix := range []string{"", ".tmp", ".tmp.parts", ".tmp.parts.new"} {
		t.Run(suffix, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "7.bin")
			path := target + suffix
			original := []byte(`{"finished":[],"last_ts":123}`)
			require.NoError(t, os.WriteFile(path, original, 0o600))
			store, state, err := LoadStateStore(path)
			require.NoError(t, err)
			api := tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				if _, ok := in.(*tg.ChannelsGetMessagesRequest); ok {
					return encodeResult(&tg.MessagesChannelMessages{Messages: []tg.MessageClass{resultMedia(7, "file.bin", 11)}}, out)
				}
				return fmt.Errorf("unexpected download RPC %T", in)
			}))
			m := peers.Options{}.Build(api)
			peer := m.Channel(&tg.Channel{ID: 50})
			job := &Job{ChatURL: "https://t.me/c/50", dir: dir, StartComment: ptr(7), EndComment: ptr(8)}
			r := &Runner{cfg: &Config{Jobs: []Job{*job}}, opts: Options{Template: "{{.MessageID}}.bin", StateFile: path}, pool: batchPool{api}, manager: m, dialogs: map[string]peers.Peer{"false:50": peer}}
			require.NoError(t, r.reserveStates())
			err = r.download(context.Background(), job, Link{Chat: "50"}, dir, []int{7}, store, state, 1, 1)
			require.ErrorContains(t, err, "reserved by job 1 state")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, original, after)
			require.False(t, state.IsFinished(7))
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
}
