package autodl

import (
	"fmt"
	"strings"
)

// ConfigError keeps actionable configuration context separate from call stacks.
type ConfigError struct {
	Path    string
	Job     int // one-based; zero denotes a top-level error
	ChatURL string
	Err     error
}

func (e *ConfigError) context() string {
	var b strings.Builder
	if e.Path != "" {
		fmt.Fprintf(&b, "config \"%s\" is invalid", e.Path)
	} else {
		b.WriteString("invalid batch configuration")
	}
	if e.Job > 0 {
		fmt.Fprintf(&b, "\n  Job: %d", e.Job)
	}
	if e.ChatURL != "" {
		fmt.Fprintf(&b, "\n  Chat: %s", e.ChatURL)
	}
	return b.String()
}

func (e *ConfigError) Error() string {
	return e.context() + "\n  Reason: " + strings.ReplaceAll(e.Err.Error(), "\n", "\n          ")
}

func (e *ConfigError) Unwrap() error { return e.Err }

// Format preserves the cause's source information when --debug requests %+v.
func (e *ConfigError) Format(s fmt.State, verb rune) {
	if verb == 'v' && s.Flag('+') {
		fmt.Fprintf(s, "%s\n  Cause:\n%+v", e.context(), e.Err)
		return
	}
	fmt.Fprint(s, e.Error())
}
