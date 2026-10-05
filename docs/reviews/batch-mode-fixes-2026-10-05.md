# batch 修复与整合验收记录

日期：2026-10-05；基线：`a6ebd5c3390c994c0b54dbcab5aca20ce8393ec2`。
对应审查：`batch-mode-review-2026-10-04.md`。原审查及诊断附件保留。

## 实施结果

| 项目 | 已实施的修复/改进 |
| --- | --- |
| F01 | core 在 worker 结束后调用可返回错误的 Finalize；三个 batch 家族与普通 dl 共用 transfer.Commit。Flush、Close、长度校验、Rename、journal 清理失败均传播；队列 Drain/取消也传播关闭错误。失败/取消不推进窗口。 |
| F02 | 扫描与下载使用同一个实际 peer；显式 chat 覆盖必须解析到相同类型/数字 ID，冲突在扫描和写状态前拒绝。 |
| F03 | v3/archive-v2 身份绑定实际 peer、命名空间和登录用户 ID、有效讨论组模式、选择/过滤规则及规范绝对输出路径。重试/性能参数不参与身份。 |
| F04 | v2 分片清单绑定媒体类型、文件 ID、照片尺寸、大小、DC、part size。同身份新引用可续传；旧/损坏/不同身份的临时文件及清单成对保留为唯一 unverified 备份。 |
| F05 | 统一验证 comment 类型/正整数旧 ID、topic/reply、范围、模式组合、表达式和模板；已知非法值不再变成全对话扫描。 |
| F06 | 文件启动 delay 接入三个家族，等待可取消；共享连接池/peers；额外 middlewares 接入。SkipResumePrompt 保留兼容，并明确 batch 自动续传语义。 |
| F07 | check-only 不创建输出/导出/批次状态、不迁移状态、不保存 retry-skipped；新增 validate-only，登录前离线检查。账号缓存/日志仍遵循客户端行为。 |
| F08–F09 | 入口产生一次不可变 PreparedRun；配置、CWD、namespace、参数来源及 updates 要求固定。自动启动与显式 batch 共用账号优先级。 |
| F10 | 登录前预检状态/静态输出占用，执行前预留所有 job 状态；执行中预留媒体、tmp、parts、原子替换和元数据路径，明确拒绝冲突。 |
| F11 | core/util/tgref 作为共享底层引用解析器，各门面保留模式限制；统一媒体执行、renderer 和 counts。 |
| F12 | 普通 dl 用稳定来源/媒体/实际输出键恢复，移除按位置认领；重复来源不重复打开同一文件，失败后关闭未消费队列。共享相册扫描遇到后页失败或取消时返回错误，不以先前成员冒充完整相册。 |
| F13 | GetMessages 按 peer 类型分派 channel 或普通消息 RPC，校验返回消息来源；支持普通 Chat/User。过期引用刷新也复核来源类型与数字 ID，拒绝同媒体、不同对话的响应。 |
| F14 | prog.Start 返回 stop-and-join，覆盖所有退出路径；相邻 export/users/up/forward 调用同步迁移。 |
| F15 | autodl 拆成 prepared/job/source/identity/download/result 等职责；linked 传输从编排中分离，公共生命周期进入 internal/transfer。 |

正常媒体完成记录包含稳定身份、实际路径、大小和修改时间。已完成消息仍批量复核当前媒体与最终文件；有效文件不重新下载，缺失/截断/同大小替换/本地修改会重下，已知不兼容旧文件先备份。检查不做全文件哈希，无法检测同时保持身份、大小和修改时间的任意内容篡改。

标签改为按完整相册扫描后直接下载，不再通过 JSON→app/dl 再请求元数据。每帖 `.tdl-completed.json` 保存不含说明正文的最小内部完成记录；关闭可见元数据也能验证同大小媒体替换。旧标签媒体没有可验证身份时先备份再下载，正确 ChatID 的旧目录可迁移，其他来源/账号目录不被认领。

链接归档保持稳定媒体文件名、主帖目录、完整相册、整链重请求和有界清理。完成 fingerprint 覆盖解析选择参数，扩大深度/链接等限制不会误用旧清单。JobResult/Counts 明确消息、帖子、文件、提交字节，以及 existing/filtered/unavailable/no-media/no-links/failed 原因，最终 CLI 显示这些单位。

资源侧论坛话题根与普通消息区分处理：仅 MessageActionTopicCreate 且服务器确认目标是对应论坛话题时才展开；扫描保留跨页相册、普通文本中的后续入口，并校验 peer 类型/ID、话题归属和分页进展。显式资源话题路径或 thread 参数参与链接身份，并校验消息及相册归属，不能静默忽略。`max_topic_messages` 默认 1000、上限 100000，根消息、服务消息与删除占位均计入限额，超限不以截断结果完成。该参数进入归档 scope 和每帖 fingerprint；主帖来源不开放新的 topic/reply 组合，主帖 URL 的话题上下文仍按原契约选择实际消息。

已解析的 Saved Messages 来源以真实 UserID/AccessHash 规范化，避免严格来源校验误拒 InputPeerSelf；没有真实 ID 的 Self 不能匹配任意用户。自身份/异身份以及空终页忽略取消的行为由合成回归验证，未执行真实 Saved Messages RPC。

## 性能与工具

- 普通范围改为游标，每次最多生成 100 个 ID，不再物化并复制百万 ID 数组。
- 普通论坛增量使用服务器 getReplies，显式保留符合窗口/表达式的话题根媒体；保留跨页与窗口边界。
- 标签只保留当前完整相册；可见索引和普通增量诊断 JSON 流式写临时文件，完整后原子发布。
- 状态 dirty/revision 判断避免未变化时反复复制、排序、序列化和写入；部分记录仍每 32 片/1 秒 checkpoint，退出补存。
- 增加 scripts/verify-batch.ps1；CI 的 build/test/race 覆盖根、core、extension 独立模块。

Windows amd64 / Go 1.26.3 / i7-12700H，`-benchtime=1x` 的分配量对照（时间仅为单次样本，不能据此推断网络吞吐）：

| 基准 | 基线 | 新实现 |
| --- | --- | --- |
| 1 万 ID 范围初始化 | 171,072 B/op | 7,344 B/op |
| 10 万 ID 范围初始化 | 1,612,864 B/op | 7,344 B/op |
| 100 万 ID 范围初始化 | 16,014,400 B/op | 7,344 B/op |
| 百万条未变化状态 Save | 每次全量序列化，约 28.7 MB/op、137 ms | 1,168 B/op、约 0.104 ms，无重复写入 |
| 128 MiB 分片 checkpoint | — | 4 journals/op |

范围基线从 HEAD 独立快照运行相同范围+iterator 初始化基准。dirty Save 仍然是全量 JSON，不声称大规模脏快照已有数量级提速。保留 100000 条增量和 100 万 ID 范围保护，不在本轮默认引入 job 并发、数据库替换或新配置格式。

## D:\AVMYS\log.md 与真实链路

日志中的入口提取已成功，失败发生在机器人 RPC/清理阶段，主要为 `400 CONNECTION_LAYER_INVALID`，另有一轮 `MSGID_DECREASE_RETRY`。用户当前执行位置为 `D:\AVMYS\2`，namespace 为 avmys-tag；本轮没有改配置或用户批次状态。

当前固定依赖 gotd v0.140.0 会给初始化后的应用 RPC 加 standalone invokeWithLayer。按 [Telegram 调用协议](https://core.telegram.org/api/invoking#layers)，该封装应与 initConnection 配合。增加窄范围 encoder adapter，移除库自动添加的普通 RPC 前缀，保留握手、显式初始化/层、withoutUpdates、Takeout、顺序封装和请求内容；主客户端及池的应用 RPC 都接入。没有把上述错误加入盲目重试。

本地测试检查真实 TL 编码、crypto 共享缓冲区长度、初始化和自定义 wrapper 顺序、并发及错误身份。协议测试中的模拟 400 是按协议规则编写的夹具，不是服务端复现。gotd 内部 DC auth transfer 在应用池中间件安装前执行，适配器没有声称重写这一内部路径。

用户明确授权单帖真实请求和本次清理后：

- 诊断在当前配置窗口挑选了带直接机器人入口的 [DreamTraveleo/3481](https://t.me/DreamTraveleo/3481)，预览媒体 1，入口指向 jisou2。随后部署规划确认 3481 是搜索引擎广告帖；该选择可验证协议调用，不能代表真正资源机器人的返回行为。
- 真实 messages.startBot 只执行 1 次并成功，本次没有出现 CONNECTION_LAYER_INVALID 或 MSGID_DECREASE_RETRY。
- 120 秒仅收到 2 条不含可用媒体/后续链接的回复，资源链超时；bot 解析媒体 0，bot 媒体读取字节 0。未保留清理前的回复摘要，不能确定机器人自身的具体限制。
- 本次请求及回复 3 条已清理；实际残留 0。唯一历史项是 MessageActionHistoryClear 服务占位，不是资源或请求残留。
- 当时 history RPC 共 133 次，并触发 4 次 FLOOD_WAIT(16)。因此进一步修复为未变化 history 自适应退避、实时更新保持配置频率、结束前复核 history；回复 timeout 同时约束 history RPC 等待。新超时信息包含 bot、无资源/未稳定原因及末条回复摘要。

本次证明真实 startBot 与清理阶段可以执行，不能据此声称机器人资源已完整下载；广告入口超时也不能证明实际资源机器人不可用。更新后的退避/超时/自删更新由模拟回归验证，没有追加真实 bot 请求。

追加只读源预览验证：同帖照片 `photo_5917039198475500130_y`，89,652 字节，文件 DC4、主连接 DC5。
两次生产连接池跨 DC 读取，以及一次完整 core downloader→OpenPartialFile→Finalize/Commit 提交，
SHA256 均为 `48e3f79626bb6f5cd75210ff8d2e50c602d2c2339c442f1f96d2c38dce5815a1`。
提交后 journal 删除，诊断源码和独立临时目录清理。该样本证明源预览下载/提交及这条跨 DC 路径可用，
不是机器人返回文件，也没有测试真实中断后的续传或真实过期引用重请求。

另一次只读元数据诊断确认：资源目标的真实 RPC 成功，Forum=true，返回
*tg.MessageService / *tg.MessageActionTopicCreate。初次“资源消息不可用”的结论不成立：
原 getLinkedMessage 只接受普通 Message，把存在的话题根服务消息误报为不可用，已补齐通用资源话题分支。
该特定样本涉及疑似未成年人性内容，已停止展开/下载，不公开源帖及资源链接。
用户选择完成合成回归和程序更新，后续通用话题解析仅用合成数据验证；不能写成该资源的媒体读取已通过。

## 验收与迁移

运行 `scripts/verify-batch.ps1 -Benchmarks`，三个模块 build/test/vet 全部通过。根模块排除需要外部 Telegram 测试服务的 test E2E；extension 只有编译/vet、没有测试文件。CGO_ENABLED=0，机器没有可用 GCC/Clang，未在本机运行 race；CI 已加入 CGO race。

回归覆盖文件提交失败、取消、引用过期/重新请求、同大小不同身份部分数据、完成后文件变化、来源冲突、scope/账号/选择变化、非法配置、命令优先级、只读规划、状态/输出碰撞、相册边界、元数据关闭、渲染退出、队列关闭、结果单位和有界 RPC。资源话题合成用例覆盖论坛/话题证明、类型与 ID 归属、跨页相册及后续链接、服务/删除消息限额、分页未前进、取消，以及引用过期后整链刷新和保留已完成分片。新增文件在对应包内，原审查探针仍保留为文本附件。

非空旧 v2/archive-v1/未绑定 scope 状态无法证明新身份时保留并明确拒绝，需选择新 state_file/subdir 重新扫描。v1 parts/孤立 tmp/身份冲突保留唯一备份，再重新下载。旧 message.json 仍读取；不删除旧输出，也不移除 namespace 数据库锁。

## 本机交付

最终 `scripts/verify-batch.ps1` 再次通过所有模块；最后的来源规范化改动补跑相关包测试和 vet。gofmt、git diff --check 和 actionlint 无错误。
重新构建仓库 `tdl.exe`，备份并替换实际执行文件 `D:\AVMYS\2\tdl.exe`。

- 旧程序备份：`D:\AVMYS\2\tdl.before-batch-fixes-20261005-092502.exe`。
- 新程序 SHA256：`F627C9120D7767AA2D77A159F6FCCB057A8E89D4E3D76C328E533CE6C3C45E6A`；仓库、暂存及最终目标一致。
- 从 `D:\AVMYS\2` 执行当前配置的 `--validate-only` 成功，namespace=avmys-tag、pool=16、threads=8、limit=4。
- 修复过程中前一轮部署版本从同目录执行当前配置的 `--check-only` 成功：35 messages、18 selected posts、0 failed；只预览入口，没有新机器人请求。最终话题补修后的构建按用户选择仅追加离线 `--validate-only`，未再读取真实资源。
