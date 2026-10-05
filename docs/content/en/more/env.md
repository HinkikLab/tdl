---
title: "Env"
weight: 20
---

# Env

{{< hint info >}}
The values of all environment variables have a lower priority than flags.
{{< /hint >}}

Avoid typing the same flag values repeatedly every time by setting environment variables.

|           NAME            |          FLAG           |
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

Choose the language for command help, prompts, progress and errors:

{{< command >}}
tdl --language zh batch
tdl --language en download ...
tdl --language auto batch
{{< /command >}}

Language selection follows this order: `--language`, `TDL_LANGUAGE`, locale environment variables (`LC_ALL`, `LC_MESSAGES`, `LANGUAGE`, then `LANG`), Windows' preferred display language, and English. `auto` uses this detection order. Chinese regions and scripts such as `zh-TW` and `zh-Hant` currently use Simplified Chinese.

`batch init --lang zh|en` only chooses the language of comments in the generated YAML file. Without `--lang`, those comments follow the selected application language.

{{< hint warning >}}
- `TDL_STORAGE` format in env is different from that in flags: `{"type": "bolt", "path": "/path/to/data-dir"}` (JSON object).
{{< /hint >}}
