package login

import (
	"context"
	"fmt"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/skip2/go-qrcode"
	"github.com/spf13/viper"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/console"
	localizedprompt "github.com/iyear/tdl/pkg/console/prompt"
	"github.com/iyear/tdl/pkg/consts"
	"github.com/iyear/tdl/pkg/key"
	"github.com/iyear/tdl/pkg/kv"
	"github.com/iyear/tdl/pkg/messages"
	"github.com/iyear/tdl/pkg/tclient"
)

func QR(ctx context.Context) error {
	kvd, err := kv.From(ctx).Open(viper.GetString(consts.FlagNamespace))
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "open kv"), corei18n.Message{ID: "errors.context.open_kv", Args: map[string]any{"Reason": err}})
	}

	if err = kvd.Set(ctx, key.App(), []byte(tclient.AppDesktop)); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "set app"), corei18n.Message{ID: "errors.context.set_app", Args: map[string]any{"Reason": err}})
	}

	d := tg.NewUpdateDispatcher()

	c, err := tclient.New(ctx, tclient.Options{
		KV:               kvd,
		Proxy:            viper.GetString(consts.FlagProxy),
		NTP:              viper.GetString(consts.FlagNTP),
		ReconnectTimeout: viper.GetDuration(consts.FlagReconnectTimeout),
		UpdateHandler:    d,
	}, true)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "create client"), corei18n.Message{ID: "errors.context.create_client", Args: map[string]any{"Reason": err}})
	}

	return c.Run(ctx, func(ctx context.Context) error {
		color.Blue("%s", console.Translate(ctx, messages.LoginScanQR()))

		var lines int
		_, err = c.QR().Auth(ctx, qrlogin.OnLoginToken(d), func(ctx context.Context, token qrlogin.Token) error {
			qr, err := qrcode.New(token.URL(), qrcode.Medium)
			if err != nil {
				return diagnostic.Describe(errors.Wrap(err, "create qr"), corei18n.Message{ID: "errors.context.create_qr", Args: map[string]any{"Reason": err}})
			}
			code := qr.ToSmallString(false)
			lines = strings.Count(code, "\n")

			fmt.Print(code)
			fmt.Print(strings.Repeat(text.CursorUp.Sprint(), lines))
			return nil
		})

		// clear qrcode
		out := &strings.Builder{}
		for i := 0; i < lines; i++ {
			out.WriteString(text.EraseLine.Sprint())
			out.WriteString(text.CursorDown.Sprint())
		}
		out.WriteString(text.CursorUp.Sprintn(lines))
		fmt.Print(out.String())

		if err != nil {
			// https://core.telegram.org/api/auth#2fa
			if !tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
				return diagnostic.Describe(errors.Wrap(err, "qr auth"), corei18n.Message{ID: "errors.context.qr_auth", Args: map[string]any{"Reason": err}})
			}

			pwd := ""
			prompt := &localizedprompt.Password{
				Message: console.Translate(ctx, messages.LoginPasswordPrompt()),
			}

			if err = localizedprompt.AskOne(ctx, prompt, &pwd, survey.WithValidator(localizedprompt.Required)); err != nil {
				return diagnostic.Describe(errors.Wrap(err, "2fa password"), corei18n.Message{ID: "errors.context.2fa_password", Args: map[string]any{"Reason": err}})
			}

			if _, err = c.Auth().Password(ctx, pwd); err != nil {
				return diagnostic.Describe(errors.Wrap(err, "2fa auth"), corei18n.Message{ID: "errors.context.2fa_auth", Args: map[string]any{"Reason": err}})
			}
		}

		user, err := c.Self(ctx)
		if err != nil {
			return diagnostic.Describe(errors.Wrap(err, "get self"), corei18n.Message{ID: "errors.context.get_self", Args: map[string]any{"Reason": err}})
		}

		fmt.Print(text.EraseLine.Sprint())
		color.Green("%s", console.Translate(ctx, messages.LoginSuccess(user.ID, user.Username)))
		return nil
	})
}
