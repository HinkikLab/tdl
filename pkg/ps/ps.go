package ps

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/shirou/gopsutil/v3/process"

	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/utils"
)

var proc *process.Process

// Humanize returns human-readable string slice, containing cpu, memory and network info
func Humanize(ctx context.Context) []string {
	str := make([]string, 0, 3)

	if cpu, err := GetSelfCPU(ctx); err == nil {
		str = append(str, console.Translate(ctx, corei18n.Message{ID: "progress.cpu", Default: "CPU: {{.Value}}%", Args: map[string]any{"Value": fmt.Sprintf("%.2f", cpu)}}))
	}

	if mem, err := GetSelfMem(ctx); err == nil {
		str = append(str, console.Translate(ctx, corei18n.Message{ID: "progress.memory", Default: "Memory: {{.Value}}", Args: map[string]any{"Value": utils.Byte.FormatBinaryBytes(int64(mem.RSS))}}))
	}

	str = append(str, console.Translate(ctx, corei18n.Message{ID: "progress.goroutines", Default: "Goroutines: {{.Count}}", Args: map[string]any{"Count": GetGoroutineNum()}}))

	return str
}

func init() {
	var err error
	proc, err = process.NewProcess(int32(os.Getpid()))
	if err != nil {
		panic(err)
	}
}

func GetSelfCPU(ctx context.Context) (float64, error) {
	cpu, err := proc.PercentWithContext(ctx, 0)
	if err != nil {
		return 0, err
	}

	return cpu, nil
}

// GetSelfMem returns self memory info
func GetSelfMem(ctx context.Context) (*process.MemoryInfoStat, error) {
	m, err := proc.MemoryInfoWithContext(ctx)
	if err != nil {
		return nil, err
	}

	return m, nil
}

func GetGoroutineNum() int {
	return runtime.NumGoroutine()
}
