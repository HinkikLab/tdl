package console

import (
	"fmt"
	"os"
	"testing"

	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

func TestFormatErrorLocalizesKnownExternalTypes(t *testing.T) {
	translator, err := corei18n.NewTranslator(corei18n.Chinese)
	require.NoError(t, err)

	got := FormatError(fmt.Errorf("request: %w", tgerr.New(420, "FLOOD_WAIT_30")), translator)
	require.Equal(t, "request: Telegram 请求受到速率限制，请等待 30 秒后重试。", got)

	got = FormatError(&os.PathError{Op: "open", Path: "missing.yaml", Err: os.ErrNotExist}, translator)
	require.Contains(t, got, "找不到文件或路径：")
	require.Contains(t, got, "missing.yaml")
}

func TestFormatErrorKeepsUnknownCauseAndStructuredContext(t *testing.T) {
	translator, err := corei18n.NewTranslator(corei18n.Chinese)
	require.NoError(t, err)

	cause := fmt.Errorf("third-party detail")
	require.Equal(t, cause.Error(), FormatError(cause, translator))
	structured := diagnostic.Wrap("errors.cli.invalid_flags", map[string]any{"Reason": cause.Error()}, cause)
	require.Equal(t, "命令行参数无效：third-party detail", FormatError(structured, translator))
}
