package autodl

import (
	"context"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func TestIncrementalTopicQueriesRepliesAndPreservesRootBoundary(t *testing.T) {
	for _, test := range []struct {
		name     string
		rootDate int
		filter   string
		want     []int
	}{
		{"root inside", 100, "", []int{16, 17, 18}},
		{"root outside", 99, "", []int{17, 18}},
		{"filter root", 100, "ID >= 17", []int{17, 18}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rootCalls, repliesCalls := 0, 0
			message := func(id, date, reply int) *tg.Message {
				m := &tg.Message{ID: id, Date: date, PeerID: &tg.PeerChannel{ChannelID: 50}}
				m.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: int64(id), Size: 11, DCID: 2, MimeType: "video/mp4"}})
				if reply > 0 {
					m.SetReplyTo(&tg.MessageReplyHeader{ReplyToMsgID: reply})
				}
				return m
			}
			api := tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				switch req := in.(type) {
				case *tg.ChannelsGetMessagesRequest:
					rootCalls++
					require.Len(t, req.ID, 1)
					require.Equal(t, 16, req.ID[0].(*tg.InputMessageID).ID)
					return encodeResult(&tg.MessagesChannelMessages{Messages: []tg.MessageClass{message(16, test.rootDate, 0)}}, out)
				case *tg.MessagesGetRepliesRequest:
					repliesCalls++
					require.Equal(t, 16, req.MsgID)
					require.Equal(t, 151, req.OffsetDate)
					var messages []tg.MessageClass
					if req.OffsetID == 0 {
						messages = []tg.MessageClass{message(20, 200, 16), message(19, 101, 99), message(18, 101, 16), message(17, 100, 16), message(15, 99, 16)}
					}
					return encodeResult(&tg.MessagesChannelMessages{Count: len(messages), Messages: messages}, out)
				default:
					return fmt.Errorf("unexpected RPC %T; topic must not scan whole history", in)
				}
			}))
			manager, dialog := newIteratorTestChannel(t, api)
			r := &Runner{pool: batchPool{api}, manager: manager, opts: Options{CheckOnly: true}}
			job := &Job{TopicID: ptr(16), ExportFilter: test.filter}
			ids, err := r.collectIDs(context.Background(), job, dialog, 100, 150, t.TempDir())
			require.NoError(t, err)
			require.Equal(t, test.want, ids)
			require.Equal(t, 1, rootCalls)
			require.Equal(t, 1, repliesCalls)
		})
	}
}
