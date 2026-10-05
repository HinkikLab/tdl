# batch 模式全面审查与优化方案

审查日期：2026-10-04  
代码基线：a6ebd5c3390c994c0b54dbcab5aca20ce8393ec2  
范围：当前 Go batch 实现及直接调用的归档、普通下载、状态和分片组件。  
交付性质：审查与方案。本次没有修改生产实现，没有执行真实 Telegram 下载、机器人请求或消息清理。

## 1. 审查结论

优化重点是**先修复完成判定和状态身份，再统一配置与执行模型，最后减少重复 RPC、磁盘中转和内存积累**。

现有实现已有值得保留的基础：单 batch 复用客户端和连接池，范围消息按最多 100 个 ID 查询，文件按需打开，分片状态批量保存，下载失败不把消息标为完成，归档边界保留完整相册，链接解析有循环/深度/数量上限，文件引用刷新校验当前媒体身份。这些能力应作为整合后的验收基线。

当前真正的分叉有三条：消息范围/增量走 pkg/autodl 自己的 iterator/elem/progress；标签归档写 JSON 索引后转入 app/dl；链接归档直接组装另一套 iterator/elem/progress。三条路径最终都使用 core/downloader，但文件提交、完成记录、错误传播、规划和显示规则分别维护，已经产生可复现的行为差异。

本次确认的高优先级问题包括：标签归档落盘失败仍返回成功；增量扫描与下载可以落到不同对话；状态标识遗漏实际讨论组模式及增量过滤条件；同大小的替换媒体会复用旧分片；普通增量任务的非法选择器可以被静默忽略。

建议按五个阶段实施。第一阶段的缺陷修复可以单独交付，不必等全部结构改造完成。

## 2. 当前执行链路与支持矩阵

~~~text
cmd/root.go（无参数自动启动）
        └─ cmd/batch.go
              ├─ 第一次 LoadConfigForRun：namespace、是否接收 bot updates
              └─ 创建客户端并认证
                    └─ autodl.Run：第二次 LoadConfigForRun
                          └─ Runner.runJob
                                ├─ FollowLinks → runArchiveWindow → chat.DownloadLinked
                                ├─ Tag/Tags    → runArchiveWindow → chat.DownloadTag → dl.Run
                                └─ 消息任务    → rangeIDs / planIncremental → Runner.download
                                                                  ↓
                                                           core/downloader
~~~

| 执行家族 | 来源 | 选择窗口 | 过滤与资源 | 当前完成判断 |
| --- | --- | --- | --- | --- |
| 消息下载 | 频道/超级群；评论模式解析关联讨论组 | 左闭右开 ID 范围，或增量时间窗口；增量可配 topic/reply | export_filter、include/exclude，消息中的媒体 | finished/skipped ID 集合；未入状态的文件解析后按大小判断 |
| 标签归档 | 频道/群主页或已确认论坛话题 | 全历史、ID 范围、增量时间窗口 | 图片/视频说明中的标签；匹配后保留完整媒体相册 | 转给 app/dl 的续传状态及 SkipSame 文件检查 |
| 链接归档 | 主对话主页或单篇主帖 | 全历史/单帖、ID 范围、增量时间窗口 | 可配主帖标签；解析隐藏链接、按钮、评论、消息及机器人多层入口 | 每帖完成清单与文件大小；无元数据时重新解析并检查实际文件 |

配置模板的 11 个 job 是不同能力组合的示例，不需要为它们建立 11 个执行器。

需要保留现有模式边界：范围下载不自动扩展相册；标签归档仅选择图片/视频；链接归档保持主帖目录和资源文件身份；标签和链接模式目前不支持的评论/topic/reply 组合，结构整合时不自动开放。

相关代码：

- [任务入口与分发](D:/github/tdl/pkg/autodl/run.go:200)
- [配置归一化](D:/github/tdl/pkg/autodl/config.go:224)
- [增量消息扫描](D:/github/tdl/pkg/autodl/incremental.go:106)
- [标签归档](D:/github/tdl/app/chat/tag.go:76)
- [链接归档](D:/github/tdl/app/chat/linked_archive.go:77)
- [共享下载器](D:/github/tdl/core/downloader/downloader.go:57)

## 3. 已确认的缺陷

优先级：P1 表示可能漏下载、混合文件或错误复用状态，需要优先修复；P2 表示功能差异、资源管理或维护成本问题；P3 表示整理和后续工具改进。复现实验使用内存 RPC 和临时目录，未连接 Telegram。诊断探针的 PASS 表示“成功确认当前问题”，不表示问题已经修复。

### F01 / P1：标签归档文件提交失败后仍可被认为成功

**证据**

[app/dl.progress.OnDone](D:/github/tdl/app/dl/progress.go:81) 在 Close、分片 Flush 或最终 Rename 失败时，只更新显示并记录错误；错误没有回传到 dl.Run 的返回值。[core/downloader](D:/github/tdl/core/downloader/downloader.go:85) 只汇总下载过程本身的错误，因为 Progress.OnDone 没有返回值。

[标签归档](D:/github/tdl/app/chat/tag.go:183) 直接使用 dl.Run 的返回值。完整增量窗口只要回调返回 nil，[runArchiveWindow](D:/github/tdl/pkg/autodl/archive.go:37) 就会推进 last_ts，未校验每个媒体最终文件是否提交完成。

**本地复现**

模拟一个 11 字节视频，在下载过程中创建占用最终文件路径的目录，使 Rename 报 Access is denied。downloadTag 返回 nil，媒体只留下 .tmp 文件。结合上层窗口提交代码，具备错误推进时间戳的条件。本次实验没有把实际 chat 下载与 Runner 的窗口提交串成一次跨包调用，时间戳风险由两段已检查的调用链共同确定。

**修复方向**

先给 app/dl progress 汇总提交错误，并由 dl.Run 返回；标签归档只有所有目标达到明确终态才提交窗口。后续共享执行器应把关闭、校验、重命名、分片清理和完成记录列为执行步骤，Progress 只负责显示。不能以进度条显示 done 或网络下载返回 nil 代表归档成功。

**验收**

Rename、Close、Flush 任一步失败，都返回失败、保留可恢复临时文件、保持旧 last_ts；重新运行后能完成并提交一次。

### F02 / P1：增量扫描来源与下载来源可以不同，甚至推进错误窗口

**证据**

[scanPeer](D:/github/tdl/pkg/autodl/run.go:564) 优先读取 job.chat；[download](D:/github/tdl/pkg/autodl/run.go:388) 重新使用 chat_url 和 comment 规则解析下载对话。扫描结果仅保留 ID，未携带原始 peer，未比较两个来源是否相同。

**本地复现**

chat_url 指向对话 50，chat 指向对话 60。对话 60 扫到媒体 ID 7，下载阶段却向对话 50 查询 ID 7。模拟返回 MessageEmpty 后，程序报告 0 downloaded、1 skipped，并实际把 last_ts 推进到当前窗口结束时间。

真实环境中，若错误对话恰好存在相同 ID，还可能下载无关媒体。

**修复方向**

把每个发现项绑定到确定的 PeerKey；扫描和下载使用同一 ResolvedSource。保留旧配置里通过 chat 指向讨论组的写法，但解析后校验它与预期讨论组一致；来源冲突在执行下载和写完成状态前报告配置错误。

**验收**

明确复测频道、自动讨论组、显式讨论组、论坛话题、单帖回复、冲突来源。冲突不能被解释为媒体已删除，不能推进窗口。

### F03 / P1：状态标识没有覆盖所有有效选择条件

**证据**

[stateScope](D:/github/tdl/pkg/autodl/run.go:606) 使用 link.Chat、job.mode、job.chat、topic/reply、输出路径、模板和扩展名过滤，但没有包含：

1. URL 的 comment 查询参数导致的**实际讨论组模式**；
2. 普通增量任务的 export_filter 和 export_all。

[commentDialog](D:/github/tdl/pkg/autodl/run.go:554) 使用 URL 与 job 的 OR 结果；自动模式的 job.mode 只根据 job.comment 推导。因此同一频道直接任务和通过 URL 进入讨论组的任务，可以得到相同 scope。

**本地复现**

- https://t.me/source/10 与追加 ?comment=20 的链接，实际下载对话模式不同，但 scope 完全相同。
- export_filter 从 ID > 10 改为空，同时切换 export_all，scope 仍完全相同。

前者可能错误跳过另一个对话中的同 ID；后者使扩大后的过滤规则继承旧 last_ts，较早的未选媒体不会重新扫描。

**修复方向**

先修正 scope 的有效模式和过滤字段；随后使用归一化后的数字 peer 类型/ID、选择器、过滤表达式、输出语义生成有版本的 JobIdentity。输出路径转为绝对规范路径。namespace/账号隔离策略也应明确进入身份设计，避免不同账号的可访问性记录被混用。

URL 的 comment 目前优先于 --mode direct 是现有文档明确说明的行为，不能当作新发现的覆盖缺陷直接改变；本次确认的缺陷是这一有效模式没有进入 state scope。

archiveStateScope 对标签、链接选项已有额外绑定，应保留，但需区分实际选择/输出语义和纯重试参数。调整 bot_timeout 等执行参数不应必然让选择身份变化；标签大小写、重复项应规范化，同时保留会影响目录首标签的原始顺序。

**验收**

实际 peer、讨论组模式、export_filter、topic/reply 或输出语义变化会拒绝不兼容状态；纯性能/重试参数变化不误拒绝。旧状态迁移必须有明确策略，不能静默丢弃或盲目认领。

### F04 / P1：跨运行续传只校验大小，替换媒体可与旧分片拼接

**证据**

[PartsStore 磁盘格式](D:/github/tdl/core/downloader/parts.go:25) 仅保存版本、分片数、大小和完成分片，没有媒体 ID、类型、照片尺寸选择或 DC。普通消息/标签文件名主要绑定消息 ID，附件变化时仍可能使用相同路径。

[引用刷新](D:/github/tdl/core/downloader/reference.go:176) 已校验单次运行中的媒体身份，这个保护没有覆盖下次运行重新载入 .tmp.parts 的场景。

**本地复现**

先保存 2 MiB 文件的旧 A 内容第一片，再以新 document ID=999、相同大小恢复。只请求 offset=1048576 的新 B 内容。最终文件第一片为 A、第二片为 B，状态却标记完成。

**修复方向**

给分片清单绑定稳定 FileIdentity：媒体类型/文件 ID/照片尺寸、大小、DC 和分片规格。OpenPartial 校验身份再决定复用。file_reference、access_hash、机器人临时消息 ID 不作为稳定文件身份。

旧分片记录无法证明媒体身份时保留原始恢复资料，并从已验证的新身份重新下载，不能只凭相同大小复用旧字节。链接资源现有文件名已包含稳定媒体身份，应继续支持机器人换消息 ID 后续传。

**验收**

同文件新引用可以续传；同大小新文件 ID、照片尺寸或 DC 变化拒绝旧分片。重新请求同一资源后仍只获取缺失部分。

### F05 / P1：普通增量的非法选择器和 comment 类型可以被忽略

**证据**

[普通任务 normalize](D:/github/tdl/pkg/autodl/config.go:321) 没有与标签分支同等的 topic_id/reply_post_id 正整数校验，也没有完整校验 comment 的类型。运行时扫描只对大于零的选择器生效；字符串 comment 不等于布尔值，也无法转换为 legacy integer。

**本地复现**

topic_id=-1、reply_post_id=0、comment="true" 的普通增量配置通过 Normalize。非正数选择器不会限定扫描；字符串 comment 不开启评论模式。

**修复方向**

统一基础字段校验，再按模式检查支持的组合。comment 只接受旧协议已有的 nil/bool/integer；存在的 topic/reply 必须为有效正 ID。未知配置字段继续兼容忽略，已识别字段的非法值要报告具体 job/字段。表达式和模板在建立下载任务前编译。

**验收**

非法选择器不能退化成全对话扫描；legacy integer comment 仍能推导 start_comment；报错包含配置路径、job、字段及原因。

## 4. 已确认的差异和结构问题

| 编号 / 优先级 | 证据与影响 | 建议 |
| --- | --- | --- |
| F06 / P2 | cmd/batch 把 --delay 写入 Options.Delay，autodl 范围执行和 linked 执行没有读取它；标签经 app/dl 读取 Viper delay。相同 batch 参数随家族变化。Options.Middlewares、SkipResumePrompt 也没有使用点。 | 统一 EffectiveOptions 和任务调度 delay；定义它作用于文件启动，而 bot 文本限流仍使用专属退避。废弃或接入未使用选项。 |
| F07 / P2 | runJob 在 CheckOnly 分支前创建下载目录；planIncremental 写 .tdl_tmp 导出；RetrySkipped 可保存状态；LoadStateStore 在读取旧无 scope 状态时立即写回。 | 将状态加载与迁移/保存分开。规划阶段只读，明确离线 validate 和需要账号的 plan；现有 check-only 的写入行为有部分文档说明，调整时补充兼容说明。 |
| F08 / P2 | cmd/batch 和 autodl.Run 各解析一次配置；两次之间创建账号客户端和更新处理器。文件若被编辑，namespace/updates 与执行内容可能不一致。auto-start 又单独加载。 | 在入口形成一个配置快照，后续传递同一个 PreparedRun。通过显式来源信息处理 CLI > job/global > defaults，不从执行层继续读取 Viper。 |
| F09 / P2 | AutoStart 无条件以配置 namespace 覆盖传入 namespace，用它判断 LoggedIn；root 后面才判断 --ns 是否显式指定。 | 自动启动与显式 batch 共用 namespace 解析，避免检查账号 A 的登录状态却执行账号 B。保持原有自动启动入口。 |
| F10 / P2 | 全局 state_file/--state-file 让多个 job 使用同一路径；不同 scope 的冲突通常到后一个 job 才发现。路径只 Clean，没有绝对化。文件模板也没有输出路径占用检查。 | 在 PreparedRun 里预检状态路径及已知输出冲突；对媒体逐项预留规范路径。动态命名冲突有明确错误，避免并发写同一个 .tmp。 |
| F11 / P2 | TagOptions、LinkedOptions、autodl.Options 各有不同参数面；多个 peers.Manager 分别创建。URL 分别经过 ParseLink、ParseTagTarget、parseResourceLink，支持范围及校验严格度不同。 | 共享底层 Telegram 引用解析与 peer 服务，保留各模式约束。采用小型明确类型，控制包依赖方向。 |
| F12 / P2 | app/dl 的续传 key 按 peer/ID 列表生成，完成项按逻辑位置记录；目录映射/模板未进入 fingerprint。标签转入这条链后又多一层位置状态。 | 统一为稳定媒体/消息键，记录实际输出路径和终态；恢复时不能仅凭位置跳过新目标目录中的文件。 |
| F13 / P2 | GetMessages 当前只接受可转成 InputChannel 的 peer；普通 Chat 类型会直接报 not a channel，虽然来源解析及其他家族能处理它。 | 先明确来源能力矩阵，在 peer 解析后提前诊断；若完整支持普通群，则按 peer 类型选择合适消息 RPC。不能把全体“群组”笼统视为同一能力。 |
| F14 / P2 | autodl.download 在 newIter 成功前就启动 Render，后续创建 iterator 出错直接返回，未 Stop。prog.Wait 只检查 IsRenderInProgress，缺少对异步启动完成的同步；linked 独自补了一套停止并等待逻辑。 | 共用渲染生命周期辅助器：配置验证后启动，所有退出路径 Stop 并 join，取消也完成关闭。终态错误与显示处理分离。 |
| F15 / P3 | run.go 749 行混合分发、来源解析、下载、状态身份、命名、确认和参数选择；linked_archive.go 639 行混合归档编排、文件引用刷新、iterator 和 progress。 | 按职责拆文件/服务，用当前测试约束行为；文件拆分是完成边界抽取后的整理工作。 |

相关位置：[CLI 选项](D:/github/tdl/cmd/batch.go:53)、[重复加载](D:/github/tdl/pkg/autodl/run.go:103)、[自动启动](D:/github/tdl/pkg/autodl/session.go:41)、[状态加载时迁移](D:/github/tdl/pkg/autodl/state.go:280)、[普通下载 fingerprint](D:/github/tdl/app/dl/iter.go:434)、[只支持 channel 的批量查询](D:/github/tdl/core/util/tutil/tutil.go:224)、[渲染启动位置](D:/github/tdl/pkg/autodl/run.go:395)、[Wait](D:/github/tdl/pkg/prog/prog.go:50)。

## 5. 性能与恢复策略的优化空间

以下属于源码确认的成本或策略限制，本次没有测真实 Telegram 吞吐，因此不承诺具体加速倍数。

### 5.1 标签归档的 JSON 中转和重复解析

标签扫描已经取得每个媒体的消息、说明、大小，随后又组装 JSON manifest 落盘，经 FromFile 读取，再在 app/dl 内按单条 GetHistory 查询媒体。原本范围路径使用的每 100 条批量解析优势没有覆盖标签下载链路。

建议让发现结果直接以 ArchiveUnit/MediaItem 输入共享执行器；媒体位置失效时再主动刷新。保存给用户查看的索引成为独立输出步骤。metadata=false 的语义仍控制可见归档信息，不让执行链强制依赖公开的 meta.json。

预期收益是少一轮文件编码/解析和逐条元数据 RPC；使用模拟 RPC 计数与真实授权样本验证，不靠 wall-clock 单测声称网络吞吐提高。

### 5.2 全历史匹配结果在内存中积累

downloadTag 先保存全部 posts，再保存 manifest、目录映射，最后开始下载。普通增量也同时保存 IDs、dates 和 exported，dates 返回后未被使用；大量历史与完整正文会增加内存占用。

建议使用有界流：发现相册/消息 → 过滤 → 排队 → 下载提交 → checkpoint。保留完整相册边界；链接资源继续逐帖及时请求，避免预取机器人资源后引用过期。归档索引可流式写入临时文件后原子提交。

rangeIDs 最多允许 100 万个 ID，随后又复制/排序；后续可改成区间迭代器。先测 1 万/10 万/100 万 ID 和大量归档元数据的峰值内存。

### 5.3 增量扫描与首次历史回填

普通增量按 OffsetDate 定位，而 archive history 主要从当前历史顶部扫描；普通论坛增量先扫整群再本地过滤，标签论坛模式使用 GetReplies。两种策略对大群成本不同。

建议统一边界及“扫描完整”结果，在 API 支持处向服务端下推时间/topic/reply 条件，并用跨页、边界相册和话题根消息回归验证。首次超过 100000 条当前会失败并保留 last_ts，不能简单解除保护；可以另设可恢复历史回填游标，分段完成后再交给常规增量。

### 5.4 状态保存成本

消息状态每次保存会复制、排序全部 finished/skipped，序列化整个 JSON，500 ms 只是减少次数。长时间运行后，保存成本随历史记录总量增长。

先加入 dirty 判断、批量终态提交和写入计数/耗时基准。仅当大规模场景测出瓶颈时，再评估紧凑区间或追加日志加定期快照。当前没有依据要求把全部状态切到 SQLite，也不建议在本轮默认引入它。

原子替换与崩溃持久性是两种保证；若要承诺断电后分片可复用，需要明确数据文件和 journal 的刷盘顺序及 fsync 策略，不能仅凭 Rename 推导。

### 5.5 不可用目标和关闭元数据的恢复限制

失效目标导致整帖跳过、batch 结束后清空缓存，是当前已明确写入文档的策略。多入口主帖有一个失效入口时，其余健康入口也不会归档。这是策略边界，不能作为未记录的程序错误直接改变。

增量跳过后若推进 last_ts，后来恢复的目标只有仍处于 overlap 窗口或主动回填时才会重新发现。可增加独立“待复查目标/帖子”队列和退避策略，保持默认跳过行为，并在结果里区分 unavailable 与 complete。

关闭 meta.json 后，linked 的 completedLinkedPost 无法使用新生成的完成清单，后续需要重新走链才能发现并跳过相同文件。可评估独立的最小内部完成状态，不保存用户关闭的说明/公开元数据；这属于后续产品语义选择，不是第一阶段修复的前提。

### 5.6 日志和进度统计

普通路径把已存在文件和扩展名过滤计入 finished，但不一定计入下载摘要的 skipped；标签/链接摘要使用帖子数，普通路径使用消息数。用户难以判断“已下载”“已存在”“过滤”“不可用”“失败”分别多少。

建议统一 JobResult，明确 messages/posts/files/bytes 各单位和 skipped 原因。保留每个 job 的错误原因，最终 batch 不只返回“有 N 个失败”。执行层输出事件，由一个显示层负责终端日志和进度，方便测试、将来 JSON 报告和脚本使用。

## 6. 建议的整合模型

### 6.1 配置 DTO 与执行计划分离

继续接受现有 JSON/YAML、字段别名、comment bool/int、未知字段和 11-job 模板。Config/Job 作为旧格式兼容 DTO，解析后生成不可变的执行计划，不在 Runner 里修改 job.mode、dir 或再次判断优先级。

~~~text
Legacy Config + CLI provenance
       ↓ decode / defaults / validate
PreparedRun
       ├─ account + shared services + effective performance
       └─ PreparedJob[]
              ├─ SourceSpec       来源及讨论组/topic/reply 选择器
              ├─ SelectionSpec    ID range / incremental / history / single post
              ├─ FilterSpec       caption tags / expression / extensions
              ├─ ResolutionSpec   direct media / follow links
              ├─ OutputSpec       flat / post directory / metadata
              ├─ ResumePolicy     stable identity / completed / pending
              └─ JobIdentity      规范化状态身份
~~~

这一模型表达能力组合，但每种任务仍有清晰的验证规则。无需增加动态插件注册表或过度通用的框架。是否引入显式 kind/schema_version 可以放到后续，首轮无需用户改 config.json。

### 6.2 共用发现项、相册单元和完成结果

建议少量共享类型：

~~~go
type PeerKey struct {
    Kind string // channel/chat/user，避免只靠裸数字
    ID   int64
}

type MediaItem struct {
    Source     PeerKey
    MessageID  int
    FileKey    string // 稳定媒体身份
    Size       int64
    DC         int
    TargetPath string
    // 另携带当前下载位置、消息日期和刷新来源
}

type ArchiveUnit struct {
    SourcePostID int
    Items        []MediaItem
    // 原始说明、grouped_id、目录及资源入口按需提供
}

type WindowResult struct {
    ScanComplete bool
    Counts       JobCounts
    Pending      []PendingItem
    Err          error
}
~~~

这是接口草案，不代表需要逐字照搬。ArchiveUnit 保留主帖/相册身份；范围任务使用独立消息单元。刷新策略继续支持普通原消息刷新和 linked 全链重新请求。

窗口提交要检查：扫描完整、所有必要媒体已成功提交或进入该模式允许的跳过终态、无待重试项、没有取消。单纯 nil error 不再作为唯一证据。

### 6.3 共用传输生命周期

共享顺序：

1. 验证规范输出路径并预留占用；
2. 校验 FileIdentity，打开/恢复 .tmp；
3. 使用既有 core/downloader 下载；
4. Flush、关闭、校验最终长度、原子 Rename；
5. 清理分片记录，设置时间；
6. 写入该媒体终态；
7. 提交帖完成结果；
8. 完整窗口提交 last_ts。

普通任务、标签、linked 分别提供“来源发现、资源展开、输出布局、刷新来源”。打开文件、续传、提交和统计共用。

第一步修复可在现有 progress 内聚合错误；结构阶段再把业务提交移出显示回调。若后续修改 core/downloader 接口，应提供兼容适配，并覆盖原有下载、上传/转发邻接调用中实际受影响的实现，避免只改一个模块。

### 6.4 包依赖方向

建议保留 pkg/autodl.Run 和 chat.DownloadTag/DownloadLinked 作为兼容门面，再按需要抽到根模块的内部服务：

~~~text
cmd / app command facades / pkg.autodl facade
                    ↓
              internal/batch      配置编译、计划、编排、结果
              internal/archive    相册、布局、元数据、链接策略
              internal/transfer   文件生命周期、执行适配
                    ↓
            core/downloader / dcpool / storage
~~~

目录名称是建议，依赖方向才是约束。archive 可调用 transfer；transfer 不依赖 app/chat 或 app/dl；core 不反向依赖根模块。共用小型引用/身份模型，防止循环导入。已有 pkg/prog 可继续承担展示公共组件。

## 7. 分阶段实施与验收

| 阶段 | 实施内容 | 交付/通过条件 |
| --- | --- | --- |
| S0：锁定行为 | 把本次诊断探针转换为期望正确行为的回归；建立全部旧示例及模式组合基线；增加可注入 clock、pool、state store。 | F01–F05 的缺陷回归先失败；保留当前可用场景、目录和续传行为。 |
| S1：修复正确性 | 修复最终提交错误传播、扫描/下载 peer 绑定、有效 scope、分片身份和选择器校验。分别做小改动，先覆盖高风险路径。 | 缺陷回归通过；失败不推进窗口；旧分片与状态迁移有明确输出；无需配置格式升级。 |
| S2：统一配置和规划 | 单次配置快照，EffectiveOptions/PreparedJob，模式能力校验，namespace 和状态路径预检；将只读规划与写状态拆开。 | 混合 config 在登录/执行前得到一致计划；未知字段保持兼容；check-only 的副作用契约清晰且覆盖旧 state。 |
| S3：统一文件执行 | 抽出 transfer 生命周期和 JobResult；消息、标签、linked 逐条迁移；标签取消强制 JSON→app/dl 中转；共用 renderer start/stop/join。 | 三个家族的 Flush/Close/Rename 失败、取消、续传得到一致终态；命名与相册不变；普通 dl 的邻接回归通过。 |
| S4：性能和维护工具 | 有界扫描/消费、复用 peer 和已发现元数据、state dirty/批量保存、大数据基准、CI 多模块和 race；按测量决定更大存储改造。 | RPC 数/内存/磁盘写入有前后数据；首次 10 万限制和相册边界不被弱化；授权真实样本验证完成。 |

建议逐阶段保留可运行版本，把“提取公共类型”和“切换执行路径”拆成可审查的小提交。原有结构在每种家族迁移完成后再移除重复代码。

不建议首轮同时做 job 并发、状态数据库替换、配置格式升级和下载器重写。现在 job 按顺序执行，有助于保护机器人请求关联、output/state 文件占用和恢复顺序；后续并发需先有状态/路径所有权、机器人请求互斥及全局文件并发预算。

## 8. 必须保留的兼容约束

- 原有 config.json/config.yaml、download_dir 别名、未知字段、comment bool/int 及所有有效示例。
- ID 范围仍为 [start_comment, end_comment)，增量仍优先于范围；零值默认语义保持现状并在 EffectiveOptions 中明确表示。
- 全局和 job 的 incremental/write_metadata 继承；--incremental 强制开启；batch 性能 flag > 显式全局 flag > 配置 > 默认；pool=0 的“不限”不能丢失。
- URL comment 目前优先于 --mode direct 的契约；标签 URL 自动推导论坛选择器与普通增量需显式 topic_id 的现有差异。
- 相册完整性、说明中的 Unicode 完整标签匹配、any/all、仅标签说明的目录名、主帖说明保留和可见 URL 清理。
- meta.json 默认输出及 message.json 旧清单读取兼容；关闭元数据仍可续传/按文件跳过。
- 普通文件名默认模板、linked 稳定文件 ID 文件名，机器人换消息 ID 后仍可恢复同一媒体。
- 确认不可用的 linked 目标自动跳过、仅本轮缓存；网络/限流/单帖删除不黑名单整个目标。
- 取消时等待下载工人关闭文件、保存分片；linked 使用有界清理并报告失败；不能把取消记为完成。
- 不能移除 namespace 数据库锁来实现并行，也不能让多个 job 并发修改同一 state/.tmp。
- v2 scope、archive-v1、旧未绑定 scope 状态和 v1 parts 都要列入迁移矩阵。无法验证旧身份时保留原文件，报告需重新扫描/下载；不能清空旧完成记录后冒充兼容迁移。

## 9. 测试与验证矩阵

| 维度 | 关键用例 |
| --- | --- |
| 配置 | JSON/YAML、别名、未知字段、非法已知字段、comment int/bool、全局/job/CLI 优先级、pool/overlap 零值、模式不支持的组合 |
| 来源 | 公开/私有频道、超级群、普通 Chat、直接/讨论组、明确 chat 覆盖、topic/reply、解析/下载 peer 一致 |
| 选择 | 空窗口、范围边界、首次历史、overlap、超过扫描上限、max_posts 截断、单帖及跨页相册 |
| 过滤 | Unicode caption tags、any/all、同相册不同成员命中、表达式变化、扩展名过滤、无匹配和全部过滤 |
| 恢复 | 缺失/截断/同大小替换最终文件；旧/新 journal；同身份新引用；大小/ID/DC 变化；新目录和 caption 变化；旧清单迁移 |
| 提交 | Flush/Close/Rename 失败、输出冲突、状态保存失败、renderer 初始化前结束、取消/超时/强制结束后恢复 |
| 链接 | 隐藏实体、按钮、评论、多根入口、循环/深度/数量、恢复目标、BOT 超时/文本限流、重复 expiry、资源集合变化、清理失败 |
| 可见行为 | source/title、消息/帖子/文件计数、已存在/过滤/不可用原因、最终错误、metadata 开关、生成模板和中英文文档 |
| 性能 | 元数据 RPC 次数、首个文件开始时间、峰值内存、状态写入次数/耗时、32 片/1 秒 checkpoint 行为、并发文件独立 parts |

CI 当前 lint 已覆盖根/core/extension，但 unit-test 在根目录跑 go list ./...，不会递归进入独立 module；[master.yml](D:/github/tdl/.github/workflows/master.yml:52) 的 core 下载器测试不在这条 unit-test 中。建议给测试也建立三模块矩阵；对含分片/更新/显示并发的包在可用 CGO runner 跑 race，避免把根模块测试通过当成 core 回归已经执行。

实际 Telegram 验证应在实现后使用用户授权的有限媒体样本，并分别记录：真实 scan、真实下载字节和 hash、真实中断续传、机器人失效引用重请求及清理。模拟 RPC 结果只证明本地执行逻辑；本次不提供真实网络吞吐或机器人行为结论。

## 10. 本次完成的验证

执行环境：Windows amd64，Go 1.26.3；go.work 包含根/core/extension，模块声明 Go 1.25.8；CGO_ENABLED=0，因此未执行 race。

已有相关测试全部通过：

~~~text
# 根模块
go test ./pkg/autodl ./app/chat ./app/dl ./cmd -count=1 -timeout=180s
go test ./pkg/tmessage ./pkg/prog ./pkg/kv -count=1 -timeout=120s
go vet ./pkg/autodl ./app/chat ./app/dl ./cmd

# core 模块
go test ./downloader ./dcpool -count=1 -timeout=120s
go test ./util/tutil ./util/fsutil ./storage -count=1 -timeout=120s
go vet ./downloader ./dcpool
~~~

dcpool、pkg/tmessage 没有测试文件；其输出不能算作功能回归测试。未修改 extension，实现审查没有要求运行它的全部测试。

新增了 6 个一次性诊断探针，最终均确认预期的问题行为：

~~~text
CommentURLOverridesDirectAndCollidesScope:
  effective comment dialog, but direct/comment scopes equal

IncrementalScansDifferentChatAndCommitsSkipped:
  scanned chat=60, downloaded chat=50, skipped ID=7, last_ts advanced

SameSizeReplacementReusesOldPartial:
  only requested offset=1048576
  final payload = old A chunk + new B chunk; ID marked finished

InvalidSelectionAcceptedAndLegacyCheckWrites:
  topic_id=-1 / reply_post_id=0 / string comment accepted
  loading legacy unscoped state writes the file

IncrementalFilterScopeAndBasicGroup:
  export_filter/export_all changes leave scope equal
  InputPeerChat metadata query rejected as not a channel

TagRenameFailureReturnsSuccess:
  final Rename failed with Access is denied
  downloadTag returned nil; final payload remains .tmp
~~~

首次标签诊断的模拟 RPC 没有回应 app/dl 使用的单条 GetHistory，先产生了夹具错误；补齐该请求后复现通过。没有将这一夹具错误计为生产缺陷。

诊断源码保留为文本附件，便于后续转成正式回归；临时放入包目录的测试源码已移除：

- [autodl 诊断源码](D:/github/tdl/docs/reviews/batch-mode-review-probes/autodl_probe.go.txt)
- [chat 标签诊断源码](D:/github/tdl/docs/reviews/batch-mode-review-probes/chat_probe.go.txt)

恢复诊断时把两个文本分别复制到 pkg/autodl 与 app/chat 下，改为 batch_review_probe_test.go，运行以下命令。这些断言针对当前错误行为，修复后必须改成期望正确行为的回归断言：

~~~text
go test ./pkg/autodl ./app/chat -run '^TestReviewProbe' -v -count=1 -timeout=90s
~~~

Python 原始脚本在当前 checkout 中不存在，源码注释和现有格式说明了迁移关系；本次以当前 Go 行为、文档和兼容测试为依据，没有声称逐行核对过 Python 原实现。

