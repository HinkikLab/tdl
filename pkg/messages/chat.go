package messages

import (
	"fmt"

	corei18n "github.com/iyear/tdl/core/i18n"
)

func ChatUnnamed() corei18n.Message {
	return corei18n.Message{ID: "chat.target.unnamed", Default: "Chat"}
}

func ChatTargetTopic(label string, id int) corei18n.Message {
	return corei18n.Message{ID: "chat.target.topic", Default: "{{.Label}} / topic {{.ID}}", Args: map[string]any{"Label": label, "ID": id}}
}

func ChatTargetAllTopics(label string) corei18n.Message {
	return corei18n.Message{ID: "chat.target.all_topics", Default: "{{.Label}} / all topics", Args: map[string]any{"Label": label}}
}

func ChatExportWarning() corei18n.Message {
	return corei18n.Message{ID: "chat.export.warning", Default: "WARN: Export only generates minimal JSON for tdl download, not for backup."}
}

func ChatRateLimitNote() corei18n.Message {
	return corei18n.Message{ID: "chat.rate_limit_note", Default: "Occasional suspensions are due to Telegram rate limitations, please wait a moment."}
}

func ChatListHeader() corei18n.Message {
	return corei18n.Message{ID: "chat.list.header", Default: "ID Type VisibleName Username Topics"}
}

func ChatPaginationUser(userID int64) corei18n.Message {
	return corei18n.Message{ID: "chat.pagination.user_error", Default: "Error: failed to get user for offset, stopping pagination. User ID: {{.ID}}", Args: map[string]any{"ID": userID}}
}

func ChatPaginationChannel(channelID int64) corei18n.Message {
	return corei18n.Message{ID: "chat.pagination.channel_error", Default: "Error: failed to get channel for offset, stopping pagination. Channel ID: {{.ID}}", Args: map[string]any{"ID": channelID}}
}

func ChatExportMode(kind, input string) corei18n.Message {
	return corei18n.Message{ID: "chat.export.mode", Default: "Type: {{.Type}} | Input: {{.Input}}", Args: map[string]any{"Type": kind, "Input": input}}
}

func ChatServe(port int) corei18n.Message {
	return corei18n.Message{ID: "chat.download.serving", Default: "(Beta) Serving on http://localhost:{{.Port}}", Args: map[string]any{"Port": port}}
}

func UploadFilesCount(count int) corei18n.Message {
	return corei18n.Message{ID: "upload.files_count", Default: "Files count: {{.Count}}", Args: map[string]any{"Count": count}}
}

func ChatScanning(target string) corei18n.Message {
	return corei18n.Message{ID: "chat.scanning", Default: "Scanning: {{.Target}}", Args: map[string]any{"Target": target}}
}

func TagArchiveFile(messageID, count int, path string) corei18n.Message {
	return corei18n.Message{ID: "chat.tag.archive_file", Default: "  {{.ID}}: {{.Count}} photo/video file(s), {{.Path}}", Args: map[string]any{"ID": messageID, "Count": count, "Path": path}}
}

func TagArchiveRoot(path string) corei18n.Message {
	return corei18n.Message{ID: "chat.tag.archive_root", Default: "Archive: {{.Path}}", Args: map[string]any{"Path": path}}
}

func TagScanSummary(scanned, selected int, tags, mode string) corei18n.Message {
	return corei18n.Message{ID: "chat.tag.scan_summary", Default: "Scanned {{.Scanned}} messages; found {{.Selected}} posts matching {{.Tags}} ({{.Mode}}).", Args: map[string]any{"Scanned": scanned, "Selected": selected, "Tags": tags, "Mode": mode}}
}

func TagArchiveUnverified(path string) corei18n.Message {
	return corei18n.Message{ID: "chat.tag.unverified", Default: "Archive {{.Path}} has incompatible or unverified source/account metadata; preserved for recovery", Args: map[string]any{"Path": path}}
}

func LinkedPost(messageID, count int, path string) corei18n.Message {
	return corei18n.Message{ID: "chat.linked.post", Default: "Post {{.ID}}: {{.Count}} resource link(s) -> {{.Path}}", Args: map[string]any{"ID": messageID, "Count": count, "Path": path}}
}

func LinkedPostSkipped(messageID int, reason string) corei18n.Message {
	return corei18n.Message{ID: "chat.linked.post_skipped", Default: "Post {{.ID}} skipped: {{.Reason}}", Args: map[string]any{"ID": messageID, "Reason": reason}}
}

func LinkedPostFailed(reason string) corei18n.Message {
	return corei18n.Message{ID: "chat.linked.post_failed", Default: "Linked post failed: {{.Reason}}", Args: map[string]any{"Reason": reason}}
}

func LinkedArchiveSummary(selected, skipped int) corei18n.Message {
	return corei18n.Message{ID: "chat.linked.summary", Default: "Linked archive: {{.Selected}} selected post(s), {{.Skipped}} skipped (no links or unavailable targets).", Args: map[string]any{"Selected": selected, "Skipped": skipped}}
}

func LinkedArchiveComplete(messageID int) corei18n.Message {
	return corei18n.Message{ID: "chat.linked.complete", Default: "Post {{.ID}}: archive already complete", Args: map[string]any{"ID": messageID}}
}

func LinkedUnavailableTarget(chat, reason string) corei18n.Message {
	return corei18n.Message{ID: "chat.linked.target_unavailable", Default: "Ignoring resource target {{.Chat}} for this batch run: {{.Reason}}", Args: map[string]any{"Chat": chat, "Reason": reason}}
}

func LinkedBranchSkipped(url string, count int, reason string) corei18n.Message {
	return corei18n.Message{ID: "chat.linked.branch_skipped", Default: "Resource branch {{.URL}} skipped; keeping {{.Count}} resolved file(s): {{.Reason}}", Args: map[string]any{"URL": url, "Count": count, "Reason": reason}}
}

func LinkedRerequest(request, limit int) corei18n.Message {
	return corei18n.Message{ID: "chat.linked.rerequest", Default: "Source message expired; requesting resource links again ({{.Request}}/{{.Limit}})", Args: map[string]any{"Request": request, "Limit": limit}}
}

func LinkedPartial(path string) corei18n.Message {
	return corei18n.Message{ID: "chat.linked.partial", Default: "Preserved unverified partial download: {{.Path}}", Args: map[string]any{"Path": path}}
}

func ArchivePreserved(path string) corei18n.Message {
	return corei18n.Message{ID: "chat.archive.preserved", Default: "Preserved unverified archive file: {{.Path}}", Args: map[string]any{"Path": path}}
}

func LinkedBotRateLimit(bot int64, seconds, retry, retries int) corei18n.Message {
	return corei18n.Message{ID: "chat.linked.bot_rate_limit", Default: "Bot {{.Bot}} rate limited; waiting {{.Seconds}} seconds before retry {{.Retry}}/{{.Retries}}", Args: map[string]any{"Bot": bot, "Seconds": seconds, "Retry": retry, "Retries": retries}}
}

func LinkedBotInterval(bot int64, seconds float64) corei18n.Message {
	return corei18n.Message{ID: "chat.linked.bot_interval", Default: "Bot {{.Bot}}: waiting {{.Seconds}} seconds after its last reply before requesting again", Args: map[string]any{"Bot": bot, "Seconds": fmt.Sprintf("%.1f", seconds)}}
}
