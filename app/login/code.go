package login

import (
	"context"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
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

func Code(ctx context.Context) error {
	kvd, err := kv.From(ctx).Open(viper.GetString(consts.FlagNamespace))
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "open kv"), corei18n.Message{ID: "errors.context.open_kv", Args: map[string]any{"Reason": err}})
	}

	if err = kvd.Set(ctx, key.App(), []byte(tclient.AppDesktop)); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "set app"), corei18n.Message{ID: "errors.context.set_app", Args: map[string]any{"Reason": err}})
	}

	c, err := tclient.New(ctx, tclient.Options{
		KV:               kvd,
		Proxy:            viper.GetString(consts.FlagProxy),
		NTP:              viper.GetString(consts.FlagNTP),
		ReconnectTimeout: viper.GetDuration(consts.FlagReconnectTimeout),
		UpdateHandler:    nil,
	}, true)
	if err != nil {
		return err
	}

	return c.Run(ctx, func(ctx context.Context) error {
		if err = c.Ping(ctx); err != nil {
			return err
		}

		flow := auth.NewFlow(termAuth{}, auth.SendCodeOptions{})
		if err = c.Auth().IfNecessary(ctx, flow); err != nil {
			return err
		}

		user, err := c.Self(ctx)
		if err != nil {
			return err
		}

		color.Green("%s", console.Translate(ctx, messages.LoginSuccess(user.ID, user.Username)))

		return nil
	})
}

// noSignUp can be embedded to prevent signing up.
type noSignUp struct{}

func (c noSignUp) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, diagnostic.Describe(errors.New("don't support sign up Telegram account"), corei18n.Message{ID: "errors.message.don_t_support_sign_up_telegram_account"})
}

func (c noSignUp) AcceptTermsOfService(_ context.Context, tos tg.HelpTermsOfService) error {
	return &auth.SignUpRequired{TermsOfService: tos}
}

// termAuth implements authentication via terminal.
type termAuth struct {
	noSignUp
}

func (a termAuth) Phone(ctx context.Context) (string, error) {
	phone := ""
	prompt := &localizedprompt.Input{
		Message: console.Translate(ctx, messages.LoginPhonePrompt()),
		Default: "+86 12345678900",
	}

	if err := localizedprompt.AskOne(ctx, prompt, &phone, survey.WithValidator(localizedprompt.Required)); err != nil {
		return "", err
	}

	color.Blue("%s", console.Translate(ctx, messages.LoginSendCode()))
	return strings.TrimSpace(phone), nil
}

func (a termAuth) Password(ctx context.Context) (string, error) {
	pwd := ""
	prompt := &localizedprompt.Password{
		Message: console.Translate(ctx, messages.LoginPasswordPrompt()),
	}

	if err := localizedprompt.AskOne(ctx, prompt, &pwd, survey.WithValidator(localizedprompt.Required)); err != nil {
		return "", err
	}

	return strings.TrimSpace(pwd), nil
}

func (a termAuth) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	code := ""
	prompt := &localizedprompt.Input{
		Message: console.Translate(ctx, messages.LoginCodePrompt()),
	}

	if err := localizedprompt.AskOne(ctx, prompt, &code, survey.WithValidator(localizedprompt.Required)); err != nil {
		return "", err
	}

	return strings.TrimSpace(code), nil
}
