package forwarder

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"go.uber.org/atomic"
	"go.uber.org/multierr"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tutil"
)

//go:generate go-enum --values --names --flag --nocase

// Mode
// ENUM(direct, clone)
type Mode int

type Options struct {
	Pool     dcpool.Pool
	Threads  int
	Iter     Iter
	Progress Progress
}

type Forwarder struct {
	sent map[tuple]struct{} // used to filter grouped messages which are already sent
	rand *rand.Rand
	opts Options
}

type tuple struct {
	from int64
	msg  int
}

func New(opts Options) *Forwarder {
	return &Forwarder{
		sent: make(map[tuple]struct{}),
		rand: rand.New(rand.NewSource(time.Now().UnixNano())),
		opts: opts,
	}
}

func (f *Forwarder) Forward(ctx context.Context) error {
	if f.opts.Pool == nil {
		return diagnostic.Describe(errors.New("forward pool is required"), corei18n.Message{ID: "errors.message.forward_pool_is_required"})
	}
	if f.opts.Iter == nil {
		return diagnostic.Describe(errors.New("forward iterator is required"), corei18n.Message{ID: "errors.message.forward_iterator_is_required"})
	}
	if f.opts.Progress == nil {
		return diagnostic.Describe(errors.New("forward progress is required"), corei18n.Message{ID: "errors.message.forward_progress_is_required"})
	}

	var failures error
	for f.opts.Iter.Next(ctx) {
		elem := f.opts.Iter.Value()
		if _, ok := f.sent[f.tuple(elem.From(), elem.Msg())]; ok {
			// skip grouped messages
			continue
		}

		if _, ok := elem.Msg().GetGroupedID(); ok && elem.AsGrouped() {
			grouped, err := tutil.GetGroupedMessages(ctx, f.opts.Pool.Default(ctx), elem.From().InputPeer(), elem.Msg())
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				logctx.From(ctx).Warn("Resolve grouped messages", zap.Error(err))
				failures = multierr.Append(failures, diagnostic.Describe(errors.Wrap(err, "resolve grouped messages"), corei18n.Message{ID: "errors.context.resolve_grouped_messages", Args: map[string]any{"Reason": err}}))
				continue
			}

			if err = f.forwardMessage(ctx, elem, grouped...); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				failures = multierr.Append(failures, err)
				continue
			}

			continue
		}

		if err := f.forwardMessage(ctx, elem); err != nil {
			// canceled by user, so we directly return error to stop all
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			failures = multierr.Append(failures, err)
			continue
		}
	}

	return multierr.Append(failures, f.opts.Iter.Err())
}

func (f *Forwarder) forwardMessage(ctx context.Context, elem Elem, grouped ...*tg.Message) (rerr error) {
	f.opts.Progress.OnAdd(elem)
	defer func() {
		if rerr == nil {
			f.sent[f.tuple(elem.From(), elem.Msg())] = struct{}{}

			// grouped message also should be marked as sent
			for _, m := range grouped {
				f.sent[f.tuple(elem.From(), m)] = struct{}{}
			}
		}
		f.opts.Progress.OnDone(elem, rerr)
	}()

	log := logctx.From(ctx).With(
		zap.Int64("from", elem.From().ID()),
		zap.Int64("to", elem.To().ID()),
		zap.Int("message", elem.Msg().ID))

	// used for clone progress
	totalSize, err := mediaSizeSum(elem.Msg(), grouped...)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "media total size"), corei18n.Message{ID: "errors.context.media_total_size", Args: map[string]any{"Reason": err}})
	}
	done := atomic.NewInt64(0)

	forwardTextOnly := func(msg *tg.Message) error {
		if msg.Message == "" {
			return diagnostic.Describe(errors.Errorf("empty message content, skip send: %d", msg.ID), corei18n.Message{ID: "errors.message.empty_message_content_skip_send_value", Args: map[string]any{"Arg1": msg.ID}})
		}
		req := &tg.MessagesSendMessageRequest{
			NoWebpage:              false,
			Silent:                 elem.AsSilent(),
			Background:             false,
			ClearDraft:             false,
			Noforwards:             false,
			UpdateStickersetsOrder: false,
			Peer:                   elem.To().InputPeer(),
			ReplyTo:                getReplyTo(elem.Thread()),
			Message:                msg.Message,
			RandomID:               f.rand.Int63(),
			ReplyMarkup:            msg.ReplyMarkup,
			Entities:               msg.Entities,
			ScheduleDate:           0,
			SendAs:                 nil,
		}
		req.SetFlags()

		if _, err := f.forwardClient(ctx, elem).MessagesSendMessage(ctx, req); err != nil {
			return diagnostic.Describe(errors.Wrap(err, "send message"), corei18n.Message{ID: "errors.context.send_message", Args: map[string]any{"Reason": err}})
		}
		return nil
	}

	convForwardedMedia := func(msg *tg.Message) (tg.InputMediaClass, error) {
		if _, hasMedia := msg.GetMedia(); !hasMedia {
			// media can't be forwarded via simple copy(it depends on the server ids)
			// if it's not a media message, just break and send text copy
			return nil, diagnostic.Describe(errors.Errorf("message %d is not a media message", msg.ID), corei18n.Message{ID: "errors.message.message_value_is_not_a_media_message", Args: map[string]any{"Arg1": msg.ID}})
		}

		// if it's a media message, but it's not protected, convert it to InputMediaClass
		// or if it's protected, but it doesn't contain photo or document,

		// we should clone photo and document via re-upload, it will be banned if we forward it directly.
		// but other media can be forwarded directly via copy
		if (!protectedDialog(elem.From()) && !protectedMessage(msg)) || !photoOrDocument(msg.Media) {
			media, ok := tmedia.ConvInputMedia(msg.Media)
			if !ok {
				return nil, diagnostic.Describe(errors.Errorf("can't convert message %d to input class directly", msg.ID), corei18n.Message{ID: "errors.message.can_t_convert_message_value_to_input_class_directly", Args: map[string]any{"Arg1": msg.ID}})
			}
			return media, nil
		}

		media, ok := tmedia.GetMedia(msg)
		if !ok {
			log.Warn("Can't get media from message",
				zap.Int64("peer", elem.From().ID()),
				zap.Int("message", msg.ID))

			// unsupported re-upload media
			return nil, diagnostic.Describe(errors.Errorf("unsupported media %T", msg.Media), corei18n.Message{ID: "errors.message.unsupported_media_value", Args: map[string]any{"Arg1": fmt.Sprintf("%T", msg.Media)}})
		}

		mediaFile, err := f.cloneMedia(ctx, cloneOptions{
			elem:  elem,
			media: media,
			progress: &wrapProgress{
				elem:     elem,
				progress: f.opts.Progress,
				done:     done,
				total:    totalSize * 2,
			},
		}, elem.AsDryRun())
		if err != nil {
			return nil, diagnostic.Describe(errors.Wrap(err, "clone media"), corei18n.Message{ID: "errors.context.clone_media", Args: map[string]any{"Reason": err}})
		}

		var inputMedia tg.InputMediaClass
		// now we only have to process cloned photo or document
		switch m := msg.Media.(type) {
		case *tg.MessageMediaPhoto:
			photo := &tg.InputMediaUploadedPhoto{
				Spoiler:    m.Spoiler,
				File:       mediaFile,
				TTLSeconds: m.TTLSeconds,
			}
			photo.SetFlags()

			inputMedia = photo
		case *tg.MessageMediaDocument:
			doc, ok := m.Document.AsNotEmpty()
			if !ok {
				return nil, diagnostic.Describe(errors.Errorf("empty document %d", msg.ID), corei18n.Message{ID: "errors.message.empty_document_value", Args: map[string]any{"Arg1": msg.ID}})
			}

			document := &tg.InputMediaUploadedDocument{
				NosoundVideo: false, // do not set
				ForceFile:    false, // do not set
				Spoiler:      m.Spoiler,
				File:         mediaFile,
				MimeType:     doc.MimeType,
				Attributes:   doc.Attributes,
				Stickers:     nil, // do not set
				TTLSeconds:   0,   // do not set
			}

			if thumb, ok := tmedia.GetDocumentThumb(doc); ok {
				thumbFile, err := f.cloneMedia(ctx, cloneOptions{
					elem:     elem,
					media:    thumb,
					progress: nopProgress{},
				}, elem.AsDryRun())
				if err != nil {
					return nil, diagnostic.Describe(errors.Wrap(err, "clone thumb"), corei18n.Message{ID: "errors.context.clone_thumb", Args: map[string]any{"Reason": err}})
				}

				document.Thumb = thumbFile
			}

			document.SetFlags()

			inputMedia = document
		default:
			return nil, diagnostic.Describe(errors.Errorf("unsupported media %T", msg.Media), corei18n.Message{ID: "errors.message.unsupported_media_value", Args: map[string]any{"Arg1": fmt.Sprintf("%T", msg.Media)}})
		}

		// note that they must be separately uploaded using messages uploadMedia first,
		// using raw inputMediaUploaded* constructors is not supported.
		messageMedia, err := f.forwardClient(ctx, elem).MessagesUploadMedia(ctx, &tg.MessagesUploadMediaRequest{
			Peer:  elem.To().InputPeer(),
			Media: inputMedia,
		})
		if err != nil {
			return nil, diagnostic.Describe(errors.Wrap(err, "upload media"), corei18n.Message{ID: "errors.context.upload_media", Args: map[string]any{"Reason": err}})
		}

		inputMedia, ok = tmedia.ConvInputMedia(messageMedia)
		if !ok && !elem.AsDryRun() {
			return nil, diagnostic.Describe(errors.Errorf("can't convert uploaded media to input class"), corei18n.Message{ID: "errors.message.can_t_convert_uploaded_media_to_input_class"})
		}

		return inputMedia, nil
	}

	switch elem.Mode() {
	case ModeDirect:
		// it can be forwarded via API
		if !protectedDialog(elem.From()) && !protectedMessage(elem.Msg()) {
			directForward := func(ids ...int) error {
				randIDs := make([]int64, 0, len(ids))
				for range ids {
					randIDs = append(randIDs, f.rand.Int63())
				}

				req := &tg.MessagesForwardMessagesRequest{
					Silent:            elem.AsSilent(),
					Background:        false,
					WithMyScore:       false,
					DropAuthor:        false,
					DropMediaCaptions: false,
					Noforwards:        false,
					FromPeer:          elem.From().InputPeer(),
					ID:                ids,
					RandomID:          randIDs,
					ToPeer:            elem.To().InputPeer(),
					TopMsgID:          elem.Thread(),
					ScheduleDate:      0,
					SendAs:            nil,
				}
				req.SetFlags()
				if _, err := f.forwardClient(ctx, elem).MessagesForwardMessages(ctx, req); err != nil {
					return diagnostic.Describe(errors.Wrap(err, "directly forward"), corei18n.Message{ID: "errors.context.directly_forward", Args: map[string]any{"Reason": err}})
				}
				return nil
			}

			if len(grouped) > 0 {
				ids := make([]int, 0, len(grouped))
				for _, m := range grouped {
					ids = append(ids, m.ID)
				}

				if err = directForward(ids...); err != nil {
					goto fallback
				}

				return nil
			}

			if err = directForward(elem.Msg().ID); err != nil {
				goto fallback
			}
			return nil
		}
	fallback:
		fallthrough
	case ModeClone:
		if len(grouped) > 0 {
			media := make([]tg.InputSingleMedia, 0, len(grouped))
			for _, gm := range grouped {
				m, err := convForwardedMedia(gm)
				if err != nil {
					log.Debug("Can't convert forwarded media", zap.Error(err))
					continue
				}

				single := tg.InputSingleMedia{
					Media:    m,
					RandomID: f.rand.Int63(),
					Message:  gm.Message,
					Entities: gm.Entities,
				}
				single.SetFlags()

				media = append(media, single)
			}

			if len(media) > 0 {
				req := &tg.MessagesSendMultiMediaRequest{
					Silent:                 elem.AsSilent(),
					Background:             false,
					ClearDraft:             false,
					Noforwards:             false,
					UpdateStickersetsOrder: false,
					Peer:                   elem.To().InputPeer(),
					ReplyTo:                getReplyTo(elem.Thread()),
					MultiMedia:             media,
					ScheduleDate:           0,
					SendAs:                 nil,
				}
				req.SetFlags()
				if _, err := f.forwardClient(ctx, elem).MessagesSendMultiMedia(ctx, req); err != nil {
					return diagnostic.Describe(errors.Wrap(err, "send multi media"), corei18n.Message{ID: "errors.context.send_multi_media", Args: map[string]any{"Reason": err}})
				}
				return nil
			}

			return forwardTextOnly(elem.Msg())
		}

		media, err := convForwardedMedia(elem.Msg())
		if err != nil {
			log.Debug("Can't convert forwarded media", zap.Error(err))
			return forwardTextOnly(elem.Msg())
		}
		// send text copy with forwarded media
		req := &tg.MessagesSendMediaRequest{
			Silent:                 elem.AsSilent(),
			Background:             false,
			ClearDraft:             false,
			Noforwards:             false,
			UpdateStickersetsOrder: false,
			Peer:                   elem.To().InputPeer(),
			ReplyTo:                getReplyTo(elem.Thread()),
			Media:                  media,
			Message:                elem.Msg().Message,
			RandomID:               rand.Int63(),
			ReplyMarkup:            elem.Msg().ReplyMarkup,
			Entities:               elem.Msg().Entities,
			ScheduleDate:           0,
			SendAs:                 nil,
		}
		req.SetFlags()

		if _, err := f.forwardClient(ctx, elem).MessagesSendMedia(ctx, req); err != nil {
			return diagnostic.Describe(errors.Wrap(err, "send single media"), corei18n.Message{ID: "errors.context.send_single_media", Args: map[string]any{"Reason": err}})
		}
		return nil
	}

	return func() error {
		messageArg1 := elem.Mode()
		return diagnostic.Describe(errors.Errorf("unsupported mode %v", messageArg1), corei18n.Message{ID: "errors.message.unsupported_mode_value", Args: map[string]any{"Arg1": messageArg1}})
	}()
}

func (f *Forwarder) tuple(peer peers.Peer, msg *tg.Message) tuple {
	return tuple{
		from: peer.ID(),
		msg:  msg.ID,
	}
}

type nopInvoker struct{}

func (n nopInvoker) Invoke(_ context.Context, _ bin.Encoder, _ bin.Decoder) error {
	return nil
}

type nopProgress struct{}

func (nopProgress) add(_ int64) {}

type wrapProgress struct {
	elem     Elem
	progress ProgressClone
	done     *atomic.Int64
	total    int64
}

func (w *wrapProgress) add(n int64) {
	w.progress.OnClone(w.elem, ProgressState{
		Done:  w.done.Add(n),
		Total: w.total,
	})
}

func (f *Forwarder) forwardClient(ctx context.Context, elem Elem) *tg.Client {
	if elem.AsDryRun() {
		return tg.NewClient(nopInvoker{})
	}

	return f.opts.Pool.Default(ctx)
}

func protectedDialog(peer peers.Peer) bool {
	switch p := peer.(type) {
	case peers.Chat:
		return p.Raw().GetNoforwards()
	case peers.Channel:
		return p.Raw().GetNoforwards()
	}

	return false
}

func protectedMessage(msg *tg.Message) bool {
	return msg.GetNoforwards()
}

func photoOrDocument(media tg.MessageMediaClass) bool {
	switch media.(type) {
	case *tg.MessageMediaPhoto, *tg.MessageMediaDocument:
		return true
	default:
		return false
	}
}

func mediaSizeSum(msg *tg.Message, grouped ...*tg.Message) (int64, error) {
	if len(grouped) > 0 {
		total := int64(0)
		for _, gm := range grouped {
			m, ok := tmedia.GetMedia(gm)
			if !ok {
				return 0, diagnostic.Describe(errors.Errorf("can't get media from message %d", gm.ID), corei18n.Message{ID: "errors.message.can_t_get_media_from_message_value", Args: map[string]any{"Arg1": gm.ID}})
			}
			total += m.Size
		}

		return total, nil
	}

	m, ok := tmedia.GetMedia(msg)
	if !ok { // maybe it's a text only message
		return 0, nil
	}

	return m.Size, nil
}

func getReplyTo(thread int) tg.InputReplyToClass {
	replyTo := &tg.InputReplyToMessage{
		ReplyToMsgID: thread,
	}
	replyTo.SetFlags()

	return replyTo
}
