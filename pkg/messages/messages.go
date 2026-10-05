// Package messages contains typed descriptions used by user-facing output.
package messages

import (
	"strconv"

	corei18n "github.com/iyear/tdl/core/i18n"
)

func BatchAutoStarted(path string) corei18n.Message {
	return corei18n.Message{ID: "batch.auto_started", Default: "Found {{.Path}} and a logged in account, starting batch download...", Args: map[string]any{"Path": path}}
}

func BatchConfigValid(summary string) corei18n.Message {
	return corei18n.Message{ID: "batch.config_valid", Default: "Batch configuration valid: {{.Summary}}", Args: map[string]any{"Summary": summary}}
}

func BatchInitGenerated(path string, jobs int, language string) corei18n.Message {
	return corei18n.Message{ID: "batch.init.generated", Default: "Generated {{.Path}} ({{.Jobs}} example jobs; {{.Language}} comments).", Args: map[string]any{"Path": path, "Jobs": jobs, "Language": language}}
}

func BatchInitNext(configPath string) corei18n.Message {
	return corei18n.Message{ID: "batch.init.next", Default: "Keep the jobs you need, replace example URLs/IDs/tags, and select your namespace.\nThen run: tdl batch -c {{.Path}} --check-only", Args: map[string]any{"Path": strconv.Quote(configPath)}}
}

func BatchStarted(jobs int, path string) corei18n.Message {
	return corei18n.Message{ID: "batch.started", Default: "Batch download: {{.Count}} job(s) from {{.Path}}", Args: map[string]any{"Count": jobs, "Path": path}}
}

func BatchJobStarting(index, total int, target string) corei18n.Message {
	return corei18n.Message{ID: "batch.job.starting", Default: "[{{.Index}}/{{.Total}}] {{.Target}}", Args: map[string]any{"Index": index, "Total": total, "Target": target}}
}

func BatchJobFailed(index int, reason string) corei18n.Message {
	return corei18n.Message{ID: "batch.job.failed", Default: "Job {{.Index}} failed: {{.Reason}}", Args: map[string]any{"Index": index, "Reason": reason}}
}

func BatchFinished(jobs, failed int) corei18n.Message {
	return corei18n.Message{ID: "batch.finished", Default: "Batch download finished: {{.Jobs}} job(s), {{.Failed}} failed", Args: map[string]any{"Jobs": jobs, "Failed": failed}}
}

func DownloadNoneFound(count int, dialogID int64) corei18n.Message {
	return corei18n.Message{ID: "download.none_found", Default: "None of the {{.Count}} message(s) exist in dialog {{.DialogID}}.", Args: map[string]any{"Count": count, "DialogID": dialogID}}
}
