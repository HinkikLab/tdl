package dl

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/pkg/tmessage"
)

func TestDownloadResumeVerifiesIdentityAndActualOutput(t *testing.T) {
	peer := (&peers.Manager{}).Channel(&tg.Channel{ID: 123})
	dialogs := [][]*tmessage.Dialog{{{Peer: peer.InputPeer(), Messages: []int{7}}}}
	opts := Options{Dir: t.TempDir(), Template: "{{.MessageID}}_{{.FileName}}"}
	message := func(id int64) *tg.Message {
		m := &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 123}}
		m.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: id, Size: 3, DCID: 2, MimeType: "application/octet-stream", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "file.bin"}}}})
		return m
	}
	i, err := newIter(nil, nil, dialogs, opts, 0)
	require.NoError(t, err)
	ok, skip := i.processSingle(context.Background(), message(42), peer, 0)
	require.True(t, ok)
	require.False(t, skip)
	e := i.Value().(*iterElem)
	_, err = e.to.WriteAt([]byte("one"), 0)
	require.NoError(t, err)
	require.NoError(t, e.Finalize(nil))
	i.Complete(e)
	saved := i.Completed()
	next := func(opts Options, id int64) (*iter, bool, bool) {
		i, err := newIter(nil, nil, dialogs, opts, 0)
		require.NoError(t, err)
		i.completed = saved
		ok, skip := i.processSingle(t.Context(), message(id), peer, 0)
		if ok {
			e := i.Value().(*iterElem)
			require.NoError(t, e.Finalize(context.Canceled))
		}
		return i, ok, skip
	}
	_, ok, skip = next(opts, 42)
	require.False(t, ok)
	require.True(t, skip)
	_, ok, skip = next(opts, 43)
	require.True(t, ok, "same-sized replacement must not inherit completed identity")
	require.False(t, skip)
	require.NoError(t, os.Truncate(filepath.Join(opts.Dir, "7_file.bin"), 1))
	_, ok, skip = next(opts, 42)
	require.True(t, ok, "truncated output must be downloaded again")
	require.False(t, skip)
	other := opts
	other.Dir = t.TempDir()
	j, ok, skip := next(other, 42)
	require.True(t, ok, "new directory must not inherit positional completion")
	require.False(t, skip)
	require.NotEqual(t, i.Fingerprint(), j.Fingerprint())
	other = opts
	other.Template = "changed_{{.MessageID}}"
	j, err = newIter(nil, nil, dialogs, other, 0)
	require.NoError(t, err)
	require.NotEqual(t, i.Fingerprint(), j.Fingerprint())
}
