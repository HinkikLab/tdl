package extension

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/jedib0t/go-pretty/v6/table"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/extensions"
	"github.com/iyear/tdl/pkg/messages"
)

var (
	colorPrint = func(attrs ...color.Attribute) func(ctx context.Context, padding int, message corei18n.Message) {
		return func(ctx context.Context, padding int, message corei18n.Message) {
			color.New(attrs...).Print(strings.Repeat("  ", padding) + "• ")
			fmt.Println(console.Translate(ctx, message))
		}
	}
	info = colorPrint(color.FgBlue, color.Bold)
	succ = colorPrint(color.FgGreen, color.Bold)
	fail = colorPrint(color.FgRed, color.Bold)
)

func List(ctx context.Context, em *extensions.Manager) error {
	exts, err := em.List(ctx, false)
	if err != nil {
		return diagnostic.Describe(errors.New("list extensions failed"), corei18n.Message{ID: "errors.message.list_extensions_failed"})
	}

	tb := table.NewWriter()

	style := table.StyleColoredDark
	tb.SetStyle(style)

	headers := strings.Split(console.Translate(ctx, messages.ExtensionTableHeaders()), "\t")
	header := make(table.Row, len(headers))
	for index, value := range headers {
		header[index] = value
	}
	tb.AppendHeader(header)
	for _, e := range exts {
		tb.AppendRow(table.Row{normalizeExtName(e.Name()), e.Owner(), e.CurrentVersion()})
	}

	fmt.Println(tb.Render())

	return nil
}

func Install(ctx context.Context, em *extensions.Manager, targets []string, force bool) error {
	for _, target := range targets {
		info(ctx, 0, messages.ExtensionInstalling(normalizeExtName(target)))

		if err := em.Install(ctx, target, force); err != nil {
			fail(ctx, 1, messages.ExtensionInstallFailed(normalizeExtName(target), console.FormatError(err, corei18n.FromContext(ctx))))
			continue
		}

		if em.DryRun() {
			succ(ctx, 1, messages.ExtensionWillInstall(normalizeExtName(target)))
		} else {
			succ(ctx, 1, messages.ExtensionInstalled(normalizeExtName(target)))
		}
	}

	return nil
}

func Upgrade(ctx context.Context, em *extensions.Manager, targets []string) error {
	upgradeAll := len(targets) == 0

	exts, err := em.List(ctx, upgradeAll)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "list extensions with metadata"), corei18n.Message{ID: "errors.context.list_extensions_with_metadata", Args: map[string]any{"Reason": err}})
	}
	if len(exts) == 0 {
		return diagnostic.Describe(errors.New("no extensions installed"), corei18n.Message{ID: "errors.message.no_extensions_installed"})
	}

	extMap := make(map[string]extensions.Extension)
	for _, e := range exts {
		extMap[e.Name()] = e
		if upgradeAll {
			targets = append(targets, e.Name())
		}
	}

	for _, target := range targets {
		e, ok := extMap[strings.TrimPrefix(target, extensions.Prefix)]
		if !ok {
			fail(ctx, 0, messages.ExtensionNotFound(normalizeExtName(target)))
			continue
		}

		info(ctx, 0, messages.ExtensionUpgrading(normalizeExtName(e.Name())))

		if err = em.Upgrade(ctx, e); err != nil {
			switch {
			case errors.Is(err, extensions.ErrAlreadyUpToDate):
				succ(ctx, 1, messages.ExtensionUpToDate(normalizeExtName(e.Name())))
			case errors.Is(err, extensions.ErrOnlyGitHub):
				fail(ctx, 1, messages.ExtensionCannotUpgrade(normalizeExtName(e.Name())))
			default:
				fail(ctx, 1, messages.ExtensionUpgradeFailed(normalizeExtName(e.Name()), console.FormatError(err, corei18n.FromContext(ctx))))
			}

			continue
		}

		if em.DryRun() {
			succ(ctx, 1, messages.ExtensionWillUpgrade(normalizeExtName(e.Name())))
		} else {
			succ(ctx, 1, messages.ExtensionUpgraded(normalizeExtName(e.Name())))
		}
	}

	return nil
}

func Remove(ctx context.Context, em *extensions.Manager, targets []string) error {
	exts, err := em.List(ctx, false)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "list extensions"), corei18n.Message{ID: "errors.context.list_extensions", Args: map[string]any{"Reason": err}})
	}

	extMap := make(map[string]extensions.Extension)
	for _, e := range exts {
		extMap[e.Name()] = e
	}

	for _, target := range targets {
		e, ok := extMap[strings.TrimPrefix(target, extensions.Prefix)]
		if !ok {
			fail(ctx, 0, messages.ExtensionNotFound(normalizeExtName(target)))
			continue
		}

		if err = em.Remove(e); err != nil {
			fail(ctx, 0, messages.ExtensionRemoveFailed(normalizeExtName(e.Name()), console.FormatError(err, corei18n.FromContext(ctx))))
			continue
		}

		if em.DryRun() {
			succ(ctx, 0, messages.ExtensionWillRemove(normalizeExtName(e.Name())))
		} else {
			succ(ctx, 0, messages.ExtensionRemoved(normalizeExtName(e.Name())))
		}
	}

	return nil
}

func normalizeExtName(n string) string {
	if idx := strings.IndexRune(n, '/'); idx >= 0 {
		n = n[idx+1:]
	}
	if !strings.HasPrefix(n, extensions.Prefix) {
		n = extensions.Prefix + n
	}
	n = strings.TrimSuffix(n, filepath.Ext(n))
	return color.New(color.Bold, color.FgCyan).Sprint(n)
}
