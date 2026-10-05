package prog

import (
	"sync"
	"time"

	"github.com/jedib0t/go-pretty/v6/progress"
)

// Start starts rendering and returns a stop-and-join function. Repeated Stop
// calls cover go-pretty's asynchronous initialization, even when work ends
// before Render has started. Call after iterator/configuration validation.
func Start(writer progress.Writer) func() {
	done := make(chan struct{})
	go func() { defer close(done); writer.Render() }()
	var once sync.Once
	return func() {
		once.Do(func() {
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				writer.Stop()
				select {
				case <-done:
					return
				case <-ticker.C:
				}
			}
		})
	}
}
