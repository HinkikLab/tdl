package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tgerr"

	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/messages"
)

// UnavailableLinks remembers dead targets for one batch run, across jobs and
// recursive hops. A later run probes them again so restored targets recover.
type UnavailableLinks struct {
	mu      sync.Mutex
	targets map[string]error
}

type unavailableResourceError struct {
	chat string
	err  error
}

func (e *unavailableResourceError) Error() string {
	return fmt.Sprintf("resource target %s unavailable: %s", e.chat, e.err)
}

func (e *unavailableResourceError) Unwrap() error { return e.err }

func (c *UnavailableLinks) lookup(l resourceLink) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.targets[strings.ToLower(l.Chat)]; err != nil {
		return err
	}
	if l.Kind == linkKindBot {
		return c.targets[botOnlyKey(l.Chat)]
	}
	return nil
}

// BOT_INVALID only says the target cannot serve bot requests. Keep it from
// hiding message links into the same chat.
func botOnlyKey(chat string) string { return "bot:" + strings.ToLower(chat) }

func (c *UnavailableLinks) remember(l resourceLink, err error) error {
	return c.rememberContext(context.Background(), l, err)
}

func (c *UnavailableLinks) rememberContext(ctx context.Context, l resourceLink, err error) error {
	var missingPeer *peers.PeerNotFoundError
	if !errors.As(err, &missingPeer) && !tgerr.Is(err, "USERNAME_INVALID", "USERNAME_NOT_OCCUPIED", "BOT_INVALID",
		"INPUT_USER_DEACTIVATED", "USER_DEACTIVATED", "CHANNEL_INVALID", "CHANNEL_PRIVATE") {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.targets == nil {
		c.targets = map[string]error{}
	}
	key := strings.ToLower(l.Chat)
	if tgerr.Is(err, "BOT_INVALID") {
		key = botOnlyKey(l.Chat)
	}
	if saved := c.targets[key]; saved != nil {
		return saved
	}
	missing := &unavailableResourceError{chat: l.Chat, err: err}
	c.targets[key] = missing
	fmt.Println(console.Translate(ctx, messages.LinkedUnavailableTarget(l.Chat, console.FormatError(err, corei18n.FromContext(ctx)))))
	return missing
}

func isUnavailableResource(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := err.(*unavailableResourceError); ok {
		return true
	}
	if group, ok := err.(interface{ Unwrap() []error }); ok {
		all := group.Unwrap()
		if len(all) == 0 {
			return false
		}
		for _, child := range all {
			if !isUnavailableResource(child) {
				return false
			}
		}
		return true
	}
	return isUnavailableResource(errors.Unwrap(err))
}
