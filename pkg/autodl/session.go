package autodl

import (
	"context"
	"strings"

	"github.com/go-faster/errors"

	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/storage/keygen"
	"github.com/iyear/tdl/pkg/kv"
)

// sessionKey is the storage key of the authorized telegram session. It matches
// core/storage.Session.
func sessionKey() string { return keygen.New("session") }

// LoggedIn reports whether the namespace already holds an authorized telegram
// session.
//
// This is a pure storage lookup, so it is safe to call before starting a
// client and it does not touch the network.
func LoggedIn(ctx context.Context, kv storage.Storage) bool {
	if kv == nil {
		return false
	}

	b, err := kv.Get(ctx, sessionKey())
	if err != nil {
		return false
	}

	return len(b) > 0
}

// AutoStart decides whether a bare `tdl` invocation should start the batch
// mode by itself.
//
// It returns the config path and the namespace it selected when the working
// directory holds a valid config.json and that namespace is logged in. Any
// other outcome means the caller should fall back to the regular behaviour
// (printing help).
func AutoStart(ctx context.Context, engine kv.Storage, namespace string) (string, string, error) {
	path, ok := FindConfig()
	if !ok {
		return "", "", nil
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		return "", "", errors.Wrapf(err, "config %s is invalid", path)
	}

	if ns := strings.TrimSpace(cfg.Namespace); ns != "" {
		namespace = ns
	}

	if engine == nil {
		return "", "", nil
	}

	ns, err := engine.Open(namespace)
	if err != nil {
		return "", "", nil // not logged in (or no namespace yet)
	}

	if !LoggedIn(ctx, ns) {
		return "", "", nil
	}

	return path, namespace, nil
}
