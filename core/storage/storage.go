package storage

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

type Storage interface {
	// Get returns an independent byte slice that remains valid after writes or Close.
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
}

var ErrNotFound = diagnostic.Describe(errors.New("key not found"), corei18n.Message{ID: "errors.message.key_not_found"})
