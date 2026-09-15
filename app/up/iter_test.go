package up

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/expr-lang/expr"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	pw "github.com/jedib0t/go-pretty/v6/progress"
	"github.com/stretchr/testify/require"
)

func TestResolveThumbRejectsNonImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thumb.txt")
	require.NoError(t, os.WriteFile(path, []byte("not an image"), 0o600))

	_, err := (&iter{}).resolveThumb(path)
	require.Error(t, err)
}

func TestNextClosesFileWhenDestinationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upload.bin")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))
	badDestination, err := expr.Compile("1")
	require.NoError(t, err)

	it := &iter{to: badDestination}
	_, err = it.next(context.Background(), &File{File: path})
	require.Error(t, err)

	// Windows refuses this rename while the file is still open, making the
	// assertion a direct regression check for the leaked descriptor.
	require.NoError(t, os.Rename(path, path+".moved"))
}

func TestNextDelayHonorsCancellation(t *testing.T) {
	it := &iter{
		files: []*File{{}, {}},
		cur:   1,
		delay: time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- it.Next(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case ok := <-done:
		require.False(t, ok)
		require.True(t, errors.Is(it.Err(), context.Canceled))
	case <-time.After(time.Second):
		t.Fatal("iterator ignored cancellation while waiting for delay")
	}
}

func TestUploadProgressCompletesAndReleasesTracker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upload.bin")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))
	f, err := os.Open(path)
	require.NoError(t, err)
	stat, err := f.Stat()
	require.NoError(t, err)

	writer := pw.NewWriter()
	progress := newProgress(writer)
	elem := &iterElem{
		file: &uploaderFile{File: f, size: stat.Size()},
		to:   (&peers.Manager{}).Channel(&tg.Channel{ID: 1}),
	}
	progress.OnAdd(elem)
	require.Equal(t, 1, writer.LengthActive())
	trackedValue, tracked := progress.trackers.Load(elem)
	require.True(t, tracked)
	tracker := trackedValue.(*pw.Tracker)
	progress.OnDone(elem, nil)
	require.True(t, tracker.IsDone())
	_, tracked = progress.trackers.Load(elem)
	require.False(t, tracked)
}
