package downloader

import (
	"bytes"
	"context"
	"sync"

	"github.com/go-faster/errors"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/multierr"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/tmedia"
)

// referenceInvoker is private to one download. Locations are replaced, never
// mutated, so requests already in flight can safely keep their old reference.
type referenceInvoker struct {
	next  tg.Invoker
	fetch func(context.Context) (tg.InputFileLocationClass, error)

	mu         sync.Mutex
	location   tg.InputFileLocationClass
	generation uint64
	refreshing chan struct{}
	refreshErr error
}

func (d *Downloader) client(ctx context.Context, elem Elem) *tg.Client {
	file := elem.File()
	client := d.opts.Pool.Client(ctx, file.DC())
	if elem.AsTakeout() {
		client = d.opts.Pool.Takeout(ctx, file.DC())
	}
	if refresher, ok := elem.(FileRefresher); ok {
		invoker := &referenceInvoker{next: client.Invoker(), location: file.Location()}
		invoker.fetch = func(ctx context.Context) (tg.InputFileLocationClass, error) {
			invoker.mu.Lock()
			current := invoker.location
			invoker.mu.Unlock()
			fresh, err := refresher.RefreshFile(ctx, current)
			if err != nil {
				return nil, err
			}
			if fresh == nil || !sameFileLocation(file.Location(), fresh.Location()) || file.Size() != fresh.Size() || file.DC() != fresh.DC() {
				return nil, errors.New("reissued attachment changed during download")
			}
			return fresh.Location(), nil
		}
		return tg.NewClient(invoker)
	}
	source, ok := elem.(FileSource)
	if !ok {
		return client
	}
	peer, id := source.FileSource()
	if peer == nil || id <= 0 {
		return client
	}
	return tg.NewClient(&referenceInvoker{
		next: client.Invoker(), location: file.Location(),
		fetch: func(ctx context.Context) (tg.InputFileLocationClass, error) {
			// Message metadata belongs to the account's main DC, even when
			// the file is downloaded through a different DC or takeout session.
			return refreshMessageFile(ctx, d.opts.Pool.Default(ctx), peer, id, file)
		},
	})
}

func (r *referenceInvoker) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	switch input.(type) {
	case *tg.UploadGetFileRequest, *tg.UploadGetFileHashesRequest:
	default:
		return r.next.Invoke(ctx, input, output)
	}
	for retries := 0; ; retries++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.mu.Lock()
		location, generation := r.location, r.generation
		r.mu.Unlock()
		// Keep the original offset, limit and flags when retrying a chunk.
		var request bin.Encoder
		switch in := input.(type) {
		case *tg.UploadGetFileRequest:
			copy := *in
			copy.Location = location
			request = &copy
		case *tg.UploadGetFileHashesRequest:
			copy := *in
			copy.Location = location
			request = &copy
		}
		err := r.next.Invoke(ctx, request, output)
		if !tgerr.Is(err, "FILE_REFERENCE_EXPIRED", "FILE_REFERENCE_INVALID") || retries >= 3 {
			return err
		}
		if refreshErr := r.refresh(ctx, generation); refreshErr != nil {
			return errors.Wrap(multierr.Append(err, refreshErr), "refresh file reference")
		}
	}
}

func (r *referenceInvoker) refresh(ctx context.Context, generation uint64) error {
	for {
		r.mu.Lock()
		if r.generation != generation {
			r.mu.Unlock()
			return nil // another worker already refreshed this reference
		}
		if r.refreshErr != nil {
			err := r.refreshErr
			r.mu.Unlock()
			return err
		}
		if done := r.refreshing; done != nil {
			r.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-done:
				continue
			}
		}
		done := make(chan struct{})
		r.refreshing = done
		old := r.location
		r.mu.Unlock()

		location, err := r.fetch(ctx)
		if err == nil && bytes.Equal(fileReference(old), fileReference(location)) {
			err = errors.New("source message returned the same file reference")
		}
		r.mu.Lock()
		if err == nil {
			r.location = location
			r.generation++
		} else {
			r.refreshErr = err
		}
		r.refreshing = nil
		close(done)
		r.mu.Unlock()
		return err
	}
}

func fileReference(location tg.InputFileLocationClass) []byte {
	switch loc := location.(type) {
	case *tg.InputDocumentFileLocation:
		return loc.FileReference
	case *tg.InputPhotoFileLocation:
		return loc.FileReference
	default:
		return nil
	}
}

func sameFileLocation(a, b tg.InputFileLocationClass) bool {
	switch a := a.(type) {
	case *tg.InputDocumentFileLocation:
		b, ok := b.(*tg.InputDocumentFileLocation)
		return ok && a.ID == b.ID && a.ThumbSize == b.ThumbSize
	case *tg.InputPhotoFileLocation:
		b, ok := b.(*tg.InputPhotoFileLocation)
		return ok && a.ID == b.ID && a.ThumbSize == b.ThumbSize
	default:
		return false
	}
}

func refreshMessageFile(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, id int, file File) (tg.InputFileLocationClass, error) {
	logctx.From(ctx).Info("Refresh expired file reference", zap.Int("message_id", id))
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: id}}
	var res tg.MessagesMessagesClass
	var err error
	if channel, ok := peer.(*tg.InputPeerChannel); ok {
		res, err = api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}, ID: ids,
		})
	} else {
		res, err = api.MessagesGetMessages(ctx, ids)
	}
	if err != nil {
		return nil, errors.Wrap(err, "get source message")
	}
	modified, ok := res.AsModified()
	if !ok {
		return nil, errors.Errorf("unexpected source messages type %T", res)
	}
	for _, msg := range modified.GetMessages() {
		if msg.GetID() != id {
			continue
		}
		media, ok := tmedia.GetMedia(msg)
		if !ok {
			return nil, errors.Errorf("source message %d no longer has media", id)
		}
		// Never combine already written bytes with a replacement attachment.
		if !sameFileLocation(file.Location(), media.InputFileLoc) || file.Size() != media.Size || file.DC() != media.DC {
			return nil, errors.Errorf("source message %d media changed during download", id)
		}
		return media.InputFileLoc, nil
	}
	return nil, errors.Errorf("source message %d is unavailable or deleted", id)
}
