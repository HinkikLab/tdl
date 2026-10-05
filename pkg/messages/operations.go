package messages

import corei18n "github.com/iyear/tdl/core/i18n"

func UpdateAdminRequired() corei18n.Message {
	return corei18n.Message{ID: "update.admin_required", Default: "Must be run as administrator/root"}
}

func UpdateHomebrew() corei18n.Message {
	return corei18n.Message{ID: "update.homebrew", Default: "tdl seems to be installed via Homebrew, please update it with brew instead."}
}

func UpdateCurrentVersion(version string) corei18n.Message {
	return corei18n.Message{ID: "update.current_version", Default: "Current version: {{.Version}}", Args: map[string]any{"Version": version}}
}

func UpdateTargetVersion(version string) corei18n.Message {
	return corei18n.Message{ID: "update.target_version", Default: "Target version:  {{.Version}}", Args: map[string]any{"Version": version}}
}

func UpdateLatestVersion(version string) corei18n.Message {
	return corei18n.Message{ID: "update.latest_version", Default: "Latest version:  {{.Version}}", Args: map[string]any{"Version": version}}
}

func UpdateAlreadyLatest() corei18n.Message {
	return corei18n.Message{ID: "update.already_latest", Default: "You are already using the latest version."}
}

func UpdateForce(version string) corei18n.Message {
	return corei18n.Message{ID: "update.force", Default: "--force is set, reinstalling {{.Version}}.", Args: map[string]any{"Version": version}}
}

func UpdateUnrecognized(current, target string) corei18n.Message {
	return corei18n.Message{ID: "update.unrecognized", Default: "Unrecognized current version ({{.Current}}), will update to {{.Target}}.", Args: map[string]any{"Current": current, "Target": target}}
}

func UpdateAvailable(version string) corei18n.Message {
	return corei18n.Message{ID: "update.available", Default: "An update to {{.Version}} is available.", Args: map[string]any{"Version": version}}
}

func UpdateConfirm(version string) corei18n.Message {
	return corei18n.Message{ID: "update.confirm", Default: "Update to {{.Version}}?", Args: map[string]any{"Version": version}}
}

func UpdateAborted() corei18n.Message {
	return corei18n.Message{ID: "update.aborted", Default: "Aborted."}
}

func UpdateDownloading(name string) corei18n.Message {
	return corei18n.Message{ID: "update.downloading", Default: "Downloading {{.Name}}...", Args: map[string]any{"Name": name}}
}

func UpdateVerifying() corei18n.Message {
	return corei18n.Message{ID: "update.verifying", Default: "Verifying checksum..."}
}

func UpdateExtracting(name string) corei18n.Message {
	return corei18n.Message{ID: "update.extracting", Default: "Extracting {{.Name}}...", Args: map[string]any{"Name": name}}
}

func UpdateReplacing(name string) corei18n.Message {
	return corei18n.Message{ID: "update.replacing", Default: "Replacing {{.Name}}...", Args: map[string]any{"Name": name}}
}

func UpdateSuccess(version string) corei18n.Message {
	return corei18n.Message{ID: "update.success", Default: "Successfully updated to {{.Version}}. Enjoy!", Args: map[string]any{"Version": version}}
}

func MigrateOverwritePrompt() corei18n.Message {
	return corei18n.Message{ID: "migrate.overwrite_prompt", Default: "It will overwrite the namespace data in the destination storage, continue?"}
}

func MigrateSuccess() corei18n.Message {
	return corei18n.Message{ID: "migrate.success", Default: "Migrate successfully."}
}

func MigrateNamespace(namespace string) corei18n.Message {
	return corei18n.Message{ID: "migrate.namespace", Default: " - {{.Namespace}}", Args: map[string]any{"Namespace": namespace}}
}

func BackupSuccess(path string) corei18n.Message {
	return corei18n.Message{ID: "backup.success", Default: "Backup successfully, file: {{.Path}}", Args: map[string]any{"Path": path}}
}

func RecoverSuccess(path string) corei18n.Message {
	return corei18n.Message{ID: "recover.success", Default: "Recover successfully, file: {{.Path}}", Args: map[string]any{"Path": path}}
}

func GenerateDocsStart(path string) corei18n.Message {
	return corei18n.Message{ID: "cli.generate_docs.start", Default: "Generating command-line documentation in {{.Path}}...", Args: map[string]any{"Path": path}}
}

func GenerateDocsDone() corei18n.Message {
	return corei18n.Message{ID: "cli.generate_docs.done", Default: "Done."}
}
