package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"atomicgo.dev/isadmin"
	"github.com/Masterminds/semver/v3"
	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/google/go-github/v62/github"
	"github.com/spf13/viper"
	"golang.org/x/net/proxy"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/util/netutil"
	"github.com/iyear/tdl/pkg/console"
	localizedprompt "github.com/iyear/tdl/pkg/console/prompt"
	"github.com/iyear/tdl/pkg/consts"
	"github.com/iyear/tdl/pkg/messages"
)

type Options struct {
	Yes    bool
	DryRun bool   // report availability without downloading anything
	Target string // install a specific release tag instead of the latest (e.g. v0.20.4)
	Force  bool   // reinstall even when up to date; also allows downgrades
}

// Run performs a self-update: checks the latest GitHub release, downloads the
// matching archive, verifies its checksum and atomically replaces the current
// binary.
func Run(ctx context.Context, opts Options) (rerr error) {
	if !isadmin.Check() {
		color.Red("%s", console.Translate(ctx, messages.UpdateAdminRequired()))
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "get executable path"), corei18n.Message{ID: "errors.context.get_executable_path", Args: map[string]any{"Reason": err}})
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "resolve executable path"), corei18n.Message{ID: "errors.context.resolve_executable_path", Args: map[string]any{"Reason": err}})
	}

	if strings.Contains(filepath.ToSlash(exe), "/Cellar/") {
		color.Yellow("%s", console.Translate(ctx, messages.UpdateHomebrew()))
		return nil
	}

	dialer, err := netutil.NewProxy(viper.GetString(consts.FlagProxy))
	if err != nil {
		dialer = proxy.Direct
	}

	release, err := fetchRelease(ctx, opts.Target, dialer)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "fetch release"), corei18n.Message{ID: "errors.context.fetch_release", Args: map[string]any{"Reason": err}})
	}

	tag := release.GetTagName()

	fmt.Println(color.BlueString("%s", console.Translate(ctx, messages.UpdateCurrentVersion(consts.Version))))
	if opts.Target != "" {
		fmt.Println(color.BlueString("%s", console.Translate(ctx, messages.UpdateTargetVersion(tag))))
	} else {
		fmt.Println(color.BlueString("%s", console.Translate(ctx, messages.UpdateLatestVersion(tag))))
	}

	switch needsUpdate(consts.Version, tag) {
	case updateNo:
		if !opts.Force {
			color.Green("%s", console.Translate(ctx, messages.UpdateAlreadyLatest()))
			return nil
		}
		color.Yellow("%s", console.Translate(ctx, messages.UpdateForce(tag)))
	case updateUnknown:
		color.Yellow("%s", console.Translate(ctx, messages.UpdateUnrecognized(consts.Version, tag)))
	default:
		// updateYes: proceed with the update below.
	}

	if opts.DryRun {
		color.Green("%s", console.Translate(ctx, messages.UpdateAvailable(tag)))
		return nil
	}

	if !opts.Yes {
		ok := false

		if err = localizedprompt.AskOne(ctx, &localizedprompt.Confirm{
			Message: console.Translate(ctx, messages.UpdateConfirm(tag)),
		}, &ok); err != nil {
			return diagnostic.Describe(errors.Wrap(err, "confirm (use --yes to skip the prompt)"), corei18n.Message{ID: "errors.context.confirm_use_yes_to_skip_the_prompt", Args: map[string]any{"Reason": err}})
		}

		if !ok {
			color.Red("%s", console.Translate(ctx, messages.UpdateAborted()))
			return nil
		}
	}

	goarm := goARM()
	name, ok := assetName(runtime.GOOS, runtime.GOARCH, goarm)
	if !ok {
		return diagnostic.Describe(errors.Errorf("no release assets for platform %s/%s/%s", runtime.GOOS, runtime.GOARCH, goarm), corei18n.Message{ID: "errors.message.no_release_assets_for_platform_value_value_value", Args: map[string]any{"Arg1": runtime.GOOS, "Arg2": runtime.GOARCH, "Arg3": goarm}})
	}

	asset, err := findAsset(release, name)
	if err != nil {
		return err
	}
	sumsAsset, err := findAsset(release, checksumAssetName)
	if err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "tdl-update-")
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "create temp dir"), corei18n.Message{ID: "errors.context.create_temp_dir", Args: map[string]any{"Reason": err}})
	}
	defer func() { rerr = multiErr(rerr, os.RemoveAll(tmp)) }()

	color.Cyan("%s", console.Translate(ctx, messages.UpdateDownloading(asset.GetName())))
	archivePath, _, err := download(ctx, asset.GetBrowserDownloadURL(), filepath.Join(tmp, name), dialer)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "download archive"), corei18n.Message{ID: "errors.context.download_archive", Args: map[string]any{"Reason": err}})
	}

	color.Cyan("%s", console.Translate(ctx, messages.UpdateVerifying()))
	sumsPath, _, err := download(ctx, sumsAsset.GetBrowserDownloadURL(), filepath.Join(tmp, checksumAssetName), dialer)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "download checksums"), corei18n.Message{ID: "errors.context.download_checksums", Args: map[string]any{"Reason": err}})
	}
	sumsData, err := os.ReadFile(sumsPath)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "read checksums"), corei18n.Message{ID: "errors.context.read_checksums", Args: map[string]any{"Reason": err}})
	}
	if err = verifyChecksum(sumsData, filepath.Base(archivePath), archivePath); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "verify checksum"), corei18n.Message{ID: "errors.context.verify_checksum", Args: map[string]any{"Reason": err}})
	}

	color.Cyan("%s", console.Translate(ctx, messages.UpdateExtracting(name)))
	binPath, err := extractBinary(archivePath, binaryName(runtime.GOOS), tmp)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "extract binary"), corei18n.Message{ID: "errors.context.extract_binary", Args: map[string]any{"Reason": err}})
	}

	color.Cyan("%s", console.Translate(ctx, messages.UpdateReplacing(exe)))
	if err = replaceBinary(exe, binPath); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "replace binary"), corei18n.Message{ID: "errors.context.replace_binary", Args: map[string]any{"Reason": err}})
	}

	color.Green("%s", console.Translate(ctx, messages.UpdateSuccess(tag)))
	return nil
}

// updateState is the result of comparing the current version with the latest one.
type updateState int

const (
	updateYes updateState = iota
	updateNo
	updateUnknown
)

// needsUpdate compares two version strings ("v1.2.3" or "1.2.3"). It returns
// updateUnknown if the current version is not a parseable release version
// (e.g. "dev"), so that dev builds can still self-update.
func needsUpdate(current, latest string) updateState {
	cur, err := semver.NewVersion(strings.TrimPrefix(current, "v"))
	if err != nil {
		return updateUnknown
	}

	lat, err := semver.NewVersion(strings.TrimPrefix(latest, "v"))
	if err != nil {
		return updateUnknown
	}

	if cur.Compare(lat) >= 0 {
		return updateNo
	}

	return updateYes
}

// fetchRelease returns the latest release, or the release tagged target if
// target is not empty (e.g. "v0.20.4").
func fetchRelease(ctx context.Context, target string, dialer proxy.ContextDialer) (*github.RepositoryRelease, error) {
	client := github.NewClient(&http.Client{
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
		},
		Timeout: 30 * time.Second,
	})

	// Use GITHUB_TOKEN if set, to avoid hitting the unauthenticated rate limit.
	if ghToken := os.Getenv("GITHUB_TOKEN"); ghToken != "" {
		client = client.WithAuthToken(ghToken)
	}

	var (
		release *github.RepositoryRelease
		err     error
	)

	if target == "" {
		release, _, err = client.Repositories.GetLatestRelease(ctx, repoOwner, repoName)
	} else {
		if _, err = semver.NewVersion(strings.TrimPrefix(target, "v")); err != nil {
			return nil, diagnostic.Describe(fmt.Errorf("invalid target version %q: %w", target, err), corei18n.Message{ID: "errors.message.invalid_target_version_value_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", target), "Arg2": err}})
		}
		release, _, err = client.Repositories.GetReleaseByTag(ctx, repoOwner, repoName, target)
	}
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "github api"), corei18n.Message{ID: "errors.context.github_api", Args: map[string]any{"Reason": err}})
	}

	if release == nil || release.GetTagName() == "" {
		return nil, diagnostic.Describe(fmt.Errorf("release not found"), corei18n.Message{ID: "errors.message.release_not_found"})
	}

	return release, nil
}

func findAsset(release *github.RepositoryRelease, name string) (*github.ReleaseAsset, error) {
	for _, a := range release.Assets {
		if a.GetName() == name {
			return a, nil
		}
	}
	return nil, func() error {
		messageArg2 := release.GetTagName()
		return diagnostic.Describe(fmt.Errorf("asset %q not found in release %s", name, messageArg2), corei18n.Message{ID: "errors.message.asset_value_not_found_in_release_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", name), "Arg2": messageArg2}})
	}()
}

// goARM returns the GOARM value used to build the binary ("7" as fallback),
// or an empty string on non-arm platforms.
func goARM() string {
	if runtime.GOARCH != "arm" {
		return ""
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "GOARM" && s.Value != "" {
				return s.Value
			}
		}
	}
	return "7"
}

func download(ctx context.Context, url, path string, dialer proxy.ContextDialer) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, diagnostic.Describe(errors.Wrap(err, "create request"), corei18n.Message{ID: "errors.context.create_request", Args: map[string]any{"Reason": err}})
	}

	resp, err := (&http.Client{
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
		},
		Timeout: 10 * time.Minute,
	}).Do(req)
	if err != nil {
		return "", 0, diagnostic.Describe(errors.Wrap(err, "do request"), corei18n.Message{ID: "errors.context.do_request", Args: map[string]any{"Reason": err}})
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", 0, diagnostic.Describe(fmt.Errorf("unexpected status code: %d", resp.StatusCode), corei18n.Message{ID: "errors.message.unexpected_status_code_value", Args: map[string]any{"Arg1": resp.StatusCode}})
	}

	f, err := os.Create(path)
	if err != nil {
		return "", 0, diagnostic.Describe(errors.Wrap(err, "create file"), corei18n.Message{ID: "errors.context.create_file", Args: map[string]any{"Reason": err}})
	}
	defer func() { _ = f.Close() }()

	size, err := io.Copy(f, resp.Body)
	if err != nil {
		return "", 0, diagnostic.Describe(errors.Wrap(err, "save file"), corei18n.Message{ID: "errors.context.save_file", Args: map[string]any{"Reason": err}})
	}

	return path, size, nil
}

func verifyChecksum(sumsContent []byte, name, path string) error {
	expected, ok := parseChecksums(string(sumsContent))[name]
	if !ok {
		return diagnostic.Describe(fmt.Errorf("checksum for %q not found", name), corei18n.Message{ID: "errors.message.checksum_for_value_not_found", Args: map[string]any{"Arg1": fmt.Sprintf("%q", name)}})
	}

	f, err := os.Open(path)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "open file"), corei18n.Message{ID: "errors.context.open_file", Args: map[string]any{"Reason": err}})
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return diagnostic.Describe(errors.Wrap(err, "hash file"), corei18n.Message{ID: "errors.context.hash_file", Args: map[string]any{"Reason": err}})
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return diagnostic.Describe(fmt.Errorf("checksum mismatch: expected %s, got %s", expected, got), corei18n.Message{ID: "errors.message.checksum_mismatch_expected_value_got_value", Args: map[string]any{"Arg1": expected, "Arg2": got}})
	}
	return nil
}

// extractBinary extracts the executable from a .tar.gz or .zip archive into dir.
func extractBinary(archivePath, binName, dir string) (string, error) {
	dest := filepath.Join(dir, binName)

	switch {
	case strings.HasSuffix(archivePath, ".zip"):
		if err := extractFromZip(archivePath, binName, dest); err != nil {
			return "", err
		}
	case strings.HasSuffix(archivePath, ".tar.gz"):
		if err := extractFromTarGz(archivePath, binName, dest); err != nil {
			return "", err
		}
	default:
		return "", diagnostic.Describe(fmt.Errorf("unsupported archive format: %s", archivePath), corei18n.Message{ID: "errors.message.unsupported_archive_format_value", Args: map[string]any{"Arg1": archivePath}})
	}

	// 0755: the binary must be executable for everyone, like the install script does.
	if err := os.Chmod(dest, 0o755); err != nil {
		return "", diagnostic.Describe(errors.Wrap(err, "chmod binary"), corei18n.Message{ID: "errors.context.chmod_binary", Args: map[string]any{"Reason": err}})
	}
	return dest, nil
}

func extractFromTarGz(archivePath, binName, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "open archive"), corei18n.Message{ID: "errors.context.open_archive", Args: map[string]any{"Reason": err}})
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "gzip reader"), corei18n.Message{ID: "errors.context.gzip_reader", Args: map[string]any{"Reason": err}})
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return diagnostic.Describe(fmt.Errorf("%q not found in archive", binName), corei18n.Message{ID: "errors.message.value_not_found_in_archive", Args: map[string]any{"Arg1": fmt.Sprintf("%q", binName)}})
		}
		if err != nil {
			return diagnostic.Describe(errors.Wrap(err, "tar next"), corei18n.Message{ID: "errors.context.tar_next", Args: map[string]any{"Reason": err}})
		}
		if filepath.Base(hdr.Name) != binName || hdr.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return diagnostic.Describe(errors.Wrap(err, "create output file"), corei18n.Message{ID: "errors.context.create_output_file", Args: map[string]any{"Reason": err}})
		}
		if _, err = io.Copy(out, tr); err != nil {
			_ = out.Close()
			return diagnostic.Describe(errors.Wrap(err, "copy binary"), corei18n.Message{ID: "errors.context.copy_binary", Args: map[string]any{"Reason": err}})
		}
		return out.Close()
	}
}

func extractFromZip(archivePath, binName, dest string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "zip open reader"), corei18n.Message{ID: "errors.context.zip_open_reader", Args: map[string]any{"Reason": err}})
	}
	defer func() { _ = r.Close() }()

	for _, zf := range r.File {
		if filepath.Base(zf.Name) != binName || zf.FileInfo().IsDir() {
			continue
		}
		src, err := zf.Open()
		if err != nil {
			return diagnostic.Describe(errors.Wrap(err, "open zip entry"), corei18n.Message{ID: "errors.context.open_zip_entry", Args: map[string]any{"Reason": err}})
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			_ = src.Close()
			return diagnostic.Describe(errors.Wrap(err, "create output file"), corei18n.Message{ID: "errors.context.create_output_file", Args: map[string]any{"Reason": err}})
		}
		if _, err = io.Copy(out, src); err != nil {
			_ = out.Close()
			_ = src.Close()
			return diagnostic.Describe(errors.Wrap(err, "copy binary"), corei18n.Message{ID: "errors.context.copy_binary", Args: map[string]any{"Reason": err}})
		}
		if err = out.Close(); err != nil {
			_ = src.Close()
			return diagnostic.Describe(errors.Wrap(err, "close output file"), corei18n.Message{ID: "errors.context.close_output_file", Args: map[string]any{"Reason": err}})
		}
		return src.Close()
	}
	return diagnostic.Describe(fmt.Errorf("%q not found in archive", binName), corei18n.Message{ID: "errors.message.value_not_found_in_archive", Args: map[string]any{"Arg1": fmt.Sprintf("%q", binName)}})
}

// replaceBinary moves the new binary over the current executable. On unix it
// is an atomic rename within the same filesystem; on windows the old binary
// is kept as "<name>.old" until a successful replacement for rollback.
func replaceBinary(target, src string) error {
	dir := filepath.Dir(target)
	staged := filepath.Join(dir, "."+filepath.Base(target)+".update")

	if err := copyFile(src, staged, 0o755); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return diagnostic.Describe(errors.Wrapf(err, "stage new binary in %s: permission denied (check directory ownership, or use your package manager)", dir), corei18n.Message{ID: "errors.update.stage_permission", Args: map[string]any{"Arg1": dir, "Reason": err}})
		}
		return diagnostic.Describe(errors.Wrap(err, "stage new binary"), corei18n.Message{ID: "errors.context.stage_new_binary", Args: map[string]any{"Reason": err}})
	}

	if runtime.GOOS == osWindows {
		// a running .exe cannot be deleted or renamed over, but it can be
		// renamed away: move the old binary to "<name>.old", put the new one
		// in place, and roll back from the backup if anything fails.
		bak := target + ".old"
		_ = os.Remove(bak)
		if err := os.Rename(target, bak); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(staged)
			return diagnostic.Describe(errors.Wrap(err, "backup old binary"), corei18n.Message{ID: "errors.context.backup_old_binary", Args: map[string]any{"Reason": err}})
		}
		if err := os.Rename(staged, target); err != nil {
			_ = os.Rename(bak, target) // rollback
			return diagnostic.Describe(errors.Wrap(err, "replace binary"), corei18n.Message{ID: "errors.context.replace_binary", Args: map[string]any{"Reason": err}})
		}
		_ = os.Remove(bak) // best-effort cleanup of the backup
		return nil
	}

	if err := os.Rename(staged, target); err != nil {
		_ = os.Remove(staged)
		return diagnostic.Describe(errors.Wrap(err, "replace binary"), corei18n.Message{ID: "errors.context.replace_binary", Args: map[string]any{"Reason": err}})
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "open source"), corei18n.Message{ID: "errors.context.open_source", Args: map[string]any{"Reason": err}})
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "create destination"), corei18n.Message{ID: "errors.context.create_destination", Args: map[string]any{"Reason": err}})
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return diagnostic.Describe(errors.Wrap(err, "copy content"), corei18n.Message{ID: "errors.context.copy_content", Args: map[string]any{"Reason": err}})
	}
	if err = out.Sync(); err != nil {
		_ = out.Close()
		return diagnostic.Describe(errors.Wrap(err, "sync destination"), corei18n.Message{ID: "errors.context.sync_destination", Args: map[string]any{"Reason": err}})
	}
	return out.Close()
}

func multiErr(a, b error) error {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return diagnostic.Describe(fmt.Errorf("%v; %v", a, b), corei18n.Message{ID: "errors.message.value_value", Args: map[string]any{"Arg1": a, "Arg2": b}})
}
