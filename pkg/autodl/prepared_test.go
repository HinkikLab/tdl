package autodl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/pkg/kv"
)

func preparedFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestPreparedRunPreservesSingleImmutableSnapshot(t *testing.T) {
	path := preparedFixture(t, `{"namespace":"configured","pool":0,"jobs":[{"chat_url":"https://t.me/course","follow_links":true,"subdir":"links"}]}`)
	p, err := Prepare(Options{ConfigPath: path, Namespace: "default"})
	require.NoError(t, err)
	require.Equal(t, "configured", p.EffectiveOptions().Namespace)
	require.Zero(t, p.EffectiveOptions().Pool)
	require.True(t, p.NeedsBotUpdates())
	require.NotNil(t, p.BotUpdates())
	require.NoError(t, os.WriteFile(path, []byte(`{"namespace":"changed","jobs":[]}`), 0o600))
	require.Equal(t, "configured", p.EffectiveOptions().Namespace)
	require.True(t, p.NeedsBotUpdates())
	_, err = Prepare(Options{ConfigPath: path})
	require.Error(t, err)
}

func TestPreparedRunNamespaceAndPerformancePrecedence(t *testing.T) {
	path := preparedFixture(t, `{"namespace":"configured","pool":16,"threads":3,"limit":2,"jobs":[{"chat_url":"https://t.me/course","start_comment":1,"end_comment":2}]}`)
	p, err := Prepare(Options{ConfigPath: path, Namespace: "explicit", NamespaceSet: true, PoolSize: 0, PoolSizeSet: true, Threads: 7})
	require.NoError(t, err)
	require.Equal(t, EffectiveOptions{Namespace: "explicit", Pool: 0, Threads: 7, Limit: 2}, p.EffectiveOptions())
	require.Equal(t, OptionOrigins{Namespace: "namespace override", Pool: "override", Threads: "override", Limit: "config"}, p.Origins())
	_, err = Prepare(Options{ConfigPath: path, OverlapSeconds: ptr(-10)})
	require.ErrorContains(t, err, "overlap_seconds")
}

func TestPreparedRunFreezesAbsoluteOutputPaths(t *testing.T) {
	t.Chdir(t.TempDir())
	path := preparedFixture(t, `{"jobs":[{"chat_url":"https://t.me/course","start_comment":1,"end_comment":2,"subdir":"one"}]}`)
	p, err := Prepare(Options{ConfigPath: path, Dir: "output", StateFile: "state.json"})
	require.NoError(t, err)
	output := p.cfg.Jobs[0].Dir()
	state := p.opts.StateFile
	require.True(t, filepath.IsAbs(output))
	require.True(t, filepath.IsAbs(state))
	require.Empty(t, p.opts.Dir)
	t.Chdir(t.TempDir())
	r := &Runner{cfg: p.cfg, opts: p.opts}
	require.Equal(t, state, r.statePath(&p.cfg.Jobs[0]))
	require.Equal(t, output, p.cfg.Jobs[0].Dir())
}

func TestOverlapZeroKeepsLegacyFallbackSemantics(t *testing.T) {
	for _, test := range []struct{ opts, job, global, want int }{{0, 120, 60, 120}, {0, 0, 60, 60}, {0, 0, 0, DefaultOverlapSeconds}, {30, 120, 60, 30}} {
		r := &Runner{opts: Options{OverlapSeconds: ptr(test.opts)}, cfg: &Config{OverlapSeconds: ptr(test.global)}}
		require.Equal(t, test.want, r.overlapSeconds(&Job{Overlap: ptr(test.job)}))
	}
}

func TestPreparedRunValidatesBeforeCreatingFiles(t *testing.T) {
	for _, test := range []struct{ field, reason string }{
		{`"topic_id":-1`, "topic_id"}, {`"reply_post_id":0`, "reply_post_id"},
		{`"comment":"true"`, "comment"}, {`"comment":1.5`, "comment"},
		{`"export_filter":"ID >"`, "export_filter"},
	} {
		t.Run(test.reason+test.field, func(t *testing.T) {
			path := preparedFixture(t, `{"incremental":true,"jobs":[{"chat_url":"https://t.me/course",`+test.field+`}]}`)
			_, err := Prepare(Options{ConfigPath: path})
			require.ErrorContains(t, err, test.reason)
			require.ErrorContains(t, err, path)
			require.ErrorContains(t, err, "Job: 1")
		})
	}
	path := preparedFixture(t, `{"jobs":[{"chat_url":"https://t.me/course","start_comment":1,"end_comment":2}]}`)
	for _, template := range []string{"{{", "{{.UnknownField}}", "../outside.bin"} {
		_, err := Prepare(Options{ConfigPath: path, Template: template})
		require.Error(t, err, template)
	}
}

func TestPreparedRunRejectsStateAndConstantOutputCollisions(t *testing.T) {
	path := preparedFixture(t, `{"state_file":"shared.json","jobs":[{"chat_url":"https://t.me/first","start_comment":1,"end_comment":2,"subdir":"one"},{"chat_url":"https://t.me/second","start_comment":1,"end_comment":2,"subdir":"two"}]}`)
	_, err := Prepare(Options{ConfigPath: path})
	require.ErrorContains(t, err, "also owned by job 1")
	path = preparedFixture(t, `{"jobs":[{"chat_url":"https://t.me/course","start_comment":1,"end_comment":3}]}`)
	_, err = Prepare(Options{ConfigPath: path, Template: "fixed.bin"})
	require.ErrorContains(t, err, "same output path")
}

func TestPreparedRunDoesNotBorrowMutableDTOValues(t *testing.T) {
	cfg := &Config{Namespace: "account", Jobs: []Job{{ChatURL: "https://t.me/course", Tag: "#notes", Tags: []string{"#extra"}, Subdir: "tag", Incremental: ptr(true)}}}
	require.NoError(t, cfg.Normalize())
	p, err := prepareConfig(cfg, Options{})
	require.NoError(t, err)
	cfg.Jobs[0].Tags[0] = "#other"
	*cfg.Jobs[0].Incremental = false
	require.Equal(t, []string{"#extra"}, p.cfg.Jobs[0].Tags)
	require.True(t, p.cfg.Jobs[0].UsesIncremental(false))
}

type preparedKV struct{ opened []string }

func (p *preparedKV) Name() string                  { return "fixture" }
func (p *preparedKV) MigrateTo() (kv.Meta, error)   { return nil, nil }
func (p *preparedKV) MigrateFrom(kv.Meta) error     { return nil }
func (p *preparedKV) Namespaces() ([]string, error) { return nil, nil }
func (p *preparedKV) Close() error                  { return nil }
func (p *preparedKV) Open(ns string) (storage.Storage, error) {
	p.opened = append(p.opened, ns)
	return preparedSession{}, nil
}

type preparedSession struct{}

func (preparedSession) Get(context.Context, string) ([]byte, error) { return []byte("logged-in"), nil }
func (preparedSession) Set(context.Context, string, []byte) error   { return nil }
func (preparedSession) Delete(context.Context, string) error        { return nil }

func TestAutoStartChecksTheSameExplicitNamespaceItWillExecute(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("config.json", []byte(`{"namespace":"configured","jobs":[{"chat_url":"https://t.me/course","start_comment":1,"end_comment":2}]}`), 0o600))
	for _, explicit := range []bool{true, false} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			engine := &preparedKV{}
			p, err := PrepareAutoStart(context.Background(), engine, Options{Namespace: "selected", NamespaceSet: explicit})
			require.NoError(t, err)
			require.NotNil(t, p)
			want := "configured"
			if explicit {
				want = "selected"
			}
			require.Equal(t, []string{want}, engine.opened)
			require.Equal(t, want, p.EffectiveOptions().Namespace)
		})
	}
}
