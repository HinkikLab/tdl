---
title: "批量下载"
weight: 35
---

# 批量下载

`tdl batch` 是 `python/run_unified.py` 脚本的原生实现，保留其 `config.json` 基础字段，
支持直接消息、评论、增量扫描、说明标签归档和预览帖链接资源归档。
所有任务复用一个 Telegram 客户端和连接池，并支持文件分片续传。

## 快速开始

1. 一键生成带说明的示例配置，无须登录或连接 Telegram：

{{< command >}}
tdl batch init
{{< /command >}}

2. 编辑生成的 `config.json`：保留需要的 job，删除其他示例，将 `example_channel`、
   `example_discussion`、`example_forum`、消息 ID 和标签替换为自己的内容；
   `namespace` 填已登录账号的命名空间。
3. 先检查，再下载：

{{< command >}}
tdl batch -c config.json --check-only
tdl batch -c config.json
{{< /command >}}

如果当前目录存在合法的 `config.json`，并且已经登录（`tdl login`），
那么**不带任何参数直接运行 `tdl`** 也会自动进入批量下载：

{{< command >}}
tdl
{{< /command >}}

{{< hint info >}}
设置环境变量 `TDL_NO_BATCH=1` 可以禁用这个自动行为，让 `tdl` 始终只打印帮助。
{{< /hint >}}

## 一键生成示例配置

{{< command >}}
tdl batch init
tdl batch init -c examples/config.json
tdl batch init -c config.json --force
{{< /command >}}

默认写入当前目录的 `config.json`，也可用 `-c/--config` 指定输出路径；缺少的父目录
会自动创建。文件已存在时会报告错误并保留原内容，`--force` 明确允许替换。
生成只操作配置文件，不初始化账号存储、不发起机器人请求，也不执行示例中的 job。

文件是标准 UTF-8 JSON，说明使用额外的 **`_comment`** 字段，解析器会忽略该字段。
**`comment` 是评论模式配置，不能填说明文字。** 不要在 JSON 中添加 `//` 或尾随逗号。
配置解析器也接受 YAML；未指定 `-c` 时依次查找 `config.json`、`config.yaml`、`config.yml`。
所有相对路径以**执行命令的当前目录**为基准，不是配置文件所在目录。

内置模板位于 `pkg/autodl/config.example.json`，包含以下 11 个 job。
每个示例使用独立 `subdir`，避免不同任务共用消息级状态文件：

| 示例 | 用途 | 核心配置 |
| --- | --- | --- |
| 01 | 频道/群组消息范围 | `comment: false`，`start_comment` / `end_comment` |
| 02 | 关联讨论组评论 ID 范围 | `comment: true`，范围为讨论组内 ID |
| 03 | 对话历史增量下载 | `incremental: true` |
| 04 | 论坛指定话题增量下载 | `incremental: true`，`topic_id` |
| 05 | 单篇帖子评论增量下载 | `incremental: true`，`chat`，`reply_post_id` |
| 06 | 单个说明标签归档 | `tag` |
| 07 | 多个标签命中任意一个 | `tags`，`tag_match: "any"` |
| 08 | 多个标签全部命中 | `tags`，`tag_match: "all"` |
| 09 | 单个主帖的链接资源归档 | 主帖 URL，`follow_links: true`，完整 `link_options` |
| 10 | 扫描主频道并从评论中找入口 | 主页 URL，`follow_links: true`，`scan_comments: true` |
| 11 | 按主帖范围和标签筛选链接资源 | 主页 URL，`follow_links: true`，范围、标签及机器人等待参数 |

模板中的 URL 与 ID 是占位示例，须修改后使用。`max_posts: 1` 便于首次检查，
改为 `0` 才会扫描全部历史；它仅用于标签和链接资源归档。

## 选择模式

每个 job 的执行顺序是：先判断 `follow_links`，再判断 `tag` / `tags`，
其他 job 按 `incremental` 选择时间窗口或消息 ID 范围。
任务按 `jobs` 数组顺序执行，可以在同一配置中混用各类 job。

| 模式 | `chat_url` | ID 含义及限制 |
| --- | --- | --- |
| 直接范围 | 频道/群组主页或帖子 URL | 必填左闭右开范围；URL 的帖子 ID 不会自动变成下载范围 |
| 评论范围 | 主频道帖子 URL，`comment: true` | 范围是关联讨论组的消息 ID；按 ID 下载，不自动限定为这篇主帖的回复 |
| 增量 | 对话主页或主帖 URL | 不需要范围；`topic_id` / `reply_post_id` 仅在增量扫描中生效 |
| 标签归档 | 频道/群组主页或论坛话题链接 | 可用 `topic_id` 限定话题；不填 `comment`、范围、`reply_post_id`；使用独立历史扫描与文件检查，增量参数不改变其行为 |
| 链接资源归档 | 主频道主页或某篇主帖 | 可加主帖标签；主页可加主帖 ID 范围；不能与时间戳增量、`comment: true`、topic/reply 选择器组合 |

`--mode auto` 是默认值，范围/增量任务按 `comment` 和 URL 的 `?comment=N`
判断是否使用讨论组。`--mode comment` / `--mode direct` 覆盖任务的 `comment` 开关，
但 URL 自带的 `?comment=N` 仍会选择讨论组；切回频道直接下载时也要移除该查询参数。
这两个选项不改变标签或链接资源归档的模式。混合配置通常保持 `--mode auto`。
`--incremental` 会强制所有 job 的增量开关，含链接资源 job 的配置会因此校验失败；
混合配置请在各 job 中单独设置 `incremental`。

## 配置文件

保留 Python 脚本的基础字段；标签和链接资源模式属于原生 batch 的扩展。
最小的直接下载示例如下，下载消息 100 到 109：

```json
{
  "namespace": "default",
  "download_base": "downloads",
  "incremental": false,
  "jobs": [
    {
      "_comment": "直接下载消息 100 到 109",
      "chat_url": "https://t.me/example_channel/100",
      "comment": false,
      "start_comment": 100,
      "end_comment": 110,
      "subdir": "direct"
    }
  ]
}
```

### 顶层字段

| 字段 | 类型 / 默认值 | 说明 |
| --- | --- | --- |
| `_comment` | 字符串或数组，可省略 | 说明文字，忽略，不影响执行 |
| `namespace` | 字符串，`default` | tdl 账号命名空间；显式 `-n/--ns` 优先 |
| `download_base` | 字符串，`downloads` | 下载根目录；命令行 `-d` 可覆盖根目录，仍保留各 job 的 `subdir` |
| `download_dir` | 字符串，可省略 | `download_base` 的旧版别名；后者非空时优先 |
| `incremental` | 布尔值，`false` | 全局增量开关，job 可覆盖；混合模式建议保持 `false` |
| `state_file` | 字符串，可省略 | 默认 `<download_base>/<subdir>/tdl_state.json`；仅消息范围/增量模式使用，混合任务建议保留默认独立路径 |
| `overlap_seconds` | 非负整数，`3600` | 增量回看秒数；当前实现 `0` 会回落到其他层或默认值，不能用来关闭回看 |
| `jobs` | 非空数组，必填 | 任务列表，按顺序执行 |
| `pool` | 非负整数，`16` | 连接池大小；`0` 表示不限制连接池大小 |
| `threads` | 正整数，`8` | 每个文件的最大下载线程数 |
| `limit` | 正整数，`4` | 同时下载的文件数，不是同时执行的 job 数 |

### 任务字段

| 字段 | 说明 |
| --- | --- |
| `_comment` | 说明文字，忽略 |
| `chat_url` | 必填。支持公开主页/帖子、私有 `https://t.me/c/1234567890/456`、`?comment=456`、不带协议及 `/s/` 预览链接；账号须有访问权限 |
| `chat` | 增量扫描对话，可填用户名、数值 ID 或 Telegram URL；留空则从 `chat_url` 推断。覆盖扫描来源时须保证其消息 ID 与下载对话一致 |
| `subdir` | job 的相对子目录；不能为绝对路径或通过 `..` 逃出下载根目录。不同任务使用独立子目录 |
| `comment` | 默认 `false`；`true` 表示使用关联讨论组。旧版整数写法同时代表评论模式，且在未填 `start_comment` 时作为起始 ID。推荐使用布尔值及显式范围 |
| `start_comment` / `end_comment` | 正整数，`start` 包含、`end` 不包含，`end > start`，范围最多 1,000,000 条。字段沿用旧名，直接模式仍填这两个字段 |
| `incremental` | 覆盖全局增量开关 |
| `overlap_seconds` | 覆盖全局增量回看窗口 |
| `export_filter` | 增量模式的 expr 过滤表达式，等价于 `tdl chat export -f` |
| `with_content` | 增量模式导出时附带 `date`/`text` |
| `export_all` | 增量模式导出非媒体消息（默认只导出媒体） |
| `topic_id` | 正整数话题根消息 ID；用于增量或标签扫描。标签任务也能从话题链接推导，显式值须与链接一致；增量任务仍需显式填写 |
| `reply_post_id` | 增量模式使用，扫描对话中的正整数回复根消息 ID；关联讨论组中应填转发根消息 ID，不是主频道帖子 ID |
| `tag` / `tags` | 一个 tag 或多个 tag 数组；按图片/视频说明文字中的完整 hashtag 筛选，与消息 ID 范围任务二选一 |
| `tag_match` | 多 tag 匹配方式：`any`（默认，命中任意一个）或 `all`（全部命中） |
| `max_posts` | tag 任务最多匹配多少组，默认 `0` 表示扫描全部历史；可用于先做小规模验证 |
| `follow_links` | 开启预览帖资源归档，从正文超链接或评论中追踪机器人/群组资源链接 |
| `link_options` | 跳转、机器人等待、重新请求与会话消息清理参数，见下文 |

`export_filter`、`with_content`、`export_all`、`topic_id`、`reply_post_id` 和增量
`overlap_seconds` 不用于直接 ID 范围扫描。`export_all: true` 使非媒体消息也参与规划，
不会把普通文本变成媒体文件；保留文字的归档请使用标签或链接资源模式。
增量导出会保留在 `<job目录>/.tdl_tmp/` 中供排查。

### 消息范围和评论范围

直接下载某条消息 100：`comment: false`、`start_comment: 100`、`end_comment: 101`。
评论范围示例：

```json
{
  "chat_url": "https://t.me/example_channel/100",
  "comment": true,
  "start_comment": 900,
  "end_comment": 910,
  "subdir": "comments"
}
```

这里 100 是主帖 ID，900 到 909 是讨论组的消息 ID。可从具体评论链接
`https://t.me/example_channel/100?comment=900` 中取得评论 ID。
范围任务不会按主帖过滤讨论组中的这些 ID；需要只扫描某一帖的评论时，
采用示例 05 的增量 `reply_post_id`。

### 按 tag 归档图片和视频

tag 任务的 `chat_url` 填群组/频道主页或论坛话题链接，无须填写消息 ID 范围：

```json
{
  "namespace": "default",
  "download_base": "downloads",
  "jobs": [
    {
      "chat_url": "https://t.me/example_channel",
      "tags": ["#教程", "#示例"],
      "tag_match": "any",
      "max_posts": 1,
      "subdir": "tags"
    }
  ]
}
```

私有数字群组可填 `https://t.me/c/2255983776/` 扫描整个群组；
`https://t.me/c/2255983776/41872/` 仅扫描话题 41872。
也支持话题内消息链接 `https://t.me/c/2255983776/41872/42000`，
或主页链接配合 `"topic_id": 41872`。公开群组的话题路径及 `?thread=41872`
使用相同规则。运行时会向 Telegram 确认该话题存在且可访问；普通消息、
已删除或不可访问的话题不会退回全群扫描。命令行同样支持
`tdl chat download-tag --chat <话题链接> --tag <标签>`，或主页配合 `--topic 41872`。

先运行 `tdl batch -c config.json --check-only` 查看匹配数量，再运行
`tdl batch -c config.json -y` 下载。输出结构为
`downloads/tags/<chat-id>/<命中的tag> <清理后的说明> [消息ID]/`；目录名会移除
说明中的所有 hashtag 和非法字符，并将 tag 加说明限制为 64 个字符。
同时命中多个 tag 时，使用配置顺序中的第一个命中 tag 作为目录前缀。
如果原始说明只有 hashtag，目录名使用说明中按顺序出现的全部 tag，去掉 `#`，
并保留消息 ID 后缀防止重名。
每组包含图片/视频、原始说明
`message.txt` 与带所有成员消息 ID、说明和来源链接的 `message.json`。
Telegram 相册只要有一条成员的说明包含 tag，就会下载整组。
再次运行会按文件大小跳过已完成媒体，未完成文件可沿用下载器的分片续传。

标签可带或不带 `#`，支持 Unicode 字母、数字及下划线，匹配不区分大小写，
并按完整 hashtag 匹配，`#教程` 不匹配 `#教程合集`。`tag` 和 `tags` 同时填写时合并
并去重；`all` 可由同一相册不同成员的说明共同满足。只扫描图片/视频说明，不匹配
独立文本帖或其他附件的文字。标签归档使用固定命名规则，`--template`、
`--include` / `--exclude` 当前不影响此模式。

配置错误会分行显示文件路径、任务编号、群组链接和原因。终端仅将 `Error:`
标题标红；需要调用栈和源码行号时，加全局参数 `--debug`，详细信息会单独显示。

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

`tdl batch init` 的示例 09 包含完整参数；仓库根目录的
`config.linked.example.json` 也保留单模式示例。替换频道名和
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

数值 `0` 在大多数 `link_options` 字段中表示使用默认值；只有
`rerequest_limit` / `flood_retries` 的 `0` 表示禁用对应重试。
`scan_comments`、`include_previews`、`cleanup_bot_messages` 均可显式设为 `false`。
其他上限为：timeout `3600` 秒、idle `300` 秒、poll `60000` 毫秒、
bot messages / comment limit `10000`、rerequest `10` 次、flood retries `20` 次、
fallback wait `3600` 秒、maximum wait `86400` 秒。
`bot_idle_seconds < bot_timeout_seconds`，轮询间隔也须小于 timeout；
`flood_wait_seconds` 不能大于 `max_flood_wait_seconds`。

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

生成示例使用 `tdl batch init`；下表用于执行 `tdl batch`：

| 参数 | 默认值 / 作用 |
| --- | --- |
| `-c`, `--config` | 当前目录自动查找配置；显式路径优先 |
| `--check-only` | 预览规划，不下载媒体、不推进增量时间戳；范围模式只报告 ID 计划，不验证文件可用性；标签/链接模式扫描入口 |
| `-y`, `--yes` | 自动回答运行中的确认，不允许推进尚未完成的增量窗口 |
| `--mode auto/comment/direct` | 默认 `auto`；覆盖范围/增量 job 的 `comment` 开关 |
| `--incremental` | 强制所有 job 开启增量；混合链接资源 job 时不要使用 |
| `--state-file` | 覆盖所有范围/增量 job 的状态路径；多 job 应保持独立路径，避免 scope 冲突 |
| `--overlap-seconds` | 覆盖增量回看窗口；不传为 `-1`，使用配置/默认值，`0` 回落到其他层 |
| `--retry-skipped` | 重试记录为无媒体/已删除的消息；只用于范围/增量状态 |
| `-d`, `--dir` | 覆盖下载根目录，仍追加每个 job 的 `subdir` |
| `--template` | 范围/增量媒体文件名模板，默认 `{{ .DialogID }}_{{ .MessageID }}_{{ filenamify .FileName }}`；参见[模板指南](../template/) |
| `-i`, `--include` / `-e`, `--exclude` | 逗号分隔扩展名，互斥；用于范围/增量和链接资源模式 |
| `--takeout` | 使用 Telegram takeout 下载会话 |
| `--batch-threads` | 每文件线程数，正数覆盖其他层，默认 `0` 不覆盖 |
| `--batch-limit` | 文件并发数，正数覆盖其他层，默认 `0` 不覆盖 |
| `--batch-pool` | 连接池大小，显式 `0` 表示不限制 |
| `-n`, `--ns` | 全局账号命名空间；显式指定时覆盖配置 |
| `-t`, `--threads` / `-l`, `--limit` / `--pool` | 全局性能覆盖；仅显式指定时覆盖配置 |
| `--delay` | 全局每文件任务间隔，例如 `1s`，默认 `0` |

{{< command >}}
tdl batch -c config.json -y --check-only
tdl batch --incremental --overlap-seconds 7200
tdl batch -d /path/to/downloads -i mp4,jpg
tdl batch --batch-threads 8 --batch-limit 4 --batch-pool 16
tdl batch --retry-skipped
{{< /command >}}

`--check-only` 仍需登录并读取 Telegram（普通范围计划可能无需查询消息）；
范围/增量任务可能创建本地目录或增量导出文件。
不要与 `--retry-skipped` 一起用于只读检查，后者会更新状态中的不可用记录。
无账号、离线生成配置请用 `tdl batch init`。

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

首次运行没有 `last_ts`，会从历史起点扫描，而非只扫描最近一小时；单个增量窗口
最多扫描 100,000 条消息，超过会失败并保留原时间戳。之后扫描
`[last_ts - overlap_seconds, 当前时间]`，已完成消息由状态去重。
配置中不要填写范围来限制增量任务，增量执行会从时间窗口获取 ID。

只扫描一个论坛话题：

```json
{
  "chat_url": "https://t.me/example_forum",
  "incremental": true,
  "topic_id": 200,
  "subdir": "topic-200"
}
```

只扫描一篇频道帖的评论：

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

900 必须是关联讨论组中的转发根消息 ID。若已经使用讨论组自身的 URL 作为
`chat_url`，可改用直接模式（省略 `comment` 或设 `false`），并省略 `chat`。
获取转发根消息的链接后，使用其讨论组消息 ID；不要将频道的 100 直接作为 900 使用。
`export_filter` 的表达式语法和消息字段见[导出消息](../tools/export-messages/)。

### 状态文件冲突

出现 `state ... belongs to ...; use a distinct subdir or state file` 时，说明不同来源、
topic/reply、模式、下载目录或文件命名/过滤规则共用了状态路径。
为每个任务指定不同 `subdir`，并避免给多种任务指定一个顶层 `state_file` 或
`--state-file`。改变输出规则后使用新的子目录或状态文件；保留原状态便于恢复原任务。
标签和链接资源任务使用文件/归档清单续跑，不使用这个消息级状态文件。

## 性能

相比 Python 版本，批量下载做了这些优化：

- 单进程、单连接池、单 Telegram 客户端，不再为每个批次重复登录与建连；
- 消息按 100 条一批批量解析（`channels.getMessages`），而不是每条一次请求；
- 下载前先按文件是否已存在去重，避免重复请求已下载内容；
- 自动跳过已删除消息，并把它们记录到状态文件中，后续运行不再请求；
- 每个文件按大小自动选择线程数（与 `tdl dl` 相同），分片级续传避免重下。
- 批量缓存消息信息，按需打开下载文件；复用已解析的对话，已完成任务无需再次解析；
- 分片状态合并写入，减少磁盘操作；小文件不再为进度条固定等待 200 毫秒。
