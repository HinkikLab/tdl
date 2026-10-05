package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
)

// Keep the real user identity when Saved Messages resolves to InputPeerSelf.
// This lets archives and message refresh validate the returned typed peer.
func archiveInputPeer(peer peers.Peer) tg.InputPeerClass {
	if user, ok := peer.(peers.User); ok && user.Self() {
		return &tg.InputPeerUser{UserID: user.Raw().ID, AccessHash: user.Raw().AccessHash}
	}
	return peer.InputPeer()
}

func archiveAccount(ctx context.Context, client *telegram.Client, scope string, verified bool) (string, error) {
	if verified && scope != "" {
		return scope, nil
	}
	user, err := client.Self(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve archive account identity: %w", err)
	}
	key := fmt.Sprintf("user:%d", user.ID)
	if scope != "" {
		key = scope + ":" + key
	}
	return key, nil
}

func archiveSource(peer tg.InputPeerClass) string {
	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		return fmt.Sprintf("channel:%d", p.ChannelID)
	case *tg.InputPeerChat:
		return fmt.Sprintf("chat:%d", p.ChatID)
	case *tg.InputPeerUser:
		return fmt.Sprintf("user:%d", p.UserID)
	default:
		return ""
	}
}

func sameArchiveOwner(saved, expected tagPost) bool {
	return saved.ChatID == expected.ChatID && saved.MessageID == expected.MessageID &&
		(saved.Source == "" || expected.Source == "" || saved.Source == expected.Source) &&
		(saved.Account == "" || expected.Account == "" || saved.Account == expected.Account)
}

func checkArchiveOwner(dir string, expected tagPost) error {
	private := false
	b, err := readArchiveMetadata(dir)
	if os.IsNotExist(err) {
		private = true
		b, err = os.ReadFile(filepath.Join(dir, tagCompletedName))
		if os.IsNotExist(err) {
			return nil
		}
	}
	if err != nil {
		return err
	}
	var saved tagPost
	if err := json.Unmarshal(b, &saved); err != nil {
		if private {
			return nil
		}
		return fmt.Errorf("read archive owner: %w", err)
	}
	if private && (saved.ChatID == 0 || saved.MessageID == 0) {
		return nil
	}
	if !sameArchiveOwner(saved, expected) {
		return fmt.Errorf("existing archive %q belongs to another source, account or post; preserved for recovery", dir)
	}
	return nil
}
