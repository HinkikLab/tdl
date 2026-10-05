package dl

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

	"github.com/iyear/tdl/pkg/tmessage"
)

func TestAlbumResolutionFailureDrainsPreviouslyOpenedMembers(t *testing.T) {
	peer := (&peers.Manager{}).Channel(&tg.Channel{ID: 123})
	member := func(id int) *tg.Message {
		m := &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 123}}
		m.SetGroupedID(88)
		m.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: int64(id), Size: 3, DCID: 2, MimeType: "video/mp4"}})
		return m
	}
	a, b := member(7), member(8)
	api := tg.NewClient(referenceRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetHistoryRequest); !ok {
			return fmt.Errorf("unexpected RPC %T", in)
		}
		var encoded bin.Buffer
		require.NoError(t, (&tg.MessagesMessages{Messages: []tg.MessageClass{b, a}}).Encode(&encoded))
		return out.Decode(&encoded)
	}))
	opts := Options{Dir: t.TempDir(), Template: "same.bin", Group: true}
	i, err := newIter(referencePool{api}, nil, [][]*tmessage.Dialog{{{Peer: peer.InputPeer(), Messages: []int{7}}}}, opts, 0)
	require.NoError(t, err)
	i.mu.Lock()
	ok, skip := i.processGrouped(t.Context(), a, peer, 0)
	i.mu.Unlock()
	require.False(t, ok)
	require.False(t, skip)
	require.ErrorContains(t, i.Err(), "already reserved")
	require.Len(t, i.elem, 1)
	require.NoError(t, i.Drain())
	require.Empty(t, i.elem)
	path := filepath.Join(opts.Dir, "same.bin.tmp")
	require.FileExists(t, path)
	// Windows refuses removal of an open file, making this a real handle check.
	require.NoError(t, os.Remove(path))
	require.Empty(t, i.Completed())
}

func TestCancellationDrainsPreparedAlbumQueue(t *testing.T) {
	peer := (&peers.Manager{}).Channel(&tg.Channel{ID: 123})
	m := &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 123}}
	m.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: 7, Size: 3, DCID: 2, MimeType: "video/mp4"}})
	opts := Options{Dir: t.TempDir(), Template: "file.bin"}
	i, err := newIter(nil, nil, [][]*tmessage.Dialog{{{Peer: peer.InputPeer(), Messages: []int{7}}}}, opts, 0)
	require.NoError(t, err)
	ok, _ := i.processSingle(t.Context(), m, peer, 0)
	require.True(t, ok)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.False(t, i.Next(ctx))
	require.ErrorIs(t, i.Err(), context.Canceled)
	require.NoError(t, i.Drain())
	require.NoError(t, os.Remove(filepath.Join(opts.Dir, "file.bin.tmp")))
}
