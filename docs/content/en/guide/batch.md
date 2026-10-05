---
title: "Batch download"
weight: 35
---

# Batch download

`tdl batch` is a native port of the `python/run_unified.py` helper. It reads the
**same base `config.json` fields** and adds caption-tag and linked-resource
archives. Direct messages, comments, incremental jobs and archives share one
Telegram client and connection pool, with resumable file downloads.

## Quick start

For an offline check of configuration, effective options and state-path
ownership, run `tdl batch -c config.json --validate-only`. It does not open
account storage.

1. Generate an annotated configuration without logging in or connecting to Telegram:

{{< command >}}
tdl batch init
{{< /command >}}

2. Keep the jobs you need and remove the other examples. Replace
   `example_channel`, `example_discussion`, `example_forum`, message IDs and
   hashtags. Set `namespace` to your logged-in account.
3. Preview the plan, then download:

{{< command >}}
tdl batch -c config.json --check-only
tdl batch -c config.json
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

## Generate example configuration

{{< command >}}
tdl batch init
tdl batch init -c examples/config.json
tdl batch init -c config.json --force
{{< /command >}}

The default destination is `config.json` in the working directory. `-c/--config`
selects another output path, and missing parent directories are created.
Existing files are preserved unless `--force` explicitly allows replacement.
Generation only writes the configuration; it does not initialize account
storage, contact bots or execute any jobs.

The output is standard UTF-8 JSON. Extra **`_comment`** fields contain notes
and are ignored by the parser. **`comment` selects comment mode and must not
be used for descriptive text.** Do not add `//` comments or trailing commas.
The parser also accepts YAML. Without `-c`, batch searches for `config.json`,
`config.yaml`, then `config.yml`. Relative paths are based on the **working
directory**, including when the configuration lives elsewhere.

The embedded template at `pkg/autodl/config.example.json` includes 11 jobs.
Each uses its own `subdir` to keep message-level resume state separate:

| Example | Purpose | Main settings |
| --- | --- | --- |
| 01 | Direct channel/group message range | `comment: false`, `start_comment` / `end_comment` |
| 02 | Linked discussion group comment ID range | `comment: true`, discussion group IDs |
| 03 | Incremental chat history | `incremental: true` |
| 04 | Incremental forum topic | `incremental: true`, `topic_id` |
| 05 | Incremental replies to one post | `incremental: true`, `chat`, `reply_post_id` |
| 06 | Single caption hashtag archive | `tag` |
| 07 | Any caption hashtag inside an ID range | `tags`, `tag_match: "any"`, ID range |
| 08 | All caption hashtags in an incremental window | `tags`, `tag_match: "all"`, `incremental: true` |
| 09 | Linked resources from one main post | Post URL, `follow_links: true`, complete `link_options` |
| 10 | Incremental preview history with comment links | Home URL, `follow_links: true`, `incremental: true` |
| 11 | Linked resources filtered by main-post range and tags | Home URL, `follow_links: true`, range, tags and bot timing |

URLs and IDs are placeholders; edit them before use. Archive examples use
`max_posts: 1` for an initial preview. Incremental examples use `0` to process
the complete window; set it to `0` for an unrestricted archive scan.
This limit applies only to caption-tag and linked-resource archives.

## Select a mode

For each job, batch selects an incremental time window or an explicit ID range,
then processes the selected messages using links, caption tags or direct/comment
downloads. Archives without either selector continue to scan full history.
Jobs run in array order; one configuration can contain several modes.

| Mode | `chat_url` | ID meaning and constraints |
| --- | --- | --- |
| Direct range | Channel/group home or post | Both range endpoints are required; the post ID in the URL does not automatically select a download range |
| Comment range | Main channel post, `comment: true` | Range IDs belong to the linked discussion group; these IDs are not automatically restricted to replies to that post |
| Incremental | Chat home or main post | No range required; `topic_id` / `reply_post_id` apply only to incremental scanning |
| Caption-tag archive | Channel/group home or forum topic link | Supports ID ranges or timestamp incremental scanning; optional `topic_id`; comment mode and `reply_post_id` are unsupported |
| Linked-resource archive | Main channel home or one post | Supports main-post ID ranges or timestamp incremental scanning, with optional tags; comment mode and topic/reply selectors are unsupported |

`--mode auto` is the default. Range/incremental jobs choose the discussion
group from `comment` or `?comment=N` in the URL. `--mode comment/direct`
overrides the job's `comment` setting, but a URL containing `?comment=N`
still selects the discussion group. Remove that query parameter when switching
to direct channel downloads. These flags do not change tag/linked archive modes.
Use `auto` for mixed configurations. `--incremental` forces the setting on all
jobs, including tag and linked archives. Incremental selection takes precedence
over ID ranges; use per-job settings when only some jobs should be incremental.

## Configuration

The base fields remain compatible with the Python script. Tag and linked
archives are native batch extensions. This minimal direct job downloads
messages 100 through 109:

```json
{
  "namespace": "default",
  "download_base": "downloads",
  "incremental": false,
  "jobs": [
    {
      "_comment": "Download messages 100 through 109 directly",
      "chat_url": "https://t.me/example_channel/100",
      "comment": false,
      "start_comment": 100,
      "end_comment": 110,
      "subdir": "direct"
    }
  ]
}
```

### Top level fields

| Field | Type / default | Description |
| --- | --- | --- |
| `_comment` | Optional string/array | Documentation only; ignored |
| `namespace` | String, `default` | Account namespace; explicit `-n/--ns` takes precedence |
| `download_base` | String, `downloads` | Download root; `-d` overrides it while preserving job subdirectories |
| `download_dir` | Optional string | Legacy alias; nonempty `download_base` wins |
| `incremental` | Boolean, `false` | Global incremental switch, overridden per job; keep false for mixed examples |
| `write_metadata` | Boolean, `true` | Write per-post `meta.json` in tag/linked archives; `false` disables output, overridden per job |
| `state_file` | Optional string | Default `<download_base>/<subdir>/tdl_state.json`; used by message jobs and incremental archives; prefer distinct default paths |
| `overlap_seconds` | Nonnegative integer, `3600` | Incremental lookback; currently `0` falls back to another layer/default rather than disabling lookback |
| `jobs` | Required nonempty array | Download tasks, processed in order |
| `pool` | Nonnegative integer, `16` | Connection pool size; `0` is unlimited |
| `threads` | Positive integer, `8` | Maximum threads for one file |
| `limit` | Positive integer, `4` | Concurrent files, not concurrent jobs |

### Job fields

| Field | Description |
| --- | --- |
| `_comment` | Documentation only; ignored |
| `chat_url` | Required. Public home/post, private home `https://t.me/c/1234567890/` and post links, comment links, links without a scheme and `/s/` preview links are accepted; the account must have access |
| `chat` | Optional source confirmation for ordinary message jobs: username, numeric ID or Telegram URL. It must resolve to the same effective channel/discussion as `chat_url`; conflicts fail before scanning, downloading or writing completion state. Archives use `chat_url` |
| `subdir` | Relative job directory under `download_base`; absolute paths and escaping `..` paths are rejected; use distinct directories for distinct jobs |
| `comment` | Default false; true selects the linked discussion group. A legacy integer also selects comment mode and sets the start ID if omitted. Prefer a boolean with explicit endpoints |
| `start_comment` / `end_comment` | Positive integers, start inclusive, end exclusive, end greater than start; maximum range 1,000,000. Direct jobs use these same legacy field names |
| `incremental` | per job override of the global incremental switch |
| `write_metadata` | per job override of archive metadata output; omission inherits the global setting |
| `overlap_seconds` | per job override of the lookback window |
| `export_filter` | expr filter for ordinary incremental message jobs, same as `tdl chat export -f`; archives filter by tags/links |
| `with_content` | include `date`/`text` in ordinary incremental message exports |
| `export_all` | export non-media messages in ordinary incremental jobs; archives preserve captions themselves |
| `topic_id` | Positive topic root ID for incremental or tag scanning. Tag jobs also infer it from topic URLs; explicit values must agree. Incremental jobs still require the explicit selector |
| `reply_post_id` | Incremental only: positive reply root ID in the scan dialog; use the forwarded root ID in the discussion group, not the original channel post ID |
| `tag` / `tags` | one hashtag or an array matched against photo/video captions; may combine with ID ranges or incremental windows |
| `tag_match` | `any` (default) selects posts with any requested tag; `all` requires every tag |
| `max_posts` | optionally stop after this many matching posts; `0` (the default) scans the full history |
| `follow_links` | archive resources reached through links in preview posts or their comments |
| `link_options` | bounded resolution, bot waiting, reissue and cleanup settings described below |

`export_filter`, `with_content`, `export_all`, topic/reply selectors and
incremental lookback are not used by direct ID-range scanning.
`export_all: true` includes non-media messages in the plan; it does not turn
text into downloadable files. Use an archive mode to preserve post descriptions.
During downloads, incremental export files are retained under
`<job directory>/.tdl_tmp/`. Read-only planning does not write exports.

Private forum home URLs such as `https://t.me/c/2255983776/` are accepted by
tag archives, linked-resource archives, incremental scans and message-range jobs.
Without a topic selector, history scanning covers every topic. `max_posts: 0`
scans the complete history; a positive limit stops after that many matching posts
across the chat, rather than taking that many posts per topic. Tag jobs select a
single topic using a topic link or `topic_id`; incremental jobs require explicit
`topic_id`. Linked-resource jobs scan all topics from the home URL and still do
not support topic selectors.

After resolving the target, job headings show `Chat name (chat ID) / Topic name
(topic ID)`. Whole-forum scans show `Chat name (chat ID) / all topics`, with the
original configured URL on a separate line. Ordinary channels and single-post
jobs show the chat name and ID; failed resolutions retain the original URL.

### Archive metadata

Tag and linked-resource archives write one `meta.json` per post by default,
including the original caption and message/resource details. They no longer
create `message.txt` or `message.json`. Set `write_metadata` to `false` at the
top level to disable this output for all archive jobs, or inside a job to
override the global value:

```json
{
  "write_metadata": false,
  "jobs": [
    {
      "chat_url": "https://t.me/example_channel",
      "tags": ["#tutorial"],
      "subdir": "media-only"
    },
    {
      "chat_url": "https://t.me/example_channel/100",
      "follow_links": true,
      "write_metadata": true,
      "subdir": "with-metadata"
    }
  ]
}
```

Disabling visible metadata still validates completed media and resumes partial
downloads. Tag archives keep a minimal internal completion record without captions.
A linked archive without saved metadata resolves its resource links
again on reruns; `meta.json` allows it to skip a completed post before resolution.
Existing `message.json` files remain readable for archive migration and completion
checks. Changing this setting preserves existing files. Resume state is
separate from per-post metadata. Tag scans pass discovered media directly to
the downloader and no longer need a JSON download index.
The standalone tag command supports `--write-metadata=false` as well.

### Message and comment ranges

To download message 100 alone, set `comment: false`, `start_comment: 100`
and `end_comment: 101`. A comment range looks like this:

```json
{
  "chat_url": "https://t.me/example_channel/100",
  "comment": true,
  "start_comment": 900,
  "end_comment": 910,
  "subdir": "comments"
}
```

100 is the main post ID; 900–909 are discussion group message IDs. The comment
ID can be read from `https://t.me/example_channel/100?comment=900`.
Range jobs fetch those discussion IDs without filtering by the main post.
Use the incremental `reply_post_id` example to scan replies to one root.

### Archive photo and video posts by hashtag

For a tag job, `chat_url` is a chat home or forum topic URL; no message range is needed:

```json
{
  "namespace": "default",
  "download_base": "downloads",
  "jobs": [
    {
      "chat_url": "https://t.me/example_channel",
      "tags": ["#tutorial", "#example"],
      "tag_match": "any",
      "max_posts": 1,
      "subdir": "tags"
    }
  ]
}
```

Use `https://t.me/c/2255983776/` for a whole private numeric group, or
`https://t.me/c/2255983776/41872/` for topic 41872 only. A message link inside
that topic (`https://t.me/c/2255983776/41872/42000`) also selects the topic.
Alternatively, combine a home URL with `"topic_id": 41872`. Public topic
paths and `?thread=41872` use the same rules. Telegram must confirm the topic
exists and is accessible before scanning; ordinary messages, deleted topics
and inaccessible topics never fall back to scanning the whole chat. The CLI
also accepts `tdl chat download-tag --chat <topic-link> --tag <tag>`, or a
home URL with `--topic 41872`.

Run `tdl batch -c config.json --check-only` to count matches, then
`tdl batch -c config.json -y` to download. Each matching Telegram album or
individual media post goes into `downloads/tags/<chat-id>/<matched-tag> <caption> [id]/` with
its photos/videos and, by default, `meta.json` containing IDs, original captions
and the source link. The directory name removes every hashtag and
illegal path character and limits the tag plus caption to 64 characters. If
several tags match, the first matching tag in config order becomes the prefix.
If a caption consists only of hashtags, the folder uses every hashtag in its
original order without `#` and keeps the message ID suffix to avoid collisions.
A caption on any album member selects the whole
album. Reruns compare current media identity and committed file size/mtime,
then reuse valid files or resume partial downloads. A minimal `.tdl-completed.json`
without caption content preserves tag completion checks even when visible metadata is disabled.

Tags may include or omit `#`; Unicode letters, digits and underscores are
accepted. Matching is case-insensitive and requires a complete hashtag:
`#tutorial` does not match `#tutorials`. `tag` and `tags` are merged and
deduplicated. In `all` mode, different members of one album may satisfy
different tags. Only photo/video captions are scanned, not separate text posts
or other attachment types. This archive uses fixed names; `--template` and
`--include` / `--exclude` currently do not affect caption-tag jobs.

Configuration errors show the file, job number, chat URL and reason on separate
lines. Only the `Error:` heading is red; add the global `--debug` flag to show
call stacks and source locations in a separate details block.

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

Example 09 from `tdl batch init` contains every setting. The repository root
also contains the single-mode `config.linked.example.json`.
`tdl batch -c config.json --check-only` discovers main-post and comment entry
links without requesting bots, downloading, writing archives or deleting
messages. It does not verify the chain beyond those entry links.

Resolution handles plain URLs, hidden text URLs and inline URL buttons, bot
start links, `tg://` links, public/private message links, comment links and whole
resource albums. When a post has no resource link, its associated comments are
searched. Bots may return further links to bots or group messages.
Media from every resolved hop is retained and deduplicated by Telegram file
identity, including intermediate bots. Empty promotion links or URL buttons
do not prevent other branches from being resolved. Once any resource is found,
branches with no files or onward links (including bot response timeouts),
unavailable targets, cycles and depth/link-count limits are skipped. Logs and
`meta.json` entries in `hops[].skipped` record the reasons. An eight-hop chain
with media at three hops downloads the resources from all three.
Without any resources, resolution still fails or skips unavailable targets.
Network/RPC errors, exhausted rate-limit retries, timeouts while resource replies
have not settled, and caller cancellation remain failures. Bot names and short
start parameters such as `1` or `ad` are not treated as promotion markers:
promotion bots may still be requested up to the configured timeout, and any
media they return is also retained.
Resource links to forum-topic roots confirmed by Telegram scan the topic up to
`max_topic_messages`, retain complete albums across pages, and resolve further
links within the topic. Ordinary resource messages retain their single-message
or album behavior; topic/reply restrictions on the primary source still apply.
Explicit resource topic paths and `thread` parameters validate message and album membership.
Each post is resolved and downloaded before the next one is requested.

Files go into `<download_base>/<subdir>/<main-chat-id>/<main caption [post ID]>/`
with the complete preview album by default and optional `meta.json` containing
the original caption, entry links, hops, resource identities and completion
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
| `bot_timeout_seconds` | `60` | total response time after a successful start, including history RPC waits; existing middleware handles waits before sending the request |
| `bot_idle_seconds` | `3` | quiet interval after receiving media or another entry link |
| `bot_request_interval_seconds` | `0` | minimum seconds since the same bot's last reply was sent before requesting again; 0 disables it, maximum 86400; waiting is outside the reply timeout |
| `poll_interval_ms` | `500` | update-check/minimum history interval; unchanged history backs off up to max(5s, configured interval), new/edited replies reset it, live updates retain self-deleted messages |
| `max_bot_messages` | `500` | maximum responses to one bot request |
| `max_topic_messages` | `1000` | maximum messages per confirmed resource topic, including the root, service messages and deleted placeholders; exceeding the limit fails the post, at most `100000` |
| `rerequest_limit` | `3` | full-chain reissues per post after expired file references; `0` disables |
| `flood_retries` | `5` | retries after a bot's textual rate limit; `0` disables |
| `flood_wait_seconds` | `30` | wait when an explicit rate-limit message gives no duration |
| `max_flood_wait_seconds` | `3600` | maximum automatic wait for textual bot rate limits |

Zero selects defaults for most numeric `link_options`; only
`rerequest_limit` / `flood_retries` use `0` to disable retries, and
`bot_request_interval_seconds: 0` disables the request interval.
`scan_comments`, `include_previews` and `cleanup_bot_messages` can be set false.
Other upper bounds are: timeout 3600 seconds, idle 300 seconds, polling 60000 ms,
bot messages / comment limit 10000, topic messages 100000, reissues 10, flood retries 20, fallback
wait 3600 seconds and maximum wait 86400 seconds.
Idle and polling intervals must be shorter than the timeout, and
`flood_wait_seconds` must not exceed `max_flood_wait_seconds`.

With `bot_request_interval_seconds: 30`, a last reply sent at 12:00:00 allows
the next request to that bot at 12:00:30. Status text, files and trailing
promotion text all count. Download time counts toward the interval, so an
already elapsed interval adds no wait. New replies during the wait move this
boundary forward; repeated reads, file-reference refreshes, edits and your own
`/start` messages do not. Each bot has its own timestamp, shared across posts,
jobs, rate-limit retries and file reissues in the same batch. Waiting honors
cancellation and is outside `bot_timeout_seconds`.

English/Chinese rate-limit messages with seconds, minutes or hours trigger a
cancellation-aware wait followed by a fresh request. Progress notifications
keep waiting on the current request. This bot-specific backoff does not apply
to group message links. Multiple replies are collected together, including
status text, videos, photos and trailing link text. Read-state changes and
file-reference refreshes do not reset the idle interval. Timeout errors show
the actual last reply's ID and content summary.
Expired references first refresh the original message,
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

This mode validates files through archive manifests and supports timestamp
`incremental` selection of source posts. Comment-range mode and topic/reply selectors
are unsupported for primary-source selection. Resource group links must
identify accessible messages or confirmed topic roots. Invite links, group home links, external web
redirects, callback buttons, captcha and payment steps are not automatically
executed. Unrecognized custom rate-limit text results in a response timeout.

Nonexistent/deleted bots and invalid/inaccessible groups are skipped per branch.
A post with only unavailable targets is skipped; a dead entry does not prevent
healthy entries in the same post from downloading. All jobs
in one batch share a cache by target, independent of bot start parameters and
group message IDs. Recursive resolution caches only the target that failed.
Timeouts, flood waits, deleted individual messages and invalid start parameters
remain errors and never blacklist the whole target. The next batch run probes
targets again so restored resources can recover.

### Archive ranges and incremental windows

Both tag and linked archives accept source message ranges:

```json
{
  "jobs": [{
    "chat_url": "https://t.me/example_channel",
    "tags": ["#notes"],
    "start_comment": 100,
    "end_comment": 110,
    "subdir": "tag-range"
  }]
}
```

Add `"follow_links": true` to resolve linked resources. IDs belong to the source
chat, not a resource group or bot. Selecting any album member preserves the full
album, including members outside the boundaries. With `"incremental": true`,
ID fields are ignored; `last_ts` and overlap seconds select the source window.
The first run scans full history. Only a complete successful window advances
`last_ts`; failure, cancellation, the 100000-message scan limit or a `max_posts`
cutoff preserves the timestamp. Reruns validate existing files/manifests.
Check-only never advances timestamps. Use `max_posts: 0` for incremental
archives and a distinct `subdir` for each job.

## Command line

Use `tdl batch init` to generate examples. The following flags execute batch jobs:

| Flag | Default / effect |
| --- | --- |
| `-c`, `--config` | Auto-discovered in the working directory; explicit paths win |
| `--validate-only` | Offline validation of configuration, expressions, templates, precedence and known path conflicts; does not open account storage |
| `--check-only` | Preview without media downloads or advancing timestamps; range jobs report ID plans without proving availability, tag/linked jobs scan entries |
| `-y`, `--yes` | Answer runtime confirmations; cannot advance incomplete incremental windows |
| `--mode auto/comment/direct` | Default auto; override range/incremental jobs' comment switch |
| `--incremental` | Force incremental on all jobs, including tag and linked archives |
| `--state-file` | Override message-job and incremental-archive state paths; distinct jobs need separate paths to avoid scope conflicts |
| `--overlap-seconds` | Incremental lookback override; default -1 uses configuration/defaults, 0 falls back to another layer |
| `--retry-skipped` | Retry IDs recorded as unavailable; range/incremental state only |
| `-d`, `--dir` | Override root while keeping each job's subdir |
| `--template` | Range/incremental filename template; default `{{ .DialogID }}_{{ .MessageID }}_{{ filenamify .FileName }}`; see [templates](../template/) |
| `-i`, `--include` / `-e`, `--exclude` | Comma-separated extensions, mutually exclusive; range/incremental and linked-resource modes |
| `--takeout` | Download through Telegram takeout sessions |
| `--batch-threads` | Per-file threads; positive values override other layers, default 0 is no override |
| `--batch-limit` | Concurrent files; positive values override other layers, default 0 is no override |
| `--batch-pool` | Connection pool size; explicit 0 is unlimited |
| `-n`, `--ns` | Global namespace; explicit values override configuration |
| `-t`, `--threads` / `-l`, `--limit` / `--pool` | Global performance settings; override configuration only when explicitly supplied |
| `--delay` | File-start interval across all batch families, e.g. `1s`, default 0; waits honor cancellation and bot text backoff keeps its separate settings |

{{< command >}}
tdl batch -c config.json -y --check-only
tdl batch --incremental --overlap-seconds 7200
tdl batch -d /path/to/downloads -i mp4,jpg
tdl batch --batch-threads 8 --batch-limit 4 --batch-pool 16
tdl batch --retry-skipped
{{< /command >}}

`--check-only` still requires a logged-in client and may read Telegram; an
ordinary range plan may not query messages. It does not create download
directories, archives, exports or state files, and does not bind or migrate
legacy state. With `--retry-skipped`, retries are planned in memory without
changing saved records. Account caches and logs retain normal client behavior.
Use `--validate-only` for offline validation and `tdl batch init` to generate
configuration without an account.

{{< hint info >}}
`--batch-threads` / `--batch-limit` / `--batch-pool` win over the global
`-t` / `-l` / `--pool`, which in turn win over `threads` / `limit` / `pool` in
`config.json`. The final fallback is `8` / `4` / `16`, the same values the
python script used.
{{< /hint >}}

## Resume

Batch download resumes on two levels:

1. **Message level**: committed media identity, actual path, size and mtime are recorded,
   with batched writes to the state file (default `<download dir>/tdl_state.json`).
   Reruns query current media metadata in batches and validate the final file.
   Valid files are reused; missing, truncated or changed records are downloaded again.
   This check does not hash the whole payload. Deleted, media-less and extension-filtered
   messages have separate terminal records and are not requested on every run.
2. **Part level**: files are transferred in 1 MiB parts and the finished parts
   are tracked in `<file>.tmp.parts`. After an interrupt, the next run only
   fetches the missing parts instead of restarting the file. Journals bind the
   media kind, file ID, photo size selection, size, DC and part specification.
   Fresh references and reissued bot message IDs preserve the same file identity.

Successful downloads clean up their `.tmp` and `.parts` files; interrupted ones
keep them for the next run.

Both batch mode and `tdl dl` keep a separate parts journal for each concurrent
file. During downloads, journals are checkpointed after 32 new parts or on the
next write at least one second after the last checkpoint. Normal completion or
cancellation flushes the remaining records. Forcefully terminating the process
may require downloading up to 31 unrecorded parts again. Unbound legacy
journals (including v1), orphan temporary files and changed file identities
cannot be safely reused. Their bytes and journals are preserved together under
unique `.unverified` backup paths, which are reported, before a fresh download.
Atomic journal replacement does not guarantee data durability after power loss.

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

Without a saved `last_ts`, the first run scans from the beginning of history,
not just the last hour. A window scans at most 100,000 messages; exceeding
that limit fails and preserves the timestamp. Later runs use
`[last_ts - overlap_seconds, now]`, with completed IDs deduplicated by state.
ID range fields do not limit an incremental job.

A forum-topic job:

```json
{
  "chat_url": "https://t.me/example_forum",
  "incremental": true,
  "topic_id": 200,
  "subdir": "topic-200"
}
```

Replies to one channel post:

```json
{
  "chat_url": "https://t.me/example_channel/100",
  "chat": "https://t.me/example_discussion",
  "comment": true,
  "incremental": true,
  "reply_post_id": 900,
  "with_content": true,
  "subdir": "post-100-comments"
}
```

900 must be the forwarded root ID in the linked discussion group. If
`chat_url` already names that discussion group, use direct mode (omit
`comment` or set it false) and omit `chat`. Copy the discussion root's
message link to identify its ID; the channel ID 100 is not a substitute.
See [export messages](../tools/export-messages/) for `export_filter`
expression syntax and available message fields.

### State path conflicts

`state ... belongs to ...; use a distinct subdir or state file` means different
sources, topic/reply selectors, modes, output directories or naming/extension
rules share a state path. Assign distinct `subdir` values, and avoid a shared
top-level `state_file` or `--state-file` for multiple jobs. When changing output
rules, use a new directory/state path and preserve old state for the old job.
Tag and linked archives validate completion through files/manifests. Incremental
archives also use this state file for `last_ts`, without marking IDs as downloaded.
Changing tags or link selection rules triggers the scope check.

The new job identity binds the actual peer kind/ID, effective discussion mode,
account namespace and authenticated user ID, selection/filter rules, canonical absolute output paths and
output semantics. Performance and retry settings do not change the identity.
Nonempty v2, archive-v1 and unscoped legacy states cannot prove these conditions
and are explicitly refused without changing their files. Use a new subdirectory
or state path to rescan. Unbound old parts and tag payloads are preserved as
`.unverified` backups before downloading again; unverifiable completion records
are never silently claimed by a new job.
Preflight rejects shared state paths before login, and execution rejects
colliding dynamically named media targets.

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
  dialogs, and downloads discovered tag albums directly;
- queries selected forum topics using `getReplies`, retaining root media within the window/filter;
- uses bounded range cursors and streamed diagnostic exports; unchanged state avoids repeated serialization/writes;
- batches parts journal writes and removes the fixed 200 ms progress delay
  from small file downloads.
