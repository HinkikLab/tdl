// Adapted from github.com/AlecAivazis/survey/v2 v2.3.7. See LICENSE.
package prompt

import (
	"bytes"

	"github.com/AlecAivazis/survey/v2/core"
	"github.com/AlecAivazis/survey/v2/terminal"
)

func paginate(pageSize int, choices []core.OptionAnswer, sel int) ([]core.OptionAnswer, int) {
	var start, end, cursor int

	if len(choices) < pageSize {
		// if we dont have enough options to fill a page
		start = 0
		end = len(choices)
		cursor = sel

	} else if sel < pageSize/2 {
		// if we are in the first half page
		start = 0
		end = pageSize
		cursor = sel

	} else if len(choices)-sel-1 < pageSize/2 {
		// if we are in the last half page
		start = len(choices) - pageSize
		end = len(choices)
		cursor = sel - start

	} else {
		// somewhere in the middle
		above := pageSize / 2
		below := pageSize - above

		cursor = pageSize / 2
		start = sel - above
		end = sel + below
	}

	// return the subset we care about and the index
	return choices[start:end], cursor
}

type IterableOpts interface {
	IterateOption(int, core.OptionAnswer) interface{}
}

func computeCursorOffset(tmpl string, data IterableOpts, opts []core.OptionAnswer, idx, tWidth int) int {
	tmpls, err := core.GetTemplatePair(tmpl)
	if err != nil {
		return 0
	}

	t := tmpls[0]

	renderOpt := func(ix int, opt core.OptionAnswer) string {
		var buf bytes.Buffer
		_ = t.ExecuteTemplate(&buf, "option", data.IterateOption(ix, opt))
		return buf.String()
	}

	offset := len(opts) - idx

	for i, o := range opts {
		if i < idx {
			continue
		}
		renderedOpt := renderOpt(i, o)
		valWidth := terminal.StringWidth(renderedOpt)
		if valWidth > tWidth {
			splitCount := valWidth / tWidth
			if valWidth%tWidth == 0 {
				splitCount -= 1
			}
			offset += splitCount
		}
	}

	return offset
}
