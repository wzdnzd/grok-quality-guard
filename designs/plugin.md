# Grok Quality Guard 插件设计

> 状态：已确认
> 目标运行时：CLIProxyAPI v8 插件 ABI
> 插件 ID：`grok-quality-guard`
> 版本：设计 v1
> 日期：2026-11-11

## 1. 背景与目标

### 1.1 背景

本项目基于最新的 CLIProxyAPI 插件开发规范，实现一个管理 Grok / xAI 凭证与账号质量的插件。核心目标是识别 HTTP 200 但质量异常的成功响应，并在不破坏 CPA 调度边界的前提下进行观测、处置和可选透明保护。

[grok2api](https://github.com/chenyme/grok2api/tree/main) 已经形成了一套较完整的降智账号发现与处理逻辑，重点不是“请求失败重试”，而是识别 HTTP 200 但质量异常的成功响应，包括：

- 缺少真实 reasoning 证据
- reasoning token 账单与真实输出不匹配
- 极短生成窗口内倾倒大量 token
- fake encrypted thinking
- cipher-only 状态循环
- soft / hard TPS 异常
- 空流和空闲流

本设计采用“先观测、后处置、透明保护可选”的策略。默认不自动修改凭证，只有管理员显式开启自动处置后才写 `disabled`。透明拦截模式默认关闭，开启后拦截所有可识别的 Grok / xAI OAuth 请求；重放资格只决定失败后是否允许换号重试。

### 1.2 目标

1. 管理 CPA 已有的 xAI / Grok auth。
2. 观测每个账号的真实质量证据，并持久化状态。
3. 将异常账号与 Resin 账号级租约绑定，而不是绑定到整个出口节点。
4. 提供管理页与 JSON API，支持探测、冷却、禁用、恢复、绑定和轮换。
5. 可选透明拦截：在首个客户端可见 chunk 发出前识别坏流并换号。
6. 所有异常候选耗尽时 fail closed，返回 503 `quality_degraded`，不把确定异常的内容下发给客户端。
7. 文档层面的接口、默认值、状态机、错误处理和测试标准必须足够明确，使后续实现无需再次做产品或架构决策。

### 1.3 非目标

- 不实现 OAuth 登录、账号导入、账号导出。
- 不把 Resin 管理 API 当作“立即创建指定出口 IP 租约”的接口。
- 不保证删除租约后一定换到不同的出口 IP。
- 不尝试在已经向客户端 emit 数据后回滚流。
- 不把普通网络错误伪装成质量降智。
- 不自动恢复管理员手动禁用的账号。

### 1.4 术语

| 术语 | 含义 |
| --- | --- |
| 被动观测 | 通过 Usage、响应和流 chunk 观察已有请求，不改变请求路由 |
| 主动探测 | 使用固定 prompt，固定 AuthID 和代理发起独立模型请求 |
| 透明保护 | 插件接管请求，在首次 emit 前完成质量门和换号 |
| 真实思考证据 | 明文 reasoning delta，或达到下限的真实 encrypted reasoning 内容 |
| 账号级租约 | Resin `Platform.Account` 身份对应的稳定出口租约 |
| guard-owned | 由本插件写入并持有的禁用/冷却状态 |

## 2. 参考资料结论

### 2.1 grok2api 降智判定

grok2api 的关键实现位于：

- [quality_retry.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/quality_retry.go)
- [quality_retry_scan.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/quality_retry_scan.go)
- [quality_probe.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/quality_probe.go)
- [degrade.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/domain/audit/degrade.go)
- [bootstrap.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/infra/qualityguard/bootstrap.go)
- [selector.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/selector.go)
- [selector_plan.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/selector_plan.go)
- [selector_session.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/selector_session.go)
- [selector_test.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/selector_test.go)

必须保留的核心结论：

1. `usage.reasoning_tokens` 不是思考证据。降智账号可能报告几百 reasoning tokens，但流里没有 reasoning delta。
2. `: grok2api-reasoning-start` 只是 timing stub，不是思考证据。
3. 空 reasoning item 不等于思考。
4. `encrypted_content` 非空也不够，必须达到密文下限：`max(min_encrypted_bytes, reasoning_tokens * encrypted_bytes_per_reasoning_token)`，默认 `max(256, reasoning_tokens * 4)`。
5. 密文长度按 trim 后的字符串字节长度计算，与 grok2api 当前实现一致。
6. visible token 只能从可见 content delta 推导，不能用 `usage.output - usage.reasoning` 代替。
7. 判断速度异常时，TPS 使用总 output tokens，其中包含 reasoning tokens。
8. 成功请求不会自动清除 missing-thinking strike；只有主动探测成功或管理员恢复才解除冷却，strike 本身仍保留。

### 2.2 TPS 公式

```text
generationWindow = duration - firstToken
```

当满足以下条件时必须修正：

```text
reasoningTokens > 0
generationWindow < firstToken
generationWindow < 1000ms
```

此时：

```text
generationWindow = duration
```

最终：

```text
TPS = outputTokens * 1000 / generationWindow
```

如果 `outputTokens <= 0` 或 `generationWindow <= 0`，TPS 记为 0，不参与速度降智分类。

### 2.3 默认阈值

| 配置 | 默认值 | 用途 |
| --- | ---: | --- |
| `soft_tps` | 500 | soft TPS 告警阈值 |
| `hard_tps` | 1000 | hard TPS 异常阈值 |
| `min_generation_ms` | 1000 | buffered burst 的生成窗口下限 |
| `min_passive_output_tokens` | 32 | 被动速度审计最小输出 |
| `min_hold_output_tokens` | 8 | 透明流式 hold 最小可见输出 |
| `hold_timeout` | 30s | 流式扣留最长时间 |
| `hold_max_buffer_bytes` | 4 MiB | 扣留的最大缓冲 |
| `max_attempts` | 6 | 透明模式总上游尝试次数 |
| `on_exhausted` | `fail_closed` | 所有候选异常后的行为 |
| `missing_thinking_cooldown` | 12h | missing-thinking 首次冷却 |
| `strike_decay_after` | 48h | missing-thinking strike 自动降级窗口，单位小时；0 关闭 |
| `strike_decay_min_healthy` | 10 | 窗口内至少积累的连续健康观测/探测次数 |
| `empty_stream_cooldown` | 15m | 空流/空闲流冷却 |
| `min_encrypted_bytes` | 256 | encrypted thinking 字节下限 |
| `encrypted_bytes_per_reasoning_token` | 4 | 每 reasoning token 的密文下限 |
| `fail_closed` | false | 短生成窗口和高 TPS 是否直接判为 burst，以及窗口不足是否单独分类 |

### 2.4 异常分类

| 分类 | 含义 | 典型证据 |
| --- | --- | --- |
| `missing_thinking` | 应有 reasoning，但没有真实思考证据 | 无 reasoning delta，密文不足下限 |
| `buffered_burst` | 可见窗口结束后极短跨度内倾倒输出 | `VisibleSpanMS < 1000ms`、可见流式证据不足且账单异常 |
| `cipher_drool` | cipher 已达到下限但无明文 reasoning、无 reasoning tokens，却持续倾倒 visible text | encrypted 存在、reasoning tokens 为 0、visible 很大 |
| `fake_encrypted_dump` | 先出现 fake encrypted 账单，再在极短可见窗口内倾倒完整答案 | 无 reasoning delta、窗口结束、可见跨度小于 2000ms、账单异常 |
| `fast_reasoning_ratio_dump` | 明文 reasoning 存在，但 reasoning 占比过高且可见窗口极短 | reasoning tokens >= 80% output、窗口结束、可见跨度小于 2000ms |
| `soft_tps` | TPS 达到 soft 阈值但未达到 hard | TPS >= 500 且低于 hard |
| `hard_tps` | TPS 达到 hard 阈值 | TPS >= 1000 |
| `rate_limited` | 上游限速但不是明确额度耗尽 | 429 或明确 rate limit 文案，无强 quota 特征；只设置探测退避，不写 auth |
| `empty_stream` | 上游完成但没有有效内容 | 无 thinking、无 usage、无 visible/semantic output |
| `probe_error` | 主动探测请求失败或流不完整 | 网络、超时、非 2xx、无终止事件 |
| `proxy_error` | 代理层失败 | 代理连接、认证或 Resin 错误 |

分类优先级：

1. `buffered_burst`
2. `cipher_drool`
3. `fake_encrypted_dump`
4. `fast_reasoning_ratio_dump`
5. `missing_thinking`
6. `hard_tps`
7. `soft_tps`
8. `empty_stream`

dump 检测必须先于“已有 thinking 就放行”，否则 fake reasoning 可以绕过。

时间型 dump（`buffered_burst`、`fake_encrypted_dump`、`fast_reasoning_ratio_dump`）只能在可见窗口结束后判断；流式进行中只能依据可见 token 体量触发 `cipher_drool`。


### 2.5 CPA v8 插件能力与边界

参考版本：

- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI/tree/main) 默认分支 `main`
- Go module：`github.com/router-for-me/CLIProxyAPI/v8`
- ABI version：1
- Schema version：10
- 动态库构建方式：`go build -buildmode=c-shared`

本插件声明以下能力：

- `ManagementAPI`
- `UsagePlugin`
- `StreamChunkInterceptor`
- `ModelRouter`
- `Executor`
- `ExecutorModelScope=both` 或 `static`，以 xAI auth 类型实测结果为准
- `ExecutorInputFormats=["codex"]`
- `ExecutorOutputFormats=["codex"]`

关键限制：

1. `StreamChunkInterceptor` 只能修改 headers、修改当前 chunk、drop chunk，不能回滚已发送内容，也不能重新选择账号。
2. `RequestInterceptor.Terminate` 会直接结束请求，不会触发 CPA 内置换号。
3. CPA 原生重试只覆盖上游 bootstrap 错误或首个 payload 之前发生的错误。
4. 一旦开始向客户端 emit，任何换号都无法撤销已下发内容。
5. 透明门依赖 CPA 实际调用插件 `ModelRouter`；插件自身不对 Home 模式或请求元数据做主动旁路。
6. `host.model.execute_stream` 支持 `AuthID`、`ProxyURL`、`ForcedProvider`、`EntryProtocol`、`ExitProtocol`、`Body`、`Headers`。
7. 嵌套调用必须转发 `HostCallbackID`，否则可能递归触发本插件拦截器。
8. `host.model.execute_stream` 返回的是 host 管理的 stream，需要 `stream_read` 和 `stream_close`。
9. 插件管理路由路径不得包含 `:`、`*`、空白字符。Action API 注册为 `/plugins/grok-quality-guard/api`。
10. `host.auth.save` 会写物理 auth JSON 并 upsert runtime auth；保存时必须保留 access/refresh token 等所有非代理字段。

### 2.6 Resin 租约语义

关键文件：

- [handler_lease.go](https://github.com/Resinat/Resin/blob/master/internal/api/handler_lease.go)
- [router.go](https://github.com/Resinat/Resin/blob/master/internal/routing/router.go)
- [control_plane_leases.go](https://github.com/Resinat/Resin/blob/master/internal/service/control_plane_leases.go)

删除单个租约：

```http
DELETE /api/v1/platforms/{platform_id}/leases/{account}
Authorization: Bearer <RESIN_ADMIN_TOKEN>
```

结果：

- `204 No Content`：删除成功
- `404 NOT_FOUND`：租约不存在
- `400 INVALID_ARGUMENT`：参数错误
- `401`：缺少或错误的 Admin Token
- `500 INTERNAL`：服务端错误

Resin 没有显式“创建指定账号租约”的 API。租约在带 Account 的代理请求首次路由时惰性创建。因此：

1. 删除旧租约后，下一次固定身份代理请求会创建新租约。
2. 新租约可能仍选中相同节点或相同出口 IP。
3. 插件不得承诺“必然换 IP”。
4. 不查询 egress IP 作为轮换成功条件。
5. 为让下一次请求使用全新身份，插件会同时生成新的 Resin account 名并写回 auth 的 `proxy_url`。

Resin 身份格式示例：

```text
socks5h://Default.{account}:{proxy_token}@resin:2260
```

Account 只允许 `A-Z a-z 0-9 _ -`，最长 128 字符。

## 3. 总体架构

### 3.1 双模式

#### passive_guard

默认开启。职责：

- 读取已有请求的 usage、响应和流 chunk
- 解析真实 thinking 证据
- 解析 TPS 和 dump 特征
- 记录账号质量状态
- 执行主动恢复探测
- 在管理员开启自动处置后写冷却/禁用状态

passive_guard 不改变正常请求的路由和响应。它无法阻止当前请求已经收到的坏响应，只能影响后续调度或凭证状态。

#### transparent_guard

默认关闭。职责：

- `ModelRouter` 判断请求是否属于 Grok / xAI 路由模型
- 路由到插件自有 `Executor`
- executor 固定 AuthID 和 ProxyURL 调用 `host.model.execute_stream`
- 在首个客户端可见 chunk 前完成质量门
- 可安全重放的异常流关闭上游流并换号；不可安全重放的请求只执行一次
- 所有候选异常时 fail closed

透明模式覆盖全部可识别的 Grok / xAI 路由请求。禁用推理、非推理模型和不可安全重放请求都不得绕过质量门；它们只在 thinking 判定和失败后的重试策略上有所区别。

### 3.2 组件划分

```text
plugin entry
  ├─ lifecycle/config
  ├─ account catalog
  ├─ quality analyzer
  ├─ state machine
  ├─ probe worker
  ├─ Resin lease manager
  ├─ passive observers
  │    ├─ UsagePlugin
  │    ├─ StreamChunkInterceptor
  │    └─ ResponseInterceptor
  ├─ transparent executor
  │    ├─ ModelRouter
  │    └─ ProviderExecutor
  └─ management API + embedded UI
```

### 3.3 passive 数据流

```text
client request
  -> CPA built-in auth selection
  -> upstream
  -> usage / response / stream interceptors
  -> quality analyzer
  -> state update
  -> optional auto action
```

被动观察到异常时，当前响应已经可能被客户端接收。插件只记录和更新状态。

### 3.4 transparent 数据流

```text
client request
  -> ModelRouter
  -> plugin Executor
  -> load immutable selection snapshot
  -> healthy / unknown quality layers, Priority descending
  -> preferred / session / layer strategy selection
  -> fixed AuthID + ProxyURL
  -> host.model.execute_stream
  -> hold stream and analyze
       ├─ deliver: emit buffered prefix, then pass through
       ├─ withhold + replayable: close upstream, rotate Resin, next attempt
       ├─ withhold + unsafe replay: return 503 quality_degraded
       └─ all exhausted: host.stream.close 503 quality_degraded
```

### 3.5 主动探测数据流

```text
probe scheduler
  -> select unknown / suspect / cooling account
  -> temporarily make guard-owned auth available if necessary
  -> host.model.execute_stream with fixed prompt
  -> analyze reasoning evidence, TPS, marker
  -> success: healthy + clear cooldown, keep strike
  -> missing_thinking after cooldown: permanent disable
  -> probe error: re-disable and backoff
```

## 4. 质量判定规范

### 4.1 适用范围

质量判定对象是 xAI / Grok OAuth 请求的响应。

- `transparent_guard=true` 时，所有可识别的 Grok 路由请求都进入透明门，不按 reasoning 能力或重放资格旁路。
- 非推理模型、显式关闭 reasoning 和未知推理能力的请求跳过 `missing_thinking` 与 dump 类；空流、速度和缓冲上限仍须执行。
- 可安全重放只决定质量失败后能否换号或换代理，不决定是否接管请求。
- passive 观测只对具有可解析文本流的样本形成质量结论，不改变 CPA 原生路由。
- 主动探测请求本身不重复进入普通账号被动样本。
- 上游非 2xx 请求不分类为质量降智。

### 4.2 真实思考证据

以下任一项成立时，`HasThinking=true`：

1. 非空明文 reasoning delta
2. 非空 reasoning summary delta
3. 非空 thinking delta
4. encrypted content 字节数达到下限

以下情况不算真实思考：

- 空 reasoning item
- 只有 reasoning item ID
- 只有 `: grok2api-reasoning-start` 注释
- 只有 `usage.reasoning_tokens`
- 只有不足下限的 encrypted stub

### 4.3 协议解析

#### OpenAI Chat Completions

检查：

- `choices[].delta.reasoning`
- `choices[].delta.reasoning_content`
- `choices[].delta.thinking_content`
- `choices[].delta.content`
- `choices[].delta.tool_calls`
- `choices[].delta.function_call`
- `choices[].finish_reason`
- `usage.completion_tokens`
- `usage.completion_tokens_details.reasoning_tokens`

#### OpenAI Responses / Codex SSE

检查：

- `response.output_item.added/done`
- reasoning item 的 `encrypted_content`
- `response.reasoning_text.delta`
- `response.reasoning_summary_text.delta`
- `response.output_text.delta`
- message item content
- tool call item
- `response.completed`
- `usage.output_tokens`
- `usage.output_tokens_details.reasoning_tokens` 或 `thinking_tokens`

#### Anthropic Messages

检查：

- `content_block_start` 的 `thinking` / `redacted_thinking` / `text` / `tool_use`
- `content_block_delta` 的 `thinking_delta` / `signature_delta` / `text_delta` / `input_json_delta`
- `message_delta.usage.output_tokens`
- `message_delta.usage.output_tokens_details.thinking_tokens`
- `message_stop`

#### SSE 通用规则

- 同时支持 `data: {...}` 和 `event: ...` 包装
- 支持分块跨 chunk 的 SSE 行缓存
- 支持缺失末尾换行
- `[DONE]` 和 `response.completed` 都视为 terminal
- `finish_reason` 和 `message_stop` 也视为 terminal

### 4.4 可见 token 估算

优先使用协议 delta 的可见文本：

```text
visibleTokens = ceil(visibleRunes / 4)
```

仅在完全无法获得可见 token 计数时，才用 output tokens 作为 terminal 兼容回退。

不得使用：

```text
usage.output_tokens - usage.reasoning_tokens
```

作为 visible tokens，因为 Chat 协议经常把 reasoning 包含在 completion tokens 中。

### 4.5 dump 检测

先定义可见窗口信号：

- `VisibleSpanMS = lastVisibleAt - firstVisibleAt`
- `VisibleChunks` 为可见文本 delta 的到达次数
- `VisibleTPS = VisibleTokens * 1000 / VisibleSpanMS`
- `windowFinal = Terminal || HoldExpired`
- `qualityVisibleStreaming = FirstVisible && VisibleSpanMS >= 100ms && VisibleTPS <= 1000 && (VisibleChunks >= 4 || VisibleSpanMS >= 200ms)`

#### buffered_burst

- hold 超时后出现短 visible 文本，但 reasoning bill 很大；密文达到下限本身不满足该分支
- `windowFinal` 且没有持续流式证据，`VisibleSpanMS < 1000ms`，并存在明显 dump 账单
- hold 超时分支同样先检查 `qualityVisibleStreaming`，持续流式输出即使窗口已过期也不判 dump

#### cipher_drool

- 无明文 reasoning delta
- reasoning tokens 为 0
- encrypted bytes 大于 0
- visible tokens >= 1024 可在流式进行中触发；terminal 时 visible tokens >= 最小输出阈值

#### fake_encrypted_dump

- 无明文 reasoning delta
- `windowFinal` 且没有持续流式证据
- `VisibleSpanMS < 2000ms`
- reasoning tokens >= 80 或 encrypted bytes 达到下限

#### fast_reasoning_ratio_dump

- 有明文 reasoning delta
- `windowFinal` 且没有持续流式证据
- `VisibleSpanMS < 2000ms`
- reasoning tokens * 5 >= output tokens * 4

`qualityVisibleStreaming` 成立时，时间型 dump 直接跳过；单块、可见跨度不足 100ms 或可见速率异常高的样本仍按上述规则拦截。

### 4.6 速度分类

后置审计或探测完成时：

```text
if fail_closed && generationWindow < min_generation_ms && tps >= soft_tps:
    buffered_burst
else if tps >= hard_tps:
    hard_tps
else if tps >= soft_tps:
    soft_tps
else:
    healthy
```

被动速度分类只对满足以下条件的样本生效：

- streaming
- 有 first token 记录
- output tokens >= min_passive_output_tokens
- HTTP 成功
- 非 compaction
- 非 forced egress probe
- 非流式响应不计算生成窗口和 TPS；只执行空响应、缺少真实思考和语义输出检查
- 流式响应若 reasoning tokens >= 80 且总时长小于 1000ms，说明 reasoning 持续时间不可观测，跳过 TPS 分类而不是判 `hard_tps`
- 非流式响应和上述重推理短窗口样本不得返回 `insufficient_generation_window`

### 4.7 空流

当流终止时：

- `HasThinking=false`
- `ReasoningTokens<=0`
- `OutputTokens<=0`
- `VisibleTokens<=0`

并且没有 semantic output（tool call、function call 等），判定为 `empty_stream`。

空流使用独立冷却时间，默认 15m，不与 missing-thinking 的 12h 冷却混用。

### 4.8 判定结果

分析器输出：

```go
type QualityVerdict struct {
    Class            string
    HasThinking      bool
    VisibleTokens    int64
    ReasoningTokens  int64
    OutputTokens     int64
    EncryptedBytes   int64
    EncryptedFloor   int64
    FirstTokenMS     int64
    FirstTokenKnown  bool
    VisibleSpanMS    int64
    VisibleChunks    int
    DurationMS       int64
    GenerationMS     int64
    TPS              float64
    Terminal         bool
    HoldExpired      bool
    SemanticOutput   bool
}
```

`Class` 为空表示未发现异常，不等于完成主动健康探测。

### 4.9 与参考实现对齐的判定路径

本插件的流式分析器按 grok2api 的 `ClassifyQualityHold`、`quality_retry_scan.go` 和 egress-quality-guard 的 `classify_result` / `classify_audit` 对齐，不做额外的“启发式猜测”：

1. 先解析协议内容，再按 `buffered_burst`、`cipher_drool`、`fake_encrypted_dump`、`fast_reasoning_ratio_dump` 的顺序执行 dump 判定。
2. dump 判定必须先于 thinking 放行，避免伪造 reasoning 账单绕过。
3. 明文 reasoning delta 是思考证据；仅有 `usage.reasoning_tokens`、空 reasoning item、timing stub 或未达下限的密文都不是。
4. `hold_expired` 后的短 visible 文本只有在 `reasoning_tokens >= 80` 时按 burst 处理；密文达到下限本身不满足该分支。
5. `: grok2api-reasoning-evidence <bytes>` 只提供密文字节长度，仍需经过 `max(min_encrypted_bytes, reasoning_tokens * encrypted_bytes_per_reasoning_token)` 下限校验。
6. 聚合 message 文本会计入 visible token，但不会伪造流式首个可见块的 flush 时间。
7. Chat、Responses、Anthropic Messages 的流式与非流式响应共享内容、thinking、密文下限和空流规则；TPS 仅适用于具有可观测生成窗口的流式样本。

判定路径存在明确差异：

| 路径 | 判定范围 | 不会推断的内容 |
| --- | --- | --- |
| 被动审计 | 仅 streaming、已有 TTFT、输出 >= 32、生成窗口有效的样本；已知要求推理时检查 missing-thinking | 不推断未知模型能力、显式关闭推理或非流式被动样本 |
| 主动探测 | marker、输出下限、thinking 要求、生成窗口、TPS，并复用 `ClassifyQualityHold` 的 dump 判定 | 不把传输错误或上游非 2xx 当成质量降智；成功探测保留 strike |
| 非流式质量门 | 空响应、缺少真实思考、密文下限、语义输出 | 不计算 TPS，不做流式 burst/dump 推断 |

`ignored` 表示本次样本不是质量信号。它只更新最近尝试，不增加质量 strike，也不覆盖上一次真实质量指标。`probe_error` / `proxy_error` 会更新最近尝试和探测退避，但同样保留上一次质量证据。

### 4.10 账号详情证据

账号详情抽屉只展示脱敏后的判定统计，不展示 prompt、响应正文、access token、refresh token 或代理凭据。证据包括：

- 判定来源、结果、说明、协议、流式/非流式、终止和 hold 超时状态
- visible/output/reasoning token、密文字节与密文下限
- TTFT、总时长、生成窗口、visible flush、visible span、visible chunks 和 TPS
- has thinking、明文 reasoning delta、reasoning started、semantic output、usage reported
- chunk、byte、event、unknown event 和 parse failure 计数

“最近尝试”与“最近质量判定”分开保存。这样即使最近一次是 `ignored` 或探测错误，也能看到上一次真实质量结论及其指标，而不是把所有指标误显示为 0。

### 4.11 复核后补充契约

- 主动探测与透明 hold 门共用 dump 判定，探测成功只解除冷却，不清除 `strike_missing_thinking`。
- `rate_limited` 是账号调度信号，不是质量失败：状态置 `suspect`，只写探测退避，不写 auth，不清零连续计数。
- `model_prefixes` 采用严格前缀匹配，不使用 `grok` 子串兜底；不以配置前缀开头的别名只能通过 `host.auth.models` 缓存识别。
- 被动拦截入口也使用 auth 模型缓存识别别名，不因严格前缀匹配丢失已发现模型。
- 非流式响应没有可观测生成窗口，速度维度整体跳过；非推理和显式关闭推理请求跳过 missing-thinking 与 dump 类，但仍执行空流、速度和缓冲上限检查。





## 5. 状态机与惩罚策略

### 5.1 状态

| 状态 | 含义 | 是否可调度 |
| --- | --- | --- |
| `unknown` | 尚未观测或探测 | 是 |
| `healthy` | 最近主动探测通过 | 是 |
| `suspect` | 有一次弱异常、探测错误或证据不完整 | 是，但降低优先级 |
| `degraded` | 已确认质量异常，但未自动处置 | 是，仅 observe 模式 |
| `cooling` | 已确认异常并处于冷却 | 否 |
| `quota_exhausted` | 上游明确额度耗尽，进入 24 小时冷却 | 否 |
| `credential_invalid` | 上游拒绝凭据，账号被禁用并等待重新认证 | 否 |
| `disabled_by_guard` | 冷却后再次确认，或管理员显式禁用 | 否 |
| `externally_disabled` | 初始就被管理员禁用，插件默认不得修改 | 否 |

`probing` 是临时运行标记，不作为持久状态。

### 5.2 状态转换

```text
unknown
  ├─ probe pass -> healthy
  ├─ weak anomaly -> suspect
  └─ confirmed anomaly -> degraded

healthy
  ├─ request success -> healthy
  ├─ weak anomaly -> suspect
  └─ confirmed anomaly -> degraded

suspect
  ├─ probe pass -> healthy
  ├─ probe error -> suspect + backoff
  └─ confirmed anomaly -> degraded

degraded
  ├─ auto_action=observe -> degraded
  ├─ auto_action=cooldown_only -> cooling
  └─ auto_action=cooldown_then_disable -> cooling

cooling
  ├─ cooldown not expired -> cooling
  ├─ probe pass -> healthy, keep missing-thinking strike
  ├─ cooldown_only + missing_thinking after cooldown -> cooling again
  ├─ cooldown_then_disable + missing_thinking after cooldown -> disabled_by_guard
  └─ probe error -> cooling + backoff

quota_exhausted
  ├─ 24h cooldown -> automatic re-enable and recovery probe
  ├─ probe pass -> healthy
  └─ quota response again -> quota_exhausted

credential_invalid
  ├─ disabled=true until manual restore
  └─ no automatic restore or probe

disabled_by_guard
  ├─ manual restore -> unknown, clear guard strike
  └─ no automatic restore
```

### 5.3 惩罚规则

`auto_action=observe`：

- 质量异常只记录
- 质量异常不修改 CPA auth
- 质量异常不自动轮换 Resin
- 质量异常不写 disabled
- `credential_invalid` 和 `quota_exhausted` 仍直接处置，避免失效账号继续参与路由
- 历史 anomaly 数量用于展示，不在切换到自动策略时追溯处罚

`auto_action=cooldown_only`：

1. 第一次确认异常：auth 写 `disabled=true`，state 写 `disable_kind=cooldown`、`cooldown_until=now+对应冷却时长`，状态 `cooling`。
2. 冷却期内再次观测到异常：保持当前冷却，不重复延长。
3. 冷却到期后执行恢复探测。
4. 恢复探测成功：恢复 `disabled=false`，状态 `healthy`。
5. 恢复探测仍异常：重新进入冷却，状态保持 `cooling`，不写永久禁用。
6. 恢复探测网络错误：重新进入 cooldown + backoff，不写永久禁用。

`auto_action=cooldown_then_disable`：

1. 第一次确认 `missing_thinking`：auth 写 `disabled=true`，state 写 `disable_kind=cooldown`、`cooldown_until=now+12h`，状态 `cooling`。
2. 冷却期内再次观测到异常：保持当前冷却，不重复延长或立即禁用。
3. 冷却到期：只有在 state 仍记录该账号由 guard 控制且未被管理员手动改动时，临时恢复。
4. 恢复探测成功：恢复 `disabled=false`，清除 cooldown，状态 `healthy`，保留 `strike_missing_thinking=true`。
5. 恢复探测再次确认 `missing_thinking`：auth 写 `disabled=true`，state 写 `disable_kind=permanent`，状态 `disabled_by_guard`。
6. 恢复探测网络错误：重新写回 cooldown，增加 backoff，不直接永久禁用。

上游账号状态错误独立于质量 strike：`credential_invalid` 直接禁用并等待凭据更新；`quota_exhausted` 使用固定 24 小时冷却，因为 Grok 免费额度按滚动 24 小时窗口恢复。这两类处置不受 `auto_action: observe` 限制，避免失效凭据或耗尽额度继续参与路由。

`rate_limited` 语义：

- 普通 429 或明确 rate limit 文案，仅在缺少强 quota 特征时归类为 `rate_limited`
- 状态置 `suspect`，只写 `probe_backoff_until`，不写 auth、不自动轮换
- 不清零 soft/error/thinking 连续计数，因为限速不是成功样本

上游错误匹配优先使用结构化状态码和强特征：

- `401` -> `credential_invalid`
- `402` -> `quota_exhausted`
- `403` 仅当响应体为 JSON 且包含明确凭据拒绝短语时判 `credential_invalid`，HTML/文本 403 判 `ignored`
- `429` 仅命中 `free-usage-exhausted`、`run out of credits`、`spending-limit`、`included free usage` 等强额度短语时判 `quota_exhausted`，否则判 `rate_limited`
- 删除裸 `quota`、`authentication`、`unauthorized` 以及 `tokens (actual/limit)` 等过宽匹配

CPA 的 `model_cooldown` 是模型级调度状态，其文案可能携带最后一次上游额度错误，但不能证明当前 auth 自身额度耗尽。插件必须将其视为 `ignored` 宿主调度错误，不得据此写入 auth 的 `disabled` 字段。

`missing_thinking` strike 语义：

- 成功请求不清除 strike
- 主动探测成功清 cooldown，但保留 strike
- 下一次确认 missing-thinking 时直接进入永久禁用
- 管理员手动 restore 时清除 strike

strike 自动降级：

- `quality.strike_decay_after` 单位小时，默认 48；设置为 0 时关闭自动降级
- 必须同时满足自动降级窗口和 `quality.strike_decay_min_healthy` 指定的连续健康样本数，默认 10
- 健康样本包括 passive/transparent 的 `ClassNone` 观测和主动探测成功
- 真实质量失败会重置窗口和连续健康计数；探测/代理错误和限速不计入质量失败，也不增加健康计数
- 降级只清除 `strike_missing_thinking`，并记录 `strike_decay/expired` 事件

`empty_stream`：

- 默认只冷却 15m
- 不直接永久禁用
- 连续空流不自动升级为 missing-thinking 禁用，除非出现真实缺失思考证据

### 5.4 冷却写入与恢复

写 disabled 时必须读取完整 auth JSON，但只允许修改 CPA 原生字段 `disabled`。所有权、冷却截止时间、strike 和简短的 `disable_reason` 全部保存在 `state.json`；重启后从 state 恢复，不能依赖内存推断。

必须保留 access token、refresh token、id token、expiry、email、type、provider 及其他未知字段。

恢复时只能操作满足以下条件的 auth：

- `disabled=true`
- state 中该账号处于插件隔离状态
- state 中存在非空 `disable_kind`

如果 auth 已 `disabled=true` 但 state 中没有 `disable_kind`，视为外部禁用，且不得伪造禁用原因。若管理员在冷却期间手动启用 auth，reconciler 只在冷却仍有效时修复临时启用窗口；冷却已过期时尊重 auth 当前启用状态并清理插件隔离字段。

CPA 或插件重启时，catalog 可能短暂为空或不完整。guard-owned state 不能在单次 catalog 缺失时删除；恢复 catalog 后继续使用原 `disable_kind` 和 `disable_reason`。

### 5.5 外部禁用

插件首次发现 auth 已 `disabled=true` 且 state 中没有插件禁用所有权时：

- 状态标为 `externally_disabled`
- 不探测
- 不自动恢复
- 管理页默认只展示，批量恢复时必须再次显式确认是否同时启用
- 管理员单项 restore 或未勾选“同时恢复外部禁用账号”时，只清除插件状态，不修改 auth；批量恢复经显式确认后可修改 `disabled=false`

## 6. 透明保护设计

### 6.1 ModelRouter

Router 输入包含 SourceFormat、RequestedModel、Stream、Headers、Query、Body、Metadata、AvailableProviders。

Router 处理条件：

1. 插件配置 `transparent_guard=true`
2. 请求不是插件内部主动探测
3. 请求模型匹配 Grok：默认 model prefix 为 `grok-`，或从缓存的 `host.auth.models` 可确认该模型属于 xAI

否则返回 `Handled=false`，由 CPA 原生路径处理。符合条件的请求即使不可安全重放或没有候选账号，也必须由插件接管并返回明确错误，不能静默回退 CPA。

### 6.2 重放资格与尝试次数

允许：

- 普通文本 OpenAI Chat Completions
- 普通文本 OpenAI Responses
- 普通文本 Anthropic Messages
- 非流式或流式
- 无状态 tool declaration：`function`、`custom`、`local_shell`、`apply_patch`、`tool_search`、`shell` 且 `environment.type=local`

排除：

- image / video / speech / audio / realtime / websocket
- compaction
- `previous_response_id` 非空
- `store=true`
- `background=true`
- `conversation` 非空
- `input` 中包含 `compaction_trigger`
- `web_search_options` 非空
- `mcp_servers` 非空数组
- hosted tool：`web_search`、`code_interpreter`、`file_search`、`image_generation`、远程 `shell`
- 任何未知 server/native tool
- 请求体无法解析为预期 JSON
- 请求路径表明媒体或非文本能力

重放资格只控制失败后的失败恢复：可安全重放请求受 `max_attempts` 限制并允许换号/换代理；不可证明可安全重放时仍执行一次透明质量门，失败返回 `503 quality_degraded`，不换号重试。只有显式配置 `on_exhausted: fail_open` 才会下发最后一个已缓冲响应。

### 6.3 候选账号

候选来源：

- `host.auth.list`
- `host.auth.get` 补全的物理 auth
- 插件状态中的冷却和禁用信息

过滤：

- provider/type 必须为 xAI/Grok
- 只允许 `healthy` / `unknown`
- 跳过 `suspect`、`degraded`、`cooling` 及其他隔离状态
- 跳过 `disabled_by_guard`
- 跳过 `externally_disabled`
- 跳过 runtime unavailable
- 跳过没有有效 access token 的账号

主动探测标记只用于识别没有请求级 pending observation 的内部 probe usage，不得按 AuthID 过滤透明候选，也不得丢弃同一窗口内真实请求的 usage。

分层：

1. `healthy` 整体优先于 `unknown`
2. 同一质量状态下，Auth Priority 数值越大越优先
3. 每个 `(quality, priority)` 组合是独立选择层，层内按 AuthID 稳定排序
4. 当前层未耗尽时不进入下一层
5. 当前层全部失败后才 fallback 到下一层

透明请求不得依赖 CPA 内置 auth selection 再次选号，必须显式传 `AuthID`。
完整的分层索引、preferred、session affinity、in-flight 跟踪和层内策略见 6.10。

### 6.4 Executor 调用

对每个候选：

```go
req := pluginapi.HostModelExecutionRequest{
    EntryProtocol:  "codex",
    ExitProtocol:   "codex",
    Model:          requestedModel,
    Stream:         true,
    Body:           originalRequestBody,
    Headers:        forwardedHeaders,
    Query:          forwardedQuery,
    ForcedProvider: "xai",
    AuthID:         candidate.ID,
    ProxyURL:       candidate.ProxyURL,
}
```

调用必须转发 `HostCallbackID`。如果 host 返回的 format 需要调整，必须以集成测试结果为准；不得自行实现 xAI 上游协议。

#### 6.4.1 请求事实单次解析

透明请求前置阶段必须一次解析 body，得到 `model`、`reasoningRequired`、`replayable`、`compact` 和 `sessionSeed`，复用于选择、执行和会话键计算。

加密 reasoning include 注入需要保留大整数精度时，可再使用 `map[string]json.RawMessage` 做一次受控解析；不得在 model、reasoning、replay、session 四个独立函数中重复完整 unmarshal。

请求事实只来自原始请求；include 注入不改变 model、reasoning、replay 资格和 session signal。


### 6.5 流式质量门

```text
open upstream stream
buffer = empty
emitStarted = false

loop read upstream chunk:
    append chunk to buffer
    observe chunk
    verdict = classify(buffer)

    if verdict == deliver:
        emit all buffered chunks
        emitStarted = true
        pass through remaining chunks
        break

    if verdict == withhold:
        close upstream stream
        record anomaly
        maybe rotate Resin
        continue next candidate

    if buffer > 4 MiB:
        emit buffer
        emitStarted = true
        record buffer_overflow
        pass through remaining chunks
        break

    if context done:
        close stream
        return client cancellation

if terminal and no verdict:
    final classify
    if deliver: emit buffer
    if withhold: close upstream and next candidate
```

一旦 `emitStarted=true`，不得换号、不得回滚，上游错误直接下传。

- 时间型 dump 只在 `terminal` 或 `hold_expired` 后判定；流式进行中不得因前两个 delta 提前拦截
- emit 失败或请求 context 结束时，整个请求按客户端中止处理：关闭上游流、不换号、不记 strike、不写失败事件
- 缓冲超过 4 MiB 时放行已缓冲内容并记录 `transparent_overflow` 事件，不对该样本应用健康/异常判定，也不清零 strike
- 非推理请求在首个可见块后约 200ms 提前放行，但仍执行空流和速度等结构性检查

### 6.6 非流式请求

非流式请求可以完整缓冲响应后再分类：

- 通过：返回原始响应
- 异常：记录并尝试下一个账号
- 所有候选异常：返回 503 `quality_degraded`

非流式请求不涉及“首个 emit 前”的限制。

### 6.7 耗尽行为

所有尝试都异常或没有候选时：

```text
host.stream.close(
  stream_id,
  error="all Grok candidates failed the quality gate",
  error_detail={
    "http_status": 503,
    "code": "quality_degraded",
    "message": "no healthy Grok account produced a valid response"
  }
)
```

要求：

- 不返回最后一次异常响应
- 不伪造 402 或 429
- 不把异常内容以 200 发出
- 不泄露账号、代理或上游响应细节

### 6.8 Resin 轮换与当前请求

仅对 `safeReplayRequest=true` 的请求执行换号或轮换。不可安全重放请求只执行一次，不进入轮换或换号。

`missing_thinking` 按账号级质量证据处理：

1. 记录异常并把该账号标为 `suspect` 或 `degraded`
2. 如果还有其他透明候选，立即切换账号，不在当前账号上做代理轮换
3. 只有单账号池才使用代理轮换作为兜底，同一账号最多轮换一次
4. 轮换后仍异常则 fail closed；后续请求不会再次选择该账号
5. 总上游尝试次数仍受 `max_attempts=6` 限制

其他质量异常仍可按代理轮换策略在同账号重试一次，再切换账号。所有后续透明请求都跳过 `suspect` / `degraded` 账号。

### 6.9 无法覆盖的场景

- 已开始 streaming 后的坏流
- 不可安全重放请求的跨账号失败恢复；这些请求仍会执行一次质量门
- CPA 未调用插件 `ModelRouter` 的请求
- Host 绕过插件 executor 的 WebSocket / realtime 请求
- 客户端取消后无法重放

### 6.10 透明 Auth 选择与并发路由

本节是透明 Auth 选择与并发路由的唯一设计依据，描述当前实现和后续维护约束。

#### 6.10.1 选择权归属

透明模式下插件完全接管 Grok/xAI auth 选择：

1. `RouteModel` 命中 Grok 请求后返回 `Handled=true`、`TargetKind=Self`
2. 插件自己的透明执行器选择 auth
3. 插件把选中的 `AuthID` 写入 `HostModelExecutionRequest`
4. CPA 只负责使用指定 auth 执行，不再为这次请求重新选择 auth

因此并发的首轮分发、会话亲和、preferred、失败切换和分层 fallback 都由插件完成，不能依赖 CPA 内置的 auth 选择或 `routing.session-affinity`。

选择器需要同时满足：

- 质量优先：`healthy` 整体优先于 `unknown`
- Priority 优先：同一质量层内数值越大越优先
- 并发分散：10 个以上空闲 auth 的并发首请求不能全部落到同一个 auth
- 稳定填仓：顺序流量可以在健康且空闲时持续复用上次成功 auth
- 会话连续性：同一稳定 session 在 auth 正常时尽量固定
- 故障隔离：账号级失败立即退出候选，临时错误不误删健康偏好

#### 6.10.2 grok2api 机制核查

grok2api 没有把“上次执行成功账号”直接作为下一请求的全局第一候选。其实际机制包括：

- `lastSelectedAt`：记录账号最近一次被成功取得并发起执行的时间
- `lastSuccessAt`：记录最近成功时间，但主要用于限制持久化 `TouchLastUsed` 的写频率
- `LastUsedAt`：持久化的最近使用时间
- 候选评分：模型支持、额度、Web Tier、Priority、billing、in-flight、最近选择时间和稳定 AuthID 共同排序
- 并发租约：达到 `MaxConcurrent` 的账号从候选计划中排除
- sticky session：会话命中绑定账号时优先复用，绑定失效后重新选择

关键参考位置：

- `references/grok2api/backend/internal/application/gateway/selector.go`
  - `Selector.lastSelectedAt`
  - `Selector.lastSuccessAt`
  - `markSuccess`
- `references/grok2api/backend/internal/application/gateway/selector_plan.go`
  - `candidateScoreBetter`
  - `inFlight` 比较发生在 `lastSelected` 之前
- `references/grok2api/backend/internal/application/gateway/selector_session.go`
  - sticky session 命中、失效和 fallback
- `references/grok2api/backend/internal/application/gateway/selector_test.go`
  - `TestCandidatePlanExcludesSaturatedAccounts`
  - `TestCandidatePlanPreservesSelectorOrdering`

grok2api 的关键结论是：同 Priority 账号会随 in-flight 和 lastSelected 分散，高 Priority 账号可以被优先填满，但达到并发上限后会排除并继续使用下一账号。

本插件吸收“优先复用上次成功账号”的收益，但增加两个比 grok2api 原行为更保守的约束：

- 只有该 auth 仍属于当前最高可用层时才作为 preferred
- 只有该 auth 健康且当前没有进行中的插件请求时才作为 preferred

#### 6.10.3 候选资格

透明选择索引只允许：

- `healthy`
- `unknown`
- 状态为空但 catalog 可用，按 `unknown` 处理

明确排除：

- `Disabled`
- `Unavailable`
- 非 Grok provider/type
- `suspect`
- `degraded`
- `cooling`
- `quota_exhausted`
- `credential_invalid`
- `externally_disabled`
- `disabled_by_guard`

索引快照只是快速路径，真正执行前仍要做一次只看当前账号的 O(1) 最终检查：

- auth 是否仍存在于 catalog
- state 是否仍为透明候选状态
- auth 是否仍可解析出有效 Grok payload
- auth 是否已被当前请求尝试过

最终检查失败时跳过该 candidate，并触发该 auth 的定向索引刷新，不扫描整个账号池。

#### 6.10.4 分层规则

先按质量状态分组，再按 Priority 分组：

```text
healthy / Priority=20
healthy / Priority=10
healthy / Priority=0
unknown / Priority=20
unknown / Priority=10
unknown / Priority=0
```

规则：

- `healthy` 整体优先于 `unknown`
- 同一质量状态下 Priority 数值越大越优先
- 每个 `(quality, priority)` 组合是独立层
- 层内按 AuthID 稳定排序
- 当前层还有未尝试候选时不进入下一层
- 当前层全部失败后才进入下一层
- 请求级 `max_attempts` 仍是硬上限，达到后立即停止
- 非可安全重放请求仍只允许一次尝试

Priority 的语义必须统一为“数值越大越优先”。旧的升序排序已经移除。

#### 6.10.5 后台选择索引

请求路径不得每次复制 catalog、读取全部状态、过滤排序并重新构造分层。

选择索引由 `routingSelectionIndex` 维护：

- `routingSelectionSnapshot`：不可变快照
- `layers`：层键到 `(quality, priority, accounts)` 的映射
- `layerByAuth`：auth 到层键的映射
- `order`：已排序的非空层键列表
- `total`：候选总数

索引使用 `atomic.Pointer[routingSelectionSnapshot]` 发布。请求创建时只加载一次快照，并绑定该请求内的所有选择状态，后续后台更新不会改变正在执行的请求视图。

全量构建时机：

- 插件启动并完成 catalog 和 state 加载后
- 每 30 秒 catalog 刷新完成后
- catalog 新增、删除、Priority、Disabled、Unavailable 变化
- 周期性校验或索引压缩

全量构建复杂度为 O(n log n)，只发生在后台。

增量更新规则：

```text
unknown -> healthy
```

1. 复制当前快照的层映射和 `layerByAuth`
2. 从 `unknown / Priority=X` 层移除 auth
3. 插入 `healthy / Priority=X` 层
4. 更新 `layerByAuth`
5. 重新计算非空层顺序
6. 原子发布新快照

其他状态变化同理：

- `healthy -> suspect`：退出选择索引
- `healthy -> degraded`：退出选择索引
- `unknown -> quota_exhausted`：退出选择索引
- auth 被删除或禁用：退出所有层
- Priority 变化：移动到新的 Priority 层

状态更新必须先在 state 提交成功，再发布索引快照。普通事件写入、请求错误日志等不改变账号候选层级的 state 更新不得触发索引全量重建。

请求期复杂度：

- 加载快照：O(1)
- 层查找：O(1)
- 策略起点：O(1)
- 请求内候选遍历：O(attempts)
- 单账号状态增量移动：只复制受影响层和索引元数据，不复制全部 Account 数据

#### 6.10.6 Preferred Last-Success Auth

插件维护进程内全局偏好：

```text
preferredAuthID
preferredSuccessAt
```

它只记录透明请求的成功结果，不记录：

- 主动 probe 成功
- passive 观测成功
- session affinity 直接命中的成功
- 被 session 绑定直接使用的成功

顺序流量语义：

1. 没有 preferred 时按当前层策略选号
2. 选中 auth A 且质量门通过后，A 成为 preferred
3. 后续请求优先使用 A
4. 只要 A 仍为 `healthy`、属于当前最高层且当前没有进行中的请求，就继续使用 A
5. A 额度耗尽、进入冷却、变成非候选状态、被删除或不可执行后清除 preferred
6. fallback 成功后，如果当前 preferred 已失效，则新 auth 成为 preferred

并发语义：

- `TryReservePreferred` 必须在同一把锁内完成“检查空闲并占用”
- 第一个请求占用 preferred A
- 第二个请求看到 A 正在使用，保留 preferred，但本轮不等待 A
- 第二个请求进入当前层的基础策略
- 基础策略优先选择其他空闲 auth
- 只有当前层所有候选都忙时，才允许通过第二遍 busy fallback 共享已有 in-flight 的 auth
- A 完成后释放 in-flight，后续顺序请求仍优先回到 A

preferred 更新规则：

- 当前没有 preferred：设置成功 auth
- 当前 preferred 就是成功 auth：刷新 `preferredSuccessAt`
- 当前 preferred 已失效或已不在最高层：用成功 auth 替换
- 当前 preferred 仍健康且只是 busy：fallback 成功不替换 preferred
- session affiliation 命中：只刷新 session 绑定，不修改全局 preferred

插件没有实时额度余额来源，因此“额度用完”通过以下信号体现：

- 上游 402
- 上游 429 且判定为明确 quota exhaustion
- state machine 转为 `quota_exhausted`
- auth 进入冷却、隔离或退出透明候选

不承诺提前预测额度，只保证出现这些信号后停止优先使用该 auth。

#### 6.10.7 Session Affinity

目标是在 auth 正常时，将同一稳定会话尽量固定到同一 auth。

支持的 session signal 按优先级提取：

1. `X-Claude-Code-Session-Id`
2. `X-Codex-Turn-Metadata` 中的 `prompt_cache_key`
3. `X-Codex-Turn-Metadata` 中的 `window_id`
4. `X-Codex-Window-Id`
5. `X-Session-Id`
6. `Session-Id`
7. `Session_id`
8. `X-Conversation-Id`
9. `Conversation-Id`
10. `Conversation_id`
11. `X-Client-Session-Id`
12. `X-Grok-Conv-Id`
13. body `prompt_cache_key`
14. body `metadata.session_id` / `metadata.sessionId`
15. body `conversation_id` / `conversationId`
16. body `client_metadata.x-codex-turn-metadata`
17. body `client_metadata.x-codex-window-id`

不使用：

- `X-Request-ID`
- trace ID
- 每次请求变化的 request UUID
- 随机生成的 session ID

session key：

```text
sessionKey = SHA256(
  "grok-quality-guard:affinity:v1" +
  callerScope +
  provider +
  model +
  sourceFormat +
  sessionSeed
)
```

`callerScope` 优先取 CPA metadata 的 `CallerScopeMetadataKey`。缺失时使用下行 API Key 的 SHA-256 摘要作为隔离域。原始 key 和原始 session signal 不写日志、不写 state。

绑定语义：

- 绑定 auth 在当前最高层且仍可用时优先使用
- 绑定 auth 不在当前最高层时，不跨层强行使用
- 绑定 auth 被删除、禁用、不可执行或状态不再合格时删除绑定
- 绑定 auth 健康但正被其他请求使用时，session affinity 仍允许同一会话继续使用它，以保持会话连续性
- 同一 session 的并发首请求通过原子 `GetOrBind` 选择一个 auth，避免各自选到不同账号
- 成功请求刷新 TTL
- TTL 过期后删除绑定
- 绑定只在内存中保存，进程重启后自然丢失
- 超过 `session_affinity_max_entries` 时淘汰最旧绑定
- TTL 过期和超限淘汰使用按到期时间、绑定时间维护的最小堆；堆项惰性校验并定期压缩，避免每次请求全表扫描

没有稳定 session signal 时不创建绑定，请求仍享受分层、preferred 和并发分散。

#### 6.10.8 In-flight 与层内策略

插件维护进程内：

```text
activeAttempts map[authID]int
```

计数时机：

- 在真正调用 host model execution 前占用
- 非流式、缓冲流、实时流结束或失败后释放
- auth 解析失败、执行前失败、Resin 轮换后失败和所有提前返回路径都必须释放
- 使用 `attempt.Release()` 统一释放，禁止在 selection lock 内调用上游网络

普通策略采用两遍选择：

1. idle pass：优先尝试当前层中未被占用且未尝试的 auth
2. busy pass：只有 idle pass 没有任何候选时，才允许按同一策略使用正在被其他请求占用的 auth

这避免了 preferred 正在使用时，普通 round-robin 又立刻选中同一个 auth，同时不会因为所有 auth 都忙而直接返回无候选。

层内策略：

`round_robin`

- 每个 `(quality, priority)` 层维护独立游标
- 每个新请求从游标当前位置开始
- 游标在选号时原子推进，不等待网络请求完成
- session 或 preferred 命中不推进普通游标
- 回退顺序按层内环形顺序遍历，不重复尝试

`random`

- 每次请求在当前层随机选择一个起始下标
- 从该下标开始环形遍历
- 不生成完整随机排列
- 不使用有放回随机

`fill_first`

- 当前层始终从排序后的第一个 auth 开始
- 如果第一个 auth 忙，idle pass 会继续寻找空闲 auth
- 当前层全部不可用后才进入下一层
- 适合明确希望集中消耗账号的场景，不作为默认值

默认：

```yaml
routing:
  selection_strategy: round_robin
  prefer_last_success: true
  session_affinity: true
  session_affinity_ttl: 1h
  session_affinity_max_entries: 10000
```

#### 6.10.9 单请求选择流程

```text
snapshot := selectionIndex.Load()

for each layer in snapshot.Order:
  if no remaining attempts: stop

  if session binding exists and belongs to this layer:
    if binding auth is eligible:
      try bound auth
      if success: refresh binding, return
      if hard account failure: delete binding
      continue to next candidate in this layer

  if preferred auth exists and belongs to top layer:
    if preferred is healthy and TryReservePreferred succeeds:
      try preferred
      if success: refresh preferred, return
      if hard account failure: clear preferred
      continue to next candidate in this layer
    else if preferred is healthy but busy:
      keep preferred
      skip it in idle pass

  start := strategyStart(layer)
  for idle candidate in layer:
    if excluded, tried, ineligible or busy: continue
    reserve idle attempt
    resolve auth
    if session binding was created by a concurrent request: switch to bound auth
    try candidate
    if success: bind/refresh session and update preferred if needed
    if retryable with Resin rotation: retry same candidate once
    mark excluded

  if idle pass found no candidate:
    for busy candidate in layer:
      same strategy order, reserve shared attempt, try once

  release attempt
end for
return quality_degraded
```

请求不会复制完整 catalog，也不会对全部 auth 排序。

#### 6.10.10 失败分类与绑定更新

账号级失败包括：

- credential invalid
- quota exhausted
- missing thinking 达到处置阈值
- isolate / degraded
- auth disabled
- auth removed
- auth 不可执行

处理：

- 当前 candidate 在请求内排除
- 清除该 auth 的 session 绑定
- 若该 auth 是 preferred，清除 preferred
- 继续当前层剩余 auth
- 当前层结束后再进入下一层

临时容量或传输失败包括：

- host 暂时无容量
- 临时 proxy 错误
- 网络超时
- 可重试临时执行错误

处理：

- 当前请求可以切换到当前层其他 auth
- 不因为一次临时失败就替换全局 preferred
- auth 仍为 healthy 时保留 preferred 和 session 绑定
- 后续 state machine 若将其转为非候选状态，再按账号级失败处理

Resin 轮换语义保持不变：

- 仅对可安全重放请求执行
- 同一 candidate 最多轮换一次
- 轮换后重新解析 auth，确保使用新的 `proxy_url`
- `missing_thinking` 存在其他候选时优先换号，不先轮换代理
- `proxy_error` / `probe_error` 达到 `consecutive_errors` 时，存在其他候选则先换号并异步轮换失败账号的代理
- 没有其他候选时才同步轮换代理并重试同一 auth
- 单账号池才使用代理轮换作为兜底
- 轮换次数计入 `max_attempts`

#### 6.10.11 配置与热更新

新增运行时策略：

```yaml
routing:
  selection_strategy: round_robin
  prefer_last_success: true
  session_affinity: true
  session_affinity_ttl: 1h
  session_affinity_max_entries: 10000
```

校验：

- `selection_strategy` 只能是 `round_robin`、`random`、`fill_first`
- `session_affinity_ttl > 0` 且不超过 `168h`
- `session_affinity_max_entries > 0` 且不超过 `100000`
- 缺失字段使用默认值
- 旧 state.json 没有 `routing` 时不需要 schema 迁移
- 管理页保存策略后必须同步更新 `p.policy.Routing` 和 `p.cfg.Routing`

preferred、session binding、round-robin 游标和 in-flight 计数不写入 state.json。

#### 6.10.12 并发安全要求

必须保证：

- `routingSelectionSnapshot` 发布后不可变
- 请求只读取加载到本次请求的原子快照
- 索引更新使用 copy-on-write，不修改旧快照
- preferred 的空闲检查和占用是原子的
- round-robin 游标推进是原子的
- session binding 的 `GetOrBind` 是原子的
- activeAttempts 的增减是原子的
- 不同层游标互不影响
- 选择锁不得跨越上游网络调用
- 不得在持有 selection lock 时执行 host callback、Resin API 调用或长耗时 I/O
- 请求失败时只能条件删除自己的 session 绑定，不能误删其他请求刚写入的新绑定
- `unknown -> healthy` 在 state 提交成功后触发增量移动，下一个请求读取到的新快照即可看到 healthy 层
- 普通事件写入不得使索引失效或触发全量重建

#### 6.10.13 测试与验收

已覆盖的测试：

- Priority 数值越大越优先
- `healthy` 整体优先于 `unknown`
- `suspect` / `degraded` 不进入候选
- `unknown -> healthy` 增量移动到 healthy 层且不触发全量重建
- 10 个同层 healthy auth 并发首轮选择 10 个不同 auth
- round-robin 游标并发的精确推进
- preferred 健康且空闲时被复用
- preferred busy 时新请求选择其他 idle auth
- preferred busy 期间 fallback 成功不替换 preferred
- session 并发首请求通过原子绑定落到同一 auth
- 不同 caller scope 和不同 session 不共享绑定
- binding auth 失效后重新选择并更新绑定
- binding TTL 和 max entries 淘汰
- 普通事件写入不使选择索引失效
- 路由配置热更新
- Resin 轮换后同 auth 重试使用新 proxy
- `go test ./...`
- `go test -race ./...`

透明选择重构的实现验收还必须满足：

1. 10 个以上 healthy auth 并发请求时不会全部使用同一个 auth
2. 顺序请求在 preferred 健康且空闲时优先使用同一 auth
3. preferred 正在使用时，新请求能立即使用其他 auth
4. 同一 session 的连续请求在正常状态下保持同一 auth
5. 当前最高 Priority 层未耗尽时不会使用低 Priority 层
6. 当前层全部失败后才 fallback 到下一层
7. 账号额度耗尽、隔离或删除后自动换号并重建绑定
8. probe 成功不会改变普通流量的 preferred
9. Resin 轮换、`max_attempts` 和非重放语义保持不变
10. unknown -> healthy 后，下一个请求能从后台索引的 healthy 层看到该 auth
11. 请求路径不因账号总数增加而执行全量复制、全量状态读取或全量排序
12. race 检测通过，且日志不泄露 session/token 原文
## 7. passive 设计

### 7.1 StreamChunkInterceptor

职责：

- 只读解析 request body 和 stream chunk
- 记录 first visible time、first token、usage、reasoning evidence、tool calls
- terminal 时计算 verdict
- 不修改 chunk
- 不 drop chunk
- 不返回 error 拦截正常响应

必须防止重复计数：

- 忽略 `X-Grok-Quality-Probe: 1`
- 忽略带 probe metadata 的内部请求
- 同一 request ID 只完成一次状态更新

### 7.2 ResponseInterceptor

用于非流式响应：

- 读取完整响应体
- 解析 reasoning / usage
- 记录最终 verdict
- 不修改正常响应

### 7.3 UsagePlugin

UsagePlugin 只作为辅助指标来源：

- input/output/reasoning tokens
- TTFT
- duration
- success/failure

不得单独使用 usage 判定 missing-thinking。

### 7.4 自动处置

passive 发现 confirmed anomaly 时：

- `observe`：只记录
- `cooldown_only`：执行第 5 节的冷却状态机，恢复探测失败时继续冷却
- `cooldown_then_disable`：执行第 5 节的状态机，恢复探测失败时永久禁用

被动路径无法阻止当前请求，状态更新只影响后续请求和 CPA auth selection。

## 8. 主动探测设计

### 8.1 固定 prompt

```text
先用三点总结为什么天空呈蓝色，最后一行只输出 QUALITY_OK。
```

期望 marker：

```text
QUALITY_OK
```

### 8.2 探测请求

使用：

- `host.model.execute_stream`
- `ForcedProvider=xai`
- 固定 `AuthID`
- 固定 `ProxyURL`
- `EntryProtocol=codex`
- `ExitProtocol=codex`
- `Stream=true`
- 保留 `HostCallbackID`
- 默认 max output tokens 768
- 90s timeout
- 4 MiB 最大响应
- 不进行请求级自动重试

探测结果必须同时满足：

1. 流正常终止
2. 无质量异常分类
3. 包含真实 thinking 证据
4. visible output >= 32 tokens
5. 最后一个非空行等于 `QUALITY_OK`

### 8.3 调度

默认只做恢复性探测：

- 新发现账号：`unknown`，延迟 30s 后探测一次
- `suspect`：15m 后探测一次，失败 backoff 最大 1h
- `cooling`：冷却到期后探测一次
- `healthy`：没有周期探测
- 管理员可随时手动触发 force probe

并发：

- 扫描阶段一次性过滤出到期的非健康账号，再放入有界队列
- 队列容量由 `probe.queue_size` 控制，默认 10000，最大 1000000
- 单类探测或批量任务内部只启动与 `probe.concurrency` 相同数量的 worker
- 同一账号同时最多 1 个 probe
- `probe.unknown_jitter` 为首次探测增加稳定抖动，避免启动时全部到期
- 队列按 auth ID 去重；worker 出队后再次检查 due 状态
- 调度器向任务中心暴露只读快照；启用守护模式或队列仍有工作时显示“质量检测队列”，不把自动探测拆成逐账号任务

### 8.4 探测与凭证 disabled

CPA 不允许直接执行 disabled auth。恢复探测流程必须：

1. 读取完整 auth
2. 检查 state 中的 `disable_kind` 和 `AuthDisabledIntent`
3. 临时写 `disabled=false`
4. 立即发起固定 AuthID 探测
5. 成功后保持启用
6. 失败则恢复/升级 disabled 状态
7. 异常退出由后台 reconciler 在下次启动时修复

必须记录这段短暂启用窗口。该窗口无法完全消除，除非 CPA 未来提供“允许 disabled auth 的固定 AuthID 探测”能力。

### 8.5 探测结果

- 通过：`healthy`
- 探测与透明 hold 门共用 `ClassifyQualityHold` 的 dump 判定；成功探测只解除冷却，保留 `strike_missing_thinking`
- 探测成功计入 strike 自动降级的连续健康样本；失败不增加健康计数
- `missing_thinking`：冷却后再次确认 -> `disabled_by_guard`
- 空流：15m 冷却
- probe 网络/超时/流不完整：`suspect` + backoff
- 代理错误：`proxy_error`，冷却并在条件允许时轮换 Resin
- `401`：`credential_invalid`，直接写入 `disabled=true`，不自动探测
- `402` 或包含明确 free usage/spending-limit/credits 短语的 `403/429`：`quota_exhausted`，直接进入 24 小时冷却
- 普通 `429` 或明确 rate limit 文案：`rate_limited`，只设置探测退避，不写 auth、不清零连续计数
- 其他非 2xx：`ignored` 或 `probe_error`，不直接永久禁用

## 9. Resin 来源

### 9.1 模板化模型

Resin 不是全局单例配置，而是代理来源的一种特殊模板：

- 与节点、模板共用新增类型下拉、列表、编辑、检测、删除、绑定和轮换流程。
- 可以同时创建多个 Resin 来源。
- 同一 Resin 服务可以按不同平台、Admin 地址或账号生成规则拆成多个来源。
- 来源配置保存在 `Proxy.ResinSources[sourceID]`，可见端点和占位符保存在 `Proxy.Templates`。
- 不存在全局 YAML `resin:` 配置块，也不读取旧 `resin.*` 环境变量回退。

每个 Resin 来源包含：

- 通用字段：名称、启用、Priority、最大绑定数、更新策略
- Resin 字段：平台名、平台 ID、Admin URL、Admin Token、Proxy Token、账号格式、本地轮换模式
- 模板字段：用户名占位符和关联值组

代理轮换支持两种互斥模式：

- API 模式：管理 Resin 远端租约，可以首次绑定、轮换和清除租约。
- 仅本地轮换：不调用 Resin API，只改写 auth 中已有代理的用户名。

`local_only=true` 时不需要 Admin API URL、Platform ID、Admin Token、Proxy Token 或代理模板。保存该模式会清空该来源中的 API URL、Platform ID 和 token。再次切回 API 模式时必须重新填写并验证 API 配置。

### 9.2 Account 命名

`{account}` 是 Resin 专用保留占位符，语义固定：

- 取 auth 邮箱 `@` 前的 local part
- 转小写
- 仅保留 `a-z0-9_-`
- 其他字符替换为 `_`，连续 `_` 合并
- 去掉首尾 `_`，最大 48 字符
- email 缺失时使用 auth label
- label 仍为空时使用 `auth_<authID sha256 前 8 位>`

`{account}` 不接受 enum、random、关联组或候选值配置。它只能由 auth 邮箱前缀自动提供。

除 `{account}` 外，账号格式可以继续使用普通模板占位符，例如：

```text
xai-{account}-{region}
grok-{account}-{suffix}
svc-{account}-{vendor}-{plan}
```

默认新来源使用：

```text
grok-{account}-{suffix}
```

其中 `suffix` 默认是 4 位随机字符。新 account 必须与旧 account 不同，且不得与本地已管理 account 冲突；冲突时最多重试 16 次。

### 9.3 API 模式验证

API 模式保存时必须同时验证：

1. `admin_url` 是带 host 的合法 `http` 或 `https` URL，且不包含 userinfo、query 或 fragment。
2. 使用 Admin Token 调用 `GET /api/v1/system/info`，验证 Admin API 可达且 Token 有效。
3. 使用同一 Admin Token 调用 `GET /api/v1/platforms?keyword={platform}`，按平台名称精确解析 `platform_id`，并以 API 返回的
ame` 作为后续代理模板使用的规范平台名。
4. 按账号格式渲染验证账号 `grok-validation`，对代理主机执行 SOCKS5 RFC1929 用户名/密码认证，验证 Proxy Token 被接受。
5. 任一步失败都返回明确错误，且不得写入 state。

`admin_token` 和 `proxy_token` 只在 API 模式全部验证通过后写入 state。管理页与 JSON API 永远不返回 token 原值，只返回 `admin_token_set` 和 `proxy_token_set`；留空再次保存时保留已有 token。

### 9.4 首次绑定

默认不自动绑定，首次绑定仅在 API 模式可用。本地轮换模式不能创建新代理绑定。

管理页提供：

- 绑定单个账号
- 绑定全部未绑定账号
- 预览将要生成的 account 名
- 标记已有非插件管理 proxy_url 为外部配置，不覆盖

绑定单个账号：

1. 读取完整 auth JSON
2. 如果 `proxy_url` 非空且不是插件管理绑定，则标记外部配置或漂移
3. 使用来源 Provider 生成新 account 和 proxy_url
4. 写 `proxy_url`
5. 保存 auth
6. 更新通用 `proxy_binding`，记录 SourceID、SourceRevision、EndpointHash、Generation 和 Lease

### 9.5 轮换

轮换分为 API 模式和仅本地轮换。两种模式共用以下步骤：

1. 读取 auth 和 state 中的通用代理绑定
2. 从 binding lease 或现有代理用户名中取得旧 account
3. 生成新 account，必须与旧 account 不同
4. 调用 `host.auth.save` 写回，保留 access/refresh token 等所有字段
5. 更新 binding generation
6. 每个 auth 使用独立互斥锁，防止并发请求重复轮换

API 模式固定顺序：

1. 使用来源配置、规范平台名和账号格式渲染新 `proxy_url`
2. 写回 auth 并更新通用 binding
3. 将旧租约删除任务写入 state 的持久化队列
4. 后台 worker 执行 `DELETE /api/v1/platforms/{platform_id}/leases/{account}`
5. `204` 或 `404` 视为成功并移除任务
6. 网络错误、`401/403`、`429` 和 `5xx` 保留任务并按指数退避重试，最长间隔 1 小时
7. 不调用 GET lease 查询 egress IP，不同步等待租约删除

仅本地轮换固定顺序：

1. 要求 auth 中已有非空 `proxy_url`
2. 解析代理 URL，要求包含 scheme、host、用户名和密码
3. 用户名必须至少包含一个 `.`，最后一个 `.` 后的部分视为旧 account
4. 只替换用户名中最后一个 `.` 后的 account，保留 scheme、host、port、password、path、query 等其余部分
5. 写回 auth 中的 `proxy_url` 并更新 binding
6. 不调用任何 Resin API，不删除远端旧租约
7. 事件详情必须标记本次轮换未调用 Resin API

本地轮换示例：

```text
socks5h://Default.grok-alice-old1:proxy-secret@resin:2260
  -> socks5h://Default.grok-alice-new1:proxy-secret@resin:2260
```

如果现有代理用户名没有 `.` 分隔的 account，或缺少用户名/密码，则本地轮换失败且不得修改 auth。

### 9.6 失败矩阵

| 场景 | 处理 |
| --- | --- |
| DELETE 204 | 删除任务完成 |
| DELETE 404 | 删除任务完成 |
| DELETE 401/403 | 保留任务并退避重试，记录配置错误 |
| DELETE 429 | 保留任务并退避重试 |
| DELETE 5xx | 保留任务并退避重试 |
| 网络超时 | 保留任务并退避重试 |
| 删除队列写入失败 | 本次轮换仍以本地 proxy 切换为准，记录 internal error |
| auth save 失败 | 不更新 state，记录失败，保持旧绑定 |
| 随机 account 冲突 | 重试最多 16 次 |
| 新 proxy 渲染失败 | 不修改旧 `proxy_url`，记录错误 |
| 本地模式无 proxy_url | 返回错误，不写 auth |
| 本地代理用户名无 account 后缀 | 返回错误，不写 auth |
| 本地代理缺少密码 | 返回错误，不写 auth |

### 9.7 租约清理快照

旧租约删除使用不可变 `ResinSnapshot`：

- snapshot 保存 Admin URL、Platform ID 和 Admin Token
- cleanup job 保存 SnapshotID、AuthID、Admin URL、Platform ID、Account、重试状态
- 配置修改后，已排队任务仍使用旧快照
- 只有 binding、operation 或 cleanup job 引用结束后才 GC 快照
- 不从 auth 代理 URL 反推历史租约
- 不从当前来源配置拼接旧租约的删除目标

### 9.8 清除与批量操作

代理管理页的账号绑定子视图是账号与代理生命周期的操作面，适合承载账号列表、脱敏代理、勾选、批量绑定和批量清除。账号状态与质量判定保留在 `账号` Tab；连接凭据保留在代理来源编辑器。

批量绑定通过 `proxy.bind_unbound` 或 `proxy.bindings.bind` 完成。已绑定账号保持原租约，非插件管理的外部代理不会被覆盖。

批量绑定、绑定全部未绑定、批量清除使用与主动探测相同的异步任务和有界工作队列模式。任务不会为每个账号单独启动一个不受控 goroutine，而是由固定数量 worker 从有界队列消费。

批量清除通过 `proxy.bindings.clear` 和 `auth_ids` 参数完成：

1. 先移除 auth 中的 `proxy_url`，清除 state 中的通用 binding，保留其他 auth 字段
2. API 模式下，插件管理的旧租约写入 state 的持久化删除队列
3. `204` 或 `404` 视为远端租约已删除；其他错误保留任务并指数退避重试
4. 外部代理没有插件管理的租约，不删除外部代理服务
5. 仅本地轮换模式只移除 auth 中的代理，不调用 Resin API，也不处理远端租约
6. 任一账号失败只影响该账号，批量接口继续处理其他账号并在结果中返回逐项错误

当 refresh catalog 发现某个 auth 已从 CPA 列表消失时，插件在删除账号 state 前读取其通用 binding。只有绑定中包含有效 Resin SnapshotID 和 lease account 时才写入 `resin_lease_deletes`。本地轮换、缺少快照或连接信息不完整的记录只删除 state，不调用 Resin API。

## 10. 管理页与 JSON API

### 10.1 路径

CPA 的插件资源路由会自动加 `/v0/resource/plugins/<pluginID>/` 前缀，Action API 路由会自动加 `/v0/management/` 前缀。为避免完整 URL 中重复插件 ID，注册路径只保留页面和动作语义。

资源页注册路径：

```text
/dashboard
```

实际完整资源 URL：

```text
/v0/resource/plugins/grok-quality-guard/dashboard
```

Action API 注册路径：

```text
/plugins/grok-quality-guard/api
```

实际完整 Action API URL：

```text
/v0/management/plugins/grok-quality-guard/api
```

合法性依据：`internal/pluginhost/management.go` 中的
ormalizeResourceRoute` 会把 `ResourceRoute.Path` 挂到 `/v0/resource/plugins/<pluginID>/` 下，仅拒绝空白、`:`、`*` 和 `..`；
ormalizeManagementRoute` 会把 action 路由挂到 `/v0/management/` 下，仅拒绝空白、`:` 和 `*`。因此上述注册路径合法，且 `grok-quality-guard` 在每个完整 URL 中只出现一次。

旧路径 `/plugin/grok/quality-guard`、`/v0/management/plugin/grok/quality-guard/api` 不作为兼容别名。本次文档和后续实现统一使用：

- 资源页：`/dashboard`
- action API：`/plugins/grok-quality-guard/api`

### 10.2 管理页

页面采用与 CPA Management Center 一致的中性色视觉体系，自动跟随宿主页面的明暗主题，所有界面文字使用中文，并按任务频率拆分 Tab：

- `概览`：守护状态、检测模式、账号总数、需关注账号、最近 24h 自动检测统计、质量分布、最近事件、当前策略摘要和代理摘要
- `账号`：搜索、状态筛选和分页、脱敏账号信息、最近尝试、真实思考证据、TPS/TTFT/output/reasoning/encrypted 指标、代理绑定、冷却和探测退避；详情抽屉提供脱敏判定依据，以及主动探测、冷却、恢复、禁用、绑定和轮换
- `代理管理`：内部按 `账号绑定` 和 `代理来源` 两个任务子视图组织。账号绑定子视图展示每个 Grok 账号、脱敏代理、代理账号和轮换代数，支持搜索、状态筛选、分页、单账号绑定/轮换/清除，以及批量绑定/清除。代理来源子视图统一展示节点、模板和 Resin，新增类型下拉同时包含三种来源。
- `任务中心`：统一展示账号、代理、Resin 清理和质量交叉验证等异步任务，包含进度、总数、处理中、等待、成功、失败、跳过、来源、时间和逐项结果；常驻队列只显示实际启用或仍有工作的队列。主动探测调度器以“质量检测队列”展示实时等待、处理中、成功和失败数量，自动探测不逐账号创建任务历史。历史任务列表从紧凑索引按页读取，逐项结果仅在打开任务时按需解压加载。`#tasks/<task-id>` 可直接打开任务详情
- `事件日志`：按关键词和事件类型筛选并分页展示时间、auth ID、分类、动作、结果和脱敏详情
- `设置`：低频配置集中在 `守护策略`；运行模式、层内选择策略、Preferred Last-Success、Session Affinity、最大尝试次数、候选耗尽行为和探测调度都在此编辑

页面技术要求：

- 内嵌于 Go 动态库，不依赖外部 CDN
- 使用与 CPA Dashboard 一致的紧凑布局、中性色背景、细边框和黑色主按钮；从同源父页面读取主题状态，支持明暗主题
- 支持桌面、窄屏和移动端，表格允许横向滚动；账号详情使用抽屉，避免在主表格堆叠大量操作
- 所有动态文本做 HTML escape
- 所有 fetch 使用同源 management API；API 只使用 `action` 和 `params`，不依赖请求体 UI 标记或自定义 Header
- 不显示 Secret 原值
- Tab 使用可键盘访问的按钮和 `aria-selected` 状态，并支持左右方向键切换
- 图标使用内嵌 SVG，不使用 emoji
- 遵守 `prefers-reduced-motion`
- 危险和批量操作使用内嵌确认对话框，不使用 `window.confirm` 或浏览器原生弹窗；支持 Escape 取消、焦点恢复和键盘焦点循环
### 10.3 JSON API 协议

统一请求：

```http
POST /v0/management/plugins/grok-quality-guard/api
Content-Type: application/json
```

请求体：

```json
{
  "action": "accounts.list",
  "params": {}
}
```

插件 API 不要求请求体 UI 标记，也不要求 `X-Grok-Quality-Guard-UI` 等自定义 Header。管理 API 的安全性由 CPA management 认证层负责，避免网关剥离自定义 Header 时管理页无法加载。

成功响应：

```json
{
  "ok": true,
  "data": {}
}
```

失败响应：

```json
{
  "ok": false,
  "error": {
    "code": "not_found",
    "message": "auth not found"
  }
}
```

状态码：200 成功、202 已接受异步任务、400 参数错误或 Resin 验证失败、403 动作不允许、404 auth/事件不存在、409 状态冲突、429 操作过于频繁、500 插件内部错误、502 Resin Admin API/代理不可达、503 CPA host 回调或上游不可用。异步任务的最终失败通过 `tasks.get` 的 `error` 和 `error_status` 返回。任务状态包括 `queued`、`running`、`succeeded`、`failed`、`cancelled` 和重启后恢复的 `interrupted`。

### 10.4 Action 列表

| Action | 参数 | 说明 |
| --- | --- | --- |
| `overview` | 无 | 汇总状态和计数 |
| `accounts.list` | `page`、`page_size`、`state`、`query`、`summary` | 分页账号列表 |
| `accounts.get` | `auth_id` | 单个账号详情 |
| `accounts.probe` | `auth_id`、`force` | 提交异步探测任务，返回 `task_id` |
| `accounts.probe_selected` | `auth_ids` | 将选中账号放入有界探测队列，需要恢复时先恢复再检测 |
| `accounts.cooldown` | `auth_id`、`duration` | 手动冷却 |
| `accounts.disable` | `auth_id`、`reason` | 手动永久禁用并修改 OAuth `disabled=true` |
| `accounts.disable_selected` | `auth_ids`、`modify_auth` | 批量禁用；插件隔离或同时修改 OAuth |
| `accounts.restore_preview` | `auth_ids` | 返回恢复范围与外部禁用数量 |
| `accounts.restore` | `auth_id` | 恢复 guard-owned auth 并清除 strike |
| `accounts.restore_selected` | `auth_ids`、`include_external` | 恢复选中账号；显式确认后可同时恢复外部禁用账号 |
| `proxy.summary` | 无 | 代理来源、绑定状态和自动绑定运行时摘要 |
| `proxy.sources.list/get` | `source_id` 等 | 代理来源列表与脱敏详情，节点、模板和 Resin 共用 |
| `proxy.sources.validate/preview/test` | `kind`、`source` 等 | 配置验证、条件预览和端点检测 |
| `proxy.sources.create/import_nodes/update/delete` | `kind`、`source`、`source_id` | 来源 CRUD 和批量节点导入 |
| `proxy.sources.test_selected/delete_selected` | `source_ids` | 来源页多选后的异步批量检测与删除 |
| `proxy.bindings.list` | 分页、筛选参数 | 账号绑定、external、drifted、unknown 筛选 |
| `proxy.bindings.bind/clear/rotate/adopt` | `auth_id`、`source_id` 等 | 手动绑定、清除、轮换、接管 |
| `proxy.bindings.endpoint` | `auth_id` | 按单个 auth 显式读取完整代理端点 |
| `proxy.bindings.set_auto_bind` | `auth_ids`、`excluded` | 批量排除或恢复自动绑定 |
| `proxy.template.apply` | `source_id`、`dry_run` | 按固定 revision 将模板应用到已绑定账号 |
| `proxy.bind_unbound` | `source_scope`、`source_ids`、`dry_run` | 显式批量绑定未绑定账号 |
| `proxy.auto_bind.get/update/run` | 配置或空 | 策略、开关与发现唤醒 |
| `proxy.changes.list/get/retry/cancel` | `change_id` 等 | 同步与回溯任务进度 |
| `resin.cleanup.list/retry/pause` | 分页、`job` 参数 | 已排队租约清理、重试和暂停/恢复 |
| `policy.get` | 无 | 返回运行时策略 |
| `policy.update` | `policy` | 校验并持久化策略 |
| `policy.reset` | 无 | 重置为 YAML 默认值 |
| `state_file.update` | `state_file` | 写入并切换状态文件路径；目标文件必须不存在 |
| `events.list` | `limit`、`cursor`、`auth_id` | 事件列表 |
| `tasks.center.list` | `page`、`page_size`、`category`、`status`、`query` | 查询任务中心的任务索引和常驻队列 |
| `tasks.center.get` | `task_id`、`page`、`page_size`、`item_status` | 按页读取逐项结果；历史任务从归档懒加载 |
| `tasks.center.cancel` | `task_id` | 请求取消等待中或执行中的任务 |
| `tasks.get` | `task_id` | 兼容旧客户端的任务状态查询；历史任务会恢复完整结果 |
| `tasks.list` | `limit` | 列出最近异步任务 |

不存在 `resin.config.*`、`resin.bind`、`resin.bind_unbound`、`resin.rotate` 或 `resin.clear`。Resin 的配置、绑定、清除和轮换统一使用上述 `proxy.*` action；只保留 `resin.cleanup.*` 处理已排队远端租约。
### 10.5 管理页与 host 认证

管理 API 由 CPA management 层认证。插件额外约束：

- 仅接受 POST
- 不要求自定义 Header 或请求体 UI 标记
- 不信任客户端传入的 auth ID 之外的代理或 token 字段
- 不返回任何 secret 原值
- 对 destructive action 返回明确结果

## 11. 配置 schema

### 11.1 示例

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    grok-quality-guard:
      enabled: true
      priority: 10

      passive_guard: true
      transparent_guard: false
      auto_action: observe

      model_prefixes:
        - "grok-"

      routing:
        selection_strategy: round_robin
        prefer_last_success: true
        session_affinity: true
        session_affinity_ttl: "1h"
        session_affinity_max_entries: 10000

      quality:
        soft_tps: 500
        hard_tps: 1000
        consecutive_soft: 2
        consecutive_errors: 2
        consecutive_missing_thinking: 1
        strike_decay_after: 48
        strike_decay_min_healthy: 10
        thinking_guard: true
        soft_cross_verify: false
        thinking_cross_verify: false
        min_generation_ms: 1000
        fail_closed: false
        min_passive_output_tokens: 32
        min_hold_output_tokens: 8
        hold_timeout: "30s"
        hold_max_buffer_bytes: 4194304
        min_encrypted_bytes: 256
        encrypted_bytes_per_reasoning_token: 4
        missing_thinking_cooldown: "12h"
        empty_stream_cooldown: "15m"
        max_attempts: 6
        on_exhausted: "fail_closed"

      probe:
        recovery_only: true
        model: "grok-4.5"
        timeout: "90s"
        max_output_tokens: 768
        concurrency: 1
        queue_size: 10000
        unknown_delay: "30s"
        unknown_jitter: "30s"
        suspect_backoff: "15m"
        max_backoff: "1h"

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

不存在全局 `resin:` YAML 配置块。Resin 来源的连接信息、账号格式、占位符和本地轮换模式通过管理页或 `proxy.sources.create/update` 的 `kind=resin` 配置。

YAML 使用严格字段校验，未知字段会直接报错。旧 `resin.*` 注册字段、环境变量回退和单例 Resin 配置不再读取。

### 11.2 配置来源与优先级

YAML 决定以下部署级字段：

- `enabled`
- `priority`
- 首次启动时的 `passive_guard`、`transparent_guard`、`auto_action`、`model_prefixes`
- `routing`、`quality`、`probe`
- `proxy.auto_rotate_after_quality_failure`、`proxy.auto_bind.*`、`task_history.*`
- `state_file`、`events_limit`

`state_file` 是状态文件的启动引导路径。管理页可以通过 `state_file.update` 写入并切换状态文件路径，并在引导路径旁写入 `<state_file>.location` 指针；指针存在时，插件重启后使用其指向的实际状态文件。修改 YAML 的 `state_file` 会切换到新的引导位置。

运行时策略首次从 YAML 初始化到 state，之后由管理页修改：

- `passive_guard`
- `transparent_guard`
- `auto_action`
- `model_prefixes`
- `routing`
- `quality`
- `probe`
- `proxy.auto_rotate_after_quality_failure`
- `task_history`
- `events_limit`

`policy.reset` 使用当前 YAML 值重新初始化运行时策略。`plugin.reconfigure` 不得静默覆盖用户在管理页保存的策略。

`model_prefixes` 采用严格前缀匹配：配置了 `grok-` 时，`my-grok-proxy` 不匹配；`gpt-` 不会匹配 `grok-4.5`。不以配置前缀开头的已发现别名只能通过 `host.auth.models` 缓存识别，不使用 `grok` 子串兜底。

### 11.3 默认值

- `passive_guard=true`
- `transparent_guard=false`
- `auto_action=observe`
- `on_exhausted=fail_closed`
- `max_attempts=6`
- `routing.selection_strategy=round_robin`
- `routing.prefer_last_success=true`
- `routing.session_affinity=true`
- `routing.session_affinity_ttl=1h`
- `routing.session_affinity_max_entries=10000`
- `quality.consecutive_soft=2`
- `quality.consecutive_errors=2`
- `quality.consecutive_missing_thinking=1`
- `quality.strike_decay_after=48`
- `quality.strike_decay_min_healthy=10`
- `quality.thinking_guard=true`
- `quality.soft_cross_verify=false`
- `quality.thinking_cross_verify=false`
- `probe.recovery_only=true`
- `probe.concurrency=1`
- `proxy.auto_rotate_after_quality_failure=true`
- `proxy.auto_bind.enabled=false`
- Resin 来源默认账号格式 `grok-{account}-{suffix}`
## 12. 状态文件与持久化

### 12.1 路径

默认路径按顺序选择第一个可写位置：

```text
/CLIProxyAPI/plugins/data/grok-quality-guard/state.json
plugins/data/grok-quality-guard/state.json
<os.UserConfigDir()>/cliproxyapi/grok-quality-guard/state.json
<temp>/grok-quality-guard/state.json
```

Docker 部署必须将 `/CLIProxyAPI/plugins/data/grok-quality-guard` 挂载为宿主持久卷，否则容器重建后 state 会丢失。也允许通过 `state_file` 显式覆盖；显式路径不可写时不会静默替换。管理页可编辑实际状态文件路径；切换时先完整写入目标文件，再更新引导路径旁的 `<state_file>.location` 指针。目标文件已存在时拒绝覆盖。目录权限 `0700`，文件权限 `0600`。

插件运行时只允许修改 auth JSON 的原生字段 `disabled` 和 `proxy_url`。禁用所有权、冷却原因、strike、代理 binding 和 Resin 清理任务只保存在 state。任务中心历史不写入 state：它位于状态文件同目录的 `task-center/index.jsonl`、`active/` 和 `archive/YYYY-MM/`；索引只在进入任务中心时读取，逐项明细只在打开任务时按页解压，插件启动时只扫描 `active/` 恢复未完成任务并标记为 `interrupted`。

### 12.2 Schema

状态文件固定为 schema v1。加载规则：

- `schema_version` 必须严格等于 1。
- JSON 使用 `DisallowUnknownFields` 解码。
- 不接受旧 schema、旧单例 Resin 字段、旧账号 `resin` 字段或旧 token 引用字段。
- 不执行字段迁移、旧数据继承或兼容回退；旧 state 必须删除后重建。
- 保存时再次校验 schema，禁止把非 v1 内存状态写回磁盘。

结构示意：

```json
{
  "schema_version": 1,
  "updated_at": "2026-11-17T12:00:00Z",
  "policy": {},
  "policy_yaml_revision": "sha256:...",
  "accounts": {
    "auth-id": {
      "auth_id": "auth-id",
      "state": "unknown",
      "proxy_binding": {
        "auth_id": "auth-id",
        "source_id": "resin-source-id",
        "source_kind": "resin",
        "source_revision": 1,
        "generation": 1,
        "endpoint_hash": "<sha256>",
        "lease": {
          "provider": "resin",
          "platform_id": "11111111-1111-1111-1111-111111111111",
          "account": "grok-alice-abcd",
          "snapshot_id": "<snapshot-id>"
        }
      },
      "metrics": {}
    }
  },
  "proxy": {
    "nodes": [],
    "templates": [],
    "resin_sources": {},
    "source_runtime": {},
    "operations": {},
    "changes": [],
    "auto_bind": {}
  },
  "resin_snapshots": {
    "<snapshot-id>": {
      "admin_url": "https://resin.example",
      "platform_id": "11111111-1111-1111-1111-111111111111",
      "admin_token": "<stored-secret>"
    }
  },
  "resin_lease_deletes": [
    {
      "auth_id": "auth-id",
      "admin_url": "https://old-resin.example",
      "platform_id": "11111111-1111-1111-1111-111111111111",
      "account": "grok-alice-old1",
      "snapshot_id": "<snapshot-id>",
      "attempts": 0,
      "next_attempt_at": "",
      "status": "pending"
    }
  ],
  "events": []
}
```

### 12.3 写入

- 使用临时文件写入后 rename
- 使用相邻的 `<state_file>.proxy-journal` 保存代理提交，每条记录包含单调序号和校验和
- 先持久化 prepared 操作，再修改 auth
- auth 修改结果明确后，持久化 binding、事件和 cleanup outbox
- 重启恢复按实际 auth、目标指纹和操作记录判定，不根据全局配置猜测
- 事件最多保留 `events_limit`
- 纯观测更新可节流 1s 写入
- `RecordObservation` 只更新内存并合并落盘；状态转换、禁用、恢复、探测成功、代理变更和管理员动作走同步写入
- `UpdateIfChanged` 由回调显式返回 changed，避免无变化重写文件
- Resin 清理快照和 token 只存储在本机 state；依赖目录 `0700`、文件 `0600` 限制访问

### 12.4 启动 reconcile

启动后后台执行，不得阻塞 `plugin.register`：

1. 后台加载 auth 列表
2. 移除 state 中已不存在的 auth，并在删除前按通用 binding 排队 Resin 租约清理
3. 识别被管理员手动禁用的 auth
4. 识别 guard-owned 冷却已过期但 auth 仍 disabled 的账号
5. 恢复未完成的代理操作和 proxy journal
6. 只自动修复 guard-owned 状态
7. 记录 reconcile 事件
## 13. 运行时并发与生命周期

- `plugin.register` / `plugin.reconfigure` 不同步扫描全部 auth
- 启动后延迟 1s 在后台加载
- 每个 auth 一把互斥锁
- state 使用全局读写锁
- probe 使用串行队列
- transparent 请求使用独立 attempt 上下文
- 插件 shutdown 时停止 probe worker、关闭 host model streams、flush 状态、不主动恢复任何 disabled auth
- 同一 request ID 的 passive 状态只更新一次
- 所有后台 goroutine 必须可通过 context cancel
- 透明流 bridge 使用请求生命周期 `requestCtx` 并纳入 `taskWG`；Quiesce/Shutdown 取消并等待，避免卸载后继续调用 host 回调
- 每个 model stream 使用一个常驻读协程，不为每个 chunk 创建 goroutine
- 会话绑定过期和超限淘汰使用最小堆；状态观测更新不复制完整选择索引

## 14. 安全与日志

### 14.1 禁止泄漏

不得出现在管理页、JSON API、插件事件、host log、错误消息中的内容：

- 渲染后的 proxy URL
- 代理用户名或密码
- Resin Admin Token
- Resin Proxy Token
- xAI access token
- xAI refresh token
- Cookie
- Authorization header 原值

### 14.2 脱敏函数

- `maskEmail`：保留首字符和域名后缀，例如 `a***@example.com`
- `maskProxy`：只显示 scheme 和 host
- `maskAuthID`：管理页默认显示前 8 位
- `maskToken`：固定返回 `[redacted]`
- 错误中的上游 body 限制 512 字节并清除 token 形态字符串

### 14.3 日志级别

- INFO：状态转换、cooldown、disable、restore、Resin rotation
- WARN：探测错误、空流、代理错误
- ERROR：插件内部错误、auth save 失败、Resin 配置错误
- DEBUG：请求级质量指标，不包含正文

### 14.4 Resin 来源 secret

- 管理页 token 输入框永不回填原值，只提示“已配置，留空保持”
- `proxy.sources.get`、`proxy.sources.validate`、`proxy.sources.update` 均不返回 token 原值
- API 模式保存前必须完成 Admin API、平台和 SOCKS5 代理认证验证；本地模式不调用 API
- 验证失败时不得覆盖旧来源配置
- state 内的 Admin Token、Proxy Token 和清理快照依赖目录 `0700`、文件 `0600` 权限保护
- 不存在环境变量 fallback；连接凭据只在来源配置中保存
- 旧租约清理使用不可变快照，不读取或回退到当前来源配置

## 15. 失败模式与限制

1. 流已经开始后无法回滚。
2. TPS 异常通常只能在响应结束时确认；若必须在 emit 前确认 TPS，则需完整缓冲，影响首字延迟。
3. CPA 不允许直接执行 disabled auth，恢复探测需要短暂重新启用。
4. Resin 没有显式创建租约 API，删除后由下一次请求惰性创建。
5. 删除租约不保证换到不同出口 IP。
6. 代理切换与异步 DELETE 不是跨服务原子事务；租约删除失败会保留在持久化队列中后台重试。
7. 透明门依赖 CPA 调用插件 `ModelRouter`；插件自身不按 Home 模式旁路。
8. hosted tool、stored response、compaction 等仍经过透明质量门，但不可安全重放，因此只执行一次且不换号重试。
9. auth 文件可能被管理员或其他进程并发修改；写回前必须重新读取并做字段级合并。
10. 事件日志不能保存 prompt、响应正文和原始错误 body。
11. 本地轮换只能改写已有代理用户名，不能创建新绑定，也不会删除远端旧租约。

## 16. 测试计划

### 16.1 单元测试

质量判定：

- 空 reasoning stub
- 只有 reasoning_tokens
- 真实 reasoning delta
- encrypted stub 不足下限
- encrypted 达到 max(256, 4 * reasoning tokens)
- visible token 估算
- generation window 边界
- soft / hard TPS
- buffered burst
- cipher drool
- fake encrypted dump
- fast reasoning ratio dump
- empty stream
- terminal 缺失末尾换行
- OpenAI Chat SSE
- OpenAI Responses / Codex SSE
- Anthropic Messages SSE

状态机：

- unknown -> healthy
- suspect -> healthy
- degraded -> cooling
- cooling -> disabled_by_guard
- cooling probe success 保留 strike
- 成功请求不清除 strike
- empty stream 15m
- 外部 disabled 不自动恢复
- 手动 restore 清除 strike

透明选择与路由：

- Priority 数值越大越优先
- `healthy` 整体优先于 `unknown`
- `suspect` / `degraded` / `cooling` 等状态不进入候选
- 当前层未耗尽时不进入下一层
- `unknown -> healthy` 增量移动且不触发全量重建
- 10 个同层健康 auth 的并发首轮选择使用不同 auth
- round-robin 游标并发精确推进
- preferred 健康且空闲时复用
- preferred busy 时选择其他 idle auth
- preferred busy 期间 fallback 成功不替换 preferred
- session 并发首请求通过原子绑定落到同一 auth
- 不同 caller scope 和不同 session 不共享绑定
- binding auth 失效后重新绑定
- binding TTL 过期和 max entries 淘汰
- 普通事件写入不使选择索引失效
- routing 策略热更新
- Resin 轮换后同 auth 重试使用新 proxy
- 质量失败时优先换账号，只有单账号池才代理轮换

Resin / 代理来源：

- 多个 Resin 来源可以同时存在并独立增删改查
- 同一 Resin 服务不同平台和账号格式不互相覆盖
- `{account}` 只取邮箱前缀并转小写
- `{account}` 是 reserved placeholder，不接受 enum/random/关联组
- 用户自定义占位符支持 enum/random/关联组
- 账号格式拒绝完整 URL 和 `{platform}` 前缀
- proxy template 渲染
- Admin API 系统信息校验
- 平台名解析为规范名称和 `platform_id`
- Proxy Token SOCKS5 RFC1929 认证
- 验证失败不写 state
- 留空 token 保留已保存值
- DELETE 204/404/401/403/429/5xx
- DELETE 不跟随重定向、不使用环境代理、不泄露凭据
- 持久化删除队列去重与指数退避
- 删除成功后从队列移除，失败后保留并重试
- 清理任务使用不可变 SnapshotID
- 配置修改后旧任务仍清理旧 Admin URL / Platform ID
- catalog 删除时从通用 binding 排队租约清理
- auth save 保留 access/refresh token
- 每个请求最多轮换一次
- 并发轮换互斥
- 本地模式只改写代理 account 后缀并保留其余 URL 字段
- 本地模式不发起 Resin Admin API 请求
- state 重载后代理来源和清理任务恢复

安全：

- proxy URL 脱敏
- admin token 脱敏
- auth token 脱敏
- 事件中不包含正文和上游响应 body
- 管理 API 不接受客户端传入的 token

### 16.2 集成测试

- mock CPA host callbacks
- mock xAI SSE
- mock Resin Admin API
- 透明模式首次 emit 前换号
- 所有候选耗尽返回 503 `quality_degraded`
- 坏内容不 emit
- 第一候选 deliver 后不再换号
- 4 MiB buffer overflow 放行并记录
- Resin 轮换后 auth proxy_url 立即更新且旧租约删除异步入队
- 本地模式轮换后 auth proxy_url 更新且旧租约接口未被调用
- 新 account 与旧 account 不同
- DELETE 404 仍继续
- DELETE 401 保留任务并退避重试
- 插件 reload 后状态恢复
- 并发请求状态一致
- 10 个以上同层健康 auth 的并发首轮请求分散到不同 auth
- preferred 和 session affinity 在透明流式路径中生效
- 大量账号加载不阻塞注册
- Home 模式元数据不会旁路透明路由
- 管理 API action 正常和错误响应

### 16.3 手工验收

- 安装插件后 management 页面可打开
- 默认 passive、observe
- 不修改任何 auth
- API 模式的管理页 Resin 配置在验证成功前无法保存，本地模式可直接保存
- 错误 Admin Token 或 Proxy Token 不会覆盖已保存配置
- 留空 Secret 输入框再次保存会保留已有值
- 手动绑定单个账号成功
- 手动轮换后 auth proxy_url 变化且 token 未丢失
- 冷却账号在 12h 内不可调度
- 冷却到期探测成功恢复
- 冷却到期探测失败永久禁用
- 管理员手动禁用账号不被恢复
- 所有日志不含 secret

### 16.4 构建命令

```text
go test ./...
go build -buildmode=c-shared
git diff --check
```

## 17. 实施阶段

### Phase 1：插件骨架与状态

- C ABI 初始化
- plugin.register / reconfigure
- 配置解析和校验
- 状态文件
- 后台 auth catalog
- 管理 API 和页面骨架

### Phase 2：被动质量观测

- SSE 解析器
- response / usage / stream interceptor
- 质量分类器
- 状态机
- 事件日志

### Phase 3：主动探测与恢复

- host.model.execute_stream 探测
- 固定 prompt
- probe worker
- cooldown 到期恢复
- disabled auth 临时启用和 reconciler

### Phase 4：Resin 来源管理

- Resin 作为 `kind=resin` 模板来源
- 多来源增删改查
- 显式绑定
- 租约 DELETE
- `{account}` 保留占位符和自定义账号格式
- proxy_url 写回
- 轮换与错误处理
- 管理页操作

### Phase 5：透明模式

- ModelRouter
- Grok 模型和 xAI auth model alias 识别
- 不可安全重放请求只执行一次
- 非推理请求完整缓冲并跳过 missing-thinking
- 插件 Executor
- 流式 hold / emit
- 多账号重试
- 后台分层选择索引和原子快照
- preferred last-success、session affinity 和 in-flight 跟踪
- round_robin / random / fill_first 层内策略
- fail closed
- HostCallbackID 防递归

### Phase 6：验收与加固

- 单元测试
- 集成测试
- 构建
- 安全审计
- 文档更新

## 18. 验收标准

实现必须满足：

1. 默认启动不会写 auth，不会禁用账号，不会自动绑定 Resin。
2. passive_guard 能正确区分 reasoning stub 和真实 thinking。
3. 所有质量异常分类与默认阈值和本文一致。
4. 自动处置只操作 guard-owned auth。
5. 成功请求不清除 missing-thinking strike。
6. 冷却到期探测成功会解除冷却并保留 strike。
7. 冷却到期探测失败会永久禁用。
8. Resin 绑定和轮换保留 auth 中所有 token 字段。
9. 删除租约后不查询 IP，不承诺换 IP。
10. 透明模式仅在首个 emit 前换号。
11. 所有候选耗尽返回 503 quality_degraded。
12. 管理页和 API 不泄漏任何 secret。
13. Resin 来源以 kind=resin 模板保存，可以并存多个；API 模式只有在 Admin API、Admin Token、平台 ID 和 Proxy Token 全部验证通过后才会保存，本地轮换模式无需这些字段。
14. 插件注册和热重载不因账号扫描阻塞。
15. 文档中的路径统一为 `/dashboard`、`/v0/resource/plugins/grok-quality-guard/dashboard`、`/plugins/grok-quality-guard/api` 和 `/v0/management/plugins/grok-quality-guard/api`。
16. 透明模式下 10 个以上健康同层 auth 的并发首请求不会全部使用同一个 auth。
17. 顺序请求在 preferred 健康且空闲时优先复用同一 auth，busy 时不等待并转向其他 auth。
18. 同一稳定 session 在 auth 正常时保持同一 auth，跨 caller scope 不共享绑定。
19. 当前最高 Priority 层未耗尽时不使用低 Priority 层，当前层全部失败后才 fallback。
20. unknown -> healthy 后下一个请求能从后台索引看到新的 healthy 层。
21. 普通事件写入不得触发透明选择索引全量重建。

## 19. 明确默认决策

| 决策 | 默认值 |
| --- | --- |
| 插件 ID | `grok-quality-guard` |
| 资源页完整 URL | `/v0/resource/plugins/grok-quality-guard/dashboard` |
| Action API 完整 URL | `/v0/management/plugins/grok-quality-guard/api` |
| passive_guard | true |
| transparent_guard | false |
| auto_action | observe |
| 透明失败策略 | fail_closed |
| 最大尝试次数 | 6 |
| 层内选择策略 | round_robin |
| preferred last-success | true，仅健康且空闲时生效 |
| session affinity | true，TTL 1h，最多 10000 条 |
| 主动探测 | 仅恢复性 |
| Resin 配置入口 | 代理来源新增类型 `resin`，不使用全局 YAML 配置 |
| Resin 账号占位符 | `{account}` 仅取邮箱前缀，其他占位符按普通模板配置 |
| Resin 首次绑定 | 管理员显式操作 |
| Resin 轮换 | API 模式立即切换新代理并异步排队删除旧租约；本地模式只改写已有代理用户名，不查 IP |
| 自动恢复 | 仅 guard-owned 冷却账号 |
| OAuth/导入/导出 | 不支持 |
| auth 写入字段 | 仅 `disabled`、`proxy_url` |
| 状态文件版本 | 1 |
| 事件数量上限 | 1000 |

## 20. 参考文件

### grok2api

- [quality_retry.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/quality_retry.go)
- [quality_retry_scan.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/quality_retry_scan.go)
- [quality_probe.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/quality_probe.go)
- [degrade.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/domain/audit/degrade.go)
- [bootstrap.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/infra/qualityguard/bootstrap.go)
- [selector.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/selector.go)
- [selector_plan.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/selector_plan.go)
- [selector_session.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/selector_session.go)
- [selector_test.go](https://github.com/chenyme/grok2api/blob/main/backend/internal/application/gateway/selector_test.go)

### CLIProxyAPI v8

- [pluginabi/types.go](https://github.com/router-for-me/CLIProxyAPI/blob/main/sdk/pluginabi/types.go)
- [pluginapi/types.go](https://github.com/router-for-me/CLIProxyAPI/blob/main/sdk/pluginapi/types.go)
- [handlers_stream.go](https://github.com/router-for-me/CLIProxyAPI/blob/main/sdk/api/handlers/handlers_stream.go)
- [handlers_interceptors.go](https://github.com/router-for-me/CLIProxyAPI/blob/main/sdk/api/handlers/handlers_interceptors.go)
- [handlers_execution.go](https://github.com/router-for-me/CLIProxyAPI/blob/main/sdk/api/handlers/handlers_execution.go)
- [model_execution.go](https://github.com/router-for-me/CLIProxyAPI/blob/main/sdk/api/handlers/model_execution.go)
- [conductor_stream.go](https://github.com/router-for-me/CLIProxyAPI/blob/main/sdk/cliproxy/auth/conductor_stream.go)
- [conductor_execution.go](https://github.com/router-for-me/CLIProxyAPI/blob/main/sdk/cliproxy/auth/conductor_execution.go)
- [adapters_interceptors.go](https://github.com/router-for-me/CLIProxyAPI/blob/main/internal/pluginhost/adapters_interceptors.go)
- [auth_callbacks.go](https://github.com/router-for-me/CLIProxyAPI/blob/main/internal/pluginhost/auth_callbacks.go)
- [host_model_stream_callbacks.go](https://github.com/router-for-me/CLIProxyAPI/blob/main/internal/pluginhost/host_model_stream_callbacks.go)
- [management.go](https://github.com/router-for-me/CLIProxyAPI/blob/main/internal/pluginhost/management.go)
- [claude-web-search-router](https://github.com/router-for-me/CLIProxyAPI/tree/main/examples/plugin/claude-web-search-router)
- [host-model-callback](https://github.com/router-for-me/CLIProxyAPI/tree/main/examples/plugin/host-model-callback)
- [host-callback-auth-files](https://github.com/router-for-me/CLIProxyAPI/tree/main/examples/plugin/host-callback-auth-files)

### Resin

- [handler_lease.go](https://github.com/Resinat/Resin/blob/master/internal/api/handler_lease.go)
- [server.go](https://github.com/Resinat/Resin/blob/master/internal/api/server.go)
- [middleware.go](https://github.com/Resinat/Resin/blob/master/internal/api/middleware.go)
- [control_plane_leases.go](https://github.com/Resinat/Resin/blob/master/internal/service/control_plane_leases.go)
- [router.go](https://github.com/Resinat/Resin/blob/master/internal/routing/router.go)
- [DESIGN.md](https://github.com/Resinat/Resin/blob/master/DESIGN.md)
- [README.md](https://github.com/Resinat/Resin/blob/master/README.md)
