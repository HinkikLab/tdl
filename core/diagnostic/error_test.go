package diagnostic

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	corei18n "github.com/iyear/tdl/core/i18n"
)

func TestErrorKeepsKindCauseAndLocalizedPresentation(t *testing.T) {
	kind := errors.New("range sentinel")
	cause := errors.New("source parser detail")
	err := WithKind(Wrap("errors.config.invalid_range", map[string]any{"Start": 110, "End": 100}, cause), kind)
	require.ErrorIs(t, err, kind)
	require.ErrorIs(t, err, cause)
	require.Equal(t, "Invalid message range: start ID 110 must be less than end ID 100.: source parser detail", err.Error())
	zh, loadErr := corei18n.NewTranslator(corei18n.Chinese)
	require.NoError(t, loadErr)
	require.Equal(t, "消息范围无效：起始 ID 110 必须小于结束 ID 100。: source parser detail", FormatError(err, zh))
}
