package messages

import corei18n "github.com/iyear/tdl/core/i18n"

func LoginPhonePrompt() corei18n.Message {
	return corei18n.Message{ID: "login.prompt.phone", Default: "Enter your phone number:"}
}

func LoginPasswordPrompt() corei18n.Message {
	return corei18n.Message{ID: "login.prompt.password", Default: "Enter 2FA Password:"}
}

func LoginCodePrompt() corei18n.Message {
	return corei18n.Message{ID: "login.prompt.code", Default: "Enter Code:"}
}

func LoginSendCode() corei18n.Message {
	return corei18n.Message{ID: "login.send_code", Default: "Sending Code..."}
}

func LoginScanQR() corei18n.Message {
	return corei18n.Message{ID: "login.scan_qr", Default: "Scan QR code with your Telegram app..."}
}

func LoginSuccess(id int64, username string) corei18n.Message {
	return corei18n.Message{ID: "login.success", Default: "Login successfully! ID: {{.ID}}, Username: {{.Username}}", Args: map[string]any{"ID": id, "Username": username}}
}

func LoginOverwriteWarning() corei18n.Message {
	return corei18n.Message{ID: "login.overwrite_warning", Default: "WARN: If data exists in the namespace, data will be overwritten"}
}

func LoginImportDesktop(path string) corei18n.Message {
	return corei18n.Message{ID: "login.import_desktop", Default: "Importing session from desktop client: {{.Path}}", Args: map[string]any{"Path": path}}
}

func LoginChooseUserID() corei18n.Message {
	return corei18n.Message{ID: "login.choose_user_id", Default: "Choose a user ID:"}
}

func LoginUserIDHelp() corei18n.Message {
	return corei18n.Message{ID: "login.user_id_help", Default: "You can get user ID from @userinfobot"}
}

func LoginImported(userID, namespace string) corei18n.Message {
	return corei18n.Message{ID: "login.imported", Default: "Import {{.UserID}} successfully to '{{.Namespace}}' namespace!", Args: map[string]any{"UserID": userID, "Namespace": namespace}}
}

func LoginLogoutPrompt() corei18n.Message {
	return corei18n.Message{ID: "login.logout_prompt", Default: "Do you want to logout existing desktop session?"}
}

func LoginLogoutHelp() corei18n.Message {
	return corei18n.Message{ID: "login.logout_help", Default: "Logout existing desktop session to separate from imported session, which can prevent session conflict.\nNB: Ensure that you can re-login to desktop client"}
}

func LoginLoggedOut(userID uint64) corei18n.Message {
	return corei18n.Message{ID: "login.logged_out", Default: "Logout desktop session of {{.UserID}} successfully! Please re-launch Telegram Desktop client", Args: map[string]any{"UserID": userID}}
}

func DownloadRestart() corei18n.Message {
	return corei18n.Message{ID: "download.restart", Default: "Restart download by 'restart' flag"}
}

func DownloadDestination(path string) corei18n.Message {
	return corei18n.Message{ID: "download.destination", Default: "All files will be downloaded to '{{.Path}}' dir", Args: map[string]any{"Path": path}}
}

func DownloadLegacyState() corei18n.Message {
	return corei18n.Message{ID: "download.legacy_state", Default: "Legacy positional download state retained; outputs will be checked and unverified partials preserved before downloading again"}
}

func DownloadResumePrompt(finished, total int) corei18n.Message {
	return corei18n.Message{ID: "download.resume_prompt", Default: "Found unfinished download, continue from '{{.Finished}}/{{.Total}}'", Args: map[string]any{"Finished": finished, "Total": total}}
}

func DownloadDeletedMessages(count int64, ids string, extra int) corei18n.Message {
	if extra > 0 {
		return corei18n.Message{ID: "download.deleted_messages_more", Default: "⚠️ {{.Count}} message(s) were skipped because they were deleted: {{.IDs}}... and {{.Extra}} more", Args: map[string]any{"Count": count, "IDs": ids, "Extra": extra}}
	}
	return corei18n.Message{ID: "download.deleted_messages", Default: "⚠️ {{.Count}} message(s) were skipped because they were deleted: {{.IDs}}", Args: map[string]any{"Count": count, "IDs": ids}}
}

func DownloadItemError(name, reason string) corei18n.Message {
	return corei18n.Message{ID: "download.item_error", Default: "{{.Name}} error: {{.Reason}}", Args: map[string]any{"Name": name, "Reason": reason}}
}

func ProgressDone() corei18n.Message {
	return corei18n.Message{ID: "progress.done", Default: "done!"}
}

func ProgressFailed() corei18n.Message {
	return corei18n.Message{ID: "progress.failed", Default: "failed!"}
}

func TransferItemError(name, reason string) corei18n.Message {
	return corei18n.Message{ID: "transfer.item_error", Default: "{{.Name}} error: {{.Reason}}", Args: map[string]any{"Name": name, "Reason": reason}}
}
