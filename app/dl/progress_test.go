package dl

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	pw "github.com/jedib0t/go-pretty/v6/progress"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/core/tmedia"
)

func TestConcurrentProgressKeepsSeparatePartStores(t *testing.T) {
	it := &iter{mu: &sync.Mutex{}, finished: make(map[int]struct{})}
	p := newProgress(pw.NewWriter(), it, Options{})
	dir := t.TempDir()
	makeElem := func(id int, name string) *iterElem {
		f, s, err := downloader.OpenPartial(filepath.Join(dir, name+tempExt), 2*downloader.MaxPartSize)
		require.NoError(t, err)
		return &iterElem{id: id, logicalPos: id, from: (&peers.Manager{}).Channel(&tg.Channel{ID: 123}), fromMsg: &tg.Message{ID: id}, file: &tmedia.Media{Size: 2 * downloader.MaxPartSize}, to: f, parts: s}
	}
	a := makeElem(1, "a")
	b := makeElem(2, "b")
	var wg sync.WaitGroup
	for _, e := range []*iterElem{a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); p.OnAdd(e) }()
	}
	wg.Wait()
	data := make([]byte, downloader.MaxPartSize)
	_, err := a.to.WriteAt(data, 0)
	require.NoError(t, err)
	_, err = b.to.WriteAt(data, downloader.MaxPartSize)
	require.NoError(t, err)
	for index, e := range []*iterElem{a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); p.PartDone(e, index); p.OnDone(e, context.Canceled) }()
	}
	wg.Wait()
	require.Empty(t, it.Finished())
	require.Equal(t, map[int]struct{}{0: {}}, downloader.NewPartsStore(a.to.Name(), a.Size()).Done())
	require.Equal(t, map[int]struct{}{1: {}}, downloader.NewPartsStore(b.to.Name(), b.Size()).Done())
	require.FileExists(t, a.to.Name())
	require.FileExists(t, b.to.Name())
	count := 0
	p.trackers.Range(func(_, _ any) bool { count++; return true })
	require.Zero(t, count)
}

func TestRenameFailureDoesNotMarkFinished(t *testing.T) {
	it := &iter{mu: &sync.Mutex{}, finished: make(map[int]struct{})}
	p := newProgress(pw.NewWriter(), it, Options{})
	path := filepath.Join(t.TempDir(), "blocked")
	require.NoError(t, os.Mkdir(path, 0755))
	f, s, err := downloader.OpenPartial(path+tempExt, 1)
	require.NoError(t, err)
	e := &iterElem{id: 1, logicalPos: 1, from: (&peers.Manager{}).Channel(&tg.Channel{ID: 123}), fromMsg: &tg.Message{ID: 1}, file: &tmedia.Media{Size: 1}, to: f, parts: s}
	_, err = f.Write([]byte{1})
	require.NoError(t, err)
	p.OnAdd(e)
	p.PartDone(e, 0)
	p.OnDone(e, nil)
	require.Empty(t, it.Finished())
	require.FileExists(t, path+tempExt)
}
