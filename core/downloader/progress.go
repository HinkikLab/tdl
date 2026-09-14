package downloader

import (
	"time"

	"go.uber.org/atomic"
)

type Progress interface {
	OnAdd(elem Elem)
	OnDownload(elem Elem, state ProgressState)
	OnDone(elem Elem, err error)
	// TODO: OnLog to log something that is not an error but should be sent to the user
}

// ProgressState carries the download progress of one element.
type ProgressState struct {
	Downloaded int64
	Total      int64

	// Resumed is the number of bytes already present on disk when the
	// download starts. It is zero unless the element was resumed from a
	// partially downloaded temp file by Resume.
	Resumed int64
	// Skipped is the number of bytes that did not need to be fetched because
	// they were already stored on disk.
	Skipped int64
}

// Resumer is an optional Progress extension.
//
// If a Progress implementation also implements Resumer, the built-in
// downloader will keep the partially downloaded temp file of a failed
// element and, on the next run, only fetch the parts that are still
// missing instead of starting the element from scratch.
//
// Implementations must be safe for concurrent use: progress callbacks are
// invoked from multiple download goroutines.
type Resumer interface {
	// Resume reports whether the element should be resumed from the partial
	// data already written to its temp file. elem.To() is the already opened
	// temp file when this is called.
	//
	// It returns the set of downloaded parts (keyed by part index), the
	// current size of the partial file and whether to resume.
	Resume(elem Elem) (parts map[int]struct{}, current int64, ok bool)
	// PartDone is called after a part has been written to disk. It must be
	// persisted by the implementation if it wants the part to survive a
	// restart.
	PartDone(elem Elem, index int)
	// Reset is called when the partial data must be discarded, e.g. the file
	// size changed on the server.
	Reset(elem Elem)
}

// writeAt wrapper for file to use progress bar
//
// do not need mutex because gotd has use syncio.WriteAt
type writeAt struct {
	elem     Elem
	progress Progress
	resumer  Resumer
	partSize int

	downloaded *atomic.Int64
}

func newWriteAt(elem Elem, progress Progress, resumer Resumer, partSize int) *writeAt {
	return &writeAt{
		elem:       elem,
		progress:   progress,
		resumer:    resumer,
		partSize:   partSize,
		downloaded: atomic.NewInt64(0),
	}
}

func (w *writeAt) WriteAt(p []byte, off int64) (int, error) {
	at, err := w.elem.To().WriteAt(p, off)
	if err != nil {
		return 0, err
	}

	// some small files may finish too fast, terminal history may not be overwritten
	// this is just a simple way to avoid the problem
	if at < w.partSize && at > 0 { // last part(every file only exec once)
		time.Sleep(time.Millisecond * 200) // to ensure the progress render next time
	}

	if w.resumer != nil {
		// mark the part as downloaded so a later interrupt can skip it
		w.resumer.PartDone(w.elem, int(off/int64(w.partSize)))
	}

	w.progress.OnDownload(w.elem, ProgressState{
		Downloaded: w.downloaded.Add(int64(at)),
		Total:      w.elem.File().Size(),
	})
	return at, nil
}
