package i18n

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

const (
	deprecatedAnnotation          = "tdl_i18n_deprecated"
	shorthandDeprecatedAnnotation = "tdl_i18n_shorthand_deprecated"
)

func initializeDefaultCommands(root *cobra.Command) {
	existing := findChild(root, "completion")
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	completion := findChild(root, "completion")
	if existing != nil || completion == nil {
		return
	}
	// Cobra captures the output writer at construction. Resolve it at execution
	// instead so callers can redirect scripts after creating the command tree.
	for _, command := range completion.Commands() {
		command.RunE = func(current *cobra.Command, _ []string) error {
			noDescriptions := root.CompletionOptions.DisableDescriptions
			if flag := current.Flags().Lookup("no-descriptions"); flag != nil {
				noDescriptions, _ = current.Flags().GetBool(flag.Name)
			}
			out := current.OutOrStdout()
			switch current.Name() {
			case "bash":
				return root.GenBashCompletionV2(out, !noDescriptions)
			case "fish":
				return root.GenFishCompletion(out, !noDescriptions)
			case "zsh":
				if noDescriptions {
					return root.GenZshCompletionNoDesc(out)
				}
				return root.GenZshCompletion(out)
			case "powershell":
				if noDescriptions {
					return root.GenPowerShellCompletion(out)
				}
				return root.GenPowerShellCompletionWithDesc(out)
			}
			return nil
		}
	}
}

func warnDeprecated(command *cobra.Command, translator corei18n.Translator) {
	if reason := command.Annotations[deprecatedAnnotation]; reason != "" {
		fmt.Fprintln(command.ErrOrStderr(), translator.Translate(corei18n.Message{ID: "cli.deprecated.command_warning", Default: "Command {{.Name}} is deprecated, {{.Reason}}", Args: map[string]any{"Name": command.Name(), "Reason": reason}}))
	}
	command.Flags().Visit(func(flag *pflag.Flag) {
		if reasons := flag.Annotations[deprecatedAnnotation]; len(reasons) > 0 {
			fmt.Fprintln(command.ErrOrStderr(), translator.Translate(corei18n.Message{ID: "cli.deprecated.flag_warning", Default: "Flag --{{.Name}} has been deprecated, {{.Reason}}", Args: map[string]any{"Name": flag.Name, "Reason": reasons[0]}}))
		}
	})
}

// Validate flags before pre-run hooks open storage or connect to Telegram.
// Read Cobra's annotation metadata instead of parsing its English error text.
func validateFlagConstraints(command *cobra.Command) error {
	if command.DisableFlagParsing {
		return nil
	}
	if cause := command.ValidateRequiredFlags(); cause != nil {
		var missing []string
		command.Flags().VisitAll(func(flag *pflag.Flag) {
			if len(flag.Annotations[cobra.BashCompOneRequiredFlag]) > 0 && !flag.Changed {
				missing = append(missing, "--"+flag.Name)
			}
		})
		return diagnostic.Describe(cause, corei18n.Message{ID: "errors.cli.required_flags", Args: map[string]any{"Flags": strings.Join(missing, ", ")}})
	}
	cause := command.ValidateFlagGroups()
	if cause == nil {
		return nil
	}
	for _, kind := range []string{"required_if_others_set", "one_required", "mutually_exclusive"} {
		groups := map[string]bool{}
		command.Flags().VisitAll(func(flag *pflag.Flag) {
			for _, group := range flag.Annotations["cobra_annotation_"+kind] {
				groups[group] = true
			}
		})
		var names []string
		for group := range groups {
			names = append(names, group)
		}
		sort.Strings(names)
		for _, group := range names {
			var set, missing []string
			valid := true
			for _, name := range strings.Fields(group) {
				flag := command.Flags().Lookup(name)
				if flag == nil {
					valid = false
					break
				}
				if flag.Changed {
					set = append(set, "--"+name)
				} else {
					missing = append(missing, "--"+name)
				}
			}
			if !valid {
				continue
			}
			if kind == "mutually_exclusive" && len(set) > 1 {
				return diagnostic.Describe(cause, corei18n.Message{ID: "errors.cli.exclusive_flags", Args: map[string]any{"Flags": strings.Join(set, ", ")}})
			}
			if kind == "one_required" && len(set) == 0 || kind == "required_if_others_set" && len(set) > 0 && len(missing) > 0 {
				return diagnostic.Describe(cause, corei18n.Message{ID: "errors.cli.required_flags", Args: map[string]any{"Flags": strings.Join(missing, ", ")}})
			}
		}
	}
	return diagnostic.InvalidCLIFlags(cause)
}

// MinimumArgs preserves Cobra's original error while describing its arity.
func MinimumArgs(minimum int) cobra.PositionalArgs {
	return func(command *cobra.Command, args []string) error {
		cause := cobra.MinimumNArgs(minimum)(command, args)
		return diagnostic.Describe(cause, corei18n.Message{ID: "errors.cli.minimum_arguments", Args: map[string]any{"Command": command.CommandPath(), "Minimum": minimum, "Actual": len(args)}})
	}
}
