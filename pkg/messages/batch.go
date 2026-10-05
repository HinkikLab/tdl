package messages

import corei18n "github.com/iyear/tdl/core/i18n"

func BatchPerformance(pool, threads, limit int) corei18n.Message {
	return corei18n.Message{ID: "batch.performance", Default: "Performance: pool={{.Pool}} threads={{.Threads}} limit={{.Limit}}", Args: map[string]any{"Pool": pool, "Threads": threads, "Limit": limit}}
}

func BatchSource(source string) corei18n.Message {
	return corei18n.Message{ID: "batch.source", Default: "Source: {{.Source}}", Args: map[string]any{"Source": source}}
}

func BatchStatus(status string) corei18n.Message {
	ids := map[string]string{"planned": "batch.status.planned", "complete": "batch.status.complete", "canceled": "batch.status.canceled", "failed": "batch.status.failed"}
	if id := ids[status]; id != "" {
		return corei18n.Message{ID: id, Default: status}
	}
	return corei18n.Message{Default: status}
}

type BatchSummary struct {
	Job                                                                   int
	Messages, Posts, Files, Downloaded, Existing, Filtered, Failed, Bytes int64
	Status, Reasons                                                       string
}

func BatchJobSummary(summary BatchSummary) corei18n.Message {
	return corei18n.Message{
		ID:      "batch.job.summary",
		Default: "Job {{.Job}} ({{.Status}}): {{.Messages}} messages, {{.Posts}} posts, {{.Files}} files; downloaded {{.Downloaded}}, existing {{.Existing}}, filtered {{.Filtered}}, failed {{.Failed}}; committed {{.Bytes}} bytes{{if .Reasons}}; {{.Reasons}}{{end}}",
		Args: map[string]any{
			"Job": summary.Job, "Status": summary.Status, "Messages": summary.Messages,
			"Posts": summary.Posts, "Files": summary.Files, "Downloaded": summary.Downloaded,
			"Existing": summary.Existing, "Filtered": summary.Filtered, "Failed": summary.Failed,
			"Bytes": summary.Bytes, "Reasons": summary.Reasons,
		},
	}
}

type BatchReasonKind string

const (
	UnavailableMessages  BatchReasonKind = "unavailable_messages"
	MessagesWithoutMedia BatchReasonKind = "messages_without_media"
	UnavailablePosts     BatchReasonKind = "unavailable_posts"
	PostsWithoutLinks    BatchReasonKind = "posts_without_links"
)

func BatchReason(kind BatchReasonKind, count int64) corei18n.Message {
	ids := map[BatchReasonKind]string{
		UnavailableMessages:  "batch.reason.unavailable_messages",
		MessagesWithoutMedia: "batch.reason.messages_without_media",
		UnavailablePosts:     "batch.reason.unavailable_posts",
		PostsWithoutLinks:    "batch.reason.posts_without_links",
	}
	defaults := map[BatchReasonKind]string{
		UnavailableMessages:  "unavailable messages",
		MessagesWithoutMedia: "messages without media",
		UnavailablePosts:     "unavailable posts",
		PostsWithoutLinks:    "posts without links",
	}
	return corei18n.Message{ID: ids[kind], Default: "{{.Count}} " + defaults[kind], Args: map[string]any{"Count": count}}
}

func BatchCheckOnly() corei18n.Message {
	return corei18n.Message{ID: "batch.check_only", Default: "Check only: skipping the download step"}
}

func BatchEverythingDownloaded() corei18n.Message {
	return corei18n.Message{ID: "batch.everything_downloaded", Default: "Everything is already downloaded"}
}

func BatchNoMessages() corei18n.Message {
	return corei18n.Message{ID: "batch.no_messages", Default: "No messages to download for this job"}
}

func BatchRetrySkipped(count int) corei18n.Message {
	return corei18n.Message{ID: "batch.retry_skipped", Default: "Retrying {{.Count}} message(s) that an earlier run recorded as unavailable", Args: map[string]any{"Count": count}}
}

func BatchAutoConfirm(question string) corei18n.Message {
	return corei18n.Message{ID: "batch.auto_confirm", Default: "{{.Question}} [auto: yes]", Args: map[string]any{"Question": question}}
}

func BatchMissing(count int) corei18n.Message {
	return corei18n.Message{ID: "batch.missing", Default: "{{.Count}} message(s) are still missing", Args: map[string]any{"Count": count}}
}

func BatchKeepIncrementalTimestamp() corei18n.Message {
	return corei18n.Message{ID: "batch.keep_incremental_timestamp", Default: "Keeping the incremental timestamp so the next run covers this range again"}
}

func BatchTargetRange(value string) corei18n.Message {
	return corei18n.Message{ID: "batch.target_range", Default: "Target range: {{.Range}}", Args: map[string]any{"Range": value}}
}

func BatchRecorded(finished, pending int) corei18n.Message {
	return corei18n.Message{ID: "batch.recorded", Default: "Recorded as finished: {{.Finished}}, to validate/download: {{.Pending}}", Args: map[string]any{"Finished": finished, "Pending": pending}}
}

func BatchIncrementalWindow(start, end, last string, overlap int, archive bool) corei18n.Message {
	id := "batch.incremental_window"
	defaultText := "Incremental window: {{.Start}} ~ {{.End}} (last={{.Last}}, overlap={{.Overlap}}s)"
	if archive {
		id = "batch.incremental_archive_window"
		defaultText = "Incremental archive window: {{.Start}} ~ {{.End}} (last={{.Last}}, overlap={{.Overlap}}s)"
	}
	return corei18n.Message{ID: id, Default: defaultText, Args: map[string]any{"Start": start, "End": end, "Last": last, "Overlap": overlap}}
}

func BatchIncrementalNoMedia() corei18n.Message {
	return corei18n.Message{ID: "batch.incremental_no_media", Default: "The incremental window holds no media messages"}
}

func BatchLastTimestampAdvanced(value string) corei18n.Message {
	return corei18n.Message{ID: "batch.last_ts_advanced", Default: "last_ts advanced to {{.Timestamp}}", Args: map[string]any{"Timestamp": value}}
}

func BatchRetryPrompt(count int) corei18n.Message {
	return corei18n.Message{ID: "batch.retry_prompt", Default: "Retry the {{.Count}} missing message(s) now?", Args: map[string]any{"Count": count}}
}

func BatchMissingAfterRun(count int) corei18n.Message {
	return corei18n.Message{ID: "batch.missing_after_run", Default: "{{.Count}} message(s) are still missing after this run", Args: map[string]any{"Count": count}}
}

func DownloadNoneFoundHint() corei18n.Message {
	return corei18n.Message{ID: "download.none_found.hint", Default: "Check --mode / the \"comment\" setting of the job and the ID range, then run again with --retry-skipped."}
}
