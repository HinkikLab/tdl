package autodl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExampleConfigCoversBatchModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	require.NoError(t, WriteExampleConfig(path, false))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, json.Valid(b), "examples must be strict JSON, including their annotations")
	cfg, err := LoadConfig(path)
	require.NoError(t, err, "every generated job must pass the real batch parser")
	require.Len(t, cfg.Jobs, 11)

	var raw struct {
		Comment []string `json:"_comment"`
		Jobs    []struct {
			Comment string `json:"_comment"`
		} `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal(b, &raw))
	require.NotEmpty(t, raw.Comment)
	seen := map[string]bool{}
	for i, job := range cfg.Jobs {
		require.NotEmpty(t, raw.Jobs[i].Comment)
		require.NotEmpty(t, job.Subdir)
		require.False(t, seen[job.Subdir], "jobs must not collide in resume state")
		seen[job.Subdir] = true
	}
	require.False(t, cfg.Jobs[0].CommentMode())
	require.Equal(t, []int{100, 101, 102, 103, 104, 105, 106, 107, 108, 109}, rangeIDs(&cfg.Jobs[0]))
	require.True(t, cfg.Jobs[1].CommentMode())
	for _, i := range []int{2, 3, 4} {
		require.True(t, cfg.Jobs[i].UsesIncremental(cfg.Incremental))
	}
	require.Equal(t, 200, *cfg.Jobs[3].TopicID)
	require.Equal(t, 900, *cfg.Jobs[4].ReplyPostID)
	require.True(t, cfg.Jobs[4].CommentMode())
	require.Equal(t, "https://t.me/example_discussion", cfg.Jobs[4].Chat)
	for _, i := range []int{5, 6, 7} {
		require.True(t, cfg.Jobs[i].IsTagJob())
	}
	require.Equal(t, "any", cfg.Jobs[6].TagMatch)
	require.True(t, cfg.Jobs[5].WritesMetadata(cfg.WriteMetadata))
	require.False(t, cfg.Jobs[6].WritesMetadata(cfg.WriteMetadata))
	require.Equal(t, "all", cfg.Jobs[7].TagMatch)
	for _, i := range []int{8, 9, 10} {
		require.True(t, cfg.Jobs[i].FollowLinks)
		require.True(t, *cfg.Jobs[i].LinkOptions.CleanupBotMessages)
	}
	require.True(t, cfg.Jobs[7].UsesIncremental(false))
	require.True(t, cfg.Jobs[9].UsesIncremental(false))
	require.Zero(t, cfg.Jobs[7].MaxPosts)
	require.Zero(t, cfg.Jobs[9].MaxPosts)
	require.False(t, cfg.Jobs[8].UsesIncremental(true))
	require.False(t, cfg.Jobs[10].UsesIncremental(true))
	require.Equal(t, 100, *cfg.Jobs[6].StartComment)
	require.NotNil(t, cfg.Jobs[10].StartComment)
	require.True(t, cfg.Jobs[10].IsTagJob())
}

func TestWriteExampleConfigPreservesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte("user configuration\n")
	require.NoError(t, os.WriteFile(path, original, 0o644))
	require.ErrorContains(t, WriteExampleConfig(path, false), "--force")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, b)
	require.NoError(t, WriteExampleConfig(path, true))
	_, err = LoadConfig(path)
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1, "temporary replacement files must be removed")
}

func TestWriteExampleConfigInvalidDestination(t *testing.T) {
	require.ErrorContains(t, WriteExampleConfig(" ", false), "empty")
	dir := t.TempDir()
	marker := filepath.Join(dir, "keep.txt")
	require.NoError(t, os.WriteFile(marker, []byte("keep"), 0o644))
	require.Error(t, WriteExampleConfig(dir, true))
	b, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "keep", string(b))
	entries, err := os.ReadDir(filepath.Dir(dir))
	require.NoError(t, err)
	for _, entry := range entries {
		require.NotContains(t, entry.Name(), ".tdl-config-")
	}
}

func TestBatchDocumentationConfigs(t *testing.T) {
	for _, language := range []string{"en", "zh"} {
		t.Run(language, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "..", "docs", "content", language, "guide", "batch.md"))
			require.NoError(t, err)
			blocks := regexp.MustCompile("(?s)```json\\r?\\n(.*?)```").FindAllSubmatch(b, -1)
			require.NotEmpty(t, blocks)
			for _, block := range blocks {
				var value map[string]any
				require.NoError(t, json.Unmarshal(block[1], &value))
				if _, config := value["jobs"]; !config {
					continue
				}
				path := filepath.Join(t.TempDir(), "config.json")
				require.NoError(t, os.WriteFile(path, block[1], 0o600))
				_, err := LoadConfig(path)
				require.NoError(t, err, string(block[1]))
			}
		})
	}
}
