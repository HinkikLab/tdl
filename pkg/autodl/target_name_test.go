package autodl

import (
	"context"
	"testing"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func TestRangeJobReportsResolvedChatNameAndIgnoresUnusedTopicSelector(t *testing.T) {
	peer := (&peers.Manager{}).Channel(&tg.Channel{ID: 2255983776, Title: "Example forum", Forum: true})
	start, end := 1, 2
	topic := 41872
	job := &Job{ChatURL: "https://t.me/c/2255983776/", StartComment: &start, EndComment: &end, TopicID: &topic, dir: t.TempDir()}
	runner := &Runner{cfg: &Config{}, opts: Options{CheckOnly: true}, dialogs: map[string]peers.Peer{"false:2255983776": peer}}
	var targets []string
	require.NoError(t, runner.runJob(context.Background(), job, 1, 1, func(target string) { targets = append(targets, target) }))
	require.Equal(t, []string{"Example forum (2255983776) / all topics"}, targets)
}
