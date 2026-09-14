---
title: "批量下载"
weight: 35
---

# 批量下载

`tdl batch` 是 `python/run_unified.py` 脚本的原生实现：读取**同一个 `config.json`**，
支持评论下载、直接消息下载、增量模式与断点续传，但不再为每个批次 fork 一个 `tdl`
进程，而是直接复用 tdl 内置下载器 —— 全程只有一个连接池、一个客户端，消息也按批
量请求解析。

## 快速开始

1. 在下载目录放置 `config.json`（格式见下）。
2. 执行：

{{< command >}}
tdl batch
{{< /command >}}

如果当前目录存在合法的 `config.json`，并且已经登录（`tdl login`），
那么**不带任何参数直接运行 `tdl`** 也会自动进入批量下载：

{{< command >}}
tdl
{{< /command >}}

{{< hint info >}}
设置环境变量 `TDL_NO_BATCH=1` 可以禁用这个自动行为，让 `tdl` 始终只打印帮助。
{{< /hint >}}

## 配置文件

与 Python 版本完全兼容，`download_dir` 也可以作为 `download_base` 的别名：

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

### 顶层字段

| 字段 | 说明 |
| --- | --- |
| `namespace` | 使用的 tdl 账号命名空间，等价于 `-n/--ns`，留空为 `default` |
| `download_base` | 下载根目录，默认 `downloads` |
| `incremental` | 全局开启增量模式（任务内的 `incremental` 优先） |
| `state_file` | 状态文件路径，默认 `<下载目录>/tdl_state.json` |
| `overlap_seconds` | 增量回看窗口秒数，默认 `3600` |
| `jobs` | 任务列表，按顺序执行 |
| `pool` / `threads` / `limit` | 可选的性能参数，等价于 `--pool` / `-t` / `-l` |

### 任务字段

| 字段 | 说明 |
| --- | --- |
| `chat_url` | 消息链接，支持 `https://t.me/user/123`、`https://t.me/c/123/456`、`https://t.me/user/123?comment=456` |
| `chat` | 增量模式下用于导出的对话，留空则从链接推断 |
| `subdir` | 该任务在 `download_base` 下的子目录 |
| `comment` | `true` 表示评论模式：`start_comment`/`end_comment` 是**关联讨论组**里的评论 ID（等价于 tdl 的 `?comment=N`），写数字等价于同时指定 `start_comment` |
| `start_comment` / `end_comment` | 下载范围，`start` 包含、`end` 不包含 |
| `incremental` | 覆盖全局增量开关 |
| `overlap_seconds` | 覆盖全局增量回看窗口 |
| `export_filter` | 增量模式的 expr 过滤表达式，等价于 `tdl chat export -f` |
| `with_content` | 增量模式导出时附带 `date`/`text` |
| `export_all` | 增量模式导出非媒体消息（默认只导出媒体） |
| `topic_id` | 只保留该话题（topic）下的消息 |
| `reply_post_id` | 只扫描该帖子的评论区（`messages.getReplies`） |

## 命令行参数

除了 `-c/--config`、`--check-only`、`-y/--yes`、`--mode`、`--incremental`、
`--state-file`、`--overlap-seconds` 这些与 Python 版本对应的参数外，还提供了：

{{< command >}}
tdl batch -c config.json -y --check-only
tdl batch --incremental --overlap-seconds 7200
tdl batch -d /path/to/downloads -i mp4,jpg
tdl batch --batch-threads 8 --batch-limit 4 --batch-pool 16
tdl batch --retry-skipped
{{< /command >}}

{{< hint info >}}
`--batch-threads` / `--batch-limit` / `--batch-pool` 优先于全局的 `-t` / `-l` / `--pool`，
全局参数又优先于 `config.json` 中的 `threads` / `limit` / `pool`，
最后回落到默认值 `8` / `4` / `16`（与 Python 脚本一致）。
{{< /hint >}}

## 断点续传

批量下载有两层续传：

1. **消息级**：每下载完一条消息就记录完成状态，合并写入状态文件（默认 `<下载目录>/tdl_state.json`），
   下次运行直接跳过已完成的消息，无需重新请求。已删除或没有媒体的消息也会被记录，
   不会每次重复尝试。
2. **分片级**：文件按 1 MiB 分片下载，已完成的分片记录在 `<文件名>.tmp.parts` 中。
   中断后重新运行只会拉取缺失的分片，而不是整文件重下。文件大小变化时会自动丢弃
   旧的分片记录。

下载成功后会清理 `.tmp` 与 `.parts` 文件；被中断的文件会保留下来以便续传。

批量模式与 `tdl dl` 都为每个并发文件独立保存分片状态。下载期间每新增 32 个分片，
或距上次保存满 1 秒后的下一次写入，会保存一次分片记录；正常退出或取消时补存剩余记录。
强制结束进程后，最多可能需要重下尚未记录的 31 个分片。旧版未带版本号的分片记录
无法证明临时文件未被清空，因此升级后会重新下载这些未完成的文件。

## 排错

### 所有消息都被跳过（`0 downloaded, N skipped`）

这几乎总是**模式和 ID 不匹配**：ID 被拿到错误的对话里查找了，于是每一条都像"已删除"。

最常见的情况是评论模式：`start_comment`/`end_comment` 是关联讨论组里的评论 ID，
必须在 `comment: true`（或 `--mode comment`）下运行 —— 此时 tdl 会去讨论组查找，
而不是去频道本身查找。反过来，如果 ID 其实是频道里的帖子 ID，就用 `comment: false`
或 `--mode direct`。

出现这种情况时会打印红色提示：

```
None of the 227 message(s) exist in dialog 1234567890.
Check --mode / the "comment" setting of the job and the id range, then run again with --retry-skipped.
```

修正配置后，用 `--retry-skipped` 重新运行即可：它会清空状态文件里"不可用"的记录，
让这些 ID 重新参与下载（已完成的记录和增量时间戳都会保留）：

{{< command >}}
tdl batch --retry-skipped
{{< /command >}}

### 增量模式不推进 `last_ts`

只要窗口内还有未下载（且未标记不可用）的消息，时间戳就不会前进，以免漏数据。
下载失败的消息会在下次运行重试；`--yes` 也不会强制推进。重试后仍有失败时，
任务返回失败状态。扫描达到上限时也会保留原时间戳，避免漏掉尚未扫描的消息。

## 增量模式

增量模式会按时间戳扫描对话，只下载时间窗内的新媒体，窗口默认向前回看 1 小时以
避免遗漏延迟消息：

{{< command >}}
tdl batch --incremental --overlap-seconds 3600
{{< /command >}}

时间戳只有在窗口内所有消息都下载完成（或被标记为不可用）之后才会推进，
所以失败的消息下次运行仍会重试。加 `--check-only` 可以只预览不下载、也不推进时间戳。

## 性能

相比 Python 版本，批量下载做了这些优化：

- 单进程、单连接池、单 Telegram 客户端，不再为每个批次重复登录与建连；
- 消息按 100 条一批批量解析（`channels.getMessages`），而不是每条一次请求；
- 下载前先按文件是否已存在去重，避免重复请求已下载内容；
- 自动跳过已删除消息，并把它们记录到状态文件中，后续运行不再请求；
- 每个文件按大小自动选择线程数（与 `tdl dl` 相同），分片级续传避免重下。
- 批量缓存消息信息，按需打开下载文件；复用已解析的对话，已完成任务无需再次解析；
- 分片状态合并写入，减少磁盘操作；小文件不再为进度条固定等待 200 毫秒。
