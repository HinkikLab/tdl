package autodl

import (
	"io"
	"os"
	"path/filepath"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"

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

	to      *os.File
	takeout bool
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
func (e *elem) start(takeout bool) error {
	e.takeout = takeout

	if dir := filepath.Dir(e.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	temp := e.path + tempExt

	f, store, err := downloader.OpenPartial(temp, e.file.Size())
	if err != nil {
		return err
	}
	e.to = f

	e.store = store

	return nil
}

// closeFile closes the temp file.
func (e *elem) closeFile() error {
	if e.to == nil {
		return nil
	}

	flushErr := e.store.Flush()
	err := e.to.Close()
	e.to = nil
	if flushErr != nil {
		return flushErr
	}
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
func (e *elem) To() io.WriterAt { return e.to }

// AsTakeout implements downloader.Elem.
func (e *elem) AsTakeout() bool { return e.takeout }

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

	return stat.Mode().IsRegular() && stat.Size() > 0
}
