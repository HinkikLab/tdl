package kv

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-faster/errors"
	"github.com/mitchellh/mapstructure"

	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/pkg/validator"
)

func init() {
	register(DriverFile, newFile)
}

type file struct {
	path string
	mu   sync.Mutex
}

func newFile(opts map[string]any) (Storage, error) {
	type options struct {
		Path string `validate:"required" mapstructure:"path"`
	}

	var o options
	if err := mapstructure.WeakDecode(opts, &o); err != nil {
		return nil, errors.Wrap(err, "decode options")
	}

	if err := validator.Struct(&o); err != nil {
		return nil, errors.Wrap(err, "validate options")
	}

	_, err := os.Stat(o.Path)
	if err == nil {
		return &file{path: o.Path}, nil
	}

	if !os.IsNotExist(err) {
		return nil, errors.Wrap(err, "stat file")
	}

	if err = os.MkdirAll(filepath.Dir(o.Path), 0o755); err != nil {
		return nil, errors.Wrap(err, "create file directory")
	}
	if err = os.WriteFile(o.Path, []byte("{}"), 0o644); err != nil {
		return nil, errors.Wrap(err, "create file")
	}

	return &file{path: o.Path}, nil
}

func (f *file) Name() string {
	return DriverFile.String()
}

func (f *file) MigrateTo() (Meta, error) {
	meta, err := f.read()
	if err != nil {
		return nil, errors.Wrap(err, "read")
	}
	return meta, nil
}

func (f *file) MigrateFrom(meta Meta) error {
	return f.write(meta)
}

func (f *file) Namespaces() ([]string, error) {
	pairs, err := f.read()
	if err != nil {
		return nil, errors.Wrap(err, "read")
	}

	namespaces := make([]string, 0, len(pairs))
	for ns := range pairs {
		namespaces = append(namespaces, ns)
	}

	return namespaces, nil
}

func (f *file) Open(ns string) (storage.Storage, error) {
	if ns == "" {
		return nil, errors.New("namespace is required")
	}

	if err := f.update(func(data map[string]map[string][]byte) bool {
		if _, ok := data[ns]; !ok {
			data[ns] = make(map[string][]byte)
			return true
		}
		return false
	}); err != nil {
		return nil, errors.Wrap(err, "open namespace")
	}

	return &fileKV{f: f, ns: ns}, nil
}

func (f *file) Close() error {
	return nil
}

func (f *file) read() (map[string]map[string][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readLocked()
}

func (f *file) readLocked() (map[string]map[string][]byte, error) {
	bytes, err := os.ReadFile(f.path)
	if err != nil {
		return nil, err
	}

	m := make(map[string]map[string][]byte)
	if err = json.Unmarshal(bytes, &m); err != nil {
		return nil, err
	}

	return m, nil
}

func (f *file) write(m map[string]map[string][]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writeLocked(m)
}

func (f *file) writeLocked(m map[string]map[string][]byte) error {
	bytes, err := json.Marshal(m)
	if err != nil {
		return err
	}

	return os.WriteFile(f.path, bytes, 0o644)
}

func (f *file) update(fn func(map[string]map[string][]byte) bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	data, err := f.readLocked()
	if err != nil {
		return err
	}
	if !fn(data) {
		return nil
	}
	return f.writeLocked(data)
}

type fileKV struct {
	f  *file
	ns string
}

func (f *fileKV) Get(_ context.Context, key string) ([]byte, error) {
	m, err := f.f.read()
	if err != nil {
		return nil, errors.Wrap(err, "read")
	}

	if v, ok := m[f.ns][key]; ok {
		return v, nil
	}
	return nil, storage.ErrNotFound
}

func (f *fileKV) Set(_ context.Context, key string, value []byte) error {
	return f.f.update(func(data map[string]map[string][]byte) bool {
		if data[f.ns] == nil {
			data[f.ns] = make(map[string][]byte)
		}
		data[f.ns][key] = value
		return true
	})
}

func (f *fileKV) Delete(_ context.Context, key string) error {
	return f.f.update(func(data map[string]map[string][]byte) bool {
		if _, ok := data[f.ns][key]; !ok {
			return false
		}
		delete(data[f.ns], key)
		return true
	})
}
