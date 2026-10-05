package extension

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/logctx"
)

func TestExtensionLanguageProtocolResourcesAndHandlerContext(t *testing.T) {
	for _, tc := range []struct{ language, want string }{{"zh", "zh"}, {"", "en"}, {"auto", "en"}, {"unsupported", ""}} {
		t.Run(tc.language, func(t *testing.T) {
			t.Setenv("TDL_LANGUAGE", "en")
			env := Env{Name: "sample", DataDir: t.TempDir(), AppID: 1, AppHash: "hash", Language: tc.language}
			data, err := json.Marshal(env)
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "env.json")
			require.NoError(t, os.WriteFile(path, data, 0o600))
			t.Setenv(EnvKey, path)
			logger := zap.NewNop()
			ext, _, err := buildExtension(context.Background(), Options{Logger: logger})
			if tc.want == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, ext.Config().Language)
			ctx := extensionContext(context.Background(), ext)
			require.Same(t, ext.Translator(), corei18n.FromContext(ctx))
			require.Same(t, logger, logctx.From(ctx))
		})
	}
	foreign := fstest.MapFS{"sample.en.json": &fstest.MapFile{Data: []byte(`{"errors.foreign":{"other":"foreign"}}`)}}
	require.ErrorContains(t, corei18n.ValidateNamespace("ext.sample.", foreign), "must use namespace")
	owned := fstest.MapFS{
		"sample.en.json": &fstest.MapFile{Data: []byte(`{"ext.sample.ready":{"other":"ready"}}`)},
		"sample.zh.json": &fstest.MapFile{Data: []byte(`{"ext.sample.ready":{"other":"就绪"}}`)},
	}
	require.NoError(t, corei18n.ValidateNamespace("ext.sample.", owned))
	translator, err := corei18n.NewTranslator(corei18n.Chinese, corei18n.CoreResources(), owned)
	require.NoError(t, err)
	require.Equal(t, "就绪", translator.Translate(corei18n.Message{ID: "ext.sample.ready"}))
}
