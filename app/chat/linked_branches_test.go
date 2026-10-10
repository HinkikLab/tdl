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
			switch layer {
			case 2:
				m.Media.(*tg.MessageMediaDocument).Video = true
			case 5:
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
	require.Len(t, visited, 8, "promotion links are deferred and not requested once files exist; group invites are not fetched")
	require.Len(t, hops, 10, "the two deferred promotion links remain visible as skipped hops")
	for _, hop := range hops[8:] {
		require.Contains(t, hop.URL, "AnYunBot")
		require.Contains(t, hop.Skipped, "promotion link not requested")
	}
}

func TestLinkedResolutionDefersLabeledPromotions(t *testing.T) {
	for _, tc := range []struct {
		name          string
		sourceFiles   bool
		disabled      bool
		files         int
		promoRequests int
	}{
		{name: "files found elsewhere", sourceFiles: true, files: 1, promoRequests: 0},
		{name: "fallback requests every deferred link", sourceFiles: false, files: 2, promoRequests: 2},
		{name: "deferral disabled", sourceFiles: true, disabled: true, files: 3, promoRequests: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := LinkOptions{}
			if tc.disabled {
				no := false
				opts.DeferPromotions = &no
			}
			require.NoError(t, opts.Normalize())
			root := resourceLink{Kind: "bot", Chat: "source_bot", Start: "files"}
			promoRequests := 0
			r := &linkResolver{opts: opts, backend: linkedFakeBackend{fetch: func(_ context.Context, l resourceLink) ([]resourceMessage, error) {
				if l.Chat == "source_bot" {
					msgs := []resourceMessage{{Message: linkedPromotionMessage(2)}}
					if tc.sourceFiles {
						msgs = append(msgs, resourceMessage{Peer: &tg.InputPeerUser{UserID: 100}, Message: linkedDocument(1, 1, "ref", []byte("first"))})
					}
					return msgs, nil
				}
				promoRequests++
				id := int64(2)
				if l.Start == "ad" {
					id = 3
				}
				return []resourceMessage{{Peer: &tg.InputPeerUser{UserID: 200}, Message: linkedDocument(3, id, "ref", []byte("more"))}}, nil
			}}}
			files, _, err := r.Resolve(t.Context(), []resourceLink{root})
			require.NoError(t, err)
			require.Len(t, files, tc.files)
			require.Equal(t, tc.promoRequests, promoRequests)
		})
	}
}

func TestLinkedResolutionStopsAdChainsOnceFallbackFindsFiles(t *testing.T) {
	opts := LinkOptions{}
	require.NoError(t, opts.Normalize())
	ad := func(chat string) *tg.Message {
		m := &tg.Message{ID: 2, Message: "x"}
		m.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonURL{Text: "广告", URL: "https://t.me/" + chat + "?start=1"}}}}})
		return m
	}
	var visited []string
	r := &linkResolver{opts: opts, backend: linkedFakeBackend{fetch: func(_ context.Context, l resourceLink) ([]resourceMessage, error) {
		visited = append(visited, l.Chat)
		switch l.Chat {
		case "source_bot":
			return []resourceMessage{{Message: ad("first_ad_bot")}}, nil
		case "first_ad_bot":
			return []resourceMessage{
				{Peer: &tg.InputPeerUser{UserID: 200}, Message: linkedDocument(3, 3, "ref", []byte("file"))},
				{Message: ad("second_ad_bot")},
			}, nil
		}
		return nil, fmt.Errorf("unexpected request to %s", l.Chat)
	}}}
	files, hops, err := r.Resolve(t.Context(), []resourceLink{{Kind: "bot", Chat: "source_bot", Start: "files"}})
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, []string{"source_bot", "first_ad_bot"}, visited)
	require.Contains(t, hops[len(hops)-1].Skipped, "promotion link not requested")
}

func TestLinkedResolutionDoesNotGuessPromotionFromBotNameOrPayload(t *testing.T) {
	opts := LinkOptions{}
	require.NoError(t, opts.Normalize())
	root := resourceLink{Kind: "bot", Chat: "source_bot", Start: "files"}
	r := &linkResolver{opts: opts, backend: linkedFakeBackend{fetch: func(_ context.Context, l resourceLink) ([]resourceMessage, error) {
		if l.Chat == "source_bot" {
			return []resourceMessage{
				{Peer: &tg.InputPeerUser{UserID: 100}, Message: linkedDocument(1, 1, "ref", []byte("first"))},
				{Message: &tg.Message{ID: 2, Message: "https://t.me/AdYunBot?start=ad https://t.me/ad_bot?start=1"}},
			}, nil
		}
		return []resourceMessage{{Peer: &tg.InputPeerUser{UserID: 200}, Message: linkedDocument(3, int64(len(l.Chat)), "ref", []byte("more"))}}, nil
	}}}
	files, _, err := r.Resolve(t.Context(), []resourceLink{root})
	require.NoError(t, err)
	require.Len(t, files, 3, "unlabeled links are requested even with an ad-like bot name or payload")
}

func TestPromotionLabels(t *testing.T) {
	opts := LinkOptions{PromotionKeywords: []string{" VPN "}}
	require.NoError(t, opts.Normalize())
	text := strings.Join([]string{
		"资源下载：https://t.me/files_bot?start=abc 广告：https://t.me/ad_bot?start=x",
		"赞助商👇",
		"https://t.me/sponsor_bot?start=1",
		"https://t.me/vpn_bot?start=2 best VPN",
		"https://t.me/group/5 (AD)",
	}, "\n")
	links := messageResourceLinks(&tg.Message{Message: text}, opts.promotion)
	promo := map[string]bool{}
	for _, l := range links {
		promo[l.Chat] = l.Promo
	}
	require.Equal(t, map[string]bool{"files_bot": false, "ad_bot": true, "sponsor_bot": true, "vpn_bot": true, "group": true}, promo)

	m := linkedPromotionMessage(1)
	for _, l := range messageResourceLinks(m, opts.promotion) {
		require.True(t, l.Promo, "a promotion button marks every link to that bot in the message: %s", l.URL())
	}
	no := false
	opts.DeferPromotions = &no
	for _, l := range messageResourceLinks(m, opts.promotion) {
		require.False(t, l.Promo)
	}
	require.False(t, (&LinkOptions{}).promotion("Download"), "word boundaries keep 'ad' inside words unlabeled")
}

func TestPromotionBannerDroppedOnlyWhenOtherFilesExist(t *testing.T) {
	opts := LinkOptions{}
	require.NoError(t, opts.Normalize())
	banner := botMultipleReplies("ref")[3]
	banner.ID = 9
	banner.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonURL{Text: "广告", URL: "https://t.me/ad_bot?start=x"}}}}})
	for _, withFile := range []bool{true, false} {
		r := &linkResolver{opts: opts, backend: linkedFakeBackend{fetch: func(_ context.Context, l resourceLink) ([]resourceMessage, error) {
			if l.Chat == "ad_bot" {
				return nil, botResponseTimeout(200, 10, nil)
			}
			msgs := []resourceMessage{{Peer: &tg.InputPeerUser{UserID: 100}, Message: banner}}
			if withFile {
				msgs = append(msgs, resourceMessage{Peer: &tg.InputPeerUser{UserID: 100}, Message: linkedDocument(1, 1, "ref", []byte("file"))})
			}
			return msgs, nil
		}}}
		files, _, err := r.Resolve(t.Context(), []resourceLink{{Kind: "bot", Chat: "source_bot", Start: "files"}})
		require.NoError(t, err)
		require.Len(t, files, 1)
		if withFile {
			require.Equal(t, "document_1", resourceIdentity(files[0].Media))
		} else {
			require.Equal(t, resourceIdentity(files[0].Media), "photo_100_x", "a banner is kept when it is the only media")
		}
	}
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
	t.Parallel()
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
	opts := LinkedOptions{
		Chat: "https://t.me/source/42", Dir: t.TempDir(), Threads: 1, Limit: 1,
		Pool: linkedPool{api: api}, Unavailable: cache,
		Links: LinkOptions{BotTimeout: 2, BotIdle: 1, PollInterval: 10, ScanComments: &no, CleanupBotMessages: &no},
	}
	require.NoError(t, downloadLinked(t.Context(), api, &archiveMemory{}, opts))
	require.Equal(t, map[string]int{"100:files": 1}, requests, "labeled promotion links are not requested once files exist")
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
