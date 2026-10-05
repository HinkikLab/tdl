package migrate

import (
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

func Backup(ctx context.Context, dst string) (rerr error) {
	meta, err := kv.From(ctx).MigrateTo()
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "read metadata"), corei18n.Message{ID: "errors.context.read_metadata", Args: map[string]any{"Reason": err}})
	}

	f, err := os.Create(dst)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "create file"), corei18n.Message{ID: "errors.context.create_file", Args: map[string]any{"Reason": err}})
	}
	defer multierr.AppendInvoke(&rerr, multierr.Close(f))

	enc, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "create zstd encoder"), corei18n.Message{ID: "errors.context.create_zstd_encoder", Args: map[string]any{"Reason": err}})
	}
	defer multierr.AppendInvoke(&rerr, multierr.Close(enc))

	metaB, err := json.Marshal(meta)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "marshal metadata"), corei18n.Message{ID: "errors.context.marshal_metadata", Args: map[string]any{"Reason": err}})
	}

	if _, err = enc.Write(metaB); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "write metadata"), corei18n.Message{ID: "errors.context.write_metadata", Args: map[string]any{"Reason": err}})
	}

	color.Green("%s", console.Translate(ctx, messages.BackupSuccess(dst)))
	return nil
}
