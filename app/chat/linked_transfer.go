package chat

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/gotd/td/tg"
	pw "github.com/jedib0t/go-pretty/v6/progress"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/diagnostic"
	"github.com/iyear/tdl/core/downloader"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/internal/transfer"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/messages"
	"github.com/iyear/tdl/pkg/prog"
	"github.com/iyear/tdl/pkg/utils"
)

type linkedSession struct {
	mu       sync.Mutex
	resolver *linkResolver
	roots    []resourceLink
	files    []linkedResource
	hops     []linkHop
	requests int
}

func mediaReference(md *tmedia.Media) []byte {
	switch l := md.InputFileLoc.(type) {
	case *tg.InputDocumentFileLocation:
		return l.FileReference
	case *tg.InputPhotoFileLocation:
		return l.FileReference
	}
	return nil
}

func (s *linkedSession) refresh(ctx context.Context, old linkedResource) (*tmedia.Media, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := resourceIdentity(old.Media)
	find := func() *tmedia.Media {
		for _, f := range s.files {
			if resourceIdentity(f.Media) == key && f.Media.Size == old.Media.Size && f.Media.DC == old.Media.DC && !bytes.Equal(mediaReference(old.Media), mediaReference(f.Media)) {
				return f.Media
			}
		}
		return nil
	}
	if fresh := find(); fresh != nil {
		return fresh, nil
	}
	// Try the original message before reissuing the complete link chain.
	if backend, ok := s.resolver.backend.(*telegramLinkBackend); ok {
		if msg, err := getLinkedMessage(ctx, backend.api, old.Peer, old.Message.ID); err == nil {
			if md, ok := tmedia.GetMedia(msg); ok && resourceIdentity(md) == key && md.Size == old.Media.Size && md.DC == old.Media.DC && !bytes.Equal(mediaReference(old.Media), mediaReference(md)) {
				return md, nil
			}
		}
	}
	if s.requests >= *s.resolver.opts.ReRequestLimit {
		return nil, diagnostic.Describe(fmt.Errorf("resource rerequest_limit exhausted"), corei18n.Message{ID: "errors.message.resource_rerequest_key_limit_exhausted"})
	}
	s.requests++
	fmt.Println(console.Translate(ctx, messages.LinkedRerequest(s.requests, *s.resolver.opts.ReRequestLimit)))
	files, hops, err := s.resolver.Resolve(ctx, s.roots)
	if err != nil {
		return nil, err
	}
	if len(s.files) > 0 && !sameLinkedResources(s.files, files) {
		return nil, diagnostic.Describe(fmt.Errorf("resource set changed after reissuing the chain; rerun to archive the new set"), corei18n.Message{ID: "errors.message.resource_set_changed_after_reissuing_the_chain_rerun_to_archive_the_new_set"})
	}
	s.files, s.hops = files, hops
	if fresh := find(); fresh != nil {
		return fresh, nil
	}
	return nil, diagnostic.Describe(fmt.Errorf("reissued resources do not contain the same file with a fresh reference: %s", key), corei18n.Message{ID: "errors.archive.refreshed_file_missing", Args: map[string]any{"Arg1": key}})
}

func sameLinkedResources(a, b []linkedResource) bool {
	if len(a) != len(b) {
		return false
	}
	identities := map[string]string{}
	for _, f := range a {
		identities[resourceIdentity(f.Media)] = fmt.Sprintf("%d:%d", f.Media.Size, f.Media.DC)
	}
	for _, f := range b {
		if identities[resourceIdentity(f.Media)] != fmt.Sprintf("%d:%d", f.Media.Size, f.Media.DC) {
			return false
		}
	}
	return true
}

type linkedMediaFile struct{ media *tmedia.Media }

func (f linkedMediaFile) Location() tg.InputFileLocationClass { return f.media.InputFileLoc }
func (f linkedMediaFile) Size() int64                         { return f.media.Size }
func (f linkedMediaFile) DC() int                             { return f.media.DC }

type linkedElem struct {
	resource     linkedResource
	path         string
	session      *linkedSession
	file         *os.File
	parts        *downloader.PartsStore
	takeout      bool
	api          *tg.Client
	finalizeOnce sync.Once
	finalizeErr  error
	finalized    bool
	committed    bool
}

func (e *linkedElem) File() downloader.File { return linkedMediaFile{e.resource.Media} }
func (e *linkedElem) To() io.WriterAt       { return e.file }
func (e *linkedElem) AsTakeout() bool       { return e.takeout }
func (e *linkedElem) RefreshFile(ctx context.Context, current tg.InputFileLocationClass) (downloader.File, error) {
	if e.session == nil {
		message, err := getLinkedMessage(ctx, e.api, e.resource.Peer, e.resource.Message.ID)
		if err != nil {
			return nil, err
		}
		media, ok := tmedia.GetMedia(message)
		if !ok {
			return nil, diagnostic.Describe(fmt.Errorf("source message no longer contains media"), corei18n.Message{ID: "errors.message.source_message_no_longer_contains_media"})
		}
		return linkedMediaFile{media}, nil
	}
	old := e.resource
	media := *old.Media
	media.InputFileLoc = current
	old.Media = &media
	md, err := e.session.refresh(ctx, old)
	if err != nil {
		return nil, err
	}
	return linkedMediaFile{md}, nil
}

func (e *linkedElem) Finalize(downloadErr error) error {
	e.finalizeOnce.Do(func() {
		e.finalized = true
		e.finalizeErr = transfer.Commit(e.file, e.parts, e.File().Size(), e.path, int64(e.resource.Message.Date), downloadErr)
		e.committed = e.finalizeErr == nil && downloadErr == nil
	})
	return e.finalizeErr
}

type linkedIter struct {
	elems   []*linkedElem
	current *linkedElem
	err     error
	delay   *transfer.Delay
}

func (it *linkedIter) Next(ctx context.Context) bool {
	if it.err != nil || len(it.elems) == 0 {
		return false
	}
	if it.err = ctx.Err(); it.err != nil {
		return false
	}
	e := it.elems[0]
	it.elems = it.elems[1:]
	if it.delay != nil {
		if it.err = it.delay.Wait(ctx); it.err != nil {
			return false
		}
	}
	e.file, e.parts, it.err = downloader.OpenPartialFile(e.path+".tmp", e.File())
	if it.err != nil {
		return false
	}
	for _, path := range e.parts.RecoveryPaths() {
		fmt.Println(console.Translate(ctx, messages.LinkedPartial(path)))
	}
	it.current = e
	return true
}
func (it *linkedIter) Value() downloader.Elem { return it.current }
func (it *linkedIter) Err() error             { return it.err }

type linkedProgress struct {
	writer   pw.Writer
	mu       sync.Mutex
	trackers map[*linkedElem]*pw.Tracker
	err      error
	counts   *transfer.Counts
}

func (p *linkedProgress) OnAdd(elem downloader.Elem) {
	e := elem.(*linkedElem)
	t := prog.AppendTracker(p.writer, utils.Byte.FormatBinaryBytes, filepath.Base(e.path), e.File().Size())
	p.mu.Lock()
	p.trackers[e] = t
	p.mu.Unlock()
}

func (p *linkedProgress) OnDownload(elem downloader.Elem, s downloader.ProgressState) {
	p.mu.Lock()
	t := p.trackers[elem.(*linkedElem)]
	p.mu.Unlock()
	if t != nil {
		t.SetValue(s.Downloaded)
	}
}

func (p *linkedProgress) OnDone(elem downloader.Elem, err error) {
	e := elem.(*linkedElem)
	if !e.finalized {
		err = multierr.Combine(err, e.Finalize(err))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		if p.counts != nil {
			p.counts.FilesDownloaded++
			p.counts.Bytes += e.File().Size()
		}
		p.trackers[e].MarkAsDone()
	} else {
		if p.counts != nil {
			p.counts.FilesFailed++
		}
		p.trackers[e].MarkAsErrored()
		p.err = multierr.Append(p.err, err)
	}
}

func runArchiveMedia(ctx context.Context, pool dcpool.Pool, threads, limit int, elems []*linkedElem, delay *transfer.Delay, counts *transfer.Counts) error {
	if len(elems) == 0 {
		return ctx.Err()
	}
	w := prog.NewContext(ctx, utils.Byte.FormatBinaryBytes)
	progress := &linkedProgress{writer: w, trackers: map[*linkedElem]*pw.Tracker{}, counts: counts}
	it := &linkedIter{elems: elems, delay: delay}
	stop := prog.Start(w)
	defer stop()
	return downloader.New(downloader.Options{Pool: pool, Threads: threads, Iter: it, Progress: progress, SkipParts: true}).Download(ctx, max(1, limit))
}

func (p *linkedProgress) Resume(elem downloader.Elem) (map[int]struct{}, int64, bool) {
	e := elem.(*linkedElem)
	done := e.parts.Done()
	return done, e.File().Size(), len(done) > 0
}

func (p *linkedProgress) PartDone(elem downloader.Elem, index int) {
	elem.(*linkedElem).parts.PartDone(index)
}
func (p *linkedProgress) Reset(elem downloader.Elem) { elem.(*linkedElem).parts.Reset() }
