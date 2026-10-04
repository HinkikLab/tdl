package chat

import (
	"testing"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func TestTargetNameShowsChatTopicAndScope(t *testing.T) {
	manager := &peers.Manager{}
	forum := manager.Channel(&tg.Channel{ID: 2255983776, Title: "Example forum", Forum: true})
	require.Equal(t, "Example forum (2255983776) / all topics", TargetName(forum, 0, "", true))
	require.Equal(t, "Example forum (2255983776) / Photography (41872)", TargetName(forum, 41872, "Photography", false))
	require.Equal(t, "Example forum (2255983776) / topic 41872", TargetName(forum, 41872, "", false))
	require.Equal(t, "Example forum (2255983776)", TargetName(forum, 0, "", false))
	channel := manager.Channel(&tg.Channel{ID: 7, Title: "Example channel"})
	require.Equal(t, "Example channel (7)", TargetName(channel, 0, "", true))
	forum = manager.Channel(&tg.Channel{ID: 7, Title: "  Example\nforum\t ", Forum: true})
	require.Equal(t, "Example forum (7) / Some topic (42)", TargetName(forum, 42, "Some\r\ntopic", false))
	channel = manager.Channel(&tg.Channel{ID: 7})
	require.Equal(t, "Chat (7)", TargetName(channel, 0, "", true))
}
