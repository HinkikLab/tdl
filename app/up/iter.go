package up

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/expr-lang/expr/vm"
	"github.com/gabriel-vasile/mimetype"
	"github.com/go-faster/errors"
	"github.com/go-viper/mapstructure/v2"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/peers"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/uploader"
	"github.com/iyear/tdl/core/util/mediautil"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/texpr"
)

type File struct {
	File  string
	Thumb string
}

type dest struct {
	Peer   string
	Thread int
}

type iter struct {
	files   []*File
	to      *vm.Program
	caption *vm.Program
	chat    string
	topic   int
	photo   bool
	remove  bool
	delay   time.Duration
	manager *peers.Manager

	cur  int
	err  error
	file uploader.Elem
}

func newIter(files []*File, to, caption *vm.Program, chat string, topic int, photo, remove bool, delay time.Duration, manager *peers.Manager) *iter {
	return &iter{
		files:   files,
		to:      to,
		caption: caption,
		chat:    chat,
		topic:   topic,
		photo:   photo,
		remove:  remove,
		delay:   delay,
		manager: manager,

		cur:  0,
		err:  nil,
		file: nil,
	}
}

func (i *iter) Next(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		i.err = ctx.Err()
		return false
	default:
	}

	if i.cur >= len(i.files) || i.err != nil {
		return false
	}

	// if delay is set, sleep for a while for each iteration
	if i.delay > 0 && i.cur > 0 { // skip first delay
		timer := time.NewTimer(i.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			i.err = ctx.Err()
			return false
		case <-timer.C:
		}
	}

	cur := i.files[i.cur]
	i.cur++

	file, err := i.next(ctx, cur)
	if err != nil {
		i.err = err
		return false
	}

	i.file = file
	return true
}

func (i *iter) next(ctx context.Context, cur *File) (_ *iterElem, rerr error) {
	file, err := i.resolveFile(cur.File)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "resolve file"), corei18n.Message{ID: "errors.context.resolve_file", Args: map[string]any{"Reason": err}})
	}
	defer func() {
		if rerr != nil {
			_ = file.Close()
		}
	}()

	env := exprEnv(ctx, cur)

	to, thread, err := i.resolveDest(ctx, env)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "resolve destination"), corei18n.Message{ID: "errors.context.resolve_destination", Args: map[string]any{"Reason": err}})
	}

	caption, err := i.resolveCaption(env)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "resolve caption"), corei18n.Message{ID: "errors.context.resolve_caption", Args: map[string]any{"Reason": err}})
	}

	thumb, err := i.resolveThumb(cur.Thumb)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "resolve thumbnail"), corei18n.Message{ID: "errors.context.resolve_thumbnail", Args: map[string]any{"Reason": err}})
	}

	return &iterElem{
		file:    file,
		thumb:   thumb,
		to:      to,
		caption: caption,
		thread:  thread,

		asPhoto: i.photo,
		remove:  i.remove,
	}, nil
}

func (i *iter) resolveFile(path string) (*uploaderFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "open file"), corei18n.Message{ID: "errors.context.open_file", Args: map[string]any{"Reason": err}})
	}

	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, diagnostic.Describe(errors.Wrap(err, "stat file"), corei18n.Message{ID: "errors.context.stat_file", Args: map[string]any{"Reason": err}})
	}

	return &uploaderFile{
		File: f,
		size: stat.Size(),
	}, nil
}

func (i *iter) resolveDest(ctx context.Context, env Env) (peers.Peer, int, error) {
	if i.chat != "" { // compatible with old version
		to, err := i.resolvePeer(ctx, i.chat)
		if err != nil {
			return nil, 0, diagnostic.Describe(errors.Wrap(err, "resolve chat"), corei18n.Message{ID: "errors.context.resolve_chat", Args: map[string]any{"Reason": err}})
		}

		return to, i.topic, nil
	}

	// message routing
	result, err := texpr.Run(i.to, env)
	if err != nil {
		return nil, 0, diagnostic.Describe(errors.Wrap(err, "parse expression"), corei18n.Message{ID: "errors.context.parse_expression", Args: map[string]any{"Reason": err}})
	}

	var (
		to     peers.Peer
		thread int
	)

	switch r := result.(type) {
	case string:
		// pure chat, no reply to, which is a compatible with old version
		// and a convenient way to send message to self
		to, err = i.resolvePeer(ctx, r)
	case map[string]interface{}:
		// chat with reply to topic or message
		var d dest

		if err = mapstructure.WeakDecode(r, &d); err != nil {
			return nil, 0, diagnostic.Describe(errors.Wrapf(err, "decode dest: %v", result), corei18n.Message{ID: "errors.context.decode_dest_value", Args: map[string]any{"Arg1": result, "Reason": err}})
		}

		to, err = i.resolvePeer(ctx, d.Peer)
		thread = d.Thread
	default:
		return nil, 0, diagnostic.Describe(errors.Errorf("message router must return string or dest: %T", result), corei18n.Message{ID: "errors.message.message_router_must_return_string_or_dest_value", Args: map[string]any{"Arg1": fmt.Sprintf("%T", result)}})
	}

	if err != nil {
		return nil, 0, diagnostic.Describe(errors.Wrap(err, "resolve peer"), corei18n.Message{ID: "errors.context.resolve_peer", Args: map[string]any{"Reason": err}})
	}

	return to, thread, nil
}

func (i *iter) resolvePeer(ctx context.Context, peer string) (peers.Peer, error) {
	if peer == "" { // self
		return i.manager.Self(ctx)
	}

	return tutil.GetInputPeer(ctx, i.manager, peer)
}

func (i *iter) resolveCaption(env Env) (*entity.Builder, error) {
	// parse caption
	captionStr, err := texpr.Run(i.caption, env)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "parse caption"), corei18n.Message{ID: "errors.context.parse_caption", Args: map[string]any{"Reason": err}})
	}

	r, ok := captionStr.(string)
	if !ok {
		return nil, diagnostic.Describe(errors.Errorf("caption must return string, got %T", captionStr), corei18n.Message{ID: "errors.message.caption_must_return_string_got_value", Args: map[string]any{"Arg1": fmt.Sprintf("%T", captionStr)}})
	}

	caption := &entity.Builder{}
	if len(r) > 0 {
		if err = html.HTML(strings.NewReader(r), caption, html.Options{
			UserResolver:          nil,
			DisableTelegramEscape: false,
		}); err != nil {
			return nil, diagnostic.Describe(errors.Wrap(err, "parse caption HTML"), corei18n.Message{ID: "errors.context.parse_caption_html", Args: map[string]any{"Reason": err}})
		}
	}

	return caption, nil
}

func (i *iter) resolveThumb(path string) (*uploaderFile, error) {
	if path == "" {
		return nil, nil
	}

	// has thumbnail
	mime, err := mimetype.DetectFile(path)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrapf(err, "detect thumbnail file: %v", path), corei18n.Message{ID: "errors.context.detect_thumbnail_file_value", Args: map[string]any{"Arg1": path, "Reason": err}})
	}
	if !mediautil.IsImage(mime.String()) { // TODO(iyear): jpg only
		return nil, diagnostic.Describe(errors.Errorf("invalid thumbnail file: %v", path), corei18n.Message{ID: "errors.message.invalid_thumbnail_file_value", Args: map[string]any{"Arg1": path}})
	}

	thumb, err := os.Open(path)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "open thumbnail file"), corei18n.Message{ID: "errors.context.open_thumbnail_file", Args: map[string]any{"Reason": err}})
	}

	return &uploaderFile{
		File: thumb,
		size: 0,
	}, nil
}

func (i *iter) Value() uploader.Elem {
	return i.file
}

func (i *iter) Err() error {
	return i.err
}
