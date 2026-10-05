package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/downloader"
)

func linkedPromotionMessage(id int) *tg.Message {
	return &tg.Message{
		ID: id, PeerID: &tg.PeerUser{UserID: 100},
		Message:  "https://t.me/AnYunBot?start=1 https://t.me/+Yrrh9FboakRjZTIx",
		Entities: []tg.MessageEntityClass{&tg.MessageEntityTextURL{URL: "https://t.me/AnYunBot?start=ad"}},
		ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "推广", URL: "https://t.me/AnYunBot?start=ad"},
			&tg.KeyboardButtonURL{Text: "加入群组", URL: "https://t.me/+Yrrh9FboakRjZTIx"},
		}}}},
	}
}

func TestLinkedResolutionKeepsMediaAcrossEightBotLayers(t *testing.T) {
	opts := LinkOptions{}
	require.NoError(t, opts.Normalize())
	root, err := parseResourceLink("https://t.me/layer1_bot?start=files")
	require.NoError(t, err)
	var visited []string
	r := &linkResolver{opts: opts, backend: linkedFakeBackend{fetch: func(_ context.Context, l resourceLink) ([]resourceMessage, error) {
		visited = append(visited, l.Chat+":"+l.Start)
		if l.Chat == "AnYunBot" {
			if l.Start == "ad" {
				return nil, botResponseTimeout(200, 120, map[int]*tg.Message{1: {ID: 1, Message: "关注推广频道"}})
			}
			return []resourceMessage{{Message: &tg.Message{ID: 1, Message: "推广内容"}}}, nil
		}
		var layer int
		_, err := fmt.Sscanf(l.Chat, "layer%d_bot", &layer)
		require.NoError(t, err)
		var msgs []resourceMessage
		if layer == 2 || layer == 5 || layer == 8 {
			m := linkedDocument(layer, int64(layer), "ref", []byte("media"))
			if layer == 2 {
				m.Media.(*tg.MessageMediaDocument).Video = true
			} else if layer == 5 {
				m = botMultipleReplies("ref")[3]
				m.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo).ID = 5
			}
			msgs = append(msgs, resourceMessage{Peer: &tg.InputPeerUser{UserID: 100}, Message: m})
		}
		// Promotions precede the continuation, so an empty branch must not
		// prevent a later sibling from supplying additional media.
		msgs = append(msgs, resourceMessage{Message: linkedPromotionMessage(20 + layer)})
		if layer < 8 {
			msgs = append(msgs, resourceMessage{Message: &tg.Message{ID: 30 + layer, Message: fmt.Sprintf("https://t.me/layer%d_bot?start=files", layer+1)}})
		}
		return msgs, nil
	}}}
	files, hops, err := r.Resolve(t.Context(), []resourceLink{root})
	require.NoError(t, err)
	require.Len(t, files, 3)
	require.Equal(t, []string{"document_2", "photo_5_x", "document_8"}, []string{resourceIdentity(files[0].Media), resourceIdentity(files[1].Media), resourceIdentity(files[2].Media)})
	require.Len(t, visited, 10, "each distinct bot start link is visited once; group invites are not fetched")
	require.Len(t, hops, 10)
}

func TestLinkedResolutionDoesNotGuessPromotionFromBotNameOrPayload(t *testing.T) {
	opts := LinkOptions{}
	require.NoError(t, opts.Normalize())
	root := resourceLink{Kind: "bot", Chat: "source_bot", Start: "files"}
	r := &linkResolver{opts: opts, backend: linkedFakeBackend{fetch: func(_ context.Context, l resourceLink) ([]resourceMessage, error) {
		if l.Chat == "source_bot" {
			return []resourceMessage{
				{Peer: &tg.InputPeerUser{UserID: 100}, Message: linkedDocument(1, 1, "ref", []byte("first"))},
				{Message: linkedPromotionMessage(2)},
			}, nil
		}
		id := int64(2)
		if l.Start == "ad" {
			id = 3
		}
		return []resourceMessage{{Peer: &tg.InputPeerUser{UserID: 200}, Message: linkedDocument(3, id, "ref", []byte("more"))}}, nil
	}}}
	files, _, err := r.Resolve(t.Context(), []resourceLink{root})
	require.NoError(t, err)
	require.Len(t, files, 3, "media from every visited bot is retained even with a short or ad-like payload")
}

func TestLinkedResolutionMediaSurvivesDeadEndsInAnyOrder(t *testing.T) {
	for _, mode := range []string{"empty", "timeout", "unavailable", "cycle", "depth", "count"} {
		for _, mediaFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mediaFirst=%t", mode, mediaFirst), func(t *testing.T) {
				opts := LinkOptions{}
				require.NoError(t, opts.Normalize())
				root := resourceLink{Kind: "bot", Chat: "source_bot", Start: "files"}
				if mode == "depth" {
					opts.MaxDepth = 1
				}
				if mode == "count" {
					opts.MaxLinks = 1
				}
				r := &linkResolver{opts: opts, backend: linkedFakeBackend{fetch: func(_ context.Context, l resourceLink) ([]resourceMessage, error) {
					if l.Chat == root.Chat {
						media := resourceMessage{Peer: &tg.InputPeerUser{UserID: 100}, Message: linkedDocument(1, 99, "ref", []byte("data"))}
						promo := resourceMessage{Message: &tg.Message{ID: 2, Message: "https://t.me/other_bot?start=ad"}}
						if mode == "cycle" {
							promo.Message.Message = root.URL()
						}
						if mediaFirst {
							return []resourceMessage{media, promo}, nil
						}
						return []resourceMessage{promo, media}, nil
					}
					switch mode {
					case "timeout":
						return nil, botResponseTimeout(200, 120, nil)
					case "unavailable":
						return nil, tgerr.New(400, "USERNAME_NOT_OCCUPIED")
					}
					return nil, nil
				}}}
				files, hops, err := r.Resolve(t.Context(), []resourceLink{root})
				require.NoError(t, err)
				require.Len(t, files, 1)
				require.Len(t, hops, 2)
				data, err := json.Marshal(hops[1])
				require.NoError(t, err)
				require.Contains(t, string(data), "skipped", "dead ends remain visible in archive metadata")
			})
		}
	}
}

func TestLinkedResolutionNoMediaAndRealFailuresRemainErrors(t *testing.T) {
	for _, mode := range []string{"no media", "network", "unsettled resources", "canceled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			opts := LinkOptions{}
			require.NoError(t, opts.Normalize())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := &linkResolver{opts: opts, backend: linkedFakeBackend{fetch: func(_ context.Context, l resourceLink) ([]resourceMessage, error) {
				if l.Chat == "source_bot" {
					msgs := []resourceMessage{{Message: &tg.Message{ID: 1, Message: "https://t.me/other_bot?start=ad"}}}
					if mode != "no media" {
						msgs = append(msgs, resourceMessage{Peer: &tg.InputPeerUser{UserID: 100}, Message: linkedDocument(2, 99, "ref", []byte("data"))})
					}
					return msgs, nil
				}
				switch mode {
				case "network":
					return nil, tgerr.New(500, "INTERNAL")
				case "unsettled resources":
					return nil, botResponseTimeout(200, 120, map[int]*tg.Message{1: linkedDocument(1, 100, "ref", []byte("pending"))})
				case "canceled":
					cancel()
					return nil, nil
				case "deadline":
					return nil, context.DeadlineExceeded
				}
				return nil, botResponseTimeout(200, 120, nil)
			}}}
			_, _, err := r.Resolve(ctx, []resourceLink{{Kind: "bot", Chat: "source_bot", Start: "files"}})
			require.Error(t, err)
			if mode == "canceled" {
				require.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}

func TestLinkedArchiveDownloadsAndResumesDespitePromotionTimeout(t *testing.T) {
	data := bytes.Repeat([]byte{42}, 2*downloader.MaxPartSize+127)
	opts := LinkedOptions{Threads: 1, Limit: 1}
	require.NoError(t, opts.Links.Normalize())
	root := resourceLink{Kind: "bot", Chat: "source_bot", Start: "files"}
	requests := 0
	r := &linkResolver{opts: opts.Links, backend: linkedFakeBackend{fetch: func(_ context.Context, l resourceLink) ([]resourceMessage, error) {
		if l.Chat != root.Chat {
			return nil, botResponseTimeout(200, 120, nil)
		}
		requests++
		ref := "old"
		if requests > 1 {
			ref = "fresh"
		}
		return []resourceMessage{
			{Peer: &tg.InputPeerUser{UserID: 100}, Message: linkedDocument(10+requests, 99, ref, data)},
			{Message: linkedPromotionMessage(20 + requests)},
		}, nil
	}}}
	var offsets []int64
	opts.Pool = linkedPool{api: tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		req, ok := in.(*tg.UploadGetFileRequest)
		if !ok {
			return fmt.Errorf("unexpected RPC %T", in)
		}
		if string(req.Location.(*tg.InputDocumentFileLocation).FileReference) == "old" && req.Offset > 0 {
			return tgerr.New(400, "FILE_REFERENCE_EXPIRED")
		}
		offsets = append(offsets, req.Offset)
		end := min(int64(len(data)), req.Offset+int64(req.Limit))
		return linkedReply(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data[req.Offset:end]}, out)
	}))}
	dir := t.TempDir()
	post := tagPost{ChatID: 1, MessageID: 42, Text: "Original caption", Directory: "Original caption [42]"}
	require.NoError(t, archiveLinkedPost(t.Context(), dir, post, nil, nil, []resourceLink{root}, r, opts))
	require.Equal(t, 2, requests, "expired media references reissue the same chain including optional branches")
	require.Equal(t, []int64{0, downloader.MaxPartSize, 2 * downloader.MaxPartSize}, offsets, "completed chunks survive the bot reissue")
	saved := completedLinkedPost(filepath.Join(dir, post.Directory), post, linkedFingerprint([]resourceLink{root}, opts))
	require.NotNil(t, saved)
	require.Len(t, saved.Resources, 1)
	require.Equal(t, 12, saved.Resources[0].MessageID)
	got, err := os.ReadFile(filepath.Join(dir, post.Directory, saved.Resources[0].File))
	require.NoError(t, err)
	require.Equal(t, data, got)
	meta, err := os.ReadFile(filepath.Join(dir, post.Directory, "meta.json"))
	require.NoError(t, err)
	require.Contains(t, string(meta), "skipped")
}

func TestLinkedArchiveBotRepliesWithPromotionLinksAndCachedDeadRoot(t *testing.T) {
	media := botMultipleReplies("ref")
	promo := linkedPromotionMessage(6)
	media[len(media)-1] = promo
	promo.SetEntities(promo.Entities)
	promo.SetReplyMarkup(promo.ReplyMarkup)
	requests := map[string]int{}
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			switch strings.ToLower(req.Username) {
			case "source":
				return linkedReply(&tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 50}, Chats: []tg.ChatClass{archiveChannel(50)}}, out)
			case "source_bot", "anyunbot":
				id := int64(100)
				if strings.EqualFold(req.Username, "AnYunBot") {
					id = 200
				}
				return linkedReply(&tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: id}, Users: []tg.UserClass{&tg.User{ID: id, Bot: true}}}, out)
			default:
				return fmt.Errorf("unexpected target %s", req.Username)
			}
		case *tg.ChannelsGetMessagesRequest:
			return linkedReply(&tg.MessagesChannelMessages{Messages: []tg.MessageClass{&tg.Message{
				ID: 42, PeerID: &tg.PeerChannel{ChannelID: 50}, Message: "Source caption https://t.me/dead_bot?start=ad https://t.me/source_bot?start=files",
			}}}, out)
		case *tg.MessagesGetHistoryRequest:
			return linkedReply(&tg.MessagesMessages{}, out)
		case *tg.MessagesStartBotRequest:
			botID := req.Bot.(*tg.InputUser).UserID
			requests[fmt.Sprintf("%d:%s", botID, req.StartParam)]++
			var replies []tg.UpdateClass
			if botID == 100 {
				for _, m := range media {
					replies = append(replies, &tg.UpdateNewMessage{Message: m})
				}
			} else {
				replies = append(replies, &tg.UpdateNewMessage{Message: &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 200}, Message: "推广频道"}})
			}
			return linkedReply(&tg.Updates{Updates: replies}, out)
		case *tg.UploadGetFileRequest:
			data := []byte("video")
			if _, ok := req.Location.(*tg.InputPhotoFileLocation); ok {
				data = []byte("photo")
			}
			return linkedReply(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	no := false
	cache := &UnavailableLinks{}
	cache.remember(resourceLink{Kind: "bot", Chat: "dead_bot"}, tgerr.New(400, "USERNAME_NOT_OCCUPIED"))
	opts := LinkedOptions{Chat: "https://t.me/source/42", Dir: t.TempDir(), Threads: 1, Limit: 1,
		Pool: linkedPool{api: api}, Unavailable: cache,
		Links: LinkOptions{BotTimeout: 2, BotIdle: 1, PollInterval: 10, ScanComments: &no, CleanupBotMessages: &no}}
	require.NoError(t, downloadLinked(t.Context(), api, &archiveMemory{}, opts))
	require.Equal(t, map[string]int{"100:files": 1, "200:ad": 1, "200:1": 1}, requests)
	entries, err := os.ReadDir(filepath.Join(opts.Dir, "50"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	dir := filepath.Join(opts.Dir, "50", entries[0].Name())
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	require.NoError(t, err)
	var saved linkedPost
	require.NoError(t, json.Unmarshal(b, &saved))
	require.True(t, saved.Complete)
	require.Len(t, saved.Resources, 2)
	require.Len(t, saved.Hops, 4)
	for _, file := range saved.Resources {
		b, err := os.ReadFile(filepath.Join(dir, file.File))
		require.NoError(t, err)
		if file.Identity == "document_99" {
			require.Equal(t, []byte("video"), b)
		} else {
			require.Equal(t, "photo_100_x", file.Identity)
			require.Equal(t, []byte("photo"), b)
		}
	}
}
