package dl

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gabriel-vasile/mimetype"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/fsutil"
	"github.com/iyear/tdl/internal/transfer"
)

type iterElem struct {
	id         int // tracker id for progress tracking
	logicalPos int // logical position for resume/finished tracking

	from    peers.Peer
	fromMsg *tg.Message
	file    *tmedia.Media

	to    *os.File
	parts *downloader.PartsStore

	opts          Options
	finalPath     string
	resumeKey     string
	requestedPath string
	reservations  *transfer.Reservations
	finalizeOnce  sync.Once
	finalizeErr   error
	finalized     bool
}

func (i *iterElem) File() downloader.File { return i }

func (i *iterElem) To() io.WriterAt { return i.to }

func (i *iterElem) AsTakeout() bool { return i.opts.Takeout }

func (i *iterElem) Location() tg.InputFileLocationClass { return i.file.InputFileLoc }

func (i *iterElem) Name() string { return i.file.Name }

func (i *iterElem) Size() int64 { return i.file.Size }

func (i *iterElem) DC() int { return i.file.DC }

// Finalize owns payload commit. Progress only records and displays its verdict.
func (i *iterElem) Finalize(downloadErr error) error {
	i.finalizeOnce.Do(func() {
		i.finalized = true
		target := i.finalPath
		if target == "" {
			target = strings.TrimSuffix(i.to.Name(), tempExt)
		}
		var prepareErr error
		if downloadErr == nil && i.opts.RewriteExt {
			mime, err := mimetype.DetectFile(i.to.Name())
			if err != nil {
				prepareErr = err
			} else if ext := mime.Extension(); ext != "" && filepath.Ext(target) != ext {
				target = fsutil.GetNameWithoutExt(target) + ext
				if i.reservations != nil {
					target, prepareErr = i.reservations.Reserve(target, i.resumeKey)
				}
			}
		}
		i.finalizeErr = multierr.Combine(prepareErr, transfer.Commit(i.to, i.parts, i.Size(), target, i.file.Date, multierr.Combine(downloadErr, prepareErr)))
		if i.finalizeErr == nil && downloadErr == nil {
			i.finalPath = target
		}
	})
	return i.finalizeErr
}

func (i *iterElem) FileSource() (tg.InputPeerClass, int) {
	if i.from == nil || i.fromMsg == nil {
		return nil, 0
	}
	return i.from.InputPeer(), i.fromMsg.ID
}
