package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/expr-lang/expr"
	"github.com/fatih/color"
	"github.com/go-faster/jx"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	"github.com/jedib0t/go-pretty/v6/progress"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/console"
	uimessages "github.com/iyear/tdl/pkg/messages"
	"github.com/iyear/tdl/pkg/prog"
	"github.com/iyear/tdl/pkg/texpr"
)

//go:generate go-enum --names --values --flag --nocase

type ExportOptions struct {
	Type        ExportType
	Chat        string
	Thread      int // topic id in forum, message id in group
	Input       []int
	Output      string
	Filter      string
	OnlyMedia   bool
	WithContent bool
	Raw         bool
	All         bool
}

type Message struct {
	ID   int         `json:"id"`
	Type string      `json:"type"`
	File string      `json:"file"`
	Date int         `json:"date,omitempty"`
	Text string      `json:"text,omitempty"`
	Raw  *tg.Message `json:"raw,omitempty"`
}

// ExportType
// ENUM(time, id, last)
type ExportType int

func Export(ctx context.Context, c *telegram.Client, kvd storage.Storage, opts ExportOptions) (rerr error) {
	// only output available fields
	if opts.Filter == "-" {
		fg := texpr.NewFieldsGetter(nil)

		fields, err := fg.Walk(&texpr.EnvMessage{})
		if err != nil {
			return diagnostic.Describe(fmt.Errorf("failed to walk fields: %w", err), corei18n.Message{ID: "errors.message.failed_to_walk_fields_value", Args: map[string]any{"Arg1": err}})
		}

		fmt.Print(fg.SprintContext(ctx, fields, true))
		return nil
	}

	filter, err := expr.Compile(opts.Filter, expr.AsBool())
	if err != nil {
		return diagnostic.Describe(fmt.Errorf("failed to compile filter: %w", err), corei18n.Message{ID: "errors.message.failed_to_compile_filter_value", Args: map[string]any{"Arg1": err}})
	}

	var peer peers.Peer

	manager := peers.Options{Storage: storage.NewPeers(kvd)}.Build(c.API())
	if opts.Chat == "" { // defaults to me(saved messages)
		peer, err = manager.Self(ctx)
	} else {
		peer, err = tutil.GetInputPeer(ctx, manager, opts.Chat)
	}
	if err != nil {
		return diagnostic.Describe(fmt.Errorf("failed to get peer: %w", err), corei18n.Message{ID: "errors.message.failed_to_get_peer_value", Args: map[string]any{"Arg1": err}})
	}

	color.Yellow("%s", console.Translate(ctx, uimessages.ChatExportWarning()))
	color.Cyan("%s", console.Translate(ctx, uimessages.ChatRateLimitNote()))
	fmt.Println()

	color.Blue("%s", console.Translate(ctx, uimessages.ChatExportMode(opts.Type.String(), fmt.Sprint(opts.Input))))

	pw := prog.NewContext(ctx, progress.FormatNumber)
	pw.SetUpdateFrequency(200 * time.Millisecond)
	pw.Style().Visibility.TrackerOverall = false
	pw.Style().Visibility.ETA = false
	pw.Style().Visibility.Percentage = false

	tracker := prog.AppendTracker(pw, progress.FormatNumber, fmt.Sprintf("%s-%d", peer.VisibleName(), peer.ID()), 0)

	stopRender := prog.Start(pw)
	defer stopRender()

	var q messages.Query
	switch {
	case opts.Thread != 0: // topic messages, reply messages
		q = query.NewQuery(c.API()).Messages().GetReplies(peer.InputPeer()).MsgID(opts.Thread)
	default: // history
		q = query.NewQuery(c.API()).Messages().GetHistory(peer.InputPeer())
	}
	iter := messages.NewIterator(q, 100)

	switch opts.Type {
	case ExportTypeTime:
		iter = iter.OffsetDate(opts.Input[1] + 1)
	case ExportTypeId:
		iter = iter.OffsetID(opts.Input[1] + 1) // #89: retain the last msg id
	case ExportTypeLast:
	}

	f, err := os.Create(opts.Output)
	if err != nil {
		return err
	}
	defer multierr.AppendInvoke(&rerr, multierr.Close(f))

	enc := jx.NewStreamingEncoder(f, 512)
	defer multierr.AppendInvoke(&rerr, multierr.Close(enc))

	// process thread is reply type and peer is broadcast channel,
	// so we need to set discussion group id instead of broadcast id
	id := peer.ID()
	if p, ok := peer.(peers.Channel); opts.Thread != 0 && ok && p.IsBroadcast() {
		bc, _ := p.ToBroadcast()
		raw, err := bc.FullRaw(ctx)
		if err != nil {
			return diagnostic.Describe(fmt.Errorf("failed to get broadcast full raw: %w", err), corei18n.Message{ID: "errors.message.failed_to_get_broadcast_full_raw_value", Args: map[string]any{"Arg1": err}})
		}

		if id, ok = raw.GetLinkedChatID(); !ok {
			return diagnostic.Describe(fmt.Errorf("no linked group"), corei18n.Message{ID: "errors.message.no_linked_group"})
		}
	}

	enc.ObjStart()
	defer enc.ObjEnd()
	enc.Field("id", func(e *jx.Encoder) { e.Int64(id) })

	enc.FieldStart("messages")
	enc.ArrStart()
	defer enc.ArrEnd()

	count := int64(0)

loop:
	for iter.Next(ctx) {
		msg := iter.Value()
		switch opts.Type {
		case ExportTypeTime:
			if msg.Msg.GetDate() < opts.Input[0] {
				break loop
			}
		case ExportTypeId:
			if msg.Msg.GetID() < opts.Input[0] {
				break loop
			}
		case ExportTypeLast:
			if count >= int64(opts.Input[0]) {
				break loop
			}
		}

		m, ok := msg.Msg.(*tg.Message)
		if !ok {
			continue
		}
		// only get media messages
		media, ok := tmedia.GetMedia(m)
		if !ok && !opts.All {
			continue
		}

		b, err := texpr.Run(filter, texpr.ConvertEnvMessage(m))
		if err != nil {
			return diagnostic.Describe(fmt.Errorf("failed to run filter: %w", err), corei18n.Message{ID: "errors.message.failed_to_run_filter_value", Args: map[string]any{"Arg1": err}})
		}
		if !b.(bool) { // filtered
			continue
		}

		fileName := ""
		if media != nil { // #207
			fileName = media.Name
		}
		t := &Message{
			ID:   m.ID,
			Type: "message",
			File: fileName,
		}
		if opts.WithContent {
			t.Date = m.Date
			t.Text = m.Message
		}
		if opts.Raw {
			t.Raw = m
		}

		mb, err := json.Marshal(t)
		if err != nil {
			return diagnostic.Describe(fmt.Errorf("failed to marshal message: %w", err), corei18n.Message{ID: "errors.message.failed_to_marshal_message_value", Args: map[string]any{"Arg1": err}})
		}
		enc.Raw(mb)

		count++
		tracker.SetValue(count)
	}

	if err = iter.Err(); err != nil {
		return err
	}

	tracker.MarkAsDone()
	return nil
}
