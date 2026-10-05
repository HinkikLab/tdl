package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/flytam/filenamify"
	"github.com/gotd/td/tg"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/diagnostic"
	"github.com/iyear/tdl/core/downloader"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/internal/transfer"
)

// tagIndex is a visible export, not execution state. Only one album is kept in
// memory; the previous complete index is preserved if scanning/commit fails.
type tagIndex struct {
	file   *os.File
	target string
	first  bool
}

func newTagIndex(target string, id int64, r *transfer.Reservations) (*tagIndex, error) {
	target, err := r.Reserve(target, "tag-index:"+target)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".index-*.json")
	if err != nil {
		return nil, err
	}
	i := &tagIndex{file: f, target: target, first: true}
	// i18n:ignore Stable machine-readable export schema.
	if _, err := fmt.Fprintf(f, "{\"id\":%d,\"messages\":[", id); err != nil {
		_ = i.abort()
		return nil, err
	}
	return i, nil
}

func (i *tagIndex) append(m tagMedia) error {
	if !i.first {
		if _, err := i.file.WriteString(","); err != nil {
			return err
		}
	}
	i.first = false
	return json.NewEncoder(i.file).Encode(Message{ID: m.ID, Type: m.Type, File: m.File, Date: m.Date, Text: m.Text})
}

func (i *tagIndex) commit() error {
	if _, err := i.file.WriteString("]}\n"); err != nil {
		return err
	}
	if err := i.file.Close(); err != nil {
		return err
	}
	if err := os.Rename(i.file.Name(), i.target); err != nil {
		return err
	}
	i.file = nil
	return nil
}

func (i *tagIndex) abort() error {
	if i.file == nil {
		return nil
	}
	f := i.file
	i.file = nil
	return multierr.Combine(f.Close(), os.Remove(f.Name()))
}

func archiveTagPost(ctx context.Context, root string, post tagPost, album []*tg.Message, peer tg.InputPeerClass, opts TagOptions, delay *transfer.Delay) error {
	dir := filepath.Join(root, post.Directory)
	if err := migrateTagDirectoryContext(ctx, root, dir, post); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	completed, err := loadTagCompleted(ctx, dir, post, opts.Reservations)
	if err != nil {
		return err
	}
	if defaultOn(opts.WriteMetadata) {
		if _, err := opts.Reservations.Reserve(filepath.Join(dir, "meta.json"), fmt.Sprintf("tag-metadata:%s:%d", peer.TypeName(), post.MessageID)); err != nil {
			return err
		}
	}
	byID := make(map[int]*tg.Message, len(album))
	for _, m := range album {
		byID[m.ID] = m
	}
	var elems []*linkedElem
	for _, m := range post.Messages {
		if opts.counts != nil {
			opts.counts.Files++
		}
		message := byID[m.ID]
		md, ok := photoOrVideo(message)
		if !ok {
			return diagnostic.Describe(fmt.Errorf("discovered media %d is unavailable", m.ID), corei18n.Message{ID: "errors.message.discovered_media_value_is_unavailable", Args: map[string]any{"Arg1": m.ID}})
		}
		name, err := filenamify.FilenamifyV2(md.Name)
		if err != nil {
			return err
		}
		path, err := opts.Reservations.Reserve(filepath.Join(dir, strconv.Itoa(m.ID)+"_"+name), fmt.Sprintf("tag:%s:%d:%d:%s", peer.TypeName(), post.ChatID, m.ID, resourceIdentity(md)))
		if err != nil {
			return err
		}
		identity, err := downloader.FileIdentityOf(linkedMediaFile{md})
		if err != nil {
			return err
		}
		if completed.matches(m.ID, path, identity) {
			if opts.counts != nil {
				opts.counts.FilesExisting++
			}
			continue
		}
		if err := preserveArchiveFile(ctx, path, opts.Reservations); err != nil {
			return err
		}
		elems = append(elems, &linkedElem{resource: linkedResource{resourceMessage: resourceMessage{Peer: peer, Message: message}, Media: md}, path: path, takeout: opts.Takeout, api: opts.Pool.Default(ctx)})
	}
	downloadErr := runArchiveMedia(ctx, opts.Pool, opts.Threads, opts.Limit, elems, delay, opts.counts)
	for _, e := range elems {
		if e.committed {
			downloadErr = multierr.Append(downloadErr, completed.complete(e.resource.Message.ID, e.path, e.File()))
		}
	}
	if completed.dirty {
		downloadErr = multierr.Append(downloadErr, completed.save(dir))
	}
	if downloadErr != nil {
		return downloadErr
	}
	return writeArchiveMetadata(dir, post, opts.WriteMetadata)
}
