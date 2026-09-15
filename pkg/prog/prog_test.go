package prog

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jedib0t/go-pretty/v6/progress"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/stretchr/testify/require"
)

func TestProgressLinesStayWithinTerminalWidth(t *testing.T) {
	const terminalWidth = 40

	var output bytes.Buffer
	pw := New(progress.FormatBytes)
	pw.SetOutputWriter(&output)
	pw.SetTerminalWidth(terminalWidth)
	pw.SetUpdateFrequency(time.Millisecond)

	tracker := AppendTracker(
		pw,
		progress.FormatBytes,
		"a filename long enough to force progress-line trimming",
		1024*1024,
	)

	go pw.Render()
	require.Eventually(t, pw.IsRenderInProgress, time.Second, time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	tracker.MarkAsDone()
	Wait(t.Context(), pw)

	for _, line := range strings.Split(output.String(), "\n") {
		if line == "" {
			continue
		}
		require.LessOrEqual(t, text.StringWidthWithoutEscSequences(line), terminalWidth)
	}
}
