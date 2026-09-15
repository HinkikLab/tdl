package uploader

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	"golang.org/x/sync/errgroup"

	"github.com/iyear/tdl/core/util/fsutil"
	"github.com/iyear/tdl/core/util/mediautil"
)

// MaxPartSize refer to https://core.telegram.org/api/files#uploading-files
const MaxPartSize = 512 * 1024

type Uploader struct {
	opts     Options
	uploadFn func(context.Context, Elem) error
}

type Options struct {
	Client   *tg.Client
	Threads  int
	Iter     Iter
	Progress Progress
}

func New(o Options) *Uploader {
	return &Uploader{opts: o}
}

func (u *Uploader) Upload(ctx context.Context, limit int) error {
	if limit <= 0 {
		return errors.New("upload limit must be positive")
	}
	if u.opts.Client == nil {
		return errors.New("upload client is required")
	}
	if u.opts.Iter == nil {
		return errors.New("upload iterator is required")
	}
	if u.opts.Progress == nil {
		return errors.New("upload progress is required")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wg, wgctx := errgroup.WithContext(ctx)
	wg.SetLimit(limit)
	var failureMu sync.Mutex
	var failures error

	for u.opts.Iter.Next(wgctx) {
		elem := u.opts.Iter.Value()

		wg.Go(func() error {
			u.opts.Progress.OnAdd(elem)
			upload := u.upload
			if u.uploadFn != nil {
				upload = u.uploadFn
			}
			err := upload(wgctx, elem)
			u.opts.Progress.OnDone(elem, err)

			if err != nil {
				// canceled by user, so we directly return error to stop all
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return errors.Wrap(err, "upload")
				}

				// Keep processing independent files, but make the command fail after
				// every scheduled upload has settled.
				failureMu.Lock()
				failures = multierr.Append(failures, err)
				failureMu.Unlock()
			}

			return nil
		})
	}

	iterErr := u.opts.Iter.Err()
	if iterErr != nil {
		cancel()
	}
	err := wg.Wait()
	if iterErr != nil {
		err = multierr.Append(err, errors.Wrap(iterErr, "iter"))
	}
	failureMu.Lock()
	err = multierr.Append(err, failures)
	failureMu.Unlock()
	return err
}

func (u *Uploader) upload(ctx context.Context, elem Elem) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	up := uploader.NewUploader(u.opts.Client).
		WithPartSize(MaxPartSize).
		WithThreads(max(1, u.opts.Threads)).
		WithProgress(&wrapProcess{
			elem:    elem,
			process: u.opts.Progress,
		})

	f, err := up.Upload(ctx, uploader.NewUpload(elem.File().Name(), elem.File(), elem.File().Size()))
	if err != nil {
		return errors.Wrap(err, "upload file")
	}

	if _, err = elem.File().Seek(0, io.SeekStart); err != nil {
		return errors.Wrap(err, "seek file")
	}
	mime, err := mimetype.DetectReader(elem.File())
	if err != nil {
		return errors.Wrap(err, "detect mime")
	}

	// here convert underlying entities to formatters for message caption
	caption := styling.Custom(func(eb *entity.Builder) error {
		msg, entities := elem.Caption()
		eb.Format(msg, lo.Map(entities, func(item tg.MessageEntityClass, _ int) entity.Formatter {
			return func(_, _ int) tg.MessageEntityClass {
				return item
			}
		})...)
		return nil
	})

	doc := message.UploadedDocument(f, caption).MIME(mime.String()).Filename(elem.File().Name())
	// upload thumbnail TODO(iyear): maybe still unavailable
	if thumb, ok := elem.Thumb(); ok {
		if thumbFile, err := uploader.NewUploader(u.opts.Client).
			FromReader(ctx, thumb.Name(), thumb); err == nil {
			doc = doc.Thumb(thumbFile)
		}
	}

	var media message.MediaOption = doc

	switch {
	case mediautil.IsImage(mime.String()) && elem.AsPhoto():
		// webp should be uploaded as document
		if mime.String() == "image/webp" {
			break
		}
		// upload as photo
		media = message.UploadedPhoto(f, caption)
	case mediautil.IsVideo(mime.String()):
		// reset reader
		if _, err = elem.File().Seek(0, io.SeekStart); err != nil {
			return errors.Wrap(err, "seek file")
		}
		if dur, w, h, err := mediautil.GetMP4Info(elem.File()); err == nil {
			// #132. There may be some errors, but we can still upload the file
			media = doc.Video().
				Duration(time.Duration(dur)*time.Second).
				Resolution(w, h).
				SupportsStreaming()
		}
	case mediautil.IsAudio(mime.String()):
		media = doc.Audio().Title(fsutil.GetNameWithoutExt(elem.File().Name()))
	}

	_, err = message.NewSender(u.opts.Client).
		WithUploader(up).
		To(elem.To()).
		Reply(elem.Thread()).
		Media(ctx, media)
	if err != nil {
		return errors.Wrap(err, "send message")
	}

	return nil
}
