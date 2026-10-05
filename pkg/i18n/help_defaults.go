package i18n

import (
	"strings"

	corei18n "github.com/iyear/tdl/core/i18n"
)

func localizeDefaults(text string, translator corei18n.Translator) string {
	if translator == nil {
		return text
	}
	var out strings.Builder
	for {
		start := strings.Index(text, " (default ")
		if start < 0 {
			out.WriteString(text)
			return out.String()
		}
		valueStart := start + len(" (default ")
		end, depth, quoted, escaped := valueStart, 1, false, false
		for ; end < len(text); end++ {
			char := text[end]
			if char == '\n' || char == '\r' {
				break
			}
			if escaped {
				escaped = false
				continue
			}
			if quoted && char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				quoted = !quoted
				continue
			}
			if quoted {
				continue
			}
			if char == '(' {
				depth++
			}
			if char == ')' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if end >= len(text) || depth != 0 {
			out.WriteString(text)
			return out.String()
		}
		value := text[valueStart:end]
		label := translator.Translate(corei18n.Message{
			ID:      "cli.help.default_value",
			Default: "default {{.Value}}",
			Args:    map[string]any{"Value": value},
		})
		out.WriteString(text[:start])
		if translator.Language() == corei18n.Chinese {
			out.WriteString("（" + label + "）")
		} else {
			out.WriteString(" (" + label + ")")
		}
		text = text[end+1:]
	}
}
