package diagnostic

import (
	"context"
	"errors"
	"os"

	"github.com/gotd/td/tgerr"

	"github.com/iyear/tdl/core/i18n"
)

func formatExternal(err error, translator i18n.Translator) (string, bool) {
	if err == context.Canceled {
		return translator.Translate(i18n.Message{ID: "errors.context.canceled", Default: "context canceled"}), true
	}
	if err == context.DeadlineExceeded {
		return translator.Translate(i18n.Message{ID: "errors.context.deadline", Default: "context deadline exceeded"}), true
	}
	// Adapt actual external nodes, allowing wrappers to retain their context.
	if _, pathError := err.(*os.PathError); pathError || errors.Unwrap(err) == nil {
		for _, known := range []struct {
			kind error
			id   string
		}{
			{os.ErrNotExist, "errors.os.not_found"},
			{os.ErrPermission, "errors.os.permission"},
			{os.ErrExist, "errors.os.exists"},
		} {
			if errors.Is(err, known.kind) {
				return translator.Translate(i18n.Message{ID: known.id, Args: map[string]any{"Reason": err.Error()}}), true
			}
		}
	}
	rpc, ok := err.(*tgerr.Error)
	if !ok {
		return "", false
	}
	id := ""
	args := map[string]any(nil)
	switch rpc.Type {
	case tgerr.ErrFloodWait, tgerr.ErrPremiumFloodWait:
		id, args = "errors.telegram.flood_wait", map[string]any{"Seconds": rpc.Argument}
	case "CHANNEL_PRIVATE", "USER_BANNED_IN_CHANNEL":
		id = "errors.telegram.private_channel"
	case "CHAT_ADMIN_REQUIRED", "CHAT_WRITE_FORBIDDEN":
		id = "errors.telegram.admin_required"
	case "USERNAME_INVALID", "USERNAME_NOT_OCCUPIED", "BOT_INVALID":
		id = "errors.telegram.username_invalid"
	case "AUTH_KEY_UNREGISTERED", "SESSION_REVOKED", "SESSION_EXPIRED", "USER_DEACTIVATED":
		id = "errors.telegram.session_expired"
	default:
		return "", false
	}
	return translator.Translate(i18n.Message{ID: id, Args: args}), true
}
