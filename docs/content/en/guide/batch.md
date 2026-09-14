---
title: "Batch download"
weight: 35
---

# Batch download

`tdl batch` is a native port of the `python/run_unified.py` helper. It reads the
**same `config.json`** and supports comment downloads, direct message
downloads, incremental mode and resume, but it no longer spawns one `tdl`
process per batch. Instead it drives tdl's own downloader, so the whole run
shares one connection pool, one Telegram client and batched message
resolution.

## Quick start

1. Put a `config.json` next to your downloads (see below).
2. Run:

{{< command >}}
tdl batch
{{< /command >}}

When the working directory holds a valid `config.json` **and** you are logged in
(`tdl login`), running bare `tdl` starts the batch mode automatically:

{{< command >}}
tdl
{{< /command >}}

{{< hint info >}}
Set `TDL_NO_BATCH=1` to disable that automatic start and always get the help
output instead.
{{< /hint >}}

## Configuration

The format is fully compatible with the python script. `download_dir` is
accepted as an alias of `download_base`.

```json
{
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
}
```

### Top level fields

| Field | Description |
| --- | --- |
| `namespace` | tdl namespace (account) to use, same as `-n/--ns`. Empty means `default` |
| `download_base` | download root, defaults to `downloads` |
| `incremental` | enable incremental mode for every job (a job may override it) |
| `state_file` | resume/incremental state path, defaults to `<download dir>/tdl_state.json` |
| `overlap_seconds` | incremental lookback window in seconds, defaults to `3600` |
| `jobs` | the download tasks, processed in order |
| `pool` / `threads` / `limit` | optional performance settings, same as `--pool` / `-t` / `-l` |

### Job fields

| Field | Description |
| --- | --- |
| `chat_url` | message link: `https://t.me/user/123`, `https://t.me/c/123/456` or `https://t.me/user/123?comment=456` |
| `chat` | chat used for the export in incremental mode, inferred from the link when empty |
| `subdir` | directory under `download_base` for this job |
| `comment` | `true` selects comment mode: `start_comment`/`end_comment` are comment ids in the **linked discussion group** (tdl's `?comment=N`); a number also sets `start_comment` |
| `start_comment` / `end_comment` | id range, `start` inclusive, `end` exclusive |
| `incremental` | per job override of the global incremental switch |
| `overlap_seconds` | per job override of the lookback window |
| `export_filter` | expr filter for incremental mode, same as `tdl chat export -f` |
| `with_content` | include `date`/`text` in the incremental export |
| `export_all` | also export non-media messages in incremental mode |
| `topic_id` | keep only messages of one forum topic |
| `reply_post_id` | scan the comment section of one post (`messages.getReplies`) |

## Command line

Besides the flags that map one to one onto the python arguments (`-c/--config`,
`--check-only`, `-y/--yes`, `--mode`, `--incremental`, `--state-file`,
`--overlap-seconds`), the command adds:

{{< command >}}
tdl batch -c config.json -y --check-only
tdl batch --incremental --overlap-seconds 7200
tdl batch -d /path/to/downloads -i mp4,jpg
tdl batch --batch-threads 8 --batch-limit 4 --batch-pool 16
tdl batch --retry-skipped
{{< /command >}}

{{< hint info >}}
`--batch-threads` / `--batch-limit` / `--batch-pool` win over the global
`-t` / `-l` / `--pool`, which in turn win over `threads` / `limit` / `pool` in
`config.json`. The final fallback is `8` / `4` / `16`, the same values the
python script used.
{{< /hint >}}

## Resume

Batch download resumes on two levels:

1. **Message level**: every finished message is recorded, with batched writes to the state file
   (default `<download dir>/tdl_state.json`), so the next run skips it without
   requesting it again. Deleted and media-less messages are recorded as well, so
   they are not retried on every run.
2. **Part level**: files are transferred in 1 MiB parts and the finished parts
   are tracked in `<file>.tmp.parts`. After an interrupt, the next run only
   fetches the missing parts instead of restarting the file. The parts are
   discarded automatically when the file size changes on the server.

Successful downloads clean up their `.tmp` and `.parts` files; interrupted ones
keep them for the next run.

Both batch mode and `tdl dl` keep a separate parts journal for each concurrent
file. During downloads, journals are checkpointed after 32 new parts or on the
next write at least one second after the last checkpoint. Normal completion or
cancellation flushes the remaining records. Forcefully terminating the process
may require downloading up to 31 unrecorded parts again. Legacy journals without
a version cannot prove that their temporary files were preserved, so those
unfinished files restart after upgrading.

## Troubleshooting

### Everything is skipped (`0 downloaded, N skipped`)

That almost always means the ids were looked up in the wrong dialog, where every
one of them looks deleted. The usual case is comment mode: `start_comment` /
`end_comment` are comment ids of the linked discussion group and need
`comment: true` (or `--mode comment`) so they are resolved there instead of in
the channel itself. If the ids are channel post ids instead, use
`comment: false` or `--mode direct`.

The run prints a red hint when this happens:

```
None of the 227 message(s) exist in dialog 1234567890.
Check --mode / the "comment" setting of the job and the id range, then run again with --retry-skipped.
```

After fixing the config, run once with `--retry-skipped`: it forgets the ids
that were recorded as unavailable so they are requested again, while keeping the
finished messages and the incremental timestamp.

{{< command >}}
tdl batch --retry-skipped
{{< /command >}}

### Incremental mode does not advance `last_ts`

The timestamp only moves once nothing in the window is missing, so a failed
download is retried by the next run instead of being skipped forever. `--yes`
does not override this protection. Jobs that still have failures after retrying
return an error. Reaching the history scan limit also preserves the timestamp
to avoid skipping messages that have not been scanned.

## Incremental mode

Incremental mode scans the chat by timestamp and only downloads media from the
current window. The window looks back one hour by default so delayed messages
are not missed:

{{< command >}}
tdl batch --incremental --overlap-seconds 3600
{{< /command >}}

The timestamp only advances once every message of the window is downloaded (or
marked as unavailable), so failures are retried by the next run. Combine with
`--check-only` to preview the window without downloading or advancing it.

## Performance

Compared to the python script, the batch mode:

- runs in a single process with a single pool and client instead of
  re-authenticating and re-connecting per batch;
- resolves messages in batches of 100 (`channels.getMessages`) instead of one
  request per message;
- skips files that already exist before requesting them;
- records deleted messages so they are never requested again;
- picks the thread count per file size (like `tdl dl`) and resumes at part
  granularity instead of restarting whole files.
- buffers message metadata while opening files on demand, caches resolved
  dialogs, and avoids resolving already completed jobs;
- batches parts journal writes and removes the fixed 200 ms progress delay
  from small file downloads.
