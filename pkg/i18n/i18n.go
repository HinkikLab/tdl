package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

//go:embed resources
var resources embed.FS

const (
	languageFlag  = "language"
	rootCommandID = "root"
)

// New builds one immutable run translator from core and application resources.
func New(lang corei18n.Language) (corei18n.Translator, error) {
	appResources, err := fs.Sub(resources, "resources")
	if err != nil {
		return nil, fmt.Errorf("open application translations: %w", err)
	}
	return corei18n.NewTranslator(lang, corei18n.CoreResources(), appResources)
}

// ApplicationResources exposes the embedded application catalog for build and
// release checks. Callers cannot mutate the embedded filesystem.
func ApplicationResources() (fs.FS, error) { return fs.Sub(resources, "resources") }

// ResolveCommandLanguage safely reads --language before command execution.
// Flag values are consumed according to the actual Cobra flag definitions,
// -- ends scanning, and extension commands with disabled flag parsing form an
// opaque boundary for their own arguments.
func ResolveCommandLanguage(root *cobra.Command, args []string, getenv func(string) string, systemLocale func() string) (corei18n.Language, error) {
	explicit, found, err := findLanguage(root, args)
	if err != nil {
		return "", err
	}
	if !found {
		explicit = ""
	} else if strings.TrimSpace(explicit) == "" {
		return "", diagnostic.New("errors.cli.language_required", nil)
	}
	language, err := corei18n.ResolveLanguage(explicit, getenv, systemLocale)
	if err != nil {
		return "", diagnostic.New("errors.cli.invalid_language", map[string]any{"Language": explicit})
	}
	return language, nil
}

func findLanguage(root *cobra.Command, args []string) (string, bool, error) {
	current := root
	var selected string
	found := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if len(arg) > 1 && arg[0] == '-' {
			if !strings.HasPrefix(arg, "--") {
				// pflag supports both boolean clusters (-yc) and attached values
				// (-t8). Only a value-taking shorthand without a suffix consumes
				// the next token; that token may itself look like --language.
				group := arg[1:]
				for len(group) > 0 {
					r, size := utf8.DecodeRuneInString(group)
					_, flag := lookupFlag(current, string(r))
					group = group[size:]
					if flag == nil {
						break
					}
					if flag.Name == languageFlag {
						group = strings.TrimPrefix(group, "=")
						if group != "" {
							selected, found = group, true
							break
						}
						if i+1 < len(args) && args[i+1] != "--" {
							i++
							selected, found = args[i], true
							break
						}
						return "", true, diagnostic.New("errors.cli.language_required", nil)
					}
					if strings.HasPrefix(group, "=") {
						break
					}
					if flag.NoOptDefVal == "" {
						if group == "" && i+1 < len(args) {
							i++
						}
						break
					}
				}
				continue
			}
			name, value, hasValue := splitFlag(arg)
			flag, shorthand := lookupFlag(current, name)
			if flag == nil && shorthand == nil {
				continue
			}
			if name == languageFlag || (shorthand != nil && shorthand.Name == languageFlag) {
				if hasValue {
					selected, found = value, true
					continue
				}
				if i+1 < len(args) && args[i+1] != "--" {
					i++
					selected, found = args[i], true
					continue
				}
				return "", true, diagnostic.New("errors.cli.language_required", nil)
			}
			candidate := flag
			if candidate == nil {
				candidate = shorthand
			}
			if !hasValue && candidate.NoOptDefVal == "" && i+1 < len(args) {
				i++ // The next token is a parameter value, even if it starts with '-'.
			}
			continue
		}

		child := findChild(current, arg)
		if child == nil {
			continue
		}
		current = child
		if child.DisableFlagParsing {
			break
		}
	}
	return selected, found, nil
}

func splitFlag(arg string) (name, value string, hasValue bool) {
	arg = strings.TrimLeft(arg, "-")
	if i := strings.IndexByte(arg, '='); i >= 0 {
		return arg[:i], arg[i+1:], true
	}
	return arg, "", false
}

func lookupFlag(cmd *cobra.Command, name string) (long, shorthand *pflag.Flag) {
	for c := cmd; c != nil; c = c.Parent() {
		if long == nil {
			long = c.Flags().Lookup(name)
			if long == nil {
				long = c.PersistentFlags().Lookup(name)
			}
		}
		if shorthand == nil && len(name) == 1 {
			shorthand = c.Flags().ShorthandLookup(name)
			if shorthand == nil {
				shorthand = c.PersistentFlags().ShorthandLookup(name)
			}
		}
	}
	return long, shorthand
}

func findChild(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
		for _, alias := range child.Aliases {
			if alias == name {
				return child
			}
		}
	}
	return nil
}

// BindCommandTree replaces user-facing command and flag descriptions with
// translations while keeping names, flags, enum values, and file formats fixed.
func BindCommandTree(root *cobra.Command, translator corei18n.Translator) {
	if root == nil || translator == nil {
		return
	}
	initializeDefaultCommands(root)
	legacyRoot := *root
	// Cobra uses nil Args to enable its legacy unknown-command validation in
	// Find. Keep that validator available after wrapping Args for localization.
	if legacyRoot.Args == nil {
		if help := findChild(root, "help"); help != nil {
			help.Run = func(current *cobra.Command, args []string) {
				found, _, err := legacyRoot.Find(args)
				if found == nil || err != nil {
					current.Println(translator.Translate(corei18n.Message{ID: "cli.help.unknown_topic", Default: "Unknown help topic {{.Topics}}", Args: map[string]any{"Topics": fmt.Sprintf("%q", args)}}))
					_ = root.Usage()
					return
				}
				found.SetContext(current.Context())
				found.InitDefaultHelpFlag()
				_ = found.Help()
			}
		}
	}
	root.SetContext(corei18n.WithTranslator(root.Context(), translator))
	for _, group := range root.Groups() {
		id := "cli.group." + normalizeID(group.ID)
		group.Title = translator.Translate(corei18n.Message{ID: id, Default: group.Title})
	}
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		path := strings.TrimSpace(strings.TrimPrefix(command.CommandPath(), root.Name()))
		path = normalizeID(path)
		id := "cli.command." + path + ".short"
		if path == "" {
			id = "cli.root.short"
		}
		if command.Parent() == root && command.GroupID == "extensions" {
			id = "cli.command.extension_child.short"
		}
		command.Short = translator.Translate(corei18n.Message{ID: id, Default: command.Short, Args: map[string]any{"Name": command.Name()}})
		if command.Deprecated != "" {
			if command.Annotations == nil {
				command.Annotations = map[string]string{}
			}
			command.Annotations[deprecatedAnnotation] = translator.Translate(corei18n.Message{ID: "cli.command." + path + ".deprecated", Default: command.Deprecated})
			command.Deprecated = ""
		}
		validateArgs := command.Args
		command.Args = func(current *cobra.Command, args []string) error {
			warnDeprecated(current, translator)
			if current == root && validateArgs == nil && len(args) > 0 {
				if _, _, err := legacyRoot.Find(args); err != nil {
					return diagnostic.Describe(err, corei18n.Message{ID: "errors.cli.unknown_command", Args: map[string]any{"Command": current.CommandPath(), "Argument": args[0], "Suggestions": strings.Join(current.SuggestionsFor(args[0]), ", ")}})
				}
			}
			if validateArgs != nil {
				if err := validateArgs(current, args); err != nil {
					if reflect.ValueOf(validateArgs).Pointer() == reflect.ValueOf(cobra.NoArgs).Pointer() {
						return diagnostic.Describe(err, corei18n.Message{ID: "errors.cli.no_arguments", Args: map[string]any{"Command": current.CommandPath(), "Arguments": strings.Join(args, " ")}})
					}
					return diagnostic.InvalidCLIArguments(err)
				}
			}
			return validateFlagConstraints(current)
		}
		if command.Long != "" {
			longID := "cli.command." + path + ".long"
			if path == "" {
				longID = "cli.root.long"
			}
			command.Long = translator.Translate(corei18n.Message{ID: longID, Default: command.Long, Args: map[string]any{"Name": command.Name()}})
		}
		if command.Example != "" {
			exampleID := "cli.command." + path + ".example"
			command.Example = translator.Translate(corei18n.Message{ID: exampleID, Default: command.Example})
		}
		command.InitDefaultHelpFlag()
		for _, flags := range []*pflag.FlagSet{command.LocalNonPersistentFlags(), command.PersistentFlags()} {
			flags.VisitAll(func(flag *pflag.Flag) {
				if flag.Name == "help" {
					flag.Usage = translator.Translate(corei18n.Message{ID: "cli.flags.help.description", Default: flag.Usage, Args: map[string]any{"Command": command.Name()}})
					return
				}
				flagPath := path
				if flag.Shorthand != "" && command == root {
					flagPath = rootCommandID
				}
				if flagPath == "" {
					flagPath = rootCommandID
				}
				if flag.Deprecated != "" {
					if flag.Annotations == nil {
						flag.Annotations = map[string][]string{}
					}
					flag.Annotations[deprecatedAnnotation] = []string{translator.Translate(corei18n.Message{
						ID:      "cli.flags." + flagPath + "." + normalizeID(flag.Name) + ".deprecated",
						Default: flag.Deprecated,
					})}
					flag.Deprecated = ""
				}
				if flag.ShorthandDeprecated != "" {
					if flag.Annotations == nil {
						flag.Annotations = map[string][]string{}
					}
					flag.Annotations[shorthandDeprecatedAnnotation] = []string{flag.ShorthandDeprecated}
					flag.ShorthandDeprecated = ""
				}
				flag.Usage = translator.Translate(corei18n.Message{
					ID:      "cli.flags." + flagPath + "." + normalizeID(flag.Name) + ".description",
					Default: flag.Usage,
					Args:    map[string]any{"Modes": enumValues(flag.Usage)},
				})
			})
		}
		for _, child := range command.Commands() {
			visit(child)
		}
	}
	visit(root)
	root.SetUsageTemplate(localizeTemplate(root.UsageTemplate(), translator))
	root.SetHelpTemplate(localizeTemplate(root.HelpTemplate(), translator))
}

func init() {
	// This shared template function derives language from its command, never
	// from a mutable global translator or a terminal-output replacement filter.
	cobra.AddTemplateFunc("tdlFlagUsages", func(command *cobra.Command, flags *pflag.FlagSet) string {
		ctx := command.Context()
		for parent := command.Parent(); corei18n.FromContext(ctx) == nil && parent != nil; parent = parent.Parent() {
			ctx = parent.Context()
		}
		if ctx == nil {
			ctx = context.Background()
		}
		return localizeDefaults(flags.FlagUsages(), corei18n.FromContext(ctx))
	})
}

func enumValues(usage string) string {
	start := strings.IndexByte(usage, '[')
	if start < 0 {
		return ""
	}
	end := strings.IndexByte(usage[start+1:], ']')
	if end < 0 {
		return ""
	}
	return usage[start+1 : start+1+end]
}

func localizeTemplate(template string, translator corei18n.Translator) string {
	for _, flags := range []string{"LocalFlags", "InheritedFlags"} {
		template = strings.ReplaceAll(template, "."+flags+".FlagUsages", "(tdlFlagUsages . ."+flags+")")
	}
	for _, label := range []string{"more_information", "additional_help_topics", "global_flags", "available_commands", "additional_commands", "aliases", "examples", "usage", "flags"} {
		id := "cli.help." + label
		translated := translator.Translate(corei18n.Message{ID: id, Default: helpDefaults[label], Args: map[string]any{"CommandPath": "{{.CommandPath}}"}})
		template = strings.ReplaceAll(template, helpDefaults[label], translated)
	}
	template = strings.ReplaceAll(template, "Use \"", translator.Translate(corei18n.Message{ID: "cli.help.use_command", Default: "Use"})+" \"")
	template = strings.ReplaceAll(template, "for more information about a command.", translator.Translate(corei18n.Message{ID: "cli.help.more_information_tail", Default: "for more information about a command."}))
	return template
}

func normalizeID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, " ", ".")
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, "/", ".")
	return value
}

// ValidateCommandCoverage checks that every Cobra help string has a stable ID
// and both shipped languages contain that ID.
func ValidateCommandCoverage(root *cobra.Command, resourceSets ...fs.FS) error {
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	ids := make(map[string]struct{})
	for _, resources := range resourceSets {
		if err := fs.WalkDir(resources, ".", func(file string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.EqualFold(path.Ext(file), ".json") {
				return err
			}
			data, err := fs.ReadFile(resources, file)
			if err != nil {
				return err
			}
			var messages map[string]json.RawMessage
			if err := json.Unmarshal(data, &messages); err != nil {
				return fmt.Errorf("parse translation resource %q: %w", file, err)
			}
			for id := range messages {
				ids[id] = struct{}{}
			}
			return nil
		}); err != nil {
			return err
		}
	}

	var missing []string
	check := func(id string) {
		if _, ok := ids[id]; !ok {
			missing = append(missing, id)
		}
	}
	for _, group := range root.Groups() {
		check("cli.group." + normalizeID(group.ID))
	}
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		commandPath := normalizeID(strings.TrimSpace(strings.TrimPrefix(command.CommandPath(), root.Name())))
		shortID := "cli.command." + commandPath + ".short"
		if commandPath == "" {
			shortID = "cli.root.short"
		}
		if command.Parent() == root && command.GroupID == "extensions" {
			shortID = "cli.command.extension_child.short"
		}
		if command.Short != "" {
			check(shortID)
		}
		if command.Long != "" {
			longID := "cli.command." + commandPath + ".long"
			if commandPath == "" {
				longID = "cli.root.long"
			}
			check(longID)
		}
		if command.Example != "" {
			check("cli.command." + commandPath + ".example")
		}
		if command.Deprecated != "" {
			check("cli.command." + commandPath + ".deprecated")
		}
		if command.Annotations[deprecatedAnnotation] != "" {
			check("cli.command." + commandPath + ".deprecated")
		}
		command.InitDefaultHelpFlag()
		for _, flags := range []*pflag.FlagSet{command.LocalNonPersistentFlags(), command.PersistentFlags()} {
			flags.VisitAll(func(flag *pflag.Flag) {
				if flag.Name == "help" {
					check("cli.flags.help.description")
					return
				}
				flagPath := commandPath
				if command == root || flagPath == "" {
					flagPath = rootCommandID
				}
				check("cli.flags." + flagPath + "." + normalizeID(flag.Name) + ".description")
				if flag.Deprecated != "" || len(flag.Annotations[deprecatedAnnotation]) > 0 {
					check("cli.flags." + flagPath + "." + normalizeID(flag.Name) + ".deprecated")
				}
			})
		}
		for _, child := range command.Commands() {
			visit(child)
		}
	}
	visit(root)
	sort.Strings(missing)
	if len(missing) != 0 {
		return fmt.Errorf("missing translations for Cobra help: %s", strings.Join(missing, ", "))
	}
	return nil
}

var helpDefaults = map[string]string{
	"usage":                  "Usage:",
	"aliases":                "Aliases:",
	"examples":               "Examples:",
	"available_commands":     "Available Commands:",
	"additional_commands":    "Additional Commands:",
	"flags":                  "Flags:",
	"global_flags":           "Global Flags:",
	"additional_help_topics": "Additional help topics:",
	"more_information":       "Use \"{{.CommandPath}} [command] --help\" for more information about a command.",
}
