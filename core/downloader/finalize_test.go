package downloader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

type finalizingTestElem struct {
	*testElem
	finalize func(error) error
}

func (e *finalizingTestElem) Finalize(err error) error { return e.finalize(err) }

func TestFinalizationErrorReachesProgressAndCaller(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "file.tmp"))
	require.NoError(t, err)
	defer f.Close()
	expected := errors.New("final rename failed")
	finalized := false
	el := &finalizingTestElem{testElem: &testElem{f: f, size: 3, loc: &tg.InputDocumentFileLocation{ID: 42}}}
	el.finalize = func(err error) error {
		require.NoError(t, err)
		finalized = true
		return expected
	}
	var reported error
	p := callbackProgress{done: func(_ Elem, err error) {
		require.True(t, finalized, "file commit must run before completion notification")
		reported = err
	}}
	d := New(Options{Pool: testPool{tg.NewClient(&mockRPC{data: []byte("new")})}, Threads: 1, Iter: &singleIter{elem: el}, Progress: p})
	require.ErrorIs(t, d.Download(context.Background(), 1), expected)
	require.ErrorIs(t, reported, expected)
}

func TestFinalizationPreservesNetworkAndCancellationErrors(t *testing.T) {
	for _, networkErr := range []error{errors.New("network failed"), context.Canceled, context.DeadlineExceeded} {
		t.Run(networkErr.Error(), func(t *testing.T) {
			cleanupErr := errors.New("close failed")
			calls := 0
			el := &finalizingTestElem{testElem: &testElem{size: 3, loc: &tg.InputDocumentFileLocation{ID: 42}}}
			el.finalize = func(err error) error {
				calls++
				require.ErrorIs(t, err, networkErr)
				return cleanupErr
			}
			var reported error
			p := callbackProgress{done: func(_ Elem, err error) { reported = err }}
			api := tg.NewClient(rpcFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return networkErr }))
			err := New(Options{Pool: testPool{api}, Threads: 1, Iter: &singleIter{elem: el}, Progress: p}).Download(context.Background(), 1)
			require.ErrorIs(t, err, networkErr)
			require.ErrorIs(t, err, cleanupErr)
			require.ErrorIs(t, reported, networkErr)
			require.ErrorIs(t, reported, cleanupErr)
			require.Equal(t, 1, calls)
		})
	}
}
