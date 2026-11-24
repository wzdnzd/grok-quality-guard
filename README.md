# Grok Quality Guard

> 面向 CLIProxyAPI v8 的 Grok / xAI 账号质量守卫插件。

Grok Quality Guard 观测 Grok / xAI auth 的真实响应质量，识别 HTTP 200 但实际降智的输出，并提供账号级状态、冷却、恢复探测和可选的透明换号保护。质量异常默认只读观测；上游 401 凭据失效和明确额度耗尽仍会直接禁用或冷却账号。

设计细节和完整状态机见 [插件设计文档](designs/plugin.md)。

## 功能

- 被动观测 `passive_guard`：通过 response、stream chunk 和 usage 分析已有请求，不改变路由
- 可选透明保护 `transparent_guard`：拦截所有可识别的 Grok / xAI OAuth 请求，在首次向客户端 emit 前完成质量门
- 透明模式由插件完整接管 auth 选择：`healthy` / `unknown` 分层、Priority 数值越大越优先、后台快照索引、round-robin/random/fill-first、preferred last-success 与 session affinity
- 真实 thinking 证据判定，不使用空 reasoning stub 或单独的 `reasoning_tokens` 判断健康
- 检测 `missing_thinking`、`buffered_burst`、`cipher_drool`、`fake_encrypted_dump`、`fast_reasoning_ratio_dump`、`soft_tps`、`hard_tps`、`empty_stream`、`probe_error`、`proxy_error`
- 账号状态机：`unknown`、`healthy`、`suspect`、`degraded`、`cooling`、`quota_exhausted`、`credential_invalid`、`disabled_by_guard`、`externally_disabled`
- 首次确认异常可配置为只观察或临时冷却，冷却到期通过固定 prompt 恢复探测
- 通用代理节点、模板与 Resin 来源统一管理；支持 `http`、`https`、`socks5`、`socks5h`、标准 URL / 地址在前 / `host:port@user:pass` 多格式批量导入、均衡绑定、轮换和清除
- 内嵌中文化管理页，按“概览 / 账号 / 代理管理 / 任务中心 / 事件日志 / 设置”分 Tab；账号、代理和质量交叉验证等异步批量任务统一从任务中心查看与管理，事件日志只承担审计
- 账号详情展示最近尝试与脱敏质量判定依据，明确区分非质量错误和上一次真实质量结论
- 任务中心展示任务进度、总数、完成/处理中/等待/成功/失败/跳过数量、逐项结果和历史任务；历史详情按需加载
- 历史任务默认保留 3 天，可配置保留天数、记录数、归档磁盘上限和单任务明细上限
- auth、proxy URL、Admin Token、xAI token 全程脱敏

## 质量判定要点

真实思考证据只有两类：

1. 非空 reasoning / thinking delta
2. encrypted reasoning 内容达到下限

密文下限固定为：

```text
max(min_encrypted_bytes, reasoning_tokens * encrypted_bytes_per_reasoning_token)
默认 max(256 bytes, reasoning_tokens * 2 bytes)
```

每 token 系数按线上实测校准：同时带明文推理摘要、可确认健康的样本中，密文长度为每 reasoning token 2.44 至 8.67 字节，约一半低于 4 字节。系数取 4 时，不输出推理摘要的健康账号会被误判为 `missing_thinking`。下限只用于拦截与推理账单明显不符的短 stub

主动探测与透明请求一样会请求 `include: ["reasoning.encrypted_content"]`。Grok 只有在请求时才返回推理密文，缺少该字段时不输出推理摘要的账号没有任何思考证据，每次探测都会被判为 `missing_thinking`

以下内容不作为思考证据：

- 空 reasoning item
- `: grok2api-reasoning-start` timing stub
- 仅报告 `usage.reasoning_tokens`
- 未达到密文下限的 encrypted stub

TPS 使用总 output tokens，其中包含 reasoning tokens：

```text
tail = duration - firstToken
generationWindow = tail，若 tail < firstToken 且 tail < 1000ms 则取 duration
TPS = outputTokens * 1000 / generationWindow
```

尾段比等待时间还短且不足 1 秒，说明输出是在等待之后一次性到达的（工具调用参数、隐藏推理、中间链路缓冲），这段时间只反映传输，所以改用完整时长作为生成时间的上界。参考实现只在有推理账单时做这一修正，导致 Codex 一次 75 token、50ms 内到达的工具调用被算成 1500 tok/s。输出少于 `min_passive_output_tokens`（默认 32）的样本只记录 TPS，不做速度判定。`fail_closed` 的突发检测仍沿用参考实现的到达窗口，但不检测客户端工具调用。

注意：这是显示与记账口径的修正，不会放宽思考闸门。实测健康账号上同样规模的工具调用轮次带有 245–294 个推理 token，所以推理账单为 0 的工具调用仍按 `missing_thinking` 处理，只是它不再叠加速度异常。

TPS 计时从发起上游模型调用前开始，而不是从首个 host stream chunk 可读时开始。透明保护会缓冲并延迟暴露首个 chunk，若从可读时才开始计时，会把上游 TTFT 从分母中排除并产生异常高 TPS。

### 与参考实现的对齐

流式判定按 `references/grok2api` 的 `ClassifyQualityHold` 与 `egress-quality-guard` 的审计逻辑实现：

- dump 检测优先于“已有 thinking 就放行”，顺序为 `buffered_burst`、`cipher_drool`、`fake_encrypted_dump`、`fast_reasoning_ratio_dump`
- `hold_expired` 后的短 visible 文本只有在 `reasoning_tokens >= 80` 时按 burst 处理
- `fake_encrypted_dump` 与 `fast_reasoning_ratio_dump` 只在首个可见块后 2 秒内触发
- 聚合 message 文本计入 visible token，但不伪造流式首个可见块的 flush 时间
- `: grok2api-reasoning-evidence <bytes>` 作为密文字节数证据，仍必须满足密文下限
- Codex / Responses 请求会按 grok2api 的 Build/Console 边界补 `include: ["reasoning.encrypted_content"]`，避免客户端未声明密文时把正常响应误判为 `missing_thinking`
- Chat、Responses 和 Anthropic Messages 的流式与非流式响应都使用同一套内容、thinking、密文下限、TPS 和空流规则
- 保留持续可见流误杀保护：只有可见文本持续至少 2 秒且达到最小输出时才可在 dump 窗口后放行，硬 dump 检测始终优先
- `usage` 中的输出或 reasoning 账单不能替代真实思考证据，但短可见文本不能借输出下限绕过 `missing_thinking`
- 推理 stub 之后一旦可见输出已经真实流式到达（达到最小输出，且可见跨度至少 40ms 或至少 2 个可见块）仍无思考证据，立即判 `missing_thinking` 并换号；等待只保留在没有可见输出的阶段，避免每个降智账号把客户端扣满 `hold_timeout`
- hold 阶段只在新帧改变了判定所依赖的信号时才重新判定，控制帧和保活注释只进入缓冲；内容已发给客户端后，迟到的 hold 定时器不会再触发换号，避免把另一个账号的回答拼接到已开始的流上
- 含客户端工具调用（function / custom tool / shell 等）的轮次跳过基于可见窗口时序的 dump 判定：这类轮次的可见文本只是工具调用前的简短说明，健康账号也会把工具参数一次性下发；思考证据要求保持不变，缺少证据仍判 `missing_thinking`。宿主侧执行的 web search 等工具之后的文本仍是答案，照常检测
- 已有达到下限的密文证据时，工具调用一开始或扣留窗口到期且未命中 dump 即放行，不再等待上游终止事件
- 判定记账（失败事件、账号判定、交叉验证调度）在后台执行：换号不再等待状态落盘或被正在探测的账号锁阻塞，成功响应先关闭客户端流再记账

被动审计对可确定要求推理的流式样本检查 `missing_thinking`：非流式、缺少 TTFT、生成窗口无效，或有效输出与推理账单均不足 32 token 的样本记录为 `ignored`；显式关闭推理时不推断。速度审计仍只对满足流式、TTFT、输出和生成窗口条件的样本生效。

内置已知 Grok 推理能力表覆盖 Grok 3 Mini、Grok 4.3、Grok 4.5、Grok 4.6、Grok 4.7 与 Grok 4.20 推理族；未命中能力表但符合 Grok 4+ 版本回退的模型默认按推理模型处理，以优先拦截降智；未知非 Grok 模型不进入思考闸门；可通过 `quality.non_reasoning_models` 额外声明不产生推理的模型

### 非质量错误与证据保留

`ignored` 表示本次尝试不是质量信号，例如上游失败、非 Grok 请求、非流式被动样本、缺少 TTFT、输出不足或传输错误。它不会增加质量 strike，也不会覆盖上一次真实质量判定。

`probe_error` / `proxy_error` 会记录为最近一次尝试并参与错误退避，但不会把上一次质量指标清空。管理页的“最近尝试”和“最近质量判定”分开显示，因此可以直接看出某次 `ignored` 或探测错误是否只是噪声。

空流表示上游已经终止，但没有可见内容、真实思考证据、usage 输出或语义输出。空流使用独立的 15 分钟冷却，不会自动升级成 missing-thinking 永久禁用。

## 环境要求

- CLIProxyAPI v8，支持 plugin ABI
- Go 1.26.10 或与 `go.mod` 匹配的更新版本
- CGO 可用的 C 编译器
  - Windows：MinGW GCC
  - Linux：GCC
  - macOS：Clang
- 可选：Resin Admin API 和代理环境；仅本地轮换不需要 Resin API 或 Key

项目通过 Go module 引入 CLIProxyAPI v8，仅使用其中的 `sdk/pluginabi` 和 `sdk/pluginapi` 作为协议类型。

## 安装

### 在线安装

CPA 插件商店使用最新 Release 里的 registry.json，之后在管理面板的插件商店中直接安装。发布时把它和安装包一起上传，不会修改 main：

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://github.com/wzdnzd/grok-quality-guard/releases/latest/download/registry.json"
```

支持 `windows/amd64`、`linux/amd64`、`linux/arm64`、`darwin/amd64`、`darwin/arm64` 五个平台。Release 安装包同时提供 `checksums.txt`，安装前可进行 SHA-256 校验。

### 从 GitHub Release 安装

Release 资产使用 CPA 插件商店要求的命名：

```text
grok-quality-guard_<version>_<goos>_<goarch>.zip
```

将压缩包中的动态库安装到 CPA 插件目录：

```text
plugins/<goos>/<goarch>/grok-quality-guard-v<version>.<ext>
```

也可以直接放在 `plugins/` 根目录；CPA 会优先查找当前平台的子目录。支持的后缀：

- Windows：`.dll`
- Linux：`.so`
- macOS：`.dylib`

然后在 CPA 配置中启用插件并填写配置。

### 从源码构建

Windows：

```powershell
$env:CGO_ENABLED = '1'
go build -buildmode=c-shared -trimpath -buildvcs=false `
  -ldflags "-X grok-quality-guard/internal/app.pluginVersion=0.1.0" `
  -o bin/grok-quality-guard.dll .
```

Linux：

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -buildvcs=false \
  -ldflags "-X grok-quality-guard/internal/app.pluginVersion=0.1.0" \
  -o bin/grok-quality-guard.so .
```

macOS：

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -buildvcs=false \
  -ldflags "-X grok-quality-guard/internal/app.pluginVersion=0.1.0" \
  -o bin/grok-quality-guard.dylib .
```

版本号不要带 `v` 前缀。GitHub Actions 发布时会把 tag 版本自动注入插件元数据。

## 代码结构

```text
main.go                         C ABI 入口、宿主回调和插件生命周期分发
designs/                       插件与代理模块设计文档
internal/
  abi/                          ABI envelope 编解码
  app/                          插件装配、宿主桥、注册与接口适配
    auth/                       账号 JSON 元数据与身份识别
    catalog/                    账号目录、模型缓存与 state 同步
    dashboard/                  内嵌管理页与前端回归脚本
    executor/                   执行器、透明流重试与响应改写
    management/                 管理 API、账号/策略视图与响应封装
    model/                      catalog 账号投影
    passive/                    被动请求观测、usage 归并与质量审计
    action/                     管理 action 协议与参数解析
    probe/                      主动探测、恢复探测与调度
    proxy/                      代理来源、绑定、操作、清理与自动绑定
      resin/                    Resin 客户端、Provider 与租约清理
    quality/                    质量处置、冷却、恢复与状态对账
    redact/                     日志和错误脱敏
    routing/                    候选选择、会话粘性与选择索引
    state/                      持久化状态、代理 journal 与路径切换
    tasks/                      任务中心、异步任务管理与历史存储
  test/                         通过公开 ABI/管理接口驱动的跨包测试
  config/                       配置解码、默认值、校验与策略映射
  model/                        Grok 模型版本与 reasoning 能力识别
  quality/                      SSE/JSON 流分析、质量信号与判定策略
  sse/                          CPA 流式响应分帧与 SSE 输出规范化
  proxy/                        代理端点、模板、约束与 Provider 领域模型
```

`internal/app` 根目录的生产代码只保留 Plugin 生命周期、宿主桥、`plugin_ports.go` 中的应用子包 Environment 端口实现，以及注册与 ABI 边界。端口方法集中放在组合根，不再为每个子包创建 `*_adapter.go` 薄文件，也不保留 `*_facade.go`、类型别名包装文件或 `*_types.go`；仅为根包测试访问私有装配状态而存在的辅助函数集中在 `*_bridge_test.go`，不会进入构建产物。具体领域实现和领域类型直接使用对应子包。

领域实现位于 `internal/app` 的各自子包；`plugin.go`、`host_bridge.go` 和 `registration.go` 只保留装配与宿主边界。子包通过显式的 `Environment` interface 使用宿主能力，不反向导入 `internal/app`。Resin 是 `internal/app/proxy/resin` 独立子包，普通代理模板不依赖 Resin 配置。

测试按作用域组织：只验证单个子包行为的单元测试放在目标包目录；跨包验收测试优先放在 `internal/test`，通过 `NewPlugin`、`Configure`、`RouteModel`、`Execute*`、`HandleManagement` 和 fake host 等公开边界驱动，不依赖私有字段。`internal/app` 根目录只保留仍需验证同包私有装配细节的过渡性白盒测试；拆分测试时按业务域组织场景，而不是新增生产代码导出。

## 配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    grok-quality-guard:
      enabled: true
      priority: 10

      # 质量异常默认只观测；401/额度耗尽仍直接处置
      passive_guard: true
      transparent_guard: false
      auto_action: observe
      model_prefixes:
        - "grok-"

      routing:
        strategy:
          selection: round_robin
          prefer_last_success: true
        affinity:
          enabled: true
          ttl: "1h"
          max_entries: 10000
        scoring:
          enabled: true
          max_accounts_per_bucket: 512
          weights:
            success: 0.50
            ttft: 0.30
            tps: 0.20
          evidence:
            probe: 0.65
            transparent: 0.35
            passive: 0.0
          refresh:
            min_interval: "5m"
            max_age: "30m"
            drift_threshold: 0.03

      quality:
        soft_tps: 500
        hard_tps: 1000
        consecutive_soft: 2
        consecutive_errors: 2
        consecutive_missing_thinking: 1
        strike_decay_after: 48
        strike_decay_min_healthy: 10
        thinking_guard: true
        non_reasoning_models: []
        soft_cross_verify: false
        thinking_cross_verify: false
        min_generation_ms: 1000
        fail_closed: false
        min_passive_output_tokens: 32
        min_hold_output_tokens: 8
        hold_timeout: "30s"
        stream_idle_timeout: "90s"
        hold_max_buffer_bytes: 4194304
        min_encrypted_bytes: 256
        encrypted_bytes_per_reasoning_token: 2
        missing_thinking_cooldown: "12h"
        empty_stream_cooldown: "15m"
        max_attempts: 6
        on_exhausted: "fail_closed"

      probe:
        model: "grok-4.5"
        timeout: "90s"
        max_output_tokens: 768
        concurrency: 1
        queue_size: 10000
        scan_interval: "15s"
        healthy_interval_hours: 0
        unknown_delay: "30s"
        unknown_jitter: "30s"
        suspect_backoff: "15m"
        max_backoff: "1h"
        entry_protocol: "codex"
        exit_protocol: "codex"

      proxy:
        auto_rotate_after_quality_failure: true
        auto_bind:
          enabled: false
          source_scope: all
          source_ids: []
          concurrency: 2
          queue_size: 1000
          max_per_cycle: 100
          scan_interval: "30s"
          require_healthy: false
          health_max_age: "10m"
          include_guard_disabled: false
          include_externally_disabled: false
          manual_clear_policy: rebind
          retry_initial: "1m"
          retry_max: "1h"

      task_history:
        enabled: true
        retention_days: 3
        max_records: 500
        max_bytes: 134217728
        max_items_per_task: 10000

      state_file: "/CLIProxyAPI/plugins/data/grok-quality-guard/state.json"
      events_limit: 1000
```

配置使用严格 YAML 解码，未知字段（包括旧 `resin:` 配置块）会直接报错，不会静默忽略。

### 自动处置

- `auto_action: observe`：质量异常只记录事件和状态，不修改 auth，不自动轮换代理；上游 401 凭据拒绝和明确的额度耗尽仍会直接执行账号处置
- `auto_action: cooldown_only`：首次确认异常时写入临时 `disabled=true`；冷却到期后执行恢复探测，仍异常则重新进入冷却，永不自动永久禁用
- `auto_action: cooldown_then_disable`：首次确认异常时写入临时 `disabled=true`，冷却到期后执行恢复探测；再次确认 missing-thinking 时永久禁用

插件只操作自己写入的 guard-owned 状态，不自动恢复管理员在插件外禁用的 auth。只有批量恢复弹窗中显式勾选“同时恢复外部禁用账号”时，才会清除这些账号插件状态并把 OAuth `disabled` 改为 `false`。

连续阈值与旧 grok2api/egress guard 策略对齐：

- `consecutive_soft`：连续软 TPS 样本达到次数后才进入处置；默认 `2`
- `consecutive_errors`：连续传输/代理错误达到次数后才进入处置；上游 5xx、普通限流和无法确认的 403/429 只记录为 `ignored`，不计入 strike；默认 `2`；开启自动轮换且 auth 绑定插件管理的代理时，达到阈值若还有可用账号先换号并异步轮换失败账号的代理，没有可用账号时再同步轮换后重试；轮换成功清除连续错误并恢复账号，轮换不可用再回退自动处置
- 外部禁用只标记状态，插件无法得知外部原因，因此不写入 `disable_reason`；插件自身禁用会持久化简短的 `disable_reason`，dashboard 统一显示中文
- 上游 `401` 标记为 `credential_invalid`，写入 `disabled=true` 并等待管理员更新凭据后手动恢复
- 上游 `402`、包含明确 free usage/spending-limit/credits 耗尽短语的 `403/429` 标记为 `quota_exhausted`，写入 24 小时冷却；到期后自动恢复探测
- CPA 返回的 `model_cooldown` 是宿主级调度状态，不能证明当前 auth 单独额度耗尽；插件只记录为 `ignored`，不会因此写入 `disabled=true`
- `consecutive_missing_thinking`：连续缺少真实思考达到次数后才进入处置；默认 `1`
- `strike_decay_after`：missing-thinking strike 自动降级窗口，单位小时，默认 `48`；设置为 `0` 关闭自动降级
- `strike_decay_min_healthy`：窗口内至少积累的连续健康观测/探测次数，默认 `10`；健康样本包括 passive/transparent 的 `ClassNone` 和成功探测，真实质量失败会重置窗口与计数
- `thinking_guard=false`：跳过缺少思考判定，只保留 TPS、空流等其他质量检查
- `non_reasoning_models`：额外声明不产生推理的模型标识，配置项优先于内置能力表和 Grok 4+ 版本回退；支持 YAML 列表或管理页逗号分隔输入，提供方前缀会被忽略，明确 reasoning effort 后缀会归一到基础模型
- `fail_closed=true`：生成窗口小于 `min_generation_ms` 且速度达到 `soft_tps` 时判定为 `buffered_burst`；窗口不足但速度不高时判定为 `insufficient_generation_window`
- `ignored`、`probe_error`、`proxy_error` 不覆盖上一次真实质量指标；详情页会分别展示最近尝试和最近质量证据
- `soft_cross_verify=true` 或 `thinking_cross_verify=true`：被动样本达到阈值后先启动后台主动探测，由探测结果确认后再处置；默认关闭
- 任一健康样本会清零 `soft_strikes`、`error_strikes`、`thinking_strikes`，与旧插件的连续计数语义一致

主动探测调度器在插件启动后立即建立并执行第一次扫描，不再等待状态修复完成；之后按 `probe.scan_interval` 定时扫描（默认 `15s`，允许 `1s..24h`），先过滤出当前到期账号，再放入有界队列。`probe.healthy_interval_hours` 为健康账号的周期探测间隔，单位小时，默认 `0`（不主动探测）；配置为大于 `0` 的整数后，健康账号距上次主动探测达到该间隔时也会进入同一队列，尚无 `last_probe_at` 的健康账号在启用后视为到期。等待中和执行中的账号优先按 auth 文件里的 OAuth `sub`/`subject` 去重，缺失时才回退到宿主 auth ID，因此同一 OAuth 账号不会重复入队。`probe.queue_size` 默认 `10000`，最大 `1000000`；`probe.concurrency` 控制探测以及批量账号操作实际使用的 worker 数。`probe.unknown_jitter` 为 `unknown` 账号的首次探测增加稳定抖动，避免大量账号在同一时刻到期。

账号页显示的“待检测”是当前 Catalog 中状态为 `unknown` 的账号数，不是常驻队列的 `waiting`。只有按 `unknown_delay`、`unknown_jitter`、冷却时间和退避时间计算后已到期的账号才会进入队列；首次批量导入后通常会有一个逐步到期的窗口。

### 任务中心与历史记录

`任务中心` 是账号、代理、Resin 清理和质量交叉验证等异步批量任务的统一入口。任务列表显示状态、进度、总数、已完成、正在处理、等待、成功、失败、跳过和最近更新时间；等待中或执行中的任务可在状态旁直接请求取消，已结束的任务可在状态旁永久删除其历史记录和全部逐项明细。打开任务后显示脱敏逐项结果、任务来源和时间信息。代理管理页只保留当前操作入口，不再重复维护另一套任务列表。

历史任务保存在状态文件同目录：

```text
<state-file-dir>/task-center/
  index.jsonl                      紧凑任务索引
  active/<task-id>.json            进行中任务元数据
  active/<task-id>.items.jsonl     进行中逐项明细
  archive/YYYY-MM/<task-id>.json.gz
```

管理页只在进入任务中心时读取索引；历史明细仅在用户打开具体任务时按页解压读取。插件启动时不扫描全部历史，只恢复上次未完成的活动文件并标记为已中断。

| 配置 | 默认值 | 范围 | 说明 |
| --- | ---: | ---: | --- |
| `task_history.enabled` | `true` | 布尔 | 关闭后只显示当前进行中的任务，不再保留新完成记录 |
| `task_history.retention_days` | `3` | `1..365` | 从任务完成时间开始计算 |
| `task_history.max_records` | `500` | `1..10000` | 超出后优先清理最旧记录 |
| `task_history.max_bytes` | `134217728` | `1..4294967296` | 已完成任务归档的压缩磁盘占用上限，单位字节 |
| `task_history.max_items_per_task` | `10000` | `1..100000` | 超出后保留任务汇总，逐项明细标记为已截断 |

常驻队列只在确实处于运行或仍有可操作工作时展示：质量检测仅在守护模式启用或主动探测调度器仍有工作时显示，并汇总等待、处理中、成功和失败数量；自动探测不会为每个账号单独创建任务。自动绑定仅在启用或仍有队列工作时显示；代理健康检查仅在自动绑定启用且 `require_healthy=true` 时显示；代理同步仅在存在待处理、失败、部分完成或运行中记录时显示；Resin 清理仅在存在待清理租约或 Resin 来源仍有账号绑定时显示。

事件日志继续记录质量判定、自动处置和代理审计事件，不用于展示实时任务进度。

### 透明保护

`transparent_guard: true` 时，所有将使用 xAI / Grok OAuth 的文本 chat / responses 请求都会先进入插件质量门，而不是只在可安全重放时才接管。模型可通过 `grok-*` 命名或 CPA 的 xAI auth model 列表识别。

与 grok2api 一致，Responses compaction（包括 `compaction_trigger` 和 TUI 压缩摘要 prompt）不进入质量保持，避免系统摘要因没有 reasoning 而被误判换号。Codex / Responses 推理请求会在上游执行前补齐 `reasoning.encrypted_content` include，确保真实 thinking 证据可观测。

不可安全重放的请求仍会执行一次质量判定，但不会换号或代理重试；质量门拒绝时返回 `503 quality_degraded`。这类请求包括图片、音频、视频等媒体输入、`previous_response_id` 或 stored response、compaction、可能产生副作用的 hosted tools，以及无法安全重放到其他账号的请求。

禁用推理的请求、内置或配置声明的非推理 Grok 模型同样会进入透明质量门；它们跳过 `missing_thinking` 与 dump 类判定，空流仍会拦截，TPS 速度和缓冲上限在终态继续记录。非推理流在首个可见内容后提前放行，不会因为普通可见文本缺少 reasoning 而误拒。

主动探测只按请求级证据忽略内部 probe usage，不会在探测窗口内按 AuthID 丢弃真实请求的 usage，也不会把该账号从透明候选集中排除。

透明候选只使用 `healthy` / `unknown` 账号；已进入 `suspect` 或 `degraded` 的账号不会继续承接透明请求，直到主动探测或手动恢复。`missing_thinking` 被视为账号级证据：存在其他可用账号时立即切号，代理轮换只作为单账号池的兜底；所有候选都不可用时返回 `503 quality_degraded`。

### 透明 Auth 选择

透明模式完全接管 Grok/xAI auth 选择，CPA 只执行插件指定的 `AuthID`。插件在后台维护不可变分层、分桶快照，请求只读取快照，不扫描或在请求路径排序账号池：

- 先按质量分层：`healthy` 整体优先于 `unknown`
- 同一质量状态下按 Auth Priority 数值从大到小分层
- `healthy` 按 EWMA 质量分从高到低排序；`unknown` 不计算评分，保持 Auth ID 顺序
- 每个 `(质量, Priority)` 层按 `routing.scoring.max_accounts_per_bucket` 自动分桶；桶数由该层账号数决定，不写死
- 健康账号评分综合成功率、首 Token 耗时和输出速度，并按主动探测、透明请求、被动观测分别保留证据
- 同一评分来源与模型类别使用时间衰减 EWMA；半衰期由 `probe.healthy_interval_hours` 推导，并限制在 `12h..168h`（未启用周期探测时按 `168h`）
- 层内先尝试当前评分桶；当前桶没有空闲候选后再进入下一桶，所有桶都忙时才进入当前层的 busy fallback
- `round_robin`、`random`、`fill_first` 只在当前评分桶内生效
- `routing.strategy.prefer_last_success=true` 时，上次透明请求成功的 auth 只要仍健康、仍属于最高质量层且当前空闲就优先使用；EWMA 评分换桶不会丢弃该偏好
- `routing.affinity.enabled=true` 时，同一稳定 session 尽量固定到同一 auth；绑定只存在内存中，默认 TTL `1h`，最多 `10000` 条

评分权重与证据权重均限制在 `0..1`，两组分别归一化，不需要总和为 `1`。默认更重视实际成功率和主动探测证据，避免把最近被透明请求使用过的账号误判为“更新鲜”。每个账号只持久化紧凑的 EWMA 状态，不保存原始采样列表。

评分索引只由路由流量驱动：长时间没有请求时完全不刷新；有请求时，刷新间隔在 `refresh.min_interval` 和 `refresh.max_age` 之间随请求速率调整，并受 `drift_threshold` 触发。`min_interval` 至少为 `10s`，`max_age` 位于 `min_interval..7d`，`drift_threshold` 位于 `0..1`。重建在后台进行，选择请求始终读取旧快照，避免与用户请求争抢同步 CPU。账号 `unknown -> healthy` 或 `suspect` 等层变化仍通过增量 `Upsert` 在下一次请求前生效。

`max_accounts_per_bucket` 默认 `512`，允许 `16..4096`。该上限同时约束 `unknown` 的 ID 有序分桶；评分关闭时健康账号也按 ID 顺序使用相同桶容量。4000 个账号在默认配置下通常形成约 8 个健康评分桶，选择路径不会逐个重算所有账号。

透明模式在首次向客户端 emit 前完成质量门。一旦开始 emit，插件不能回滚已经发送的内容；如果所有候选账号耗尽，默认返回 `503 quality_degraded`。可配置 `on_exhausted: fail_open` 在失败时返回最后一个已缓冲响应，但这会牺牲 fail-closed 语义。透明缓冲超过 `hold_max_buffer_bytes` 时也会 fail closed，不会把未完成质量判定的内容下发给客户端。`stream_idle_timeout` 默认 `90s`，上游请求在首块到达前或流式读取中超过该时间没有新增生成输出时会终止当前尝试并更换账号重试；仅有 `response.created` 或 reasoning stub 的静默流不会永久等待，也不会在 `fail_open` 下放行空流

## 通用代理节点、模板与 Resin

插件支持三类代理来源：

1. **节点**：普通编辑保存 `协议://用户名:密码@主机:端口`；导入时自动优先按标准 URL 解析，失败后尝试地址在前 `协议://主机:端口:用户名:密码` 或 `协议://主机:端口@用户名:密码`。三种形式均支持 IPv4、IPv6 和 URL 编码凭据；标准 URL 与地址在前格式也支持无认证或仅用户名。无 scheme 的导入行会被忽略。
2. **模板**：占位符只允许出现在用户名中；每个变量可选 `enum` 或 `random`。枚举值使用用户配置的分隔符解析，替换时随机选择；随机值由英文字母和数字组成，可配置长度与大小写敏感。
3. **Resin**：一种特殊模板。与节点、模板共用新增类型下拉、列表、编辑、检测、删除、绑定和轮换流程；API 模式额外负责远端租约删除。可以添加多个 Resin 代理，以同一 Resin 服务的不同平台、Admin 地址或账号生成规则分别参与选择。

任意变量之间都可以建立级联限制。系统不内置 `country`、`state` 等字段或地理树，而是持久化用户定义的合法组合行。例如 `vendor/plan/profile` 可由同一组 rows 表达多父级约束；中文变量名同样可用。

模板和节点修改会同步受管 auth 的 `proxy_url`。模板默认不回溯已绑定账号，可在模板编辑中选择“更新已绑定账号”。手动清除默认允许自动绑定重新接管，也可通过 `manual_clear_policy: pause` 暂停这些账号。

自动绑定默认关闭。开启后，插件会把代理 URL 为空的 Grok/xAI auth 放入有界队列，跳过外部代理、读取失败和未解决绑定；来源选择支持范围、ID allowlist、优先级、配额、并发预留、健康年龄和失败退避。

代理来源统一使用绑定、轮换和清除流程；Resin 在通用流程之外还支持 API 模式与本地轮换。管理页不再提供独立的 Resin 配置入口或全局模式横幅，Resin 的类型、启用状态和 API/本地模式都显示在统一代理列表对应行中。

### API 模式

API 模式用于创建新的代理绑定，并管理 Resin 远端租约。在“新增类型”选择 Resin 后，会打开与模板一致但包含连接字段的编辑器；来源名称、Admin API URL、Platform、Admin Token、Proxy Token 和账号格式在这里配置。来源名称用于代理列表和自动绑定选择，可自定义，并可与节点、模板或其他 Resin 代理并存。用户只填写平台名称与 username 后缀；系统自动添加平台名和点号前缀。协议固定为 SOCKS5H，密码固定使用 Proxy Token，代理主机和端口从 Admin API URL 解析。保存时会按顺序验证：

1. 使用 Admin Token 请求 Resin `GET /api/v1/system/info`
2. 使用同一 Admin Token 请求 `GET /api/v1/platforms?keyword={platform}`，按名称精确解析平台 UUID 并读取规范平台名称
3. 按账号格式生成代理 URL 渲染验证账号，并执行 SOCKS5 RFC1929 用户名/密码认证，确认 Proxy Token 有效

任一步失败都不会保存配置。已保存的 token 不会回填到页面，也不会通过管理页或 JSON API 返回；再次保存时留空 Secret 输入框会保留已有值。连接凭据只保存在 state 的 Resin 来源配置中，不再从 YAML 环境变量字段回退读取。

Resin 平台名称在 Resin 中唯一。管理页只要求填写名称，保存时通过平台列表接口解析出 `platform_id` 并写入状态文件；管理员不需要手工填写 UUID。绑定操作仍会按内部 `platform_id` 重新查询规范名称；轮换使用已保存的规范平台名称和账号格式，不在请求关键路径调用平台接口。

Resin 绑定必须由管理员显式触发，不会在首次启动时自动覆盖已有 `proxy_url`。API 模式轮换顺序固定为：

1. 按账号格式生成新的 account：`{account}` 是保留变量，只替换为邮箱 `@` 前的本地部分并转小写；其他占位符按模板规则生成。例如 `xai-{account}-{region}` 可得到 `xai-alice-us`，系统再拼出 `Canonical.xai-alice-us` 的 username
2. 写回 auth 的 `proxy_url`
3. 保留 access/refresh token 等所有非代理字段
4. 将旧租约删除任务写入 state 队列
5. 后台 worker 调用 `DELETE /api/v1/platforms/{platform_id}/leases/{account}`，`204` 和 `404` 都视为成功

租约删除不在 CPA 请求关键路径上。删除失败时任务保留在 state 中，按 30 秒起步的指数退避重试，最长间隔 1 小时；插件重启后会继续处理。管理页事件日志会记录 `resin_lease_delete` 的 queued、retry_scheduled 和 succeeded 结果。

API 模式清除 `proxy_url` 时，插件会先清空本地 auth 并写入 state，再异步排队删除旧 Resin 租约，不会等待管理接口。auth 保存失败仍会使操作失败；auth 已保存但 state 写入失败时，本地清除仍然有效，只记录 `internal_error`。插件不会从 auth 代理 URL 反推历史租约；只有通用 binding 中记录了有效 SnapshotID 的租约才会进入清理队列。

当 CPA auth 列表中的账号消失时，插件会在删除对应 state 记录前，把该 state 中仍记录的 Resin 管理租约加入同一持久化删除队列。该清理只处理绑定中带有有效 Resin 快照的账号；本地轮换、缺少快照、没有租约 account 或连接信息不完整的记录不会访问 Resin API。

### 仅本地轮换

仅本地轮换在“新增类型 / Resin”编辑器中勾选“仅本地轮换”即可，不再使用 YAML 中的全局 Resin 配置。该模式不需要 Admin API URL、Platform ID、Admin Token、Proxy Token 或代理模板。
本地轮换要求 auth 中已有代理包含用户名和密码，且用户名最后一个 `.` 后是当前 account。例如：

```text
socks5h://Default.grok-alice-old1:proxy-secret@resin:2260
```

轮换时只替换用户名中的 account 后缀，按该 Resin 代理配置的账号格式生成新后缀，同时保留 scheme、代理主机、端口、密码和路径等其余部分：

```text
socks5h://Default.grok-alice-new1:proxy-secret@resin:2260
```

本地模式不会调用任何 Resin API，也不会删除远端旧租约。本地清除只移除 auth 中的 `proxy_url`，同样不会处理远端租约。由于没有 API 可用，本地模式不能创建新的代理绑定；首次绑定仍必须使用 API 模式。

Resin 没有“立即创建指定出口 IP 租约”的 API，插件不查询新 egress IP，也不保证轮换后一定使用不同出口 IP。批量绑定、绑定全部未绑定和批量清除均作为异步任务执行，内部使用有界工作队列，队列容量与 worker 数沿用 `probe.queue_size` 和 `probe.concurrency`。

## 管理页与 API

管理页：

```text
/v0/resource/plugins/grok-quality-guard/dashboard
```

Action API：

```text
POST /v0/management/plugins/grok-quality-guard/api
Content-Type: application/json
```

API 只使用请求体中的 `action` 和 `params`，不依赖自定义 HTTP Header，管理认证由 CPA management 层完成。

请求示例：

```bash
curl -X POST \
  "http://127.0.0.1:8317/v0/management/plugins/grok-quality-guard/api" \
  -H "Authorization: Bearer ${CLIPROXY_MANAGEMENT_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"action":"accounts.list","params":{"page":1,"page_size":50}}'
```

可用 action：

| Action | 说明 |
| --- | --- |
| `overview` | 汇总账号、状态、代理和最近异常 |
| `accounts.list` | 分页账号列表 |
| `accounts.get` | 账号详情 |
| `accounts.probe` | 提交异步固定 prompt 探测任务 |
| `accounts.probe_selected` | 检测选中的账号；需要恢复时在同一任务内先恢复再检测 |
| `accounts.cooldown` | 手动冷却 |
| `accounts.disable` | 手动永久禁用，同时修改 OAuth `disabled=true` |
| `accounts.disable_selected` | 批量禁用选中账号，可选择仅插件隔离或同时修改 OAuth |
| `accounts.restore_preview` | 预览选中账号的恢复范围与外部禁用数量 |
| `accounts.restore` | 恢复 guard-owned 账号并清除 strike |
| `accounts.restore_selected` | 恢复选中账号；可显式同时恢复外部禁用账号 |
| `proxy.summary` | 代理来源、绑定状态和自动绑定运行时摘要 |
| `proxy.sources.list/get` | 代理来源列表与脱敏详情 |
| `proxy.sources.validate/preview/test` | 配置验证、条件预览和端点检测 |
| `proxy.sources.create/import_nodes/update/delete` | 节点、模板 CRUD 与批量节点导入，自动识别标准 URL、地址在前和 `host:port@user:pass` 格式 |
| `proxy.bindings.list/bind/clear/rotate/adopt` | 分页查询、绑定、清除、轮换和显式接管 |
| `proxy.bindings.endpoint` | 按 auth 显式读取完整代理端点；列表默认仅返回脱敏端点 |
| `proxy.bindings.set_auto_bind` | 暂停或恢复指定 auth 的自动绑定 |
| `proxy.bind_unbound` | 显式批量绑定未绑定 auth |
| `proxy.template.apply` | 将当前模板按固定版本应用到已绑定 auth |
| `proxy.auto_bind.get/update/run` | 自动绑定策略、保存和立即扫描 |
| `proxy.changes.list/get/retry/cancel` | 同步与回溯详情、失败重试及取消 |
| `resin.cleanup.list/retry/pause` | 已排队租约清理、重试和暂停/恢复 |
| `policy.get` | 查看运行时策略 |
| `policy.update` | 校验并持久化策略 |
| `policy.reset` | 重置为 YAML 初始值 |
| `state_file.update` | 写入并切换状态文件路径；目标文件必须不存在 |
| `events.list` | 查看质量事件日志 |
| `tasks.center.list` | 按类型、状态、关键词和分页查询任务中心 |
| `tasks.center.queue` | 按 `queue_id` 分页读取质量检测、自动绑定和代理健康检查队列的执行中与等待项 |
| `tasks.center.get` | 按页懒加载任务逐项明细；历史任务从归档读取 |
| `tasks.center.cancel` | 请求取消等待中或执行中的任务 |
| `tasks.center.delete` | 永久删除已结束任务的历史索引、归档和逐项明细 |
| `tasks.get` | 兼容旧客户端的任务状态查询；历史任务会恢复完整结果 |
| `tasks.list` | 列出当前内存中的异步任务 |

主动探测、通用代理批量绑定/清除/轮换、模板回溯和 Resin 租约清理都是长耗时操作，会立即返回 `202 Accepted` 与 `task_id`，不会占住管理请求超时。调用方应轮询：

```json
{"action":"tasks.get","params":{"task_id":"<task_id>"}}
```

任务状态为 `queued`、`running`、`succeeded`、`failed`、`cancelled` 或 `interrupted`。失败时 `error` 包含脱敏后的原因，`error_status` 保留可用的上游 HTTP 状态。内嵌管理页通过 `tasks.center.*` 按需加载并以 `tasks.get` 兼容等待方轮询。

API 只接受 POST，并由 CPA management 层完成认证。插件不要求自定义 Header 或请求体 UI 标记，避免管理网关剥离 Header 时页面请求失败。普通列表、绑定响应和任务响应中的 email、proxy URL、token 与密码均脱敏；节点和模板编辑器通过 `proxy.sources.get` 的 `edit_fields:true` 显式回显完整 URL。账号列表同时提供 `auth_id_display` 供展示，操作 API 使用稳定的 `auth_id` 作为标识。`proxy_url` 可能内嵌 Proxy Token，因此普通列表只显示 `scheme://host`。

## 状态文件与 Docker 持久化

默认路径按顺序选择第一个可写位置：

```text
/CLIProxyAPI/plugins/data/grok-quality-guard/state.json
plugins/data/grok-quality-guard/state.json
<UserConfigDir>/cliproxyapi/grok-quality-guard/state.json
<temp>/grok-quality-guard/state.json
```

Docker 部署必须把第一项所在目录挂载为宿主持久卷，否则容器重建后策略、账号状态、事件、代理来源配置、任务中心历史、Resin 连接凭据和清理快照仍会丢失：

```yaml
services:
  cliproxyapi:
    volumes:
      - ./plugins/data/grok-quality-guard:/CLIProxyAPI/plugins/data/grok-quality-guard
```

如果已经挂载了 `/CLIProxyAPI/plugins`，也可以显式把 `state_file` 指向该持久目录下的文件。显式配置的 `state_file` 是启动引导路径；路径不可写时插件会报错，而不是静默改写到临时目录。

管理页“设置 / 守护策略”中的状态文件路径可以直接编辑。保存新路径时，插件会把当前完整 state 写入目标文件并立即切换；为避免覆盖已有数据，目标文件必须尚不存在。引导路径旁会生成 `<state_file>.location` 指针文件，使插件重启后继续使用新路径。修改 YAML 中的 `state_file` 会产生新的引导位置，因此可以借此重新指定路径；如要回退到旧引导文件，需要先删除对应指针文件。

状态文件使用 schema v1、原子替换写入，Unix 权限为目录 `0700`、文件 `0600`。读取时严格拒绝未知字段和非 v1 schema；不会迁移或兼容旧 state，旧数据必须删除后重建。策略首次从 YAML 初始化，之后管理页修改会持久化到 state；`policy.reset` 会恢复为当前 YAML 的默认值。通过管理页保存的多个 Resin 代理配置、连接凭据和租约清理快照也存储在 state 中，API 与页面只返回是否已设置的布尔标记。管理页设置区会显示当前实际使用的状态文件路径。

插件运行时只会修改 auth 文件中的原生字段 `disabled` 和 `proxy_url`。冷却原因、strike、禁用所有权、Resin 租约及其他控制信息全部保存在 `state.json`，不再向 auth 写入 `grok_quality_guard` 或其他插件私有字段。

代理写入使用相邻的 `state.json.proxy-journal` 保存准备与提交记录，主 state 定期原子 checkpoint 后截断日志。备份或恢复应停止插件，并保留匹配的 state、journal 和 `.location` 指针；不可在运行时只覆盖其中一份。

来源编辑须传 `expected_version`。节点端点变化自动同步绑定；模板默认保持旧绑定，选择 `rerender_bound` 才回溯。失败任务保留成功结果，调用方应检查 `counts`、`committed` 和 `needs_reconcile`。节点和模板编辑通过 `edit_fields:true` 回显完整 URL，密码只随 URL 修改；普通详情与列表仍不返回凭据。

依赖的 CLIProxyAPI v8.0.67 将 `socks5` 和 `socks5h` 都按远端 DNS 执行；插件保留这两种 scheme，不做改写。HTTPS 代理在普通 HTTP/SSE 路径使用 TLS，原生 WebSocket 仍受宿主拨号器能力限制。完整实现约束与审查记录见 [代理管理设计](designs/proxy.md)。

## 开发与测试

```bash
gofmt -l ./*.go
go test ./...
go test -race ./...
go vet ./...
go build -buildmode=c-shared -o bin/grok-quality-guard.dll .
git diff --check
```

## 发布

仓库提供 `.github/workflows/release.yaml`。推送 `v*` tag 会：

1. 校验 tag 和代码格式
2. 运行测试与 vet
3. 构建 Windows、Linux、macOS 的 amd64/arm64 动态库
4. 生成 CPA 插件商店兼容的 zip 和 `checksums.txt`
5. 创建 GitHub Release
6. 生成 `registry.json` 并和安装包一起上传到 GitHub Release，不修改 `main`

示例：

```powershell
git tag v0.1.0
git push origin v0.1.0
```

## 已知边界

- streaming 开始后无法回滚，也无法在已经 emit 后换号
- stored responses、compaction、媒体输入和有副作用的 hosted tools 不可安全重放，仍会经过透明质量门，但只执行一次且不换号重试
- CPA 不能直接以固定 AuthID 探测 disabled auth，恢复探测存在短暂重新启用窗口
- Resin 删除租约后不保证换到不同出口 IP；本地轮换不会删除远端租约
- 不实现 OAuth 登录或账号导入导出
