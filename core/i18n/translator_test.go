package i18n

import (
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestTranslatorNamedParametersAndEnglishFallback(t *testing.T) {
	zh, err := NewTranslator(Chinese)
	require.NoError(t, err)
	en, err := NewTranslator(English)
	require.NoError(t, err)
	message := Message{ID: "errors.config.invalid_range", Args: map[string]any{"Start": 110, "End": 100}}
	require.Equal(t, "消息范围无效：起始 ID 110 必须小于结束 ID 100。", zh.Translate(message))
	require.Equal(t, "Invalid message range: start ID 110 must be less than end ID 100.", en.Translate(message))
	require.Equal(t, "fallback 42", zh.Translate(Message{ID: "not.in.catalog", Default: "fallback {{.Value}}", Args: map[string]any{"Value": 42}}))
}

func TestConcurrentTranslatorsKeepTheirSelectedLanguage(t *testing.T) {
	zh, err := NewTranslator(Chinese)
	require.NoError(t, err)
	en, err := NewTranslator(English)
	require.NoError(t, err)
	message := Message{ID: "errors.interrupted"}
	var wg sync.WaitGroup
	results := make(chan string, 80)
	for range 40 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			results <- zh.Translate(message)
		}()
		go func() {
			defer wg.Done()
			results <- en.Translate(message)
		}()
	}
	wg.Wait()
	close(results)
	chinese, english := 0, 0
	for got := range results {
		switch got {
		case "操作已中断。":
			chinese++
		case "Interrupted.":
			english++
		default:
			t.Fatalf("unexpected translation %q", got)
		}
	}
	require.Equal(t, 40, chinese)
	require.Equal(t, 40, english)
}

func TestValidateResourcesDetectsMissingIDsAndParameters(t *testing.T) {
	broken := fs.FS(fstest.MapFS{
		"test.en.json": &fstest.MapFile{Data: []byte(`{"test.message":{"other":"Hi {{.Name}}"}}`)},
		"test.zh.json": &fstest.MapFile{Data: []byte(`{"test.message":{"other":"你好 {{.Count}}"}}`)},
	})
	require.ErrorContains(t, ValidateResources(broken), "inconsistent named parameters")
}
