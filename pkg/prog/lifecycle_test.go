package prog

import (
	"io"
	"testing"
	"time"

	"github.com/jedib0t/go-pretty/v6/progress"
	"github.com/stretchr/testify/require"
)

func TestStartStopJoinsEvenBeforeRendererInitialization(t *testing.T) {
	for range 20 {
		w := New(progress.FormatBytes)
		w.SetOutputWriter(io.Discard)
		stop := Start(w)
		done := make(chan struct{})
		go func() { stop(); stop(); close(done) }()
		select {
		case <-done:
			require.False(t, w.IsRenderInProgress())
		case <-time.After(time.Second):
			t.Fatal("renderer did not join")
		}
	}
}
