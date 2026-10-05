package chat

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/gotd/td/telegram/peers"

	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/messages"
)

// TargetName describes a resolved chat and the selected scan scope.
// IDs remain visible so chats or topics with the same title are distinguishable.
func TargetName(peer peers.Peer, topicID int, topicTitle string, wholeChat bool) string {
	return TargetNameContext(context.Background(), peer, topicID, topicTitle, wholeChat)
}

func TargetNameContext(ctx context.Context, peer peers.Peer, topicID int, topicTitle string, wholeChat bool) string {
	clean := func(name string) string {
		name = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, name)
		return strings.Join(strings.Fields(name), " ")
	}
	name := clean(peer.VisibleName())
	if name == "" {
		name = console.Translate(ctx, messages.ChatUnnamed())
	}
	label := fmt.Sprintf("%s (%d)", name, peer.ID())
	if topicID > 0 {
		if title := clean(topicTitle); title != "" {
			return fmt.Sprintf("%s / %s (%d)", label, title, topicID)
		}
		return console.Translate(ctx, messages.ChatTargetTopic(label, topicID))
	}
	if channel, ok := peer.(peers.Channel); ok && channel.Raw().Forum && wholeChat {
		return console.Translate(ctx, messages.ChatTargetAllTopics(label))
	}
	return label
}

func announceTarget(ctx context.Context, label string, onResolved func(string)) {
	if onResolved != nil {
		onResolved(label)
		return
	}
	fmt.Println(console.Translate(ctx, messages.ChatScanning(label)))
}
