package netutil

import (
	"net/url"

	"github.com/go-faster/errors"
	"github.com/iyear/connectproxy"
	"golang.org/x/net/proxy"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

func init() {
	connectproxy.Register(&connectproxy.Config{
		InsecureSkipVerify: true,
	})
}

func NewProxy(proxyUrl string) (proxy.ContextDialer, error) {
	u, err := url.Parse(proxyUrl)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "parse proxy url"), corei18n.Message{ID: "errors.context.parse_proxy_url", Args: map[string]any{"Reason": err}})
	}
	dialer, err := proxy.FromURL(u, proxy.Direct)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "proxy from url"), corei18n.Message{ID: "errors.context.proxy_from_url", Args: map[string]any{"Reason": err}})
	}

	if d, ok := dialer.(proxy.ContextDialer); ok {
		return d, nil
	}

	return nil, diagnostic.Describe(errors.New("proxy dialer is not ContextDialer"), corei18n.Message{ID: "errors.message.proxy_dialer_is_not_contextdialer"})
}
