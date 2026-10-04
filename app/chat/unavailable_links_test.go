package chat

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/require"
)

func TestUnavailableTargetsSkipAllPostIDsAndBotStartParameters(t *testing.T) {
	for _, target := range []struct{ first, next, code string }{
		{"https://t.me/dead_bot?start=first", "tg://resolve?domain=DEAD_bot&start=second", "USERNAME_NOT_OCCUPIED"},
		{"https://t.me/old_group/1", "https://telegram.me/OLD_GROUP/999", "CHANNEL_PRIVATE"},
		{"https://t.me/c/123/1", "https://t.me/c/123/999", "CHANNEL_INVALID"},
	} {
		t.Run(target.code, func(t *testing.T) {
			first, err := parseResourceLink(target.first)
			require.NoError(t, err)
			next, err := parseResourceLink(target.next)
			require.NoError(t, err)
			cache := &UnavailableLinks{}
			calls := 0
			backend := linkedFakeBackend{fetch: func(context.Context, resourceLink) ([]resourceMessage, error) {
				calls++
				return nil, fmt.Errorf("fetch wrapped: %w", tgerr.New(400, target.code))
			}}
			opts := LinkOptions{}
			require.NoError(t, opts.Normalize())
			for _, root := range []resourceLink{first, next, first} {
				// A new resolver models the next batch job sharing the cache.
				r := &linkResolver{backend: backend, opts: opts, unavailable: cache}
				_, _, err := r.Resolve(context.Background(), []resourceLink{root})
				require.True(t, isUnavailableResource(err))
			}
			require.Equal(t, 1, calls)
			// Next batch run probes the target again.
			r := &linkResolver{backend: backend, opts: opts}
			_, _, _ = r.Resolve(context.Background(), []resourceLink{first})
			require.Equal(t, 2, calls)
		})
	}
}

func TestTransientAndPerPostErrorsNeverBlacklistTarget(t *testing.T) {
	for _, code := range []string{"FLOOD_WAIT_1", "TIMEOUT", "INTERNAL", "MSG_ID_INVALID", "START_PARAM_INVALID", "PEER_ID_INVALID", "USER_IS_BLOCKED"} {
		t.Run(code, func(t *testing.T) {
			calls := 0
			r := &linkResolver{opts: LinkOptions{MaxDepth: 8, MaxLinks: 100}, backend: linkedFakeBackend{fetch: func(context.Context, resourceLink) ([]resourceMessage, error) {
				calls++
				return nil, tgerr.New(400, code)
			}}}
			for _, id := range []int{1, 2} {
				_, _, err := r.Resolve(context.Background(), []resourceLink{{Kind: "message", Chat: "still_live", ID: id}})
				require.Error(t, err)
				require.False(t, isUnavailableResource(err))
			}
			require.Equal(t, 2, calls)
		})
	}
}

func TestMultiHopUnavailableTargetDoesNotBlacklistLiveRedirector(t *testing.T) {
	calls := map[string]int{}
	opts := LinkOptions{}
	require.NoError(t, opts.Normalize())
	r := &linkResolver{opts: opts, backend: linkedFakeBackend{fetch: func(_ context.Context, l resourceLink) ([]resourceMessage, error) {
		calls[l.Chat]++
		if l.Chat == "redirector" {
			return []resourceMessage{{Message: &tg.Message{ID: l.ID, Message: "https://t.me/dead_bot?start=payload"}}}, nil
		}
		if l.Chat == "dead_bot" {
			return nil, tgerr.New(400, "INPUT_USER_DEACTIVATED")
		}
		return []resourceMessage{{Peer: &tg.InputPeerUser{UserID: 100}, Message: linkedDocument(10, 99, "ref", []byte("data"))}}, nil
	}}}
	for _, id := range []int{1, 2} {
		_, _, err := r.Resolve(context.Background(), []resourceLink{{Kind: "message", Chat: "redirector", ID: id}})
		require.True(t, isUnavailableResource(err))
	}
	files, _, err := r.Resolve(context.Background(), []resourceLink{{Kind: "message", Chat: "healthy", ID: 1}})
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, map[string]int{"redirector": 2, "dead_bot": 1, "healthy": 1}, calls)
}

func TestUnavailablePeerClassificationAndNoEmptyArchive(t *testing.T) {
	cache := &UnavailableLinks{}
	link := resourceLink{Kind: "message", Chat: "123", ID: 1}
	err := cache.remember(link, &peers.PeerNotFoundError{Peer: &tg.PeerChannel{ChannelID: 123}})
	require.True(t, isUnavailableResource(err))
	require.False(t, isUnavailableResource(errors.Join(err, errors.New("disk failed"))), "a dead target must not hide unrelated failures")
	require.True(t, isUnavailableResource(fmt.Errorf("wrapped: %w", err)))
	root := t.TempDir()
	r := &linkResolver{opts: LinkOptions{MaxDepth: 8, MaxLinks: 100}, unavailable: cache}
	post := tagPost{MessageID: 42, Directory: "Caption [42]"}
	err = archiveLinkedPost(context.Background(), root, post, nil, nil, []resourceLink{link}, r, LinkedOptions{})
	require.True(t, isUnavailableResource(err))
	require.NoDirExists(t, root+"/"+post.Directory)
}
