package downloader

import (
	"context"
	"io"

	"github.com/gotd/td/tg"
)

type Iter interface {
	Next(ctx context.Context) bool
	Value() Elem
	Err() error
}

type Elem interface {
	File() File
	To() io.WriterAt

	AsTakeout() bool
}

// FinalizingElem optionally commits or closes a file after its download workers
// have settled. It returns local lifecycle errors; Download combines these with
// the network error before notifying progress or recording a successful result.
type FinalizingElem interface {
	Finalize(downloadErr error) error
}

type File interface {
	Location() tg.InputFileLocationClass
	Size() int64
	DC() int
}

// FileSource is an optional Elem extension identifying the message to fetch
// again when Telegram rejects an expired file reference.
type FileSource interface {
	FileSource() (peer tg.InputPeerClass, messageID int)
}

// FileRefresher lets an expiring source request the attachment again, for
// example through a bot deep link after its original message was deleted.
// The downloader validates file identity, size and DC before using it.
// The supplied location is the currently rejected reference, including any
// previous refresh, so repeated expirations can be handled safely.
type FileRefresher interface {
	RefreshFile(context.Context, tg.InputFileLocationClass) (File, error)
}
