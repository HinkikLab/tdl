package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/go-faster/errors"
	"github.com/google/go-github/v62/github"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/extension"
)

var (
	ErrAlreadyUpToDate = diagnostic.Describe(errors.New("already up to date"), corei18n.Message{ID: "errors.message.already_up_to_date"})
	ErrOnlyGitHub      = diagnostic.Describe(errors.New("only GitHub extension can be upgraded by tdl"), corei18n.Message{ID: "errors.message.only_github_extension_can_be_upgraded_by_tdl"})
)

type Manager struct {
	dir    string
	http   *http.Client
	github *github.Client

	dryRun bool
}

func NewManager(dir string) *Manager {
	return &Manager{
		dir:    dir,
		http:   http.DefaultClient,
		github: newGhClient(http.DefaultClient),
		dryRun: false,
	}
}

func newGhClient(c *http.Client) *github.Client {
	ghToken := os.Getenv("GITHUB_TOKEN")
	if ghToken == "" {
		return github.NewClient(c)
	}
	return github.NewClient(c).WithAuthToken(ghToken)
}

func (m *Manager) SetDryRun(v bool) {
	m.dryRun = v
}

func (m *Manager) DryRun() bool {
	return m.dryRun
}

func (m *Manager) SetClient(client *http.Client) {
	m.http = client
	m.github = newGhClient(client)
}

func (m *Manager) Dispatch(ext Extension, args []string, env *extension.Env, stdin io.Reader, stdout, stderr io.Writer) (rerr error) {
	cmd := exec.Command(ext.Path(), args...)

	envFile, err := os.CreateTemp("", "*")
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "create temp"), corei18n.Message{ID: "errors.context.create_temp", Args: map[string]any{"Reason": err}})
	}
	defer func() { multierr.AppendInto(&rerr, os.Remove(envFile.Name())) }()

	envBytes, err := json.Marshal(env)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "marshal env"), corei18n.Message{ID: "errors.context.marshal_env", Args: map[string]any{"Reason": err}})
	}

	if _, err = envFile.Write(envBytes); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "write env to temp"), corei18n.Message{ID: "errors.context.write_env_to_temp", Args: map[string]any{"Reason": err}})
	}
	if err = envFile.Close(); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "close env file"), corei18n.Message{ID: "errors.context.close_env_file", Args: map[string]any{"Reason": err}})
	}

	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s", extension.EnvKey, envFile.Name()))
	cmd.Args = append([]string{Prefix + ext.Name()}, args...) // reset args[0] to extension name instead of binary path
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	return cmd.Run()
}

func (m *Manager) List(ctx context.Context, includeLatestVersion bool) ([]Extension, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "read dir entries"), corei18n.Message{ID: "errors.context.read_dir_entries", Args: map[string]any{"Reason": err}})
	}

	extensions := make([]Extension, 0, len(entries))
	for _, f := range entries {
		if !strings.HasPrefix(f.Name(), Prefix) {
			continue
		}

		if !f.IsDir() {
			continue
		}

		if _, err = os.Stat(filepath.Join(m.dir, f.Name(), manifestName)); err == nil {
			extensions = append(extensions, &githubExtension{
				baseExtension: baseExtension{path: filepath.Join(m.dir, f.Name(), f.Name())},
				client:        m.github,
			})
		} else {
			extensions = append(extensions, &localExtension{
				baseExtension: baseExtension{path: filepath.Join(m.dir, f.Name(), f.Name())},
			})
		}
	}

	if includeLatestVersion {
		m.populateLatestVersions(ctx, extensions)
	}

	return extensions, nil
}

// Upgrade only GitHub extension can be upgraded
func (m *Manager) Upgrade(ctx context.Context, ext Extension) error {
	switch e := ext.(type) {
	case *githubExtension:
		if !ext.UpdateAvailable(ctx) {
			return ErrAlreadyUpToDate
		}

		mf, err := e.loadManifest()
		if err != nil {
			return func() error {
				messageArg2 := e.Name()
				return diagnostic.Describe(errors.Wrapf(err, "load manifest of %q", messageArg2), corei18n.Message{ID: "errors.context.load_manifest_of_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", messageArg2), "Reason": err}})
			}()
		}

		if !m.dryRun {
			if err = m.Remove(ext); err != nil {
				return diagnostic.Describe(errors.Wrapf(err, "remove old version extension"), corei18n.Message{ID: "errors.context.remove_old_version_extension", Args: map[string]any{"Reason": err}})
			}
			if err = m.installGitHub(ctx, mf.Owner, mf.Repo, false); err != nil {
				return func() error {
					messageArg2 := e.Name()
					return diagnostic.Describe(errors.Wrapf(err, "install GitHub extension %q", messageArg2), corei18n.Message{ID: "errors.context.install_github_extension_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", messageArg2), "Reason": err}})
				}()
			}
		}

		return nil
	default:
		return ErrOnlyGitHub
	}
}

// Install installs an extension by target.
// Valid targets are:
// - GitHub: owner/repo
// - Local: path to executable.
func (m *Manager) Install(ctx context.Context, target string, force bool) error {
	// local
	if _, err := os.Stat(target); err == nil {
		return m.installLocal(target, force)
	}

	// github
	ownerRepo := strings.Split(target, "/")
	if len(ownerRepo) != 2 {
		return diagnostic.Describe(errors.Errorf("invalid target: %q", target), corei18n.Message{ID: "errors.message.invalid_target_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", target)}})
	}

	return m.installGitHub(ctx, ownerRepo[0], ownerRepo[1], force)
}

func (m *Manager) installLocal(path string, force bool) error {
	src, err := os.Lstat(path)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "source extension stat"), corei18n.Message{ID: "errors.context.source_extension_stat", Args: map[string]any{"Reason": err}})
	}
	if !src.Mode().IsRegular() {
		return diagnostic.Describe(errors.Errorf("invalid src extension: %q, only regular file is allowed", path), corei18n.Message{ID: "errors.message.invalid_src_extension_value_only_regular_file_is_allowed", Args: map[string]any{"Arg1": fmt.Sprintf("%q", path)}})
	}

	name := src.Name()
	if !strings.HasPrefix(name, Prefix) {
		name = Prefix + name
	}

	targetDir := filepath.Join(m.dir, strings.TrimSuffix(name, filepath.Ext(name)))
	binPath := filepath.Join(targetDir, name)
	if err = m.maybeExist(binPath, force); err != nil {
		return err
	}

	if !m.dryRun {
		if err = os.MkdirAll(targetDir, 0o755); err != nil {
			return diagnostic.Describe(errors.Wrapf(err, "create target dir %q for extension %q", targetDir, name), corei18n.Message{ID: "errors.context.create_target_dir_value_for_extension_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", targetDir), "Arg2": fmt.Sprintf("%q", name), "Reason": err}})
		}

		if err = copyRegularFile(path, binPath); err != nil {
			return diagnostic.Describe(errors.Wrapf(err, "install local extension: %q", path), corei18n.Message{ID: "errors.context.install_local_extension_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", path), "Reason": err}})
		}
	}

	return nil
}

func (m *Manager) installGitHub(ctx context.Context, owner, repo string, force bool) (rerr error) {
	if !strings.HasPrefix(repo, Prefix) {
		return diagnostic.Describe(errors.Errorf("invalid repo name: %q, should start with %q", repo, Prefix), corei18n.Message{ID: "errors.message.invalid_repo_name_value_should_start_with_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", repo), "Arg2": fmt.Sprintf("%q", Prefix)}})
	}

	platform, ext := platformBinaryName()

	targetDir := filepath.Join(m.dir, repo)
	binPath := filepath.Join(targetDir, repo) + ext
	if err := m.maybeExist(binPath, force); err != nil {
		return err
	}

	release, _, err := m.github.Repositories.GetLatestRelease(ctx, owner, repo)
	if err != nil {
		return diagnostic.Describe(errors.Wrapf(err, "get latest release of %s/%s", owner, repo), corei18n.Message{ID: "errors.context.get_latest_release_of_value_value", Args: map[string]any{"Arg1": owner, "Arg2": repo, "Reason": err}})
	}

	// match binary name
	var asset *github.ReleaseAsset
	for _, a := range release.Assets {
		if strings.HasSuffix(a.GetName(), platform+ext) {
			asset = a
			break
		}
	}

	if asset == nil {
		return func() error {
			messageArg1 := platform + ext
			messageArg2 := release.GetHTMLURL()
			return diagnostic.Describe(errors.Errorf("no matched binary(%s) found in the release(%s)", messageArg1, messageArg2), corei18n.Message{ID: "errors.message.no_matched_binary_value_found_in_the_release_value", Args: map[string]any{"Arg1": messageArg1, "Arg2": messageArg2}})
		}()
	}

	if !m.dryRun {
		if err = os.MkdirAll(targetDir, 0o755); err != nil {
			return diagnostic.Describe(errors.Wrapf(err, "create target dir %q for extension %s/%s", targetDir, owner, repo), corei18n.Message{ID: "errors.context.create_target_dir_value_for_extension_value_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", targetDir), "Arg2": owner, "Arg3": repo, "Reason": err}})
		}

		if err = m.downloadGitHubAsset(ctx, owner, repo, asset, binPath); err != nil {
			return func() error {
				messageArg2 := asset.GetBrowserDownloadURL()
				return diagnostic.Describe(errors.Wrapf(err, "download github asset %s", messageArg2), corei18n.Message{ID: "errors.context.download_github_asset_value", Args: map[string]any{"Arg1": messageArg2, "Reason": err}})
			}()
		}
	}

	mf := &manifest{
		Owner: owner,
		Repo:  repo,
		Tag:   release.GetTagName(),
	}

	mfb, err := json.Marshal(mf)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "marshal manifest"), corei18n.Message{ID: "errors.context.marshal_manifest", Args: map[string]any{"Reason": err}})
	}

	if !m.dryRun {
		if err = os.WriteFile(filepath.Join(targetDir, manifestName), mfb, 0o644); err != nil {
			return diagnostic.Describe(errors.Wrapf(err, "write manifest to %s", targetDir), corei18n.Message{ID: "errors.context.write_manifest_to_value", Args: map[string]any{"Arg1": targetDir, "Reason": err}})
		}
	}

	return nil
}

func (m *Manager) maybeExist(binPath string, force bool) error {
	targetDir := filepath.Dir(binPath)
	extName := filepath.Base(targetDir)

	if _, err := os.Lstat(binPath); err != nil {
		return nil
	}

	if !force {
		return diagnostic.Describe(errors.Errorf("extension already exists, please remove it first"), corei18n.Message{ID: "errors.message.extension_already_exists_please_remove_it_first"})
	}

	// force remove
	if !m.dryRun {
		if err := os.RemoveAll(targetDir); err != nil {
			return diagnostic.Describe(errors.Wrapf(err, "remove existing extension %q", extName), corei18n.Message{ID: "errors.context.remove_existing_extension_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", extName), "Reason": err}})
		}
	}

	return nil
}

// Remove removes an extension by name(without prefix).
func (m *Manager) Remove(ext Extension) error {
	target := Prefix + ext.Name()
	targetDir := filepath.Join(m.dir, target)
	if _, err := os.Lstat(targetDir); os.IsNotExist(err) {
		return diagnostic.Describe(errors.Errorf("no extension found: %s", targetDir), corei18n.Message{ID: "errors.message.no_extension_found_value", Args: map[string]any{"Arg1": targetDir}})
	}

	if !m.dryRun {
		return os.RemoveAll(targetDir)
	}

	return nil
}

func (m *Manager) populateLatestVersions(ctx context.Context, exts []Extension) {
	wg := &sync.WaitGroup{}
	for _, ext := range exts {
		wg.Add(1)
		go func(e Extension) {
			defer wg.Done()
			e.LatestVersion(ctx)
		}(ext)
	}
	wg.Wait()
}

func (m *Manager) downloadGitHubAsset(ctx context.Context, owner, repo string, asset *github.ReleaseAsset, dst string) (rerr error) {
	readCloser, _, err := m.github.Repositories.DownloadReleaseAsset(ctx, owner, repo, asset.GetID(), m.http)
	if err != nil {
		return func() error {
			messageArg2 := asset.GetName()
			return diagnostic.Describe(errors.Wrapf(err, "download release asset %s", messageArg2), corei18n.Message{ID: "errors.context.download_release_asset_value", Args: map[string]any{"Arg1": messageArg2, "Reason": err}})
		}()
	}
	defer multierr.AppendInvoke(&rerr, multierr.Close(readCloser))

	file, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return diagnostic.Describe(errors.Wrapf(err, "open file %s", dst), corei18n.Message{ID: "errors.context.open_file_value", Args: map[string]any{"Arg1": dst, "Reason": err}})
	}
	defer multierr.AppendInvoke(&rerr, multierr.Close(file))

	if _, err = io.Copy(file, readCloser); err != nil {
		return diagnostic.Describe(errors.Wrapf(err, "copy http body to %s", dst), corei18n.Message{ID: "errors.context.copy_http_body_to_value", Args: map[string]any{"Arg1": dst, "Reason": err}})
	}
	return nil
}

func copyRegularFile(src, dst string) (rerr error) {
	r, err := os.Open(src)
	if err != nil {
		return diagnostic.Describe(errors.Wrapf(err, "open src %s", src), corei18n.Message{ID: "errors.context.open_src_value", Args: map[string]any{"Arg1": src, "Reason": err}})
	}
	defer multierr.AppendInvoke(&rerr, multierr.Close(r))

	info, err := r.Stat()
	if err != nil {
		return diagnostic.Describe(errors.Wrapf(err, "stat file %s", src), corei18n.Message{ID: "errors.context.stat_file_value", Args: map[string]any{"Arg1": src, "Reason": err}})
	}
	if !info.Mode().IsRegular() {
		return diagnostic.Describe(errors.Errorf("invalid source file: %q, only regular file is allowed", src), corei18n.Message{ID: "errors.message.invalid_source_file_value_only_regular_file_is_allowed", Args: map[string]any{"Arg1": fmt.Sprintf("%q", src)}})
	}

	w, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666|info.Mode()&0o777)
	if err != nil {
		return diagnostic.Describe(errors.Wrapf(err, "open dst %s", dst), corei18n.Message{ID: "errors.context.open_dst_value", Args: map[string]any{"Arg1": dst, "Reason": err}})
	}
	defer multierr.AppendInvoke(&rerr, multierr.Close(w))

	if _, err = io.Copy(w, r); err != nil {
		return diagnostic.Describe(errors.Wrapf(err, "copy file %s to %s", src, dst), corei18n.Message{ID: "errors.context.copy_file_value_to_value", Args: map[string]any{"Arg1": src, "Arg2": dst, "Reason": err}})
	}
	return nil
}

func platformBinaryName() (string, string) {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}

	arch := runtime.GOARCH
	switch arch {
	case "arm":
		if goarm := extractGOARM(); goarm != "" {
			arch += "v" + goarm
		}
	}

	return fmt.Sprintf("%s-%s", runtime.GOOS, arch), ext
}

func extractGOARM() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}

	for _, setting := range info.Settings {
		if setting.Key == "GOARM" {
			return setting.Value
		}
	}

	return ""
}
