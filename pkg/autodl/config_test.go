package autodl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLink(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Link
	}{
		{
			name: "public channel",
			raw:  "https://t.me/shunv667/5639",
			want: Link{Chat: "shunv667", MessageID: 5639},
		},
		{
			name: "comment link",
			raw:  "https://t.me/shunv667/5639?comment=158865",
			want: Link{Chat: "shunv667", MessageID: 5639, Comment: 158865},
		},
		{
			name: "private channel",
			raw:  "https://t.me/c/1697797156/151",
			want: Link{Chat: "1697797156", MessageID: 151},
		},
		{
			name: "private channel comment",
			raw:  "https://t.me/c/1697797156/151?comment=99",
			want: Link{Chat: "1697797156", MessageID: 151, Comment: 99},
		},
		{
			name: "topic link keeps the topic out of the message id",
			raw:  "https://t.me/iFreeKnow/45662/55005",
			want: Link{Chat: "iFreeKnow", MessageID: 45662},
		},
		{
			name: "missing scheme",
			raw:  "t.me/telegram/193",
			want: Link{Chat: "telegram", MessageID: 193},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLink(tt.raw)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseLinkErrors(t *testing.T) {
	for _, raw := range []string{
		"",
		"https://example.com/channel/1",
		"https://t.me/",
		"https://t.me/c/123",
		"https://t.me/channel/notanumber",
		"https://t.me/channel/1?comment=abc",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseLink(raw)
			assert.Error(t, err)
		})
	}
}

func TestLoadConfigPythonFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// the exact shape of python/config.json
	body := `{
  "namespace": "",
  "download_base": "downloads",
  "incremental": false,
  "jobs": [
    {
      "chat_url": "https://t.me/shunv667/5639",
      "comment": true,
      "start_comment": 158865,
      "end_comment": 159092,
      "subdir": "example_post_48334"
    }
  ]
}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	assert.Equal(t, "downloads", cfg.DownloadBase)
	assert.False(t, cfg.Incremental)
	require.Len(t, cfg.Jobs, 1)

	job := cfg.Jobs[0]
	assert.Equal(t, "https://t.me/shunv667/5639", job.ChatURL)
	assert.True(t, job.CommentMode())
	require.NotNil(t, job.StartComment)
	require.NotNil(t, job.EndComment)
	assert.Equal(t, 158865, *job.StartComment)
	assert.Equal(t, 159092, *job.EndComment)
	assert.Equal(t, filepath.Join("downloads", "example_post_48334"), job.Dir())
}

func TestLoadConfigIntCommentImpliesStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"jobs":[{"chat_url":"https://t.me/a/1","comment":100,"end_comment":110}]}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	job := cfg.Jobs[0]
	assert.True(t, job.CommentMode())
	require.NotNil(t, job.StartComment)
	assert.Equal(t, 100, *job.StartComment)
	assert.Equal(t, "downloads", cfg.DownloadBase)
}

func TestLoadConfigYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "download_base: out\njobs:\n  - chat_url: https://t.me/a/1\n    start_comment: 1\n    end_comment: 3\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.Jobs, 1)
	assert.Equal(t, "out", cfg.DownloadBase)
	assert.Equal(t, filepath.Join("out"), cfg.Jobs[0].Dir())
}

func TestLoadConfigErrors(t *testing.T) {
	dir := t.TempDir()

	t.Run("empty jobs", func(t *testing.T) {
		path := filepath.Join(dir, "empty.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"jobs":[]}`), 0o644))

		_, err := LoadConfig(path)
		assert.Error(t, err)
	})

	t.Run("inverted range", func(t *testing.T) {
		path := filepath.Join(dir, "range.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"jobs":[{"chat_url":"https://t.me/a/1","start_comment":10,"end_comment":5}]}`), 0o644))

		_, err := LoadConfig(path)
		assert.Error(t, err)
	})

	t.Run("missing range", func(t *testing.T) {
		path := filepath.Join(dir, "norange.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"jobs":[{"chat_url":"https://t.me/a/1"}]}`), 0o644))

		_, err := LoadConfig(path)
		assert.Error(t, err)
	})

	t.Run("incremental needs no range", func(t *testing.T) {
		path := filepath.Join(dir, "inc.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"jobs":[{"chat_url":"https://t.me/a/1","incremental":true}]}`), 0o644))

		_, err := LoadConfig(path)
		assert.NoError(t, err)
	})

	t.Run("global incremental needs no range", func(t *testing.T) {
		path := filepath.Join(dir, "global-inc.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"incremental":true,"jobs":[{"chat_url":"https://t.me/a/1"}]}`), 0o644))

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		assert.True(t, cfg.Jobs[0].UsesIncremental(cfg.Incremental))
	})

	t.Run("job can turn incremental off again", func(t *testing.T) {
		path := filepath.Join(dir, "local-off.json")
		body := `{"incremental":true,"jobs":[{"chat_url":"https://t.me/a/1","incremental":false}]}`
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

		_, err := LoadConfig(path)
		assert.Error(t, err, "a range job without a range must be rejected")
	})
}

func TestRangeIDs(t *testing.T) {
	start, end := 3, 7
	job := &Job{StartComment: &start, EndComment: &end}

	assert.Equal(t, []int{3, 4, 5, 6}, rangeIDs(job))
	assert.Nil(t, rangeIDs(&Job{}))
}

func TestResolveMode(t *testing.T) {
	comment := true
	job := &Job{Comment: comment}

	mode, err := job.ResolveMode(ModeAuto)
	require.NoError(t, err)
	assert.Equal(t, ModeComment, mode)

	mode, err = job.ResolveMode(ModeDirect)
	require.NoError(t, err)
	assert.Equal(t, ModeDirect, mode)

	direct := &Job{}
	mode, err = direct.ResolveMode(ModeAuto)
	require.NoError(t, err)
	assert.Equal(t, ModeDirect, mode)

	_, err = direct.ResolveMode("bogus")
	assert.Error(t, err)
}

// TestModeOverridesConfig checks that the resolved mode wins over the job's
// "comment" key, which is what makes --mode usable.
func TestModeOverridesConfig(t *testing.T) {
	job := &Job{Comment: true}

	mode, err := job.ResolveMode(ModeDirect)
	require.NoError(t, err)
	job.mode = mode
	assert.False(t, job.CommentMode())

	plain := &Job{}
	assert.False(t, plain.CommentMode())

	mode, err = plain.ResolveMode(ModeComment)
	require.NoError(t, err)
	plain.mode = mode
	assert.True(t, plain.CommentMode())
}

// TestCommentDialog covers the mode detection that decides which dialog the
// message ids belong to. A wrong answer turns every id into "deleted", which is
// exactly what happened when a python config with "comment": true was resolved
// against the channel instead of its discussion group.
func TestCommentDialog(t *testing.T) {
	yes, no := true, false

	cases := []struct {
		name string
		job  Job
		link Link
		want bool
	}{
		{
			name: "python comment job",
			job:  Job{Comment: yes, StartComment: ptr(158865), EndComment: ptr(159092)},
			link: Link{Chat: "shunv667", MessageID: 5639},
			want: true,
		},
		{
			name: "legacy int comment",
			job:  Job{Comment: 158865},
			link: Link{Chat: "shunv667", MessageID: 5639},
			want: true,
		},
		{
			name: "comment link",
			job:  Job{Comment: false},
			link: Link{Chat: "shunv667", MessageID: 5639, Comment: 158865},
			want: true,
		},
		{
			name: "explicit direct job",
			job:  Job{Comment: false},
			link: Link{Chat: "shunv667", MessageID: 5639},
			want: false,
		},
		{
			name: "no comment key at all",
			job:  Job{},
			link: Link{Chat: "shunv667"},
			want: false,
		},
		{
			name: "incremental job with comment off",
			job:  Job{Comment: no, Incremental: &yes},
			link: Link{Chat: "shunv667"},
			want: false,
		},
		{
			name: "resolved mode beats the config",
			job:  Job{Comment: yes, mode: ModeDirect},
			link: Link{Chat: "shunv667", MessageID: 5639},
			want: false,
		},
		{
			name: "mode comment without a config key",
			job:  Job{mode: ModeComment},
			link: Link{Chat: "shunv667", MessageID: 5639},
			want: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, commentDialog(&c.job, c.link))
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestFormatIDs(t *testing.T) {
	assert.Equal(t, "empty", formatIDs(nil))
	assert.Equal(t, "1-3 (3)", formatIDs([]int{1, 2, 3}))
	assert.Equal(t, "1...5 (3)", formatIDs([]int{1, 3, 5}))
}
