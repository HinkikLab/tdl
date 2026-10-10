package kv

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/session"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"

	"github.com/iyear/tdl/core/storage"
)

func forEachBoltStorage(t *testing.T, fn func(*testing.T, Storage, *legacyKV)) {
	t.Helper()
	for _, driver := range []Driver{DriverBolt, DriverLegacy} {
		t.Run(driver.String(), func(t *testing.T) {
			path := t.TempDir()
			if driver == DriverLegacy {
				path = filepath.Join(path, "test.db")
			}
			db, err := New(driver, map[string]any{"path": path})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			ns, err := db.Open("lifetime")
			require.NoError(t, err)
			fn(t, db, ns.(*legacyKV))
		})
	}
}

// Fail before accessing an unmapped slice if a future change reintroduces aliasing.
func requireOwnedBoltValue(t *testing.T, kv *legacyKV, key string, value []byte) {
	t.Helper()
	require.NotEmpty(t, value)
	require.NoError(t, kv.db.View(func(tx *bbolt.Tx) error {
		mapped := tx.Bucket(kv.ns).Get([]byte(key))
		require.NotEmpty(t, mapped)
		require.NotSame(t, &mapped[0], &value[0], "value must outlive the read transaction")
		return nil
	}))
}

func growBoltStorage(t *testing.T, kv *legacyKV) {
	t.Helper()
	// This exceeds the initial mapping, forcing Bolt to unmap and remap its file.
	require.NoError(t, kv.Set(context.Background(), "growth", bytes.Repeat([]byte("g"), 2<<20)))
}

func TestBoltGetSurvivesWritesAndClose(t *testing.T) {
	forEachBoltStorage(t, func(t *testing.T, db Storage, kv *legacyKV) {
		ctx := context.Background()
		expected := bytes.Repeat([]byte("original"), 1024)
		require.NoError(t, kv.Set(ctx, "value", expected))
		value, err := kv.Get(ctx, "value")
		require.NoError(t, err)
		requireOwnedBoltValue(t, kv, "value", value)

		// Callers can modify their result without changing the stored value.
		value[0] = 'x'
		stored, err := kv.Get(ctx, "value")
		require.NoError(t, err)
		require.Equal(t, expected, stored)
		value[0] = expected[0]

		require.NoError(t, kv.Set(ctx, "value", bytes.Repeat([]byte("replaced"), 1024)))
		growBoltStorage(t, kv)
		require.NoError(t, db.Close())
		require.Equal(t, expected, value)
	})
}

func TestBoltGetDistinguishesEmptyAndMissing(t *testing.T) {
	forEachBoltStorage(t, func(t *testing.T, _ Storage, kv *legacyKV) {
		ctx := context.Background()
		require.NoError(t, kv.Set(ctx, "empty", []byte{}))
		value, err := kv.Get(ctx, "empty")
		require.NoError(t, err)
		require.NotNil(t, value)
		require.Empty(t, value)

		value, err = kv.Get(ctx, "missing")
		require.ErrorIs(t, err, storage.ErrNotFound)
		require.Nil(t, value)
	})
}

func TestBoltMigrationSurvivesWritesAndClose(t *testing.T) {
	forEachBoltStorage(t, func(t *testing.T, db Storage, first *legacyKV) {
		ctx := context.Background()
		second, err := db.Open("second")
		require.NoError(t, err)
		namespaces := map[string]*legacyKV{"lifetime": first, "second": second.(*legacyKV)}
		expected := make(Meta)
		for name, kv := range namespaces {
			value := bytes.Repeat([]byte(name), 1024)
			expected[name] = map[string][]byte{"value": value, "empty": {}}
			require.NoError(t, kv.Set(ctx, "value", value))
			require.NoError(t, kv.Set(ctx, "empty", []byte{}))
		}

		meta, err := db.MigrateTo()
		require.NoError(t, err)
		for name, kv := range namespaces {
			requireOwnedBoltValue(t, kv, "value", meta[name]["value"])
			require.NoError(t, kv.Set(ctx, "value", []byte("replacement")))
			growBoltStorage(t, kv)
		}
		require.NoError(t, db.Close())
		require.Equal(t, expected, meta)
	})
}

type growingSessionKV struct {
	*legacyKV
	t *testing.T
}

func (kv growingSessionKV) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := kv.legacyKV.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	requireOwnedBoltValue(kv.t, kv.legacyKV, key, value)
	// Reproduce a write between KV transaction closure and gotd JSON decoding.
	growBoltStorage(kv.t, kv.legacyKV)
	return value, nil
}

func TestBoltSessionLoadSurvivesRemap(t *testing.T) {
	forEachBoltStorage(t, func(t *testing.T, _ Storage, kv *legacyKV) {
		ctx := context.Background()
		expected := &session.Data{
			DC:        2,
			Addr:      strings.Repeat("a", 4096),
			AuthKey:   bytes.Repeat([]byte{1}, 256),
			AuthKeyID: bytes.Repeat([]byte{2}, 8),
			Salt:      123456789,
		}
		loader := session.Loader{Storage: storage.NewSession(kv, false)}
		require.NoError(t, loader.Save(ctx, expected))
		loader.Storage = storage.NewSession(growingSessionKV{legacyKV: kv, t: t}, false)
		actual, err := loader.Load(ctx)
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	})
}
