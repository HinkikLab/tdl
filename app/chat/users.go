package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/go-faster/jx"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query/channels/participants"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/jedib0t/go-pretty/v6/progress"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/messages"
	"github.com/iyear/tdl/pkg/prog"
)

type UsersOptions struct {
	Chat   string
	Output string
	Raw    bool
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Phone     string `json:"phone"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

func Users(ctx context.Context, c *telegram.Client, kvd storage.Storage, opts UsersOptions) (rerr error) {
	manager := peers.Options{Storage: storage.NewPeers(kvd)}.Build(c.API())
	if opts.Chat == "" {
		return diagnostic.Describe(fmt.Errorf("missing domain id"), corei18n.Message{ID: "errors.message.missing_domain_id"})
	}

	peer, err := tutil.GetInputPeer(ctx, manager, opts.Chat)
	if err != nil {
		return diagnostic.Describe(fmt.Errorf("failed to get peer: %w", err), corei18n.Message{ID: "errors.message.failed_to_get_peer_value", Args: map[string]any{"Arg1": err}})
	}

	ch, ok := peer.(peers.Channel)
	if !ok {
		return diagnostic.Describe(fmt.Errorf("invalid type of chat. channels/groups are supported only"), corei18n.Message{ID: "errors.message.invalid_type_of_chat_channels_groups_are_supported_only"})
	}

	color.Cyan("%s", console.Translate(ctx, messages.ChatRateLimitNote()))
	fmt.Println()

	f, err := os.Create(opts.Output)
	if err != nil {
		return err
	}
	defer multierr.AppendInvoke(&rerr, multierr.Close(f))

	enc := jx.NewStreamingEncoder(f, 512)
	defer multierr.AppendInvoke(&rerr, multierr.Close(enc))

	enc.ObjStart()
	defer enc.ObjEnd()
	enc.Field("id", func(e *jx.Encoder) { e.Int64(peer.ID()) })

	pw := prog.NewContext(ctx, progress.FormatNumber)
	pw.SetUpdateFrequency(200 * time.Millisecond)
	pw.Style().Visibility.TrackerOverall = false
	pw.Style().Visibility.ETA = true
	pw.Style().Visibility.Percentage = true

	stopRender := prog.Start(pw)
	defer stopRender()

	builder := func() *participants.GetParticipantsQueryBuilder {
		return participants.NewQueryBuilder(c.API()).
			GetParticipants(ch.InputChannel()).
			BatchSize(100)
	}

	fields := map[string]*participants.GetParticipantsQueryBuilder{
		"users":  builder(),
		"admins": builder().Admins(),
		"kicked": builder().Kicked(""),
		"banned": builder().Banned(""),
		"bots":   builder().Bots(),
	}

	for field, query := range fields {
		iter := query.Iter()
		if err = outputUsers(ctx, pw, peer, enc, field, iter, opts.Raw); err != nil {
			// skip if we get CHAT_ADMIN_REQUIRED error, just export other fields
			if tgerr.Is(err, tg.ErrChatAdminRequired) {
				continue
			}
			return diagnostic.Describe(fmt.Errorf("failed to output %s: %w", field, err), corei18n.Message{ID: "errors.message.failed_to_output_value_value", Args: map[string]any{"Arg1": field, "Arg2": err}})
		}
	}

	return nil
}

func outputUsers(ctx context.Context,
	pw progress.Writer,
	peer peers.Peer,
	enc *jx.Encoder,
	field string,
	iter *participants.Iterator,
	raw bool,
) error {
	total, err := iter.Total(ctx)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "get total count"), corei18n.Message{ID: "errors.context.get_total_count", Args: map[string]any{"Reason": err}})
	}

	tracker := prog.AppendTracker(pw,
		progress.FormatNumber,
		fmt.Sprintf("%s-%d-%s", peer.VisibleName(), peer.ID(), field),
		int64(total))

	enc.FieldStart(field)
	enc.ArrStart()
	defer enc.ArrEnd()

	for iter.Next(ctx) {
		el := iter.Value()
		u, ok := el.User()
		if !ok {
			continue
		}

		var output any = u
		if !raw {
			output = convertTelegramUser(u)
		}

		buf, err := json.Marshal(output)
		if err != nil {
			return diagnostic.Describe(errors.Wrap(err, "marshal user"), corei18n.Message{ID: "errors.context.marshal_user", Args: map[string]any{"Reason": err}})
		}

		enc.Raw(buf)

		tracker.Increment(1)
	}

	if err = iter.Err(); err != nil {
		return err
	}

	tracker.MarkAsDone()
	return nil
}

func convertTelegramUser(u *tg.User) User {
	var dst User
	dst.ID = u.ID
	dst.Username = u.Username
	dst.Phone = u.Phone
	dst.FirstName = u.FirstName
	dst.LastName = u.LastName
	return dst
}
