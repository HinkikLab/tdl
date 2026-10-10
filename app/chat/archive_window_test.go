package chat

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func TestArchiveWindowKeepsBoundaryAlbumsAcrossPages(t *testing.T) {
	for _, window := range []ArchiveWindow{
		{StartID: 100, EndID: 101}, {Since: 1000, Until: 1000},
	} {
		t.Run(fmt.Sprint(window), func(t *testing.T) {
			pages := 0
			api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				req, ok := in.(*tg.MessagesGetHistoryRequest)
				require.True(t, ok)
				pages++
				var batch []tg.MessageClass
				switch pages {
				case 1:
					if window.EndID > 0 {
						require.Equal(t, 111, req.OffsetID)
					}
					// Fill a whole page so the album crosses pagination.
					for id := 202; id >= 103; id-- {
						m := &tg.Message{ID: id, Date: 1002, PeerID: &tg.PeerChannel{ChannelID: 1}}
						if id == 103 {
							m.SetGroupedID(77)
						}
						batch = append(batch, m)
					}
				case 2:
					require.Equal(t, 103, req.OffsetID)
					for id := 102; id >= 97; id-- {
						m := &tg.Message{ID: id, Date: 999, PeerID: &tg.PeerChannel{ChannelID: 1}}
						if id >= 99 {
							m.SetGroupedID(77)
						}
						if id == 100 {
							m.Date = 1000
						}
						batch = append(batch, m)
					}
				}
				return linkedReply(&tg.MessagesMessagesSlice{Count: 106, Messages: batch}, out)
			}))
			var albums [][]int
			complete, err := scanArchiveHistory(context.Background(), query.NewQuery(api).Messages().GetHistory(&tg.InputPeerChannel{ChannelID: 1}), window,
				func(album []*tg.Message) (bool, error) {
					var ids []int
					for _, m := range album {
						ids = append(ids, m.ID)
					}
					albums = append(albums, ids)
					return true, nil
				})
			require.NoError(t, err)
			require.True(t, complete)
			require.Equal(t, [][]int{{103, 102, 101, 100, 99}}, albums)
			require.Equal(t, 2, pages, "must stop once the lower boundary album is complete")
		})
	}
}

func TestArchiveWindowCompletionFailuresAndCancellation(t *testing.T) {
	for _, scenario := range []string{"limit", "failure", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			api := tg.NewClient(linkedRPC(func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
				return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{
					&tg.Message{ID: 2, Date: 1000, PeerID: &tg.PeerChannel{ChannelID: 1}},
					&tg.Message{ID: 1, Date: 900, PeerID: &tg.PeerChannel{ChannelID: 1}},
				}}, out)
			}))
			failure := errors.New("download failed")
			complete, err := scanArchiveHistory(ctx, query.NewQuery(api).Messages().GetHistory(&tg.InputPeerChannel{ChannelID: 1}), ArchiveWindow{Since: 950, Until: 1000},
				func([]*tg.Message) (bool, error) {
					if scenario == "failure" {
						return false, failure
					}
					if scenario == "cancel" {
						cancel()
						return true, nil
					}
					return false, nil
				})
			switch scenario {
			case "failure":
				require.ErrorIs(t, err, failure)
			case "cancel":
				require.ErrorIs(t, err, context.Canceled)
			default:
				require.NoError(t, err)
				require.False(t, complete)
			}
		})
	}
}
