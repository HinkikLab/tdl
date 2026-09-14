package autodl

import (
	"io"
	"os"
	"path/filepath"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"go.uber.org/atomic"

	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/core/tmedia"
)

// tempExt is the extension of a file that is still being downloaded. It is the
// same one the regular tdl downloader uses, so both modes understand each
// other's leftovers.
const tempExt = ".tmp"

// elem is one message media that is being downloaded.
type elem struct {
	dialog peers.Peer
	msgID  int
	media  *tmedia.Media
	file   downloader.File
	date   int64

	// path is the final path without the temp extension.
	path string

	// store tracks the parts that already hit the disk.
	store *downloader.PartsStore

	to       *os.File
	takeout  bool
	writer   *elemWriter
	progress *jobProgress

	// written counts the bytes fetched during this run. It is only mutated by
	// the writer path of one element.
	written atomic.Int64
}

// newElem prepares the destination of a media item. The temp file is created
// lazily by start.
func newElem(peer peers.Peer, msgID int, file downloader.File, date int64, name string) *elem {
	return &elem{
		dialog: peer,
		msgID:  msgID,
		file:   file,
		date:   date,
		path:   name,
	}
}

// start creates the temp file and wires the write path up.
func (e *elem) start(progress *jobProgress, takeout bool) error {
	e.progress = progress
	e.takeout = takeout

	if dir := filepath.Dir(e.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	temp := e.path + tempExt

	f, err := os.Create(temp)
	if err != nil {
		return err
	}
	e.to = f

	e.store = downloader.NewPartsStore(temp, e.file.Size())

	// keep the partial file so the next run can resume from it
	ok := false
	if parts := e.store.Done(); len(parts) > 0 {
		if err = downloader.PreAllocate(f, e.file.Size()); err == nil {
			ok = true
		}
	}
	if !ok {
		e.store.Reset()
	}

	e.writer = &elemWriter{elem: e}

	return nil
}

// closeFile closes the temp file.
func (e *elem) closeFile() error {
	if e.to == nil {
		return nil
	}

	err := e.to.Close()
	e.to = nil
	return err
}

// finish closes the temp file and moves it to its final name.
func (e *elem) finish() error {
	if err := e.closeFile(); err != nil {
		return err
	}

	if err := os.Rename(e.path+tempExt, e.path); err != nil {
		return err
	}

	e.store.Remove()

	return nil
}

// cleanupFile drops the temp file and its sidecar.
func (e *elem) cleanupFile() {
	_ = e.closeFile()

	if e.store != nil {
		e.store.Reset()
	}
	_ = os.Remove(e.path + tempExt)
}

// File implements downloader.Elem.
func (e *elem) File() downloader.File { return e.file }

// To implements downloader.Elem.
func (e *elem) To() io.WriterAt { return e.writer }

// AsTakeout implements downloader.Elem.
func (e *elem) AsTakeout() bool { return e.takeout }

// elemWriter forwards writes to the temp file while feeding the progress bar
// and the parts sidecar.
type elemWriter struct {
	elem *elem
}

func (w *elemWriter) WriteAt(p []byte, off int64) (int, error) {
	n, err := w.elem.to.WriteAt(p, off)
	if err != nil {
		return n, err
	}

	if w.elem.store != nil {
		w.elem.store.PartDone(int(off / downloader.MaxPartSize))
	}

	w.elem.written.Add(int64(n))
	if w.elem.progress != nil {
		w.elem.progress.onWrite(w.elem)
	}

	return n, nil
}

// mediaFile adapts a telegram media item to downloader.File.
type mediaFile struct {
	media *tmedia.Media
	// dialogID and msgID are only used for debug logging.
	dialogID int64
	msgID    int
}

func (m mediaFile) Location() tg.InputFileLocationClass { return m.media.InputFileLoc }
func (m mediaFile) Size() int64                         { return m.media.Size }
func (m mediaFile) DC() int                             { return m.media.DC }

// isComplete reports whether the job directory already holds the final file of
// this media.
func isComplete(path string) bool {
	stat, err := os.Stat(path)
	if err != nil {
		return false
	}

	return stat.Size() > 0
}
