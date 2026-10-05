package migrate

import (
	"context"

	"github.com/fatih/color"
	"github.com/go-faster/errors"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/console"
	localizedprompt "github.com/iyear/tdl/pkg/console/prompt"
	"github.com/iyear/tdl/pkg/kv"
	"github.com/iyear/tdl/pkg/messages"
)

func Migrate(ctx context.Context, to map[string]string) error {
	var confirm bool
	if err := localizedprompt.AskOne(ctx, &localizedprompt.Confirm{
		Message: console.Translate(ctx, messages.MigrateOverwritePrompt()),
		Default: false,
	}, &confirm); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "confirm"), corei18n.Message{ID: "errors.context.confirm", Args: map[string]any{"Reason": err}})
	}
	if !confirm {
		return nil
	}

	meta, err := kv.From(ctx).MigrateTo()
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "read data"), corei18n.Message{ID: "errors.context.read_data", Args: map[string]any{"Reason": err}})
	}

	dest, err := kv.NewWithMap(to)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "create dest storage"), corei18n.Message{ID: "errors.context.create_dest_storage", Args: map[string]any{"Reason": err}})
	}

	if err = dest.MigrateFrom(meta); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "migrate from"), corei18n.Message{ID: "errors.context.migrate_from", Args: map[string]any{"Reason": err}})
	}

	color.Green("%s", console.Translate(ctx, messages.MigrateSuccess()))
	for ns := range meta {
		color.Green("%s", console.Translate(ctx, messages.MigrateNamespace(ns)))
	}
	return nil
}
