package transfer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/downloader"
)

type testFile struct{ size int64 }

func (f testFile) Location() tg.InputFileLocationClass { return &tg.InputDocumentFileLocation{ID: 123} }
func (f testFile) Size() int64                         { return f.size }
func (testFile) DC() int                               { return 1 }

func openPayload(t *testing.T, target string) (*os.File, *downloader.PartsStore) {
	t.Helper()
	f, parts, err := downloader.OpenPartialFile(target+".tmp", testFile{7})
	require.NoError(t, err)
	_, err = f.WriteAt([]byte("payload"), 0)
	require.NoError(t, err)
	parts.PartDone(0)
	return f, parts
}

func TestCommitFailuresRemainRecoverable(t *testing.T) {
	for _, stage := range []string{"flush", "close", "rename", "length", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "media.bin")
			f, parts := openPayload(t, target)
			var networkErr error
			size := int64(7)
			switch stage {
			case "flush":
				require.NoError(t, os.Mkdir(downloader.PartsPath(f.Name())+".new", 0o755))
			case "close":
				require.NoError(t, f.Close())
			case "rename":
				require.NoError(t, os.Mkdir(target, 0o755))
			case "length":
				size = 8
			case "cancel":
				networkErr = context.Canceled
			}
			err := Commit(f, parts, size, target, 0, networkErr)
			if stage == "cancel" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			_, err = f.WriteAt([]byte("x"), 0)
			require.ErrorIs(t, err, os.ErrClosed, "all outcomes must close the file")
			payload, err := os.ReadFile(target + ".tmp")
			require.NoError(t, err)
			require.Equal(t, "payload", string(payload))
			if stage != "rename" {
				require.NoFileExists(t, target)
			}
			if stage != "flush" {
				require.FileExists(t, downloader.PartsPath(target+".tmp"))
			}
		})
	}
}

func TestCommitRenameCanRetry(t *testing.T) {
	target := filepath.Join(t.TempDir(), "media.bin")
	f, parts := openPayload(t, target)
	require.NoError(t, os.Mkdir(target, 0o755))
	require.Error(t, Commit(f, parts, 7, target, 0, nil))
	require.NoError(t, os.Remove(target))
	f, parts, err := downloader.OpenPartialFile(target+".tmp", testFile{7})
	require.NoError(t, err)
	require.Equal(t, map[int]struct{}{0: {}}, parts.Done())
	require.NoError(t, Commit(f, parts, 7, target, 0, nil))
	payload, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "payload", string(payload))
	require.NoFileExists(t, target+".tmp")
	require.NoFileExists(t, downloader.PartsPath(target+".tmp"))
}

func TestReservationsProtectPayloadAndSidecars(t *testing.T) {
	dir := t.TempDir()
	var reservations Reservations
	path := filepath.Join(dir, "media.bin")
	abs, err := reservations.Reserve(path, "first")
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(abs))
	for _, collision := range []string{path, path + ".tmp", path + ".tmp.parts", path + ".tmp.parts.new", filepath.Join(dir, ".", "media.bin")} {
		_, err = reservations.Reserve(collision, "second")
		require.ErrorContains(t, err, "first")
	}
	_, err = reservations.Reserve(filepath.Join(dir, "another.bin"), "second")
	require.NoError(t, err)
}

func TestReservationsAreAtomic(t *testing.T) {
	var reservations Reservations
	path := filepath.Join(t.TempDir(), "same.bin")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, owner := range []string{"one", "two"} {
		wg.Go(func() { _, err := reservations.Reserve(path, owner); results <- err })
	}
	wg.Wait()
	close(results)
	var successes int
	for err := range results {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes)
}

func TestStateCannotOverwriteMediaOrRecovery(t *testing.T) {
	for _, suffix := range []string{"", ".tmp", ".tmp.parts", ".tmp.parts.new"} {
		for _, stateFirst := range []bool{true, false} {
			t.Run(suffix+fmt.Sprint(stateFirst), func(t *testing.T) {
				var reservations Reservations
				target := filepath.Join(t.TempDir(), "media.bin")
				if stateFirst {
					require.NoError(t, reservations.ReserveState(target+suffix, "job state"))
					_, err := reservations.Reserve(target, "media")
					require.Error(t, err)
				} else {
					_, err := reservations.Reserve(target, "media")
					require.NoError(t, err)
					require.Error(t, reservations.ReserveState(target+suffix, "job state"))
				}
			})
		}
	}
}

func TestDelayHonorsCancellation(t *testing.T) {
	delay := &Delay{Duration: time.Hour}
	require.NoError(t, delay.Wait(context.Background()), "first file starts immediately")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() { result <- delay.Wait(ctx) }()
	select {
	case err := <-result:
		require.True(t, errors.Is(err, context.Canceled))
	case <-time.After(time.Second):
		t.Fatal("cancellation left the file-start delay blocked")
	}
}

func TestInternalStateReservationIsIdempotentForItsOwner(t *testing.T) {
	var reservations Reservations
	path := filepath.Join(t.TempDir(), ".tdl-completed.json")
	require.NoError(t, reservations.ReserveState(path, "post:1"))
	require.NoError(t, reservations.ReserveState(path, "post:1"))
	require.Error(t, reservations.ReserveState(path, "post:2"))
	_, err := reservations.Reserve(path+".new", "media")
	require.Error(t, err)
}
