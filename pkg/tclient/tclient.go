package tclient

import (
	"context"
	"fmt"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/tclient"
	"github.com/iyear/tdl/pkg/key"
)

type Options struct {
	KV               storage.Storage
	Proxy            string
	NTP              string
	ReconnectTimeout time.Duration
	UpdateHandler    telegram.UpdateHandler
}

func GetApp(kv storage.Storage) (App, error) {
	mode, err := kv.Get(context.TODO(), key.App())
	if err != nil {
		mode = []byte(AppBuiltin)
	}
	app, ok := Apps[string(mode)]
	if !ok {
		return App{}, diagnostic.Describe(fmt.Errorf("can't find app: %s, please try re-login", mode), corei18n.Message{ID: "errors.message.can_t_find_app_value_please_try_re_login", Args: map[string]any{"Arg1": string(mode)}})
	}

	return app, nil
}

func New(ctx context.Context, o Options, login bool, middlewares ...telegram.Middleware) (*telegram.Client, error) {
	app, err := GetApp(o.KV)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "get app"), corei18n.Message{ID: "errors.context.get_app", Args: map[string]any{"Reason": err}})
	}

	return tclient.New(ctx, tclient.Options{
		AppID:            app.AppID,
		AppHash:          app.AppHash,
		Session:          storage.NewSession(o.KV, login),
		Middlewares:      middlewares,
		Proxy:            o.Proxy,
		NTP:              o.NTP,
		ReconnectTimeout: o.ReconnectTimeout,
		UpdateHandler:    o.UpdateHandler,
	})
}
