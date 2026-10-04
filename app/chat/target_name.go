package chat

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/gotd/td/telegram/peers"
)

// TargetName describes a resolved chat and the selected scan scope.
// IDs remain visible so chats or topics with the same title are distinguishable.
func TargetName(peer peers.Peer, topicID int, topicTitle string, wholeChat bool) string {
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
		name = "Chat"
	}
	label := fmt.Sprintf("%s (%d)", name, peer.ID())
	if topicID > 0 {
		if title := clean(topicTitle); title != "" {
			return fmt.Sprintf("%s / %s (%d)", label, title, topicID)
		}
		return fmt.Sprintf("%s / topic %d", label, topicID)
	}
	if channel, ok := peer.(peers.Channel); ok && channel.Raw().Forum && wholeChat {
		return label + " / all topics"
	}
	return label
}

func announceTarget(label string, onResolved func(string)) {
	if onResolved != nil {
		onResolved(label)
		return
	}
	fmt.Printf("Scanning: %s\n", label)
}
