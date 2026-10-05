package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/klauspost/compress/zstd"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/kv"
	"github.com/iyear/tdl/pkg/messages"
)

func Recover(ctx context.Context, file string) (rerr error) {
	f, err := os.Open(file)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "open file"), corei18n.Message{ID: "errors.context.open_file", Args: map[string]any{"Reason": err}})
	}
	defer multierr.AppendInvoke(&rerr, multierr.Close(f))

	dec, err := zstd.NewReader(f)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "create zstd decoder"), corei18n.Message{ID: "errors.context.create_zstd_decoder", Args: map[string]any{"Reason": err}})
	}
	defer dec.Close()

	metaB := bytes.NewBuffer(nil)
	if _, err = dec.WriteTo(metaB); err != nil {
		return err
	}

	var meta kv.Meta
	if err = json.Unmarshal(metaB.Bytes(), &meta); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "unmarshal metadata"), corei18n.Message{ID: "errors.context.unmarshal_metadata", Args: map[string]any{"Reason": err}})
	}

	if err = kv.From(ctx).MigrateFrom(meta); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "migrate from"), corei18n.Message{ID: "errors.context.migrate_from", Args: map[string]any{"Reason": err}})
	}

	color.Green("%s", console.Translate(ctx, messages.RecoverSuccess(file)))
	return nil
}
