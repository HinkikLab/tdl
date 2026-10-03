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
| `tag` / `tags` | one hashtag or an array of hashtags matched against photo/video captions; use instead of a message ID range |
| `tag_match` | `any` (default) selects posts with any requested tag; `all` requires every tag |
| `max_posts` | optionally stop after this many matching posts; `0` (the default) scans the full history |
| `follow_links` | archive resources reached through links in preview posts or their comments |
| `link_options` | bounded resolution, bot waiting, reissue and cleanup settings described below |

### Archive photo and video posts by hashtag

For a tag job, `chat_url` is the chat's home URL and no message range is needed:

```json
{
  "namespace": "default",
  "download_base": "downloads",
  "jobs": [
    {
      "chat_url": "https://t.me/AVMYS/",
      "tags": ["#绝区零", "#原神"],
      "tag_match": "any",
      "subdir": "AVMYS"
    }
  ]
}
```

Run `tdl batch -c config.json --check-only` to count matches, then
`tdl batch -c config.json -y` to download. Each matching Telegram album or
individual media post goes into `downloads/AVMYS/<chat-id>/<matched-tag> <caption> [id]/` with
its photos/videos, the original caption in `message.txt`, and IDs, captions and
source link in `message.json`. The directory name removes every hashtag and
illegal path character and limits the tag plus caption to 64 characters. If
several tags match, the first matching tag in config order becomes the prefix.
If a caption consists only of hashtags, the folder uses every hashtag in its
original order without `#` and keeps the message ID suffix to avoid collisions.
A caption on any album member selects the whole
album. Reruns skip completed files by size and resume partial downloads.

### Archive linked resources from preview posts

Set `follow_links: true` on a job. The source `chat_url` may identify a main chat
or one post. A home URL scans all history unless `max_posts` is set. Optional
`tags` / `tag_match` filter original main-post captions. On a home URL,
`start_comment` / `end_comment` select an inclusive/exclusive **main-post ID**
range and preserve any matching album in full; `comment: true` is not used.

```json
{
  "namespace": "default",
  "download_base": "downloads",
  "jobs": [{
    "chat_url": "https://t.me/example_channel",
    "follow_links": true,
    "subdir": "resources",
    "max_posts": 1,
    "link_options": {
      "cleanup_bot_messages": true,
      "scan_comments": true,
      "max_depth": 8,
      "rerequest_limit": 3,
      "flood_retries": 5,
      "flood_wait_seconds": 30
    }
  }]
}
```

See `config.linked.example.json` in the repository root for every setting.
`tdl batch -c config.json --check-only` discovers main-post and comment entry
links without requesting bots, downloading, writing archives or deleting
messages. It does not verify the chain beyond those entry links.

Resolution handles plain URLs, hidden text URLs and inline URL buttons, bot
start links, `tg://` links, public/private message links, comment links and whole
resource albums. When a post has no resource link, its associated comments are
searched. Bots may return further links to bots or group messages.
Each post is resolved and downloaded before the next one is requested.

Files go into `<download_base>/<subdir>/<main-chat-id>/<main caption [post ID]>/`
with the complete preview album by default, original `message.txt` and
`message.json` containing entry links, hops, resource identities and completion
status. Folder names reuse hashtag archive rules and remove visible URLs.
Stable Telegram file IDs in filenames let reissued messages resume the same
partial files even when their message IDs change.

| `link_options` field | Default | Meaning |
| --- | --- | --- |
| `scan_comments` | `true` | search comments when a main/intermediate post has no entry link |
| `comment_limit` | `100` | maximum recent comments per thread |
| `include_previews` | `true` | retain every media member of the main preview album |
| `cleanup_bot_messages` | `true` | delete this post's bot requests and responses after downloads settle |
| `max_depth` | `8` | maximum chain depth, at most `32` |
| `max_links` | `100` | maximum distinct resolved links per post, at most `1000` |
| `bot_timeout_seconds` | `60` | response collection timeout after a successful start RPC; existing middleware handles Telegram RPC flood waits |
| `bot_idle_seconds` | `3` | quiet interval after receiving media or another entry link |
| `poll_interval_ms` | `500` | history polling interval, alongside live updates that retain self-deleted messages |
| `max_bot_messages` | `500` | maximum responses to one bot request |
| `rerequest_limit` | `3` | full-chain reissues per post after expired file references; `0` disables |
| `flood_retries` | `5` | retries after a bot's textual rate limit; `0` disables |
| `flood_wait_seconds` | `30` | wait when an explicit rate-limit message gives no duration |
| `max_flood_wait_seconds` | `3600` | maximum automatic wait for textual bot rate limits |

English/Chinese rate-limit messages with seconds, minutes or hours trigger a
cancellation-aware wait followed by a fresh request. Progress notifications
keep waiting on the current request. This bot-specific backoff does not apply
to group message links. Expired references first refresh the original message,
then reissue the full chain if necessary. Identity, size and DC must match
before the downloader uses a replacement reference. Failed posts remain
incomplete. Reruns validate every complete resource by size before skipping
bot requests.

Cleanup deletes only collected bot private-chat message IDs, including relay
responses, retries and multiple replies. It never deletes whole dialogs or
group posts. Failure and normal cancellation also attempt bounded cleanup;
cleanup errors are reported. Force-killing the process prevents cleanup.
Avoid concurrent manual requests to the same bot because many bots do not
correlate their replies to a request. Increase the idle interval and timeout
when a bot sends several batches with long pauses.

This mode resumes through archive manifests and cannot use timestamp
`incremental`, comment-range mode or topic/reply selectors. Group links must
identify accessible messages. Invite links, group home links, external web
redirects, callback buttons, captcha and payment steps are not automatically
executed. Unrecognized custom rate-limit text results in a response timeout.

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
