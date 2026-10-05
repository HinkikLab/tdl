package messages

import corei18n "github.com/iyear/tdl/core/i18n"

func ExtensionTableHeaders() corei18n.Message {
	return corei18n.Message{ID: "extension.table.headers", Default: "NAME\tAUTHOR\tVERSION"}
}

func ExtensionInstalling(name string) corei18n.Message {
	return corei18n.Message{ID: "extension.install.start", Default: "installing extension {{.Name}}...", Args: map[string]any{"Name": name}}
}

func ExtensionInstallFailed(name, reason string) corei18n.Message {
	return corei18n.Message{ID: "extension.install.failed", Default: "install extension {{.Name}} failed: {{.Reason}}", Args: map[string]any{"Name": name, "Reason": reason}}
}

func ExtensionWillInstall(name string) corei18n.Message {
	return corei18n.Message{ID: "extension.install.dry_run", Default: "extension {{.Name}} will be installed", Args: map[string]any{"Name": name}}
}

func ExtensionInstalled(name string) corei18n.Message {
	return corei18n.Message{ID: "extension.install.done", Default: "extension {{.Name}} installed", Args: map[string]any{"Name": name}}
}

func ExtensionNotFound(name string) corei18n.Message {
	return corei18n.Message{ID: "extension.not_found", Default: "extension {{.Name}} not found", Args: map[string]any{"Name": name}}
}

func ExtensionUpgrading(name string) corei18n.Message {
	return corei18n.Message{ID: "extension.upgrade.start", Default: "upgrading {{.Name}}...", Args: map[string]any{"Name": name}}
}

func ExtensionUpToDate(name string) corei18n.Message {
	return corei18n.Message{ID: "extension.upgrade.current", Default: "extension {{.Name}} already up-to-date", Args: map[string]any{"Name": name}}
}

func ExtensionCannotUpgrade(name string) corei18n.Message {
	return corei18n.Message{ID: "extension.upgrade.unsupported", Default: "extension {{.Name}} can't be automatically upgraded by tdl", Args: map[string]any{"Name": name}}
}

func ExtensionUpgradeFailed(name, reason string) corei18n.Message {
	return corei18n.Message{ID: "extension.upgrade.failed", Default: "upgrade extension {{.Name}} failed: {{.Reason}}", Args: map[string]any{"Name": name, "Reason": reason}}
}

func ExtensionWillUpgrade(name string) corei18n.Message {
	return corei18n.Message{ID: "extension.upgrade.dry_run", Default: "extension {{.Name}} will be upgraded", Args: map[string]any{"Name": name}}
}

func ExtensionUpgraded(name string) corei18n.Message {
	return corei18n.Message{ID: "extension.upgrade.done", Default: "extension {{.Name}} upgraded", Args: map[string]any{"Name": name}}
}

func ExtensionRemoveFailed(name, reason string) corei18n.Message {
	return corei18n.Message{ID: "extension.remove.failed", Default: "remove extension {{.Name}} failed: {{.Reason}}", Args: map[string]any{"Name": name, "Reason": reason}}
}

func ExtensionWillRemove(name string) corei18n.Message {
	return corei18n.Message{ID: "extension.remove.dry_run", Default: "extension {{.Name}} will be removed", Args: map[string]any{"Name": name}}
}

func ExtensionRemoved(name string) corei18n.Message {
	return corei18n.Message{ID: "extension.remove.done", Default: "extension {{.Name}} removed", Args: map[string]any{"Name": name}}
}
