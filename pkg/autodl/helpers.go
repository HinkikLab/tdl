package autodl

import (
	"context"
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/go-faster/errors"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/messages"
)

// confirm asks the user a yes/no question.
func (r *Runner) confirm(ctx context.Context, question string, def bool) bool {
	if r.opts.Yes {
		color.Yellow("%s", console.Translate(ctx, messages.BatchAutoConfirm(question)))
		return true
	}

	return askConfirm(ctx, question, def)
}

// rangeIDs expands the start_comment/end_comment range, end excluded.
func rangeIDs(job *Job) []int {
	if job.StartComment == nil || job.EndComment == nil {
		return nil
	}

	start, end := *job.StartComment, *job.EndComment
	if end <= start {
		return nil
	}

	out := make([]int, 0, end-start)
	for id := start; id < end; id++ {
		out = append(out, id)
	}

	return out
}

// formatIDs renders an id list the way the python script displayed it.
func formatIDs(ids []int) string {
	if len(ids) == 0 {
		return "empty"
	}

	first, last := ids[0], ids[len(ids)-1]
	if last-first+1 == len(ids) {
		return fmt.Sprintf("%d-%d (%d)", first, last, len(ids))
	}

	return fmt.Sprintf("%d...%d (%d)", first, last, len(ids))
}

func pick(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}

	return values[len(values)-1]
}

func num(v *int) int {
	if v == nil {
		return 0
	}

	return *v
}

// askConfirm prints a y/n question and reads the answer from stdin.
func askConfirm(ctx context.Context, question string, def bool) bool {
	select {
	case <-ctx.Done():
		return def
	default:
	}

	hint := "y/N"
	if def {
		hint = "Y/n"
	}

	fmt.Printf("%s (%s): ", color.YellowString(question), hint)

	var answer string
	if _, err := fmt.Scanln(&answer); err != nil {
		return def
	}

	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}

// Mode selects how the message ids of a job are interpreted.
const (
	// ModeAuto detects the mode from the url and config, like the python script.
	ModeAuto = "auto"
	// ModeComment treats every id as a comment id (?comment=N).
	ModeComment = "comment"
	// ModeDirect treats every id as a message id in the chat itself.
	ModeDirect = "direct"
)

// ResolveMode applies --mode to a job.
func (j *Job) ResolveMode(mode string) (string, error) {
	switch strings.ToLower(mode) {
	case "", ModeAuto:
		if j.CommentMode() {
			return ModeComment, nil
		}
		return ModeDirect, nil
	case ModeComment, ModeDirect:
		return strings.ToLower(mode), nil
	default:
		return "", diagnostic.Describe(errors.Errorf("invalid mode %q", mode), corei18n.Message{ID: "errors.message.invalid_mode_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", mode)}})
	}
}

func formatIDsContext(ctx context.Context, ids []int) string {
	if len(ids) == 0 {
		return console.Translate(ctx, corei18n.Message{ID: "batch.empty", Default: "empty"})
	}
	return formatIDs(ids)
}
