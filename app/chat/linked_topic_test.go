package chat

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/core/tmedia"
)

func topicRoot() *tg.MessageService {
	return &tg.MessageService{ID: 10, PeerID: &tg.PeerChannel{ChannelID: 50}, Action: &tg.MessageActionTopicCreate{Title: "Lesson notes"}}
}

func topicReply(id int) *tg.Message {
	m := &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 50}, Message: "Lesson notes"}
	header := &tg.MessageReplyHeader{ForumTopic: true}
	header.SetReplyToMsgID(10)
	m.SetReplyTo(header)
	return m
}

func topicDocument(id int, docID int64, ref string, data []byte) *tg.Message {
	m := linkedDocument(id, docID, ref, data)
	m.PeerID = &tg.PeerChannel{ChannelID: 50}
	m.SetReplyTo(topicReply(id).ReplyTo)
	return m
}

func topicProof(forum bool, id int) *tg.MessagesForumTopics {
	return &tg.MessagesForumTopics{
		Topics: []tg.ForumTopicClass{&tg.ForumTopic{ID: id, Peer: &tg.PeerChannel{ChannelID: 50}, Title: "Lesson notes", FromID: &tg.PeerUser{UserID: 100}, NotifySettings: tg.PeerNotifySettings{}}},
		Chats:  []tg.ChatClass{&tg.Channel{ID: 50, AccessHash: 500, Forum: forum, Title: "Lessons", Photo: &tg.ChatPhotoEmpty{}}},
	}
}

func topicBackend(t *testing.T, rpc linkedRPC, opts LinkOptions) *telegramLinkBackend {
	t.Helper()
	api := tg.NewClient(rpc)
	manager := peers.Options{Cache: &peers.InmemoryCache{}}.Build(api)
	require.NoError(t, manager.Apply(t.Context(), nil, []tg.ChatClass{&tg.Channel{ID: 50, AccessHash: 500, Forum: true}, &tg.Channel{ID: 60, AccessHash: 600}}))
	return &telegramLinkBackend{api: api, manager: manager, opts: opts}
}

// The bot step is synthesized here; all Telegram RPCs used by the resource
// topic path still pass through the counting protocol fixture below.
type topicBotBackend struct {
	*telegramLinkBackend
	botCalls int
}

func (b *topicBotBackend) Fetch(ctx context.Context, l resourceLink) ([]resourceMessage, error) {
	if l.Kind != "bot" {
		return b.telegramLinkBackend.Fetch(ctx, l)
	}
	b.botCalls++
	return []resourceMessage{{Peer: &tg.InputPeerUser{UserID: 100}, Message: &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 100}, Message: "https://t.me/c/60/20"}}}, nil
}

func TestLinkedTopicCompleteAlbumAndHiddenChain(t *testing.T) {
	scanComments := false
	opts := LinkOptions{MaxTopicMessages: 5, ScanComments: &scanComments}
	require.NoError(t, opts.Normalize())
	var offsets []int
	rootGets, proofs := 0, 0
	b := topicBackend(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.ChannelsGetMessagesRequest:
			if req.Channel.(*tg.InputChannel).ChannelID == 50 {
				rootGets++
				return linkedReply(&tg.MessagesChannelMessages{Messages: []tg.MessageClass{topicRoot()}}, out)
			}
			m := linkedDocument(20, 90, "fresh", []byte("lesson"))
			m.PeerID = &tg.PeerChannel{ChannelID: 60}
			return linkedReply(&tg.MessagesChannelMessages{Messages: []tg.MessageClass{m}}, out)
		case *tg.MessagesGetForumTopicsByIDRequest:
			proofs++
			require.Equal(t, []int{10}, req.Topics)
			return linkedReply(topicProof(true, 10), out)
		case *tg.MessagesGetRepliesRequest:
			offsets = append(offsets, req.OffsetID)
			require.Equal(t, 10, req.MsgID)
			require.Equal(t, 100, req.Limit)
			var page []tg.MessageClass
			switch req.OffsetID {
			case 0:
				m := topicDocument(104, 88, "fresh", []byte("album A"))
				m.SetGroupedID(77)
				link := topicReply(103)
				link.SetEntities([]tg.MessageEntityClass{&tg.MessageEntityTextURL{Offset: 0, Length: 6, URL: "https://t.me/resources_bot?start=lesson"}})
				page = []tg.MessageClass{m, link, topicRoot()}
			case 103:
				m := topicDocument(102, 89, "fresh", []byte("album B"))
				m.SetGroupedID(77)
				svc := &tg.MessageService{ID: 101, PeerID: &tg.PeerChannel{ChannelID: 50}, Action: &tg.MessageActionTopicEdit{}}
				svc.SetReplyTo(topicReply(101).ReplyTo)
				page = []tg.MessageClass{m, svc, topicRoot()} // repeated root is counted once.
			case 101:
			default:
				return fmt.Errorf("unexpected offset %d", req.OffsetID)
			}
			return linkedReply(&tg.MessagesMessagesSlice{Count: 5, Messages: page}, out)
		default:
			return fmt.Errorf("unexpected RPC %T: topic albums must not require getHistory/group lookups", in)
		}
	}, opts)
	backend := &topicBotBackend{telegramLinkBackend: b}
	resolver := &linkResolver{backend: backend, opts: opts}
	files, hops, err := resolver.Resolve(t.Context(), []resourceLink{{Kind: "message", Chat: "50", ID: 10}})
	require.NoError(t, err)
	var identities []string
	for _, f := range files {
		identities = append(identities, resourceIdentity(f.Media))
	}
	require.ElementsMatch(t, []string{"document_88", "document_89", "document_90"}, identities)
	require.Equal(t, []int{0, 103, 101}, offsets)
	require.Equal(t, 1, rootGets)
	require.Equal(t, 1, proofs)
	require.Equal(t, 1, backend.botCalls)
	require.Len(t, hops, 3)
	require.Equal(t, []int{102, 103, 104}, hops[0].MessageIDs)
}

func TestLinkedTopicRequiresProofAndPrimarySourceRejectsService(t *testing.T) {
	for _, mode := range []string{"primary source", "unsupported action", "wrong topic", "not forum", "wrong proof peer", "wrong proof topic peer", "deleted topic", "non-channel"} {
		t.Run(mode, func(t *testing.T) {
			replies := 0
			root := topicRoot()
			peer := tg.InputPeerClass(&tg.InputPeerChannel{ChannelID: 50})
			if mode == "unsupported action" {
				root.Action = &tg.MessageActionHistoryClear{}
			}
			if mode == "non-channel" {
				peer = &tg.InputPeerChat{ChatID: 50}
				root.PeerID = &tg.PeerChat{ChatID: 50}
			}
			b := topicBackend(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.ChannelsGetMessagesRequest, *tg.MessagesGetMessagesRequest:
					return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{root}}, out)
				case *tg.MessagesGetForumTopicsByIDRequest:
					proof := topicProof(mode != "not forum", 10)
					switch mode {
					case "wrong topic":
						proof.Topics[0].(*tg.ForumTopic).ID++
					case "wrong proof peer":
						proof.Chats[0].(*tg.Channel).ID++
					case "wrong proof topic peer":
						proof.Topics[0].(*tg.ForumTopic).Peer = &tg.PeerChannel{ChannelID: 51}
					case "deleted topic":
						proof.Topics = []tg.ForumTopicClass{&tg.ForumTopicDeleted{ID: 10}}
					}
					return linkedReply(proof, out)
				case *tg.MessagesGetRepliesRequest:
					replies++
					return linkedReply(&tg.MessagesMessages{}, out)
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			}, LinkOptions{})
			var err error
			if mode == "primary source" {
				_, err = b.messageAlbum(t.Context(), peer, 10, false)
			} else {
				_, err = b.resourceMessages(t.Context(), peer, 10, false, 0)
			}
			require.Error(t, err)
			require.NotContains(t, err.Error(), "unavailable or deleted")
			if mode == "primary source" || mode == "unsupported action" {
				require.ErrorContains(t, err, "unsupported service action")
			}
			require.Zero(t, replies)
		})
	}
}

func TestLinkedMessageValidatesTypedPeerAndPreservesDeletedClassification(t *testing.T) {
	for _, mode := range []string{"ordinary foreign", "service foreign", "same ID wrong kind", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			var raw tg.MessageClass
			switch mode {
			case "ordinary foreign":
				raw = &tg.Message{ID: 10, PeerID: &tg.PeerChannel{ChannelID: 51}}
			case "service foreign":
				m := topicRoot()
				m.PeerID = &tg.PeerChannel{ChannelID: 51}
				raw = m
			case "same ID wrong kind":
				raw = &tg.Message{ID: 10, PeerID: &tg.PeerUser{UserID: 50}}
			case "deleted":
				raw = &tg.MessageEmpty{ID: 10}
			}
			api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{raw}}, out)
			}))
			_, err := getLinkedMessage(t.Context(), api, &tg.InputPeerChannel{ChannelID: 50}, 10)
			if mode == "deleted" {
				require.ErrorContains(t, err, "unavailable or deleted")
			} else {
				require.ErrorContains(t, err, "peer mismatch")
			}
		})
	}
}

func TestLinkedTopicRejectsIncompleteOrForeignHistory(t *testing.T) {
	for _, mode := range []string{"bound service", "bound deleted", "foreign peer", "foreign thread", "duplicate page", "canceled", "canceled empty page"} {
		t.Run(mode, func(t *testing.T) {
			opts := LinkOptions{MaxTopicMessages: 2}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			b := topicBackend(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.ChannelsGetMessagesRequest:
					return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{topicRoot()}}, out)
				case *tg.MessagesGetForumTopicsByIDRequest:
					return linkedReply(topicProof(true, 10), out)
				case *tg.MessagesGetRepliesRequest:
					calls++
					m := topicDocument(12, 88, "fresh", []byte("lesson"))
					page := []tg.MessageClass{m}
					switch mode {
					case "bound service":
						svc := &tg.MessageService{ID: 11, PeerID: &tg.PeerChannel{ChannelID: 50}, Action: &tg.MessageActionTopicEdit{}}
						svc.SetReplyTo(topicReply(11).ReplyTo)
						page = append(page, svc)
					case "bound deleted":
						page = append(page, &tg.MessageEmpty{ID: 11})
					case "foreign peer":
						m.PeerID = &tg.PeerChannel{ChannelID: 51}
					case "foreign thread":
						m.ReplyTo.(*tg.MessageReplyHeader).SetReplyToTopID(11)
					case "canceled":
						cancel()
					case "canceled empty page":
						cancel()
						page = nil
					}
					return linkedReply(&tg.MessagesMessages{Messages: page}, out)
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			}, opts)
			msgs, err := b.resourceMessages(ctx, &tg.InputPeerChannel{ChannelID: 50}, 10, false, 0)
			require.Error(t, err)
			require.Nil(t, msgs, "a failed bounded scan must not return a partial archive")
			switch mode {
			case "bound service", "bound deleted":
				require.ErrorContains(t, err, "max_topic_messages")
			case "foreign peer":
				require.ErrorContains(t, err, "peer mismatch")
			case "foreign thread":
				require.ErrorContains(t, err, "does not belong")
			case "duplicate page":
				require.ErrorContains(t, err, "pagination did not advance")
				require.Equal(t, 2, calls)
			case "canceled", "canceled empty page":
				require.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}

func TestArchiveSelfPeerKeepsTypedMessageIdentity(t *testing.T) {
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetMessagesRequest); !ok {
			return fmt.Errorf("unexpected RPC %T", in)
		}
		return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 100}, Message: "Saved lesson notes"}}}, out)
	}))
	manager := peers.Options{}.Build(api)
	user := manager.User(&tg.User{ID: 100, Self: true, AccessHash: 500})
	require.IsType(t, &tg.InputPeerSelf{}, user.InputPeer())
	peer := archiveInputPeer(user)
	require.Equal(t, "user:100", archiveSource(peer))
	m, err := getLinkedMessage(t.Context(), api, peer, 7)
	require.NoError(t, err)
	require.Equal(t, 7, m.ID)
	require.ErrorContains(t, validateLinkedPeer(peer, &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 101}}), "peer mismatch")
}

func TestLinkedTopicExpiryReissuesFullChainAndRetainsParts(t *testing.T) {
	data := bytes.Repeat([]byte{42}, 2*downloader.MaxPartSize+127)
	opts := LinkedOptions{Threads: 1, Limit: 1}
	require.NoError(t, opts.Links.Normalize())
	chainGets, topicGets, proofs := 0, 0, 0
	var uploads []int64
	b := topicBackend(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.ChannelsGetMessagesRequest:
			id := req.ID[0].(*tg.InputMessageID).ID
			if req.Channel.(*tg.InputChannel).ChannelID == 60 {
				if id != 20 {
					return linkedReply(&tg.MessagesMessages{}, out) // follow-up series lookup
				}
				chainGets++
				return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 20, PeerID: &tg.PeerChannel{ChannelID: 60}, Message: "https://t.me/c/50/10"}}}, out)
			}
			if id == 10 {
				topicGets++
				return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{topicRoot()}}, out)
			}
			return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{&tg.MessageEmpty{ID: id}}}, out)
		case *tg.MessagesGetForumTopicsByIDRequest:
			proofs++
			return linkedReply(topicProof(true, 10), out)
		case *tg.MessagesGetRepliesRequest:
			var page []tg.MessageClass
			if req.OffsetID == 0 {
				id, ref := 21, "old"
				if topicGets > 1 {
					id, ref = 22, "fresh"
				}
				page = []tg.MessageClass{topicDocument(id, 99, ref, data)}
			}
			return linkedReply(&tg.MessagesMessages{Messages: page}, out)
		case *tg.UploadGetFileRequest:
			if string(req.Location.(*tg.InputDocumentFileLocation).FileReference) == "old" {
				return tgerr.New(400, "FILE_REFERENCE_EXPIRED")
			}
			uploads = append(uploads, req.Offset)
			end := min(int64(len(data)), req.Offset+int64(req.Limit))
			return linkedReply(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data[req.Offset:end]}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}, opts.Links)
	opts.Pool = linkedPool{b.api}
	resolver := &linkResolver{backend: b, opts: opts.Links}
	link := resourceLink{Kind: "message", Chat: "60", ID: 20}
	post := tagPost{ChatID: 1, MessageID: 42, Directory: "Lesson [42]"}
	dir := filepath.Join(t.TempDir(), post.Directory)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	md, ok := tmedia.GetMedia(topicDocument(21, 99, "old", data))
	require.True(t, ok)
	name, err := resourceFileName(md)
	require.NoError(t, err)
	path := filepath.Join(dir, name)
	file, parts, err := downloader.OpenPartialFile(path+".tmp", linkedMediaFile{md})
	require.NoError(t, err)
	_, err = file.WriteAt(data[:downloader.MaxPartSize], 0)
	require.NoError(t, err)
	parts.PartDone(0)
	require.NoError(t, parts.Flush())
	require.NoError(t, file.Close())
	require.NoError(t, archiveLinkedPost(t.Context(), filepath.Dir(dir), post, nil, nil, []resourceLink{link}, resolver, opts))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, got)
	require.Equal(t, 2, chainGets, "the source-to-topic chain is reissued after the old media message disappears")
	require.Equal(t, 2, topicGets)
	require.Equal(t, 2, proofs)
	require.NotContains(t, uploads, int64(0), "already committed partial bytes survive the complete chain refresh")
	require.NoFileExists(t, path+".tmp")
	require.NoFileExists(t, downloader.PartsPath(path+".tmp"))
}

func TestLinkedTopicLimitNormalizationAndFingerprint(t *testing.T) {
	opts := LinkedOptions{}
	require.NoError(t, opts.Links.Normalize())
	require.Equal(t, 1000, opts.Links.MaxTopicMessages)
	first := linkedFingerprint(nil, opts)
	opts.Links.MaxTopicMessages++
	require.NotEqual(t, first, linkedFingerprint(nil, opts))
	for _, limit := range []int{-1, 100001} {
		opts.Links.MaxTopicMessages = limit
		require.ErrorContains(t, opts.Links.Normalize(), "max_topic_messages")
	}
}

func TestLinkedTopicHintIsRetainedAndChecked(t *testing.T) {
	rootLink := resourceLink{Kind: "message", Chat: "50", ID: 42, TopicID: 42}
	roundtrip, err := parseResourceLink(rootLink.URL())
	require.NoError(t, err)
	require.Equal(t, rootLink.key(), roundtrip.key(), "an explicit hint equal to the root ID is retained too")
	for _, raw := range []string{"https://t.me/c/50/42/700", "https://t.me/c/50/700?thread=42", "https://t.me/lesson_group/42/700"} {
		link, err := parseResourceLink(raw)
		require.NoError(t, err)
		require.Equal(t, 42, link.TopicID)
		require.Equal(t, 700, link.ID)
		roundtrip, err := parseResourceLink(link.URL())
		require.NoError(t, err)
		require.Equal(t, link.key(), roundtrip.key())
		other := link
		other.TopicID++
		require.NotEqual(t, link.key(), other.key())
		b := topicBackend(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
			switch in.(type) {
			case *tg.ChannelsGetMessagesRequest:
				m := topicReply(700)
				m.ReplyTo.(*tg.MessageReplyHeader).SetReplyToTopID(43)
				return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{m}}, out)
			case *tg.MessagesGetForumTopicsByIDRequest:
				return linkedReply(topicProof(true, 42), out)
			default:
				return fmt.Errorf("unexpected RPC %T: a mismatched hint must not expand any history", in)
			}
		}, LinkOptions{})
		_, err = b.resourceMessages(t.Context(), &tg.InputPeerChannel{ChannelID: 50}, link.ID, link.Single, link.TopicID)
		require.ErrorContains(t, err, "does not belong to linked forum topic 42", raw)
	}
	rootGets := 0
	b := topicBackend(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.ChannelsGetMessagesRequest); !ok {
			return fmt.Errorf("unexpected RPC %T", in)
		}
		rootGets++
		return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{topicRoot()}}, out)
	}, LinkOptions{})
	_, err = b.Fetch(t.Context(), resourceLink{Kind: "message", Chat: "50", ID: 10, TopicID: 42})
	require.ErrorContains(t, err, "conflicts with linked forum topic 42")
	require.Equal(t, 1, rootGets)
}

func TestLinkedTopicMemberKeepsSingleAndAlbumScope(t *testing.T) {
	for _, mode := range []string{"single", "album", "album foreign member", "ordinary without hint"} {
		t.Run(mode, func(t *testing.T) {
			proofs, history := 0, 0
			b := topicBackend(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				member := func(id int) *tg.Message {
					m := topicDocument(id, int64(id), "fresh", []byte("lesson"))
					m.ReplyTo.(*tg.MessageReplyHeader).SetReplyToTopID(42)
					m.SetGroupedID(77)
					return m
				}
				switch in.(type) {
				case *tg.ChannelsGetMessagesRequest:
					return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{member(700)}}, out)
				case *tg.MessagesGetForumTopicsByIDRequest:
					proofs++
					return linkedReply(topicProof(true, 42), out)
				case *tg.MessagesGetHistoryRequest:
					history++
					var page []tg.MessageClass
					if history == 1 {
						other := member(701)
						if mode == "album foreign member" {
							other.ReplyTo.(*tg.MessageReplyHeader).SetReplyToTopID(43)
						}
						page = []tg.MessageClass{other, member(700)}
					}
					return linkedReply(&tg.MessagesMessagesSlice{Count: 2, Messages: page}, out)
				default:
					return fmt.Errorf("unexpected RPC %T: selecting a member must not expand its whole topic", in)
				}
			}, LinkOptions{})
			single := mode == "single" || mode == "ordinary without hint"
			topicID := 42
			if mode == "ordinary without hint" {
				topicID = 0
			}
			msgs, err := b.resourceMessages(t.Context(), &tg.InputPeerChannel{ChannelID: 50}, 700, single, topicID)
			if mode == "album foreign member" {
				require.ErrorContains(t, err, "album message 701 does not belong")
				require.Nil(t, msgs)
			} else {
				require.NoError(t, err)
				if single {
					require.Len(t, msgs, 1)
					require.Zero(t, history)
				} else {
					require.Len(t, msgs, 2)
				}
			}
			if topicID == 0 {
				require.Zero(t, proofs)
			} else {
				require.Equal(t, 1, proofs)
			}
		})
	}
}
