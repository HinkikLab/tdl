package login

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

//go:generate go-enum --values --names --flag --nocase

// Type
// ENUM(desktop, code, qr)
type Type int

type Options struct {
	Type     Type
	Desktop  string
	Passcode string
}

func Run(ctx context.Context, opts Options) error {
	switch opts.Type {
	case TypeDesktop:
		return Desktop(ctx, opts)
	case TypeCode:
		return Code(ctx)
	case TypeQr:
		return QR(ctx)
	default:
		return diagnostic.Describe(errors.Errorf("unsupported login type: %s", opts.Type), corei18n.Message{ID: "errors.message.unsupported_login_type_value", Args: map[string]any{"Arg1": opts.Type}})
	}
}
