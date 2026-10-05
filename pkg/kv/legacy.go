package kv

import (
	"context"
	"os"
	"time"

	"github.com/go-faster/errors"
	"github.com/mitchellh/mapstructure"
	"go.etcd.io/bbolt"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/pkg/validator"
)

var boltOptions = &bbolt.Options{
	Timeout:      time.Second,
	NoGrowSync:   false,
	FreelistType: bbolt.FreelistArrayType,
}

func init() {
	register(DriverLegacy, func(m map[string]any) (Storage, error) {
		return newLegacy(m)
	})
}

func newLegacy(opts map[string]any) (*legacy, error) {
	type options struct {
		Path string `validate:"required" mapstructure:"path"`
	}

	var o options
	if err := mapstructure.WeakDecode(opts, &o); err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "decode options"), corei18n.Message{ID: "errors.context.decode_options", Args: map[string]any{"Reason": err}})
	}

	if err := validator.Struct(&o); err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "validate options"), corei18n.Message{ID: "errors.context.validate_options", Args: map[string]any{"Reason": err}})
	}

	db, err := bbolt.Open(o.Path, os.ModePerm, boltOptions)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "open db"), corei18n.Message{ID: "errors.context.open_db", Args: map[string]any{"Reason": err}})
	}

	return &legacy{bolt: db}, nil
}

type legacy struct {
	bolt *bbolt.DB
}

func (l *legacy) Name() string {
	return DriverLegacy.String()
}

func (l *legacy) MigrateTo() (Meta, error) {
	meta := make(Meta)

	if err := l.bolt.View(func(tx *bbolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bbolt.Bucket) error {
			ns := string(name)
			meta[ns] = make(map[string][]byte)
			return b.ForEach(func(k, v []byte) error {
				meta[ns][string(k)] = v
				return nil
			})
		})
	}); err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "iterate buckets"), corei18n.Message{ID: "errors.context.iterate_buckets", Args: map[string]any{"Reason": err}})
	}

	return meta, nil
}

func (l *legacy) MigrateFrom(meta Meta) error {
	return l.bolt.Update(func(tx *bbolt.Tx) error {
		for ns, pairs := range meta {
			b, err := tx.CreateBucketIfNotExists([]byte(ns))
			if err != nil {
				return diagnostic.Describe(errors.Wrap(err, "create bucket"), corei18n.Message{ID: "errors.context.create_bucket", Args: map[string]any{"Reason": err}})
			}
			for key, value := range pairs {
				if err = b.Put([]byte(key), value); err != nil {
					return diagnostic.Describe(errors.Wrap(err, "put"), corei18n.Message{ID: "errors.context.put", Args: map[string]any{"Reason": err}})
				}
			}
		}
		return nil
	})
}

func (l *legacy) Namespaces() ([]string, error) {
	namespaces := make([]string, 0)
	if err := l.bolt.View(func(tx *bbolt.Tx) error {
		return tx.ForEach(func(name []byte, _ *bbolt.Bucket) error {
			namespaces = append(namespaces, string(name))
			return nil
		})
	}); err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "iterate namespaces"), corei18n.Message{ID: "errors.context.iterate_namespaces", Args: map[string]any{"Reason": err}})
	}
	return namespaces, nil
}

func (l *legacy) Open(ns string) (storage.Storage, error) {
	return l.open(ns)
}

func (l *legacy) open(ns string) (*legacyKV, error) {
	if ns == "" {
		return nil, diagnostic.Describe(errors.New("namespace is required"), corei18n.Message{ID: "errors.message.namespace_is_required"})
	}
	if err := l.bolt.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(ns))
		return err
	}); err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "create bucket"), corei18n.Message{ID: "errors.context.create_bucket", Args: map[string]any{"Reason": err}})
	}

	return &legacyKV{db: l.bolt, ns: []byte(ns)}, nil
}

func (l *legacy) Close() error {
	return l.bolt.Close()
}

type legacyKV struct {
	db *bbolt.DB
	ns []byte
}

func (l *legacyKV) Get(_ context.Context, key string) ([]byte, error) {
	var val []byte

	if err := l.db.View(func(tx *bbolt.Tx) error {
		val = tx.Bucket(l.ns).Get([]byte(key))
		return nil
	}); err != nil {
		return nil, err
	}

	if val == nil {
		return nil, storage.ErrNotFound
	}
	return val, nil
}

func (l *legacyKV) Set(_ context.Context, key string, value []byte) error {
	return l.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(l.ns).Put([]byte(key), value)
	})
}

func (l *legacyKV) Delete(_ context.Context, key string) error {
	return l.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(l.ns).Delete([]byte(key))
	})
}
