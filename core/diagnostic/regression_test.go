package diagnostic

import (
	"context"
	"errors"
	"fmt"
	"testing"

	stackerrors "github.com/go-faster/errors"
	"github.com/stretchr/testify/require"

	corei18n "github.com/iyear/tdl/core/i18n"
)

func TestDescribePreservesOriginalChainStackAndAllJoinedBranches(t *testing.T) {
	zh, err := corei18n.NewTranslator(corei18n.Chinese)
	require.NoError(t, err)
	original := stackerrors.Wrap(context.DeadlineExceeded, "operation")
	wrapped := Describe(original, corei18n.Message{ID: "test.operation", Default: "操作：{{.Reason}}", Args: map[string]any{"Reason": context.DeadlineExceeded}})
	require.ErrorIs(t, wrapped, context.DeadlineExceeded)
	require.Equal(t, original.Error(), wrapped.Error())
	require.Equal(t, fmt.Sprintf("%+v", original), fmt.Sprintf("%+v", wrapped))
	require.Equal(t, "操作：操作超时", FormatError(wrapped, zh))
	first, second := New("errors.context.canceled", nil), New("errors.context.deadline", nil)
	joined := errors.Join(first, second)
	require.Equal(t, "操作已取消\n操作超时", FormatError(joined, zh))
	paired := fmt.Errorf("first=%w; second=%w", first, second)
	require.Equal(t, "first=操作已取消; second=操作超时", FormatError(paired, zh))
	require.Equal(t, "request: 操作已取消", FormatError(fmt.Errorf("request: %w", first), zh))
}

func TestMissingDescriptorFallsBackToOriginalAndKeepsTypedCause(t *testing.T) {
	zh, err := corei18n.NewTranslator(corei18n.Chinese)
	require.NoError(t, err)
	original := &customCause{detail: "opaque detail"}
	described := Describe(original, corei18n.Message{ID: "test.missing"})
	var target *customCause
	require.ErrorAs(t, described, &target)
	require.Same(t, original, target)
	require.Equal(t, original.Error(), FormatError(described, zh))
}

type customCause struct{ detail string }

func (c *customCause) Error() string { return c.detail }
