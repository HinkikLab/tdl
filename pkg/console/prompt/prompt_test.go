package prompt

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/core"
	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	appi18n "github.com/iyear/tdl/pkg/i18n"
)

type bufferFile struct{ bytes.Buffer }

func (*bufferFile) Fd() uintptr { return ^uintptr(0) }

func TestSelectKeyboardFilteringAndEmptyResultInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Survey's Windows rune reader requires a console handle; buffer input is exercised on Unix")
	}
	zh, err := appi18n.New(corei18n.Chinese)
	require.NoError(t, err)
	ctx := corei18n.WithTranslator(context.Background(), zh)
	for _, tc := range []struct {
		input, want string
		interrupted bool
	}{
		{"\x1b[B\r", "second", false},
		{"sec\r", "second", false},
		{"unmatched\x04", "", true},
	} {
		in, out := &bufferFile{}, &bufferFile{}
		in.WriteString(tc.input)
		var answer string
		err := AskOne(ctx, &Select{Message: "Pick", Options: []string{"first", "second"}}, &answer, survey.WithStdio(in, out, out))
		if tc.interrupted {
			require.ErrorIs(t, err, terminal.InterruptErr)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.want, answer)
		}
		require.Contains(t, out.String(), "使用方向键移动，输入文本筛选")
	}
}

func TestPromptRenderingPreservesUserTextAndValidationLocalizes(t *testing.T) {
	zh, err := appi18n.New(corei18n.Chinese)
	require.NoError(t, err)
	config := &PromptConfig{HelpInput: "?", HideCharacter: '*'}
	userText := "for help (default data) {{.Name}}"
	r := &Renderer{translator: zh}
	data := PasswordTemplateData{Password: Password{Message: userText, Help: userText}, Config: config}
	text, _, err := core.RunTemplate(r.localizeTemplate(PasswordQuestionTemplate), data)
	require.NoError(t, err)
	require.Contains(t, text, userText)
	require.Contains(t, text, "查看帮助")
	out := &bufferFile{}
	r.WithStdio(terminal.Stdio{In: &bufferFile{}, Out: out, Err: out})
	require.NoError(t, r.Error(config, Required("")))
	require.Contains(t, out.String(), "回答无效：必须填写一个值")
	require.NoError(t, Required(userText))
	c := Confirm{Renderer: *r, Message: userText}
	require.NoError(t, c.Cleanup(config, true))
	require.Contains(t, out.String(), userText)
	require.Contains(t, out.String(), "是")
	_, err = (&Select{Options: []string{"first"}, Default: -1}).Prompt(config)
	require.Error(t, err)
	require.Contains(t, diagnostic.FormatError(err, zh), "默认索引 -1")
	config.PageSize = 7
	config.Filter = func(filter, option string, _ int) bool { return strings.Contains(option, filter) }
	s := &Select{Renderer: Renderer{translator: zh}, Message: userText, Options: []string{"first", "second"}}
	s.WithStdio(terminal.Stdio{In: &bufferFile{}, Out: out, Err: out})
	require.False(t, s.OnChange(terminal.KeyArrowDown, config))
	require.True(t, s.OnChange(terminal.KeyEnter, config))
	require.Equal(t, 1, s.selectedIndex)
	for _, key := range "sec" {
		require.False(t, s.OnChange(key, config))
	}
	require.True(t, s.OnChange(terminal.KeyEnter, config))
	require.Equal(t, "second", s.filterOptions(config)[s.selectedIndex].Value)
	require.Contains(t, out.String(), "使用方向键移动，输入文本筛选")
}

func TestPromptTemplateLanguagesAreIsolated(t *testing.T) {
	for _, lang := range []corei18n.Language{corei18n.Chinese, corei18n.English} {
		t.Run(string(lang), func(t *testing.T) {
			t.Parallel()
			translator, err := appi18n.New(lang)
			require.NoError(t, err)
			r := Renderer{translator: translator}
			for range 20 {
				translated := r.localizeTemplate(SelectQuestionTemplate)
				require.Equal(t, lang == corei18n.Chinese, strings.Contains(translated, "使用方向键"))
				require.Contains(t, survey.SelectQuestionTemplate, "Use arrows to move, type to filter")
			}
		})
	}
}
