---
title: "环境变量"
weight: 20
---

# 环境变量

{{< hint info >}}
所有环境变量的值优先级低于命令行选项。
{{< /hint >}}

通过设置环境变量，避免在每次重复输入相同的命令行选项。

|           环境变量            |          对应选项           |
|:-------------------------:|:-----------------------:|
|         `TDL_NS`          |        `-n/--ns`        |
|        `TDL_PROXY`        |        `--proxy`        |
|       `TDL_STORAGE`       |       `--storage`       |
|        `TDL_DEBUG`        |        `--debug`        |
|        `TDL_SIZE`         |       `-s/--size`       |
|       `TDL_THREADS`       |     `-t/--threads`      |
|        `TDL_LIMIT`        |      `-l/--limit`       |
|        `TDL_POOL`         |        `--pool`         |
|         `TDL_NTP`         |         `--ntp`         |
|  `TDL_RECONNECT_TIMEOUT`  |  `--reconnect-timeout`  |
| `TDL_DISABLE_PROGRESS_PS` | `--disable-progress-ps` |
|      `TDL_TEMPLATE`       |     dl `--template`     |
|      `TDL_LANGUAGE`       |       `--language`      |

## `--language`

选择命令帮助、提示、进度和错误的显示语言：

{{< command >}}
tdl --language zh batch
tdl --language en download ...
tdl --language auto batch
{{< /command >}}

语言选择顺序为：`--language`、`TDL_LANGUAGE`、语言环境变量（依次为 `LC_ALL`、`LC_MESSAGES`、`LANGUAGE`、`LANG`）、Windows 首选显示语言，最后回退到英文。`auto` 使用此自动识别顺序。目前 `zh-TW`、`zh-Hant` 等所有中文地区和脚本标记统一使用简体中文。

`batch init --lang zh|en` 仅用于选择生成 YAML 文件注释的语言。未指定 `--lang` 时，注释语言跟随应用当前语言。

{{< hint warning >}}
- `TDL_STORAGE` 环境变量的格式与命令行选项不同：`{"type": "bolt", "path": "/path/to/data-dir"}` (JSON 对象)。
{{< /hint >}}
