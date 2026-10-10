# AGENTS.md

Guidance for coding agents working in this repository. It is a fork of
[iyear/tdl](https://github.com/iyear/tdl) that adds a native batch mode,
caption-tag and linked-resource archives, resumable downloads and bilingual
(English/Simplified Chinese) output.

## Layout

The repository is a Go workspace (`go.work`) with three modules. Each one is
built, tested and linted separately.

| Module | Path | Contents |
| --- | --- | --- |
| `github.com/iyear/tdl` | `.` | CLI (`cmd/`), commands (`app/`), batch runner (`pkg/autodl/`), shared helpers (`pkg/`, `internal/`) |
| `github.com/iyear/tdl/core` | `core/` | Reusable library: downloader, uploader, forwarder, storage, diagnostics, core i18n |
| `github.com/iyear/tdl/extension` | `extension/` | SDK for tdl extensions |

Key areas:

- `cmd/batch.go`, `pkg/autodl/`: `tdl batch`. Config loading/validation (`config.go`), the immutable run snapshot (`prepared.go`), job dispatch (`job.go`), range/incremental planning and state (`range_window.go`, `incremental.go`, `state.go`).
- `app/chat/tag*.go`: caption-tag archives. `app/chat/linked*.go`, `bot_updates.go`: linked-resource archives, including bot requests and cleanup.
- `internal/transfer/`: the file lifecycle shared by all download paths (path reservations, `Commit`, delay, counts).
- `core/downloader/`: part journals, resume, file identity and expired file reference refresh.
- `core/diagnostic/`, `core/i18n/`, `pkg/i18n/`, `pkg/messages/`, `pkg/console/`: structured errors and localization.
- `test/`: Ginkgo E2E tests. They need a Teamgram test server and are excluded from normal test runs.
- `python/` (ignored): the original `run_unified.py` that the batch mode replaces. Use it only as a behavioral reference.

## Verify changes

Run all of these before committing. CI runs the same steps (`.github/workflows/master.yml`).

```powershell
# build, test and vet all three modules (skips the E2E package)
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/verify-batch.ps1

# golangci-lint v2.6.0, in each module directory: ".", "core", "extension"
golangci-lint fmt ./...   # gofumpt + gci, configured in .golangci.yaml
golangci-lint run ./...

# translation catalogs, Cobra help coverage and unlocalized terminal text
go run ./tools/i18ncheck
```

- `scripts/verify-batch.ps1 -Race` needs CGO and a C compiler. CI always runs the race detector.
- `build.bat` builds a stripped Windows amd64 `tdl.exe`. Never commit binaries; `*.exe` is ignored.
- Tests must not contact Telegram. Use the in-memory RPC fakes in the existing `_test.go` files.

## Conventions

- Commits follow Conventional Commits in English, such as `fix(batch): ...` or `refactor(chat): ...`.
- Match the surrounding code. Only add a comment when it explains a non-obvious invariant.
- Validation that iterates over several fields must report errors in a fixed order. Iterate slices, not maps.
- Never write to a user's Telegram account or output directories from tests or diagnostics without explicit permission. That includes bot requests and message deletion.

### Errors and localization

All user-visible text is localized into English and Simplified Chinese.

- Wrap errors with `diagnostic.Describe(originalErr, corei18n.Message{ID: ..., Args: ...})`. `Error()` keeps the original English chain for logs and debugging; the terminal renders the localized message.
- Add every new message ID to both language files, with the same template arguments:
  - Core: `core/i18n/messages/*.{en,zh}.json`
  - Application: `pkg/i18n/resources/{en,zh}/*.json`
- Remove catalog entries when their last reference is deleted.
- Terminal output goes through `console.Translate(ctx, ...)` or the helpers in `pkg/messages`. `tools/i18ncheck` reports raw terminal strings.
- User documentation exists in both `docs/content/en` and `docs/content/zh`. Update both.

### Persisted identities: keep backward compatible

Batch runs resume from files written by earlier versions. These values are
hashed or stored on disk, so changing them silently invalidates or misattributes
existing progress:

- State scopes and archive identities (`pkg/autodl/identity.go`, `archive.go`; `archive-v2|...`).
- `resourceLink.key()` and the `linkKind*` values, which feed `LinkHash` in linked archive metadata.
- `downloader.FileIdentity` and the `FileKind*` values used by part journals.
- Tag post directory names (`postDirectory`) and the `meta.json` / `.tdl-completed.json` formats.

If one of these must change, version it and keep reading the old format, or
refuse it with a clear diagnostic. Never reinterpret old data.

### Completion semantics

A file only counts as done after `transfer.Commit` succeeds: flush, close, size
check, rename, then journal cleanup. Incremental `last_ts` must not advance
while any target in the window is missing, failed or canceled. Preserve these
guarantees in any new download path.
