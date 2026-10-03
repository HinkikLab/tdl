package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/stretchr/testify/require"
)

type linkedRPC func(context.Context, bin.Encoder, bin.Decoder) error

func (f linkedRPC) Invoke(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
	return f(ctx, in, out)
}
func linkedReply(in bin.Encoder, out bin.Decoder) error {
	var b bin.Buffer
	if err := in.Encode(&b); err != nil {
		return err
	}
	return out.Decode(&b)
}

type linkedPool struct{ api *tg.Client }

func (p linkedPool) Client(context.Context, int) *tg.Client  { return p.api }
func (p linkedPool) Takeout(context.Context, int) *tg.Client { return p.api }
func (p linkedPool) Default(context.Context) *tg.Client      { return p.api }
func (p linkedPool) Close() error                            { return nil }

type linkedFakeBackend struct {
	fetch func(context.Context, resourceLink) ([]resourceMessage, error)
}

func (b linkedFakeBackend) Fetch(ctx context.Context, l resourceLink) ([]resourceMessage, error) {
	return b.fetch(ctx, l)
}
func (linkedFakeBackend) Comments(context.Context, tg.InputPeerClass, *tg.Message, int) ([]resourceMessage, error) {
	return nil, nil
}

func linkedDocument(id int, docID int64, ref string, data []byte) *tg.Message {
	m := &tg.Message{ID: id, PeerID: &tg.PeerUser{UserID: 100}, Message: "resource", Date: 1700000000}
	m.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: docID, AccessHash: 55, DCID: 2, Size: int64(len(data)), FileReference: []byte(ref), MimeType: "application/octet-stream", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "notes.bin"}}}})
	return m
}

func TestResourceLinkEntitiesAndButtons(t *testing.T) {
	visible := "📚课程 https://t.me/course_files/12"
	offset := len(utf16.Encode([]rune("📚课程 ")))
	m := &tg.Message{Message: visible}
	m.SetEntities([]tg.MessageEntityClass{
		&tg.MessageEntityURL{Offset: offset, Length: len(utf16.Encode([]rune(visible[len("📚课程 "):])))},
		&tg.MessageEntityTextURL{Offset: 0, Length: 2, URL: "https://t.me/files_bot?start=part_A"},
		&tg.MessageEntityTextURL{Offset: 0, Length: 2, URL: "https://t.me/course_files/12"},
	})
	m.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonURL{Text: "files", URL: "tg://resolve?domain=other_bot&start=next"}, &tg.KeyboardButtonURL{Text: "web", URL: "https://example.com/"}}}}})
	links := messageResourceLinks(m)
	require.Len(t, links, 3)
	require.Equal(t, "message", links[0].Kind)
	require.Equal(t, 12, links[0].ID)
	require.Equal(t, "part_A", links[1].Start)
	require.Equal(t, "other_bot", links[2].Chat)
	require.Equal(t, "", utf16Text("abc", -1, 4))
}

func TestResourceLinkParsing(t *testing.T) {
	for _, raw := range []string{"t.me/files/13", "https://telegram.me/s/files/4/13", "tg://resolve?domain=files&post=13", "https://t.me/c/42/2/13?single", "tg://privatepost?channel=42&post=13&single"} {
		l, err := parseResourceLink(raw)
		require.NoError(t, err)
		require.Equal(t, 13, l.ID)
	}
	a, err := parseResourceLink("https://t.me/MyBot?start=Case_Sensitive")
	require.NoError(t, err)
	b, err := parseResourceLink("tg://resolve?domain=mybot&start=Case_Sensitive")
	require.NoError(t, err)
	require.Equal(t, a.key(), b.key())
	for _, raw := range []string{"https://example.com/files/3", "https://t.me/c/nope/3", "https://t.me/files/0", "https://t.me/files/3?comment=-1", "https://t.me/files/3?start=a", "file://t.me/files/3", "https://t.me:8080/files/3"} {
		_, err := parseResourceLink(raw)
		require.Error(t, err, raw)
	}
}

func TestResourceResolutionMultiHopAndGuards(t *testing.T) {
	opts := LinkOptions{}
	require.NoError(t, opts.Normalize())
	root, _ := parseResourceLink("https://t.me/first_bot?start=course")
	backend := linkedFakeBackend{fetch: func(ctx context.Context, l resourceLink) ([]resourceMessage, error) {
		m := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 100}}
		switch l.Chat {
		case "first_bot":
			m.Message = "https://t.me/second_bot?start=next"
		case "second_bot":
			m.Message = "https://t.me/resources/9"
		case "resources":
			m = linkedDocument(9, 88, "fresh", []byte("notes"))
		}
		return []resourceMessage{{Peer: &tg.InputPeerUser{UserID: 100}, Message: m}}, nil
	}}
	r := &linkResolver{backend: backend, opts: opts}
	files, hops, err := r.Resolve(context.Background(), []resourceLink{root, root})
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Len(t, hops, 3)
	r.opts.MaxDepth = 2
	_, _, err = r.Resolve(context.Background(), []resourceLink{root})
	require.ErrorContains(t, err, "depth")
	r.opts = opts
	r.opts.MaxLinks = 2
	_, _, err = r.Resolve(context.Background(), []resourceLink{root})
	require.ErrorContains(t, err, "count")
	r.opts = opts
	r.backend = linkedFakeBackend{fetch: func(context.Context, resourceLink) ([]resourceMessage, error) {
		return []resourceMessage{{Message: &tg.Message{ID: 1, Message: root.URL()}}}, nil
	}}
	_, _, err = r.Resolve(context.Background(), []resourceLink{root})
	require.ErrorContains(t, err, "cycle")
	r.backend = linkedFakeBackend{fetch: func(context.Context, resourceLink) ([]resourceMessage, error) {
		return []resourceMessage{{Message: &tg.Message{ID: 1, Message: "wait"}}}, nil
	}}
	_, _, err = r.Resolve(context.Background(), []resourceLink{root})
	require.ErrorContains(t, err, "no files")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = r.Resolve(ctx, []resourceLink{root})
	require.ErrorIs(t, err, context.Canceled)
}

func TestLinkedArchiveReissuesChainAndResumesStableFile(t *testing.T) {
	root := t.TempDir()
	data := bytes.Repeat([]byte{42}, 3*downloader.MaxPartSize+127)
	opts := LinkedOptions{Threads: 1, Limit: 2}
	require.NoError(t, opts.Links.Normalize())
	link, _ := parseResourceLink("https://t.me/first_bot?start=course")
	requests := 0
	r := &linkResolver{opts: opts.Links, backend: linkedFakeBackend{fetch: func(_ context.Context, l resourceLink) ([]resourceMessage, error) {
		m := &tg.Message{ID: 10, PeerID: &tg.PeerUser{UserID: 100}, Message: "https://t.me/last_bot?start=files"}
		if l.Chat == "first_bot" {
			requests++
		} else {
			ref := "old"
			if requests > 1 {
				ref = fmt.Sprintf("fresh%d", requests-1)
			}
			m = linkedDocument(20+requests, 99, ref, data)
		}
		return []resourceMessage{{Peer: &tg.InputPeerUser{UserID: 100}, Message: m}}, nil
	}}}
	preview := &tg.Message{ID: 42, PeerID: &tg.PeerChannel{ChannelID: 1}, Message: "Lecture notes #course"}
	post, ok := linkedSourcePost([]*tg.Message{preview}, nil, "any", 1, "course")
	require.True(t, ok)
	md, _ := tmedia.GetMedia(linkedDocument(21, 99, "old", data))
	name, err := resourceFileName(md)
	require.NoError(t, err)
	dir := filepath.Join(root, post.Directory)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name)
	f, parts, err := downloader.OpenPartial(path+".tmp", int64(len(data)))
	require.NoError(t, err)
	_, err = f.WriteAt(data[:downloader.MaxPartSize], 0)
	require.NoError(t, err)
	parts.PartDone(0)
	require.NoError(t, parts.Flush())
	require.NoError(t, f.Close())
	var mu sync.Mutex
	offsets := map[int64]int{}
	opts.Pool = linkedPool{tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		req, ok := in.(*tg.UploadGetFileRequest)
		if !ok {
			return fmt.Errorf("unexpected RPC %T", in)
		}
		ref := string(req.Location.(*tg.InputDocumentFileLocation).FileReference)
		if ref == "old" || (ref == "fresh1" && req.Offset >= 2*downloader.MaxPartSize) {
			return tgerr.New(400, "FILE_REFERENCE_EXPIRED")
		}
		mu.Lock()
		offsets[req.Offset]++
		mu.Unlock()
		end := min(int64(len(data)), req.Offset+int64(req.Limit))
		return linkedReply(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data[req.Offset:end]}, out)
	}))}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, archiveLinkedPost(ctx, root, post, []*tg.Message{preview}, &tg.InputPeerChannel{ChannelID: 1}, []resourceLink{link}, r, opts))
	require.Equal(t, 3, requests, "all hops must be reissued after both expirations")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, got)
	require.NotContains(t, offsets, int64(0), "completed part must not be downloaded again")
	require.NoFileExists(t, path+".tmp")
	require.NoFileExists(t, downloader.PartsPath(path+".tmp"))
	caption, err := os.ReadFile(filepath.Join(dir, "message.txt"))
	require.NoError(t, err)
	require.Equal(t, preview.Message, string(caption))
	require.True(t, completedLinkedPost(dir, post, linkedFingerprint([]resourceLink{link}, opts)))
	metadata, err := os.ReadFile(filepath.Join(dir, "message.json"))
	require.NoError(t, err)
	var saved linkedPost
	require.NoError(t, json.Unmarshal(metadata, &saved))
	require.Equal(t, 23, saved.Resources[0].MessageID, "manifest follows the final reissued source")
	require.NoError(t, archiveLinkedPost(ctx, root, post, []*tg.Message{preview}, &tg.InputPeerChannel{ChannelID: 1}, []resourceLink{link}, r, opts))
	require.Equal(t, 3, requests, "complete archive must not send another bot request")
	require.NoError(t, os.Truncate(path, 1))
	require.False(t, completedLinkedPost(dir, post, linkedFingerprint([]resourceLink{link}, opts)))
}

func TestLinkedSessionRejectsReplacedMediaAndBoundsRetries(t *testing.T) {
	for _, mode := range []string{"changed ID", "changed size", "changed DC", "same reference", "no retry"} {
		t.Run(mode, func(t *testing.T) {
			opts := LinkOptions{}
			require.NoError(t, opts.Normalize())
			if mode == "no retry" {
				*opts.ReRequestLimit = 0
			}
			msg := linkedDocument(1, 99, "old", []byte("data"))
			md, _ := tmedia.GetMedia(msg)
			root, _ := parseResourceLink("https://t.me/files_bot?start=course")
			calls := 0
			r := &linkResolver{opts: opts, backend: linkedFakeBackend{fetch: func(context.Context, resourceLink) ([]resourceMessage, error) {
				calls++
				fresh := linkedDocument(2, 99, "fresh", []byte("data"))
				doc := fresh.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
				switch mode {
				case "changed ID":
					doc.ID++
				case "changed size":
					doc.Size++
				case "changed DC":
					doc.DCID++
				case "same reference":
					doc.FileReference = []byte("old")
				}
				return []resourceMessage{{Peer: &tg.InputPeerUser{UserID: 100}, Message: fresh}}, nil
			}}}
			s := &linkedSession{resolver: r, roots: []resourceLink{root}}
			_, err := s.refresh(context.Background(), linkedResource{resourceMessage: resourceMessage{Peer: &tg.InputPeerUser{UserID: 100}, Message: msg}, Media: md})
			require.Error(t, err)
			if mode == "no retry" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
		})
	}
}

func TestBotCollectsOnlyNewMessagesAndKeepsDeletedReplies(t *testing.T) {
	opts := LinkOptions{BotTimeout: 3, BotIdle: 1, PollInterval: 10}
	require.NoError(t, opts.Normalize())
	bot := (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true, AccessHash: 2})
	started := false
	fetches := 0
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.MessagesGetHistoryRequest:
			if !started {
				return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{linkedDocument(10, 1, "old", []byte("old"))}}, out)
			}
			require.Equal(t, 10, req.MinID)
			fetches++
			msgs := []tg.MessageClass{}
			if fetches == 1 {
				msgs = append(msgs, linkedDocument(12, 2, "fresh", []byte("new")))
			}
			return linkedReply(&tg.MessagesMessages{Messages: msgs}, out)
		case *tg.MessagesStartBotRequest:
			started = true
			require.Equal(t, "course", req.StartParam)
			require.NotZero(t, req.RandomID)
			return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.Message{ID: 11, Out: true, PeerID: &tg.PeerUser{UserID: 100}, Message: "/start course"}}}}, out)
		}
		return fmt.Errorf("unexpected RPC %T", in)
	}))
	b := &telegramLinkBackend{api: api, opts: opts}
	got, err := b.requestBot(context.Background(), bot, "course")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, 12, got[0].Message.ID)
}

func TestBotWaitCancellationAndNoResourceTimeout(t *testing.T) {
	for _, cancelNow := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelNow), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opts := LinkOptions{BotTimeout: 2, BotIdle: 1, PollInterval: 10}
			require.NoError(t, opts.Normalize())
			api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.MessagesGetHistoryRequest:
					return linkedReply(&tg.MessagesMessages{}, out)
				case *tg.MessagesStartBotRequest:
					if cancelNow {
						cancel()
					}
					return linkedReply(&tg.Updates{}, out)
				}
				return fmt.Errorf("unexpected RPC")
			}))
			b := &telegramLinkBackend{api: api, opts: opts}
			_, err := b.requestBot(ctx, (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "course")
			if cancelNow {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorContains(t, err, "did not settle")
			}
		})
	}
}

func TestCommentLinkUsesDiscussionRootAndReturnedAccessHash(t *testing.T) {
	m := &tg.Message{ID: 42, PeerID: &tg.PeerChannel{ChannelID: 1}}
	m.SetReplies(tg.MessageReplies{Comments: true, Replies: 1, ChannelID: 2})
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.MessagesGetDiscussionMessageRequest:
			require.Equal(t, 42, req.MsgID)
			return linkedReply(&tg.MessagesDiscussionMessage{Messages: []tg.MessageClass{&tg.Message{ID: 102, PeerID: &tg.PeerChannel{ChannelID: 2}}, &tg.Message{ID: 100, PeerID: &tg.PeerChannel{ChannelID: 2}}}, Chats: []tg.ChatClass{&tg.Channel{ID: 2, AccessHash: 99, Title: "discussion", Photo: &tg.ChatPhotoEmpty{}}}}, out)
		case *tg.MessagesGetRepliesRequest:
			require.Equal(t, 100, req.MsgID)
			require.Equal(t, int64(99), req.Peer.(*tg.InputPeerChannel).AccessHash)
			return linkedReply(&tg.MessagesChannelMessages{Messages: []tg.MessageClass{&tg.Message{ID: 101, PeerID: &tg.PeerChannel{ChannelID: 2}, Message: "https://t.me/resources/9"}}}, out)
		}
		return fmt.Errorf("unexpected RPC %T", in)
	}))
	b := &telegramLinkBackend{api: api}
	result, err := b.Comments(context.Background(), &tg.InputPeerChannel{ChannelID: 1}, m, 1)
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, "resources", messageResourceLinks(result[0].Message)[0].Chat)
}

func TestLinkedMetadataAndNaming(t *testing.T) {
	m := &tg.Message{ID: 42, Message: "#course #notes https://t.me/files_bot?start=abc"}
	p, ok := linkedSourcePost([]*tg.Message{m}, []string{"#course"}, "any", 1, "course")
	require.True(t, ok)
	require.Equal(t, "course notes [42]", p.Directory)
	md, _ := tmedia.GetMedia(linkedDocument(1, 99, "x", []byte("data")))
	md.Name = `..\..\bad:name?` + strings.Repeat("课", 200) + ".zip"
	name, err := resourceFileName(md)
	require.NoError(t, err)
	require.Less(t, len(name), 200)
	require.Equal(t, name, filepath.Base(name))
	require.True(t, strings.HasSuffix(name, ".zip"))
	path := filepath.Join(t.TempDir(), "message.json")
	require.NoError(t, saveLinkedJSON(path, linkedPost{tagPost: p, Version: 1}))
	require.NoError(t, saveLinkedJSON(path, linkedPost{tagPost: p, Version: 1, Complete: true}))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var meta linkedPost
	require.NoError(t, json.Unmarshal(b, &meta))
	require.True(t, meta.Complete)
}

func TestBotCooldownWaitAndCleanupEveryGeneratedMessage(t *testing.T) {
	opts := LinkOptions{BotTimeout: 3, BotIdle: 1, PollInterval: 10, FloodWait: 1}
	require.NoError(t, opts.Normalize())
	starts := 0
	var lastStart time.Time
	var deleted []int
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.MessagesGetHistoryRequest:
			if req.Limit == 1 {
				return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 10, PeerID: &tg.PeerUser{UserID: 100}}}}, out)
			}
			return linkedReply(&tg.MessagesMessages{}, out)
		case *tg.MessagesStartBotRequest:
			starts++
			if starts == 2 {
				require.GreaterOrEqual(t, time.Since(lastStart), time.Second)
			}
			lastStart = time.Now()
			base := starts * 20
			request := &tg.Message{ID: base, Out: true, PeerID: &tg.PeerUser{UserID: 100}, Message: "/start notes"}
			var replies []tg.UpdateClass
			replies = append(replies, &tg.UpdateMessageID{RandomID: req.RandomID, ID: base}, &tg.UpdateNewMessage{Message: request})
			if starts == 1 {
				replies = append(replies, &tg.UpdateNewMessage{Message: &tg.Message{ID: base + 1, PeerID: &tg.PeerUser{UserID: 100}, Message: "请求过于频繁，请等待 1 秒后重试"}})
			} else {
				replies = append(replies, &tg.UpdateNewMessage{Message: linkedDocument(base+1, 99, "fresh", []byte("notes"))}, &tg.UpdateNewMessage{Message: linkedDocument(base+2, 100, "fresh", []byte("other notes"))}, &tg.UpdateNewMessage{Message: &tg.Message{ID: base + 3, PeerID: &tg.PeerUser{UserID: 100}, Message: "done"}})
			}
			// An unrelated chat and an old response must never be cleaned up.
			replies = append(replies, &tg.UpdateNewMessage{Message: &tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 100}}}, &tg.UpdateNewMessage{Message: &tg.Message{ID: 70, PeerID: &tg.PeerUser{UserID: 200}}})
			return linkedReply(&tg.Updates{Updates: replies}, out)
		case *tg.MessagesDeleteMessagesRequest:
			require.True(t, req.Revoke)
			deleted = append(deleted, req.ID...)
			return linkedReply(&tg.MessagesAffectedMessages{}, out)
		}
		return fmt.Errorf("unexpected RPC %T", in)
	}))
	b := &telegramLinkBackend{api: api, opts: opts}
	msgs, err := b.requestBot(context.Background(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "notes")
	require.NoError(t, err)
	require.Len(t, msgs, 3)
	require.Equal(t, 2, starts)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, cleanupLinked(ctx, b))
	require.Equal(t, []int{20, 21, 40, 41, 42, 43}, deleted)
	require.Empty(t, b.cleanupIDs)
}

func TestBotCooldownPatternsAndBounds(t *testing.T) {
	for _, tc := range []struct {
		text    string
		seconds int
		matched bool
	}{
		{"Please wait 12 seconds before trying again", 12, true},
		{"Too many requests. Retry after 2 minutes", 120, true},
		{"冷却 1 小时", 3600, true},
		{"请求过于频繁", 30, true},
		{"文件将在 30 秒后删除", 0, false},
		{"Please wait while we prepare your files", 0, false},
		{"Please wait 2 seconds, files will be sent automatically", 0, false},
	} {
		seconds, ok := botCooldown(tc.text, 30)
		require.Equal(t, tc.matched, ok, tc.text)
		require.Equal(t, tc.seconds, seconds, tc.text)
	}
	opts := LinkOptions{BotTimeout: 3, BotIdle: 1}
	require.NoError(t, opts.Normalize())
	*opts.FloodRetries = 0
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetHistoryRequest:
			return linkedReply(&tg.MessagesMessages{}, out)
		case *tg.MessagesStartBotRequest:
			return tgerr.New(420, "FLOOD_WAIT_1")
		}
		return fmt.Errorf("unexpected RPC")
	}))
	_, err := (&telegramLinkBackend{api: api, opts: opts}).requestBot(context.Background(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "notes")
	require.ErrorContains(t, err, "flood_retries exhausted")
}

func TestBotCleanupBatchesAndReportsFailures(t *testing.T) {
	calls := 0
	b := &telegramLinkBackend{api: tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		req, ok := in.(*tg.MessagesDeleteMessagesRequest)
		require.True(t, ok)
		require.LessOrEqual(t, len(req.ID), 100)
		calls++
		if calls == 1 {
			return fmt.Errorf("temporary cleanup failure")
		}
		return linkedReply(&tg.MessagesAffectedMessages{}, out)
	}))}
	for id := 1; id <= 201; id++ {
		b.rememberBotMessages(id)
	}
	require.ErrorContains(t, b.Cleanup(context.Background()), "temporary cleanup failure")
	require.Equal(t, 3, calls)
	require.Len(t, b.cleanupIDs, 100)
	require.NoError(t, b.Cleanup(context.Background()))
	require.Empty(t, b.cleanupIDs)
}

func TestBotLiveUpdatesRetainSelfDeletedFilesAndCleanupLateReplies(t *testing.T) {
	opts := LinkOptions{BotTimeout: 3, BotIdle: 1, PollInterval: 10}
	require.NoError(t, opts.Normalize())
	updates := &BotUpdates{}
	var deleted []int
	api := tg.NewClient(linkedRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.MessagesGetHistoryRequest:
			return linkedReply(&tg.MessagesMessages{}, out) // the bot deleted its files before the next history poll
		case *tg.MessagesStartBotRequest:
			require.NoError(t, updates.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: linkedDocument(2, 99, "fresh", []byte("notes"))}, &tg.UpdateDeleteMessages{Messages: []int{2}}}}))
			return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageID{RandomID: req.RandomID, ID: 1}}}, out)
		case *tg.MessagesDeleteMessagesRequest:
			deleted = append(deleted, req.ID...)
			return linkedReply(&tg.MessagesAffectedMessages{}, out)
		}
		return fmt.Errorf("unexpected RPC %T", in)
	}))
	b := &telegramLinkBackend{api: api, opts: opts, updates: updates}
	msgs, err := b.requestBot(context.Background(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "notes")
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, 2, msgs[0].Message.ID)
	require.NoError(t, updates.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.Message{ID: 3, PeerID: &tg.PeerUser{UserID: 100}, Message: "done"}}, &tg.UpdateNewMessage{Message: &tg.Message{ID: 9, PeerID: &tg.PeerUser{UserID: 200}, Message: "unrelated"}}}}))
	require.NoError(t, cleanupLinked(context.Background(), b))
	require.Equal(t, []int{1, 2, 3}, deleted)
	require.Empty(t, updates.watches)
}

func TestLinkedSessionRejectsChangedResourceSet(t *testing.T) {
	opts := LinkOptions{}
	require.NoError(t, opts.Normalize())
	oldMsg := linkedDocument(1, 99, "old", []byte("notes"))
	md, _ := tmedia.GetMedia(oldMsg)
	old := linkedResource{resourceMessage: resourceMessage{Peer: &tg.InputPeerUser{UserID: 100}, Message: oldMsg}, Media: md}
	link, _ := parseResourceLink("https://t.me/files_bot?start=notes")
	r := &linkResolver{opts: opts, backend: linkedFakeBackend{fetch: func(context.Context, resourceLink) ([]resourceMessage, error) {
		return []resourceMessage{{Message: linkedDocument(2, 99, "fresh", []byte("notes"))}, {Message: linkedDocument(3, 100, "fresh", []byte("extra"))}}, nil
	}}}
	s := &linkedSession{resolver: r, roots: []resourceLink{link}, files: []linkedResource{old}}
	_, err := s.refresh(context.Background(), old)
	require.ErrorContains(t, err, "resource set changed")
}
