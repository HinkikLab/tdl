package texpr

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	corei18n "github.com/iyear/tdl/core/i18n"
	appi18n "github.com/iyear/tdl/pkg/i18n"
)

func TestFieldDescriptionsLocalizeWithoutChangingExpressionPaths(t *testing.T) {
	getter := NewFieldsGetter(nil)
	fields, err := getter.Walk(EnvMessage{})
	require.NoError(t, err)
	zh, err := appi18n.New(corei18n.Chinese)
	require.NoError(t, err)
	english := getter.Sprint(fields, false)
	chinese := getter.SprintContext(corei18n.WithTranslator(context.Background(), zh), fields, false)
	require.Contains(t, english, "Media.Name: string # File name")
	require.Contains(t, chinese, "Media.Name: string # 文件名")
	require.Contains(t, chinese, "Message: string")
}
