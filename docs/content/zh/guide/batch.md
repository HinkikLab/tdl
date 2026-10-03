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
| `tag` / `tags` | 一个 tag 或多个 tag 数组；按图片/视频说明文字中的完整 hashtag 筛选，与消息 ID 范围任务二选一 |
| `tag_match` | 多 tag 匹配方式：`any`（默认，命中任意一个）或 `all`（全部命中） |
| `max_posts` | tag 任务最多匹配多少组，默认 `0` 表示扫描全部历史；可用于先做小规模验证 |
| `follow_links` | 开启预览帖资源归档，从正文超链接或评论中追踪机器人/群组资源链接 |
| `link_options` | 跳转、机器人等待、重新请求与会话消息清理参数，见下文 |

### 按 tag 归档图片和视频

tag 任务的 `chat_url` 填群组或频道主页链接，无须填写消息 ID 范围：

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

先运行 `tdl batch -c config.json --check-only` 查看匹配数量，再运行
`tdl batch -c config.json -y` 下载。输出结构为
`downloads/AVMYS/<chat-id>/<命中的tag> <清理后的说明> [消息ID]/`；目录名会移除
说明中的所有 hashtag 和非法字符，并将 tag 加说明限制为 64 个字符。
同时命中多个 tag 时，使用配置顺序中的第一个命中 tag 作为目录前缀。
如果原始说明只有 hashtag，目录名使用说明中按顺序出现的全部 tag，去掉 `#`，
并保留消息 ID 后缀防止重名。
每组包含图片/视频、原始说明
`message.txt` 与带所有成员消息 ID、说明和来源链接的 `message.json`。
Telegram 相册只要有一条成员的说明包含 tag，就会下载整组。
再次运行会按文件大小跳过已完成媒体，未完成文件可沿用下载器的分片续传。

### 归档预览帖链接中的实际资源

将任务的 `follow_links` 设为 `true`。`chat_url` 可以是主频道首页或某一篇
主帖链接；首页默认扫描全部历史，`max_posts` 可限制候选帖子数。
可选 `tags` / `tag_match` 仍匹配主帖原始说明。首页也可设置
`start_comment` / `end_comment`，此时表示**主频道帖子 ID** 的左闭右开范围，
命中相册任意成员会保留整组。不需要设置 `comment: true`。

```json
{
  "namespace": "default",
  "download_base": "downloads",
  "jobs": [
    {
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
    }
  ]
}
```

仓库根目录的 `config.linked.example.json` 包含完整参数示例。替换频道名和
账号 namespace 后运行 `tdl batch -c config.json --check-only` 预览主帖与评论中的
入口链接，再运行 `tdl batch -c config.json` 下载。
`--check-only` 不请求机器人、不下载文件、不删除消息，也不验证入口后的跳转链。

正文链接包括普通 URL、文字背后的 Telegram 超链接和内联 URL 按钮。
主帖无资源入口时查找关联讨论组中的评论。支持 `t.me/bot?start=...`、
`tg://resolve?...`、公开/私有消息链接、评论链接和完整资源相册。
机器人返回的中转链接可继续指向其他机器人或群组消息，受深度和链接数量限制。
主帖和资源说明不会混用：输出保持为
`<download_base>/<subdir>/<主群ID>/<主帖说明 [主帖ID]>/`，
其中保存实际资源、整组预览媒体（默认开启）、原始 `message.txt` 和
包含主帖、入口、跳转链、资源身份与完成状态的 `message.json`。
目录沿用标签归档规则，另移除可见 URL。资源文件名包含稳定 Telegram 文件 ID，
所以机器人重新发送后消息 ID 变化仍可沿用原文件和 `.tmp.parts`。

| `link_options` 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `scan_comments` | `true` | 主帖/中间群组消息无入口时，读取关联评论中的链接 |
| `comment_limit` | `100` | 每个评论区最多读取最近多少条消息 |
| `include_previews` | `true` | 同时保留主帖整组预览媒体 |
| `cleanup_bot_messages` | `true` | 本帖下载结束后清理本次各层机器人的请求及回复消息 |
| `max_depth` | `8` | 最大跳转层数，上限 `32` |
| `max_links` | `100` | 每篇主帖最多解析多少个不同入口，上限 `1000` |
| `bot_timeout_seconds` | `60` | `/start` 成功后等待完整回复的时间，Telegram RPC 限流等待沿用现有中间件 |
| `bot_idle_seconds` | `3` | 收到文件/下一层链接后，连续无新增或编辑消息多久认为回复已稳定 |
| `poll_interval_ms` | `500` | 历史查询间隔，配合实时更新捕获已自删的消息 |
| `max_bot_messages` | `500` | 单次请求允许接收的回复条数上限 |
| `rerequest_limit` | `3` | 每篇帖子中文件引用失效后完整链的重新请求次数；`0` 禁用 |
| `flood_retries` | `5` | 机器人文本限流后重新请求次数；`0` 禁用 |
| `flood_wait_seconds` | `30` | 明确限流提示没有时长时的等待秒数 |
| `max_flood_wait_seconds` | `3600` | 机器人文本限流的最大自动等待秒数，超出时报告失败 |

机器人会按主帖顺序请求，资源下载使用 batch 的连接池、线程数和文件并发数。
收到“请求频繁/冷却/Too many requests”等提示后，识别秒、分钟、小时并自动等待
再请求；只有明确限流且没有时长的提示才使用默认等待。普通“正在处理”提示继续等
当前请求。群组消息跳转不使用机器人文本退避。
文件引用过期时先刷新原消息；原消息已删或引用未更新时重新走完整链，
仅在 Telegram 文件 ID、大小和 DC 一致时继续写入原文件。
失败不会将归档标为完成；完整归档再次运行会校验所有资源文件的大小，并跳过机器人请求。

清理只使用本次收集到的机器人私聊消息 ID，不删除整个会话或群组帖子。
包括中转、限流重试、文件重新请求以及一次发送的多条回复。
正常取消/失败也会尝试清理，失败会报告错误；强制结束进程时清理无法执行。
运行期间避免手动向同一机器人发送其他请求，以免无关联信息的机器人回复混入。
分批发送间隔较长的机器人应调大 `bot_idle_seconds` 与 `bot_timeout_seconds`。

此模式使用每帖归档清单续跑，不能与时间戳 `incremental`、评论范围模式或
topic/reply 选择器混用。群组链接须指向账号可访问的具体消息；邀请链接、
仅有群首页、验证码、付费门槛、回调按钮和外部网页跳转不会自动执行。
自定义限流提示若不包含可识别关键词，会在等待超时后报告失败。

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
