# Grok Quality Guard 插件设计

> 状态：已确认
> 目标运行时：CLIProxyAPI v8 插件 ABI
> 插件 ID：`grok-quality-guard`
> 版本：设计 v2
> 日期：2026-11-20

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

本设计采用“先观测、后处置、透明保护可选”的策略。默认不自动修改凭证，只有管理员显式开启自动处置后才写 `disabled`。运行模式由 `guard: off|passive|transparent` 选择，默认 `passive`；`transparent` 拦截所有可识别的 Grok / xAI OAuth 请求，重放资格只决定失败后是否允许换号重试。

### 1.2 目标

1. 管理 CPA 已有的 xAI / Grok auth。
2. 观测每个账号的真实质量证据，并持久化状态。
3. 将异常账号与 Resin 账号级租约绑定，而不是绑定到整个出口节点。
4. 提供管理页与 JSON API，支持探测、冷却、禁用、恢复、绑定和轮换。
5. 可选透明拦截：在首个客户端可见 chunk 发出前识别坏流并换号。
6. 所有候选耗尽时不把已确认异常的内容下发给客户端：默认 `on_exhausted: fail_unverified` 只放行从未被确认降智的最后一个缓冲响应，否则返回 503 `quality_degraded`。
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
4. `encrypted_content` 非空也不够，按输出 item 累加的密文字节（`encrypted_bytes`）必须达到密文下限：`max(min_encrypted_bytes, reasoning_tokens * encrypted_bytes_per_reasoning_token)`，默认 `max(256, reasoning_tokens * 2)`。
5. 密文长度按 trim 后的字符串字节长度计算，与 grok2api 当前实现一致。
6. visible token 只能从可见 content delta 推导，不能用 `usage.output - usage.reasoning` 代替。
7. 判断速度异常时，TPS 使用总 output tokens，其中包含 reasoning tokens。
8. 成功请求不会自动清除 missing-thinking strike；只有主动探测成功或管理员恢复才解除冷却，strike 本身仍保留。

### 2.2 TPS 公式

```text
tail = duration - firstToken
```

有推理账单（`reasoningTokens > 0`）但流中没有任何推理增量时，推理是隐藏的，在首 token 之前的静默等待中生成，尾段只覆盖之后的回答：

```text
generationWindow = duration
```

线上探测样本中 1000–2000 个隐藏推理 token 的尾段为 1.0–1.5 秒，按尾段计算为 1000–1500 tok/s 并被误判为 `hard_tps`；按完整时长计算约 66 tok/s。流式推理增量会把首 token 标记在推理开始处，尾段已覆盖推理，不做此修正。

其余情况下，当满足以下条件时，说明输出在等待之后一次性到达（工具调用参数、隐藏推理或中间链路缓冲），尾段只反映传输而非生成速度，必须修正：

```text
tail < firstToken
tail < 1000ms
```

此时：

```text
generationWindow = duration
```

否则 `generationWindow = tail`。最终：

```text
TPS = outputTokens * 1000 / generationWindow
```

如果 `outputTokens <= 0` 或 `generationWindow <= 0`，TPS 记为 0，不参与速度降智分类。输出少于 `min_passive_output_tokens`（默认 32）的样本仍记录 TPS，但不做 `soft_tps` / `hard_tps` 判定。

参考实现只在 `reasoningTokens > 0` 时做上述修正。无推理账单的突发输出因此按几十毫秒的尾段计算：Codex 一次 75 token 的工具调用在 2.13s 预填充后 50ms 内到达，会算出 1500 tok/s 并被误判为 `hard_tps`。修正后按完整时长计算约为 34 tok/s，真正不可能的速度（例如 2s 内到达 3000 token）仍会命中阈值。

`burst_guard` 的 `buffered_burst` 与 `insufficient_generation_window` 仍使用参考实现的到达窗口（仅推理账单触发修正），以保留用户显式开启的缓冲突发检测；客户端工具调用轮次不参与该到达窗口检测。

### 2.3 默认阈值

| 配置 | 默认值 | 用途 |
| --- | ---: | --- |
| `soft_tps` | 500 | soft TPS 告警阈值 |
| `hard_tps` | 1000 | hard TPS 异常阈值 |
| `min_generation_ms` | 1000 | buffered burst 的生成窗口下限 |
| `min_passive_output_tokens` | 32 | 被动速度审计最小输出 |
| `min_hold_output_tokens` | 8 | 透明流式 hold 最小可见输出 |
| `hold_timeout` | 30s | 首个可判定输出前的最长扣留时间，`5s..10m` |
| `attempt_timeout` | 60s | 单账号无新增生成输出的空闲计时，不小于 `hold_timeout + 10s`，最大 `10m` |
| `request_timeout` | 120s | 单次请求所有账号尝试共享的总预算，不小于 `attempt_timeout`，最大 `30m` |
| `hold_keepalive` | 5s | 扣留期间向客户端发送 SSE 保活注释的间隔，`1s..1m` |
| `hold_max_buffer_bytes` | 4 MiB | 扣留的最大缓冲，`1..64 MiB` |
| `max_attempts` | 4 | 透明模式总上游尝试次数，最大 8 |
| `on_exhausted` | `fail_unverified` | 所有候选耗尽后的行为：`fail_unverified` 或 `fail_closed` |
| `consecutive_soft` | 2 | 连续 soft TPS 次数阈值，`1..10` |
| `consecutive_errors` | 2 | 连续传输错误次数阈值，`1..10` |
| `consecutive_missing_thinking` | 2 | 连续缺少思考次数阈值，`1..10`；主动探测来源固定为 1 |
| `missing_thinking_cooldown` | 12h | missing-thinking 首次冷却 |
| `strike_decay_after` | 48h | missing-thinking strike 自动降级窗口，单位小时；0 关闭 |
| `strike_decay_min_healthy` | 10 | 窗口内至少积累的连续健康观测/探测次数 |
| `empty_stream_cooldown` | 15m | 空流/空闲流冷却 |
| `min_encrypted_bytes` | 256 | encrypted thinking 字节下限 |
| `encrypted_bytes_per_reasoning_token` | 2 | 每 reasoning token 的密文下限，按线上健康样本 2.44 至 8.67 字节/token 校准 |
| `burst_guard` | false | 短生成窗口和高 TPS 是否直接判为 burst，以及窗口不足是否单独分类 |
| `cross_verify` | `thinking` | 达到阈值后先用主动探测确认的异常类型：`off`、`soft`、`thinking`、`all` |
| `shadow_mode` | false | 透明模式只记录结论，不扣留响应、不处置账号 |
| `debug_logging` | false | 每次透明判定额外写入 `quality_debug` 诊断事件 |
| `non_reasoning_models` | 空 | 用户额外声明不产生推理的模型标识，提供方前缀会被忽略 |

缺失字段使用默认值；所有字段在加载时校验取值范围，越界值直接拒绝，不做静默截断。
字段规格集中在 `internal/config/schema.go` 的 `FieldSpecs`，同时驱动加载校验与 `GET /settings/schema`；设置页从该接口读取 `min` `max` `enum`，不在前端重复维护区间。`Min: 0` 与「无下界」由 `zeroIsLowerBound` 区分，避免可为零的字段漏校验负数。

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
| `probe_error` | 主动探测请求失败或流不完整 | 网络、超时、非 2xx、无终止事件；属于传输错误，账号转 `unreachable` |
| `proxy_error` | 代理层失败 | 代理连接、认证或 Resin 错误；属于传输错误，账号转 `unreachable` |
| `inconclusive` | 请求预算不足以完成判定 | 剩余预算小于最小校验窗口；不计入账号质量 |

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
11. 管理 ABI 只稳定传递 locale，宿主不保证把客户端取消传播到插件；管理请求必须在插件内设置时间预算，并在分块边界主动检查。

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

### 3.1 运行模式

运行模式由单一字段 `guard` 决定：

| 值 | 含义 |
| --- | --- |
| `off` | 只保留管理功能，不观测请求、不自动调度探测 |
| `passive` | 默认值，只做被动观测，不改变路由 |
| `transparent` | 接管 Grok 请求，同时包含 `passive` 的全部被动分析 |

#### guard: passive

默认模式。职责：

- 读取已有请求的 usage、响应和流 chunk
- 解析真实 thinking 证据
- 解析 TPS 和 dump 特征
- 记录账号质量状态
- 执行主动恢复探测
- 在管理员开启自动处置后写冷却/禁用状态

`guard: passive` 不改变正常请求的路由和响应。它无法阻止当前请求已经收到的坏响应，只能影响后续调度或凭证状态。

#### guard: transparent

需显式开启。职责：

- `ModelRouter` 判断请求是否属于 Grok / xAI 路由模型
- 路由到插件自有 `Executor`
- executor 固定 AuthID 和 ProxyURL 调用 `host.model.execute_stream`
- 在首个客户端可见 chunk 前完成质量门
- 可安全重放的异常流关闭上游流并换号；不可安全重放的请求只执行一次
- 所有候选耗尽时按 `on_exhausted` 处理，已确认降智的内容永不下发

透明模式覆盖全部可识别的 Grok / xAI 路由请求。禁用推理、非推理模型和不可安全重放请求都不得绕过质量门；它们只在 thinking 判定、dump 类判定和失败后的重试策略上有所区别。

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
       ├─ withhold + replayable: close upstream, next account (proxy rotation only as fallback)
       ├─ withhold + unsafe replay: return 503 quality_degraded
       ├─ budget below min verify window: stop as inconclusive
       └─ all exhausted: on_exhausted (fail_unverified / fail_closed)
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
  -> transport error: unreachable + 30s..10m backoff, no quality strike
```

## 4. 质量判定规范

### 4.1 适用范围

质量判定对象是 xAI / Grok OAuth 请求的响应。

- `guard: transparent` 时，所有可识别的 Grok 路由请求都进入透明门，不按 reasoning 能力或重放资格旁路。
- 内置或配置声明的非推理模型、显式关闭 reasoning 和未命中能力表的未知非 Grok 模型跳过 `missing_thinking` 与 dump 类；空流仍须拦截，速度和缓冲上限在终态继续记录。
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

`usage` 中的输出或 reasoning 账单不能替代真实思考证据，但短可见文本不能借输出下限绕过 `missing_thinking`

`ReasoningStarted`（reasoning item 或 timing stub）只延迟判定到可见输出出现之前。一旦可见输出已经真实流式到达（达到 `min_hold_output_tokens`，且可见跨度至少 40ms 或至少 2 个可见块）而仍无思考证据，`missing_thinking` 立即成立，不再等满 `hold_timeout`：等待并不会让证据出现得更早，只会让客户端在每次换号前空等一个完整 hold 窗口。尚未出现可见输出时仍保持 `wait`，因为模型可能还在思考。

hold 阶段只在新到达的帧改变了判定所依赖的信号（可见文本、推理开始/证据、工具调用、usage、终止）时才重新判定；`response.created`、空 item 公告、保活注释等控制帧只进入缓冲，不触发判定。一旦内容已经发给客户端，迟到的 hold 定时器不会再触发换号，避免把另一个账号的回答拼接到已开始的流上。

已有达到下限的密文证据（无明文 reasoning delta）时，以下任一情况立即放行，不再等待上游终止事件：可见文本持续流式且达到最小输出；客户端工具调用开始；扣留窗口到期且未命中任何 dump 判定。

推理能力判断以内置已知能力表和 Grok 4+ 版本回退为主，避免模型更新后质量门失效；`quality.non_reasoning_models` 优先于内置判断并可声明额外非推理模型；未知非 Grok 模型不进入思考闸门

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

含客户端工具调用的轮次（Responses 的 `function_call`、`custom_tool_call`、`local_shell_call`、`shell_call`、`apply_patch_call`、`computer_call`，Chat 的 `tool_calls` / `function_call`，Anthropic 的 `tool_use`）跳过 `buffered_burst`、`fake_encrypted_dump`、`fast_reasoning_ratio_dump`：这类轮次的可见文本只是工具调用前的简短说明，健康账号也会在推理结束后一次性下发说明和工具参数，可见窗口时序无法区分 dump。`cipher_drool` 不受影响。

思考证据要求保持不变：推理账单为 0、没有思考证据的工具调用仍判 `missing_thinking`。线上 Codex 会话里同一请求在健康账号上带有 245–294 个推理 token，而 0 推理的同样轮次在 9 分钟内连续消耗了多个账号的额度，这正是 `missing_thinking` 要拦住的 0-thinking 降智流。

宿主侧执行的 `web_search_call`、hosted MCP 和 Anthropic `server_tool_use` 之后的文本仍是答案，照常检测。

### 4.6 速度分类

后置审计或探测完成时：

```text
if burst_guard && !toolCall && arrivalWindow < min_generation_ms && outputTokens/arrivalWindow >= soft_tps:
    buffered_burst
else if outputTokens < min_passive_output_tokens:
    healthy (TPS recorded, not judged)
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

本插件的流式分析器按 grok2api 的 `ClassifyQualityHold`、`quality_retry_scan.go` 和 egress-quality-guard 的 `classify_result` / `classify_audit` 对齐，并保留持续可见流误杀保护：

1. 先解析协议内容，再按 `buffered_burst`、`cipher_drool`、`fake_encrypted_dump`、`fast_reasoning_ratio_dump` 的顺序执行 dump 判定。
2. dump 判定必须先于 thinking 放行，避免伪造 reasoning 账单绕过。
3. 明文 reasoning delta 是思考证据；仅有 `usage.reasoning_tokens`、空 reasoning item、timing stub 或未达下限的密文都不是。
4. `hold_expired` 后的短 visible 文本只有在 `reasoning_tokens >= 80` 时按 burst 处理；密文达到下限本身不满足该分支。
5. `: grok2api-reasoning-evidence <bytes>` 只提供密文字节长度，仍需经过 `max(min_encrypted_bytes, reasoning_tokens * encrypted_bytes_per_reasoning_token)` 下限校验。
6. 聚合 message 文本会计入 visible token，但不会伪造流式首个可见块的 flush 时间。
7. Chat、Responses、Anthropic Messages 的流式与非流式响应共享内容、thinking、密文下限和空流规则；TPS 仅适用于具有可观测生成窗口的流式样本。
8. 持续可见流保护只在可见文本持续至少 2 秒且达到最小输出后生效，不能绕过 2 秒内的硬 dump 检测。

判定路径存在明确差异：

| 路径 | 判定范围 | 不会推断的内容 |
| --- | --- | --- |
| 被动审计 | 仅 streaming、已有 TTFT、有效输出或推理账单 >= 32、生成窗口有效的样本；已知要求推理时检查 missing-thinking | 不推断显式关闭推理、配置声明的非推理模型或未知非 Grok 模型 |
| 主动探测 | marker、输出下限、thinking 要求、生成窗口、TPS，并复用 `ClassifyQualityHold` 的 dump 判定 | 不把传输错误或上游非 2xx 当成质量降智；成功探测保留 strike |
| 非流式质量门 | 空响应、缺少真实思考、密文下限、语义输出 | 不计算 TPS，不做流式 burst/dump 推断 |

`ignored` 表示本次样本不是质量信号。它只更新最近尝试，不增加质量 strike，也不覆盖上一次真实质量指标。`probe_error` / `proxy_error` 是传输错误，会更新最近尝试并让账号进入 `unreachable` 退避，但同样不增加质量 strike，并保留上一次质量证据。

### 4.10 账号详情证据

账号详情抽屉只展示脱敏后的判定统计，不展示 prompt、响应正文、access token、refresh token 或代理凭据。证据包括：

- 判定来源、结果、说明、协议、流式/非流式、终止和 hold 超时状态
- visible/output/reasoning token、密文字节与密文下限
- TTFT、总时长、生成窗口、visible flush、visible span、visible chunks 和 TPS
- has thinking、明文 reasoning delta、reasoning started、semantic output、usage reported
- chunk、byte、event、unknown event 和 parse failure 计数

“最近尝试”与“最近质量判定”分开保存。这样即使最近一次是 `ignored` 或探测错误，也能看到上一次真实质量结论及其指标，而不是把所有指标误显示为 0。

管理 API 把两者合并为每个账号摘要上的单一 `verdict` 对象 `{category, code, decision, source, at, detail, evidence}`，页面只渲染一次，不在尝试行、判定行和证据区重复同一分类：

| 字段 | 取值 |
| --- | --- |
| `category` | `quality`（质量降智）、`transport`（`probe_error` / `proxy_error`）、`account`（`credential_invalid`、`quota_exhausted`、`rate_limited`）、`inconclusive`、`ignored` |
| `decision` | `deliver`、`retry`、`quarantine`、`cooldown`、`disable`、`observe`，表示插件对该观测实际采取的唯一动作 |
| `evidence` | 协议、流式、思考证据、`encrypted_bytes` 与密文下限、各类 token、TTFT、生成窗口、TPS、hold 到期、strike 与阈值 |

事件同样携带 `category`、`code`、`decision`；事件日志按 `category` 筛选。传输与账号类错误永不计入质量 strike。

### 4.11 复核后补充契约

- 主动探测与透明 hold 门共用 dump 判定，探测成功只解除冷却，不清除 `strike_missing_thinking`。
- `rate_limited` 是账号调度信号，不是质量失败：状态置 `suspect`，只写探测退避，不写 auth，不清零连续计数。
- `model_prefixes` 采用严格前缀匹配，不使用 `grok` 子串兜底；不以配置前缀开头的别名只能通过 `host.auth.models` 缓存识别。
- 被动拦截入口也使用 auth 模型缓存识别别名，不因严格前缀匹配丢失已发现模型。
- 非流式响应没有可观测生成窗口，速度维度整体跳过；非推理和显式关闭推理请求跳过 missing-thinking 与 dump 类，空流仍须拦截，流式速度和缓冲上限在终态继续记录。





## 5. 状态机与惩罚策略

### 5.1 状态

| 状态 | 含义 | 是否可调度 |
| --- | --- | --- |
| `unknown` | 尚未观测或探测 | 是 |
| `healthy` | 最近主动探测通过 | 是 |
| `suspect` | 有一次弱异常、探测错误或证据不完整 | 是，但降低优先级 |
| `degraded` | 已确认质量异常，但未自动处置 | 是，仅 observe 模式 |
| `unreachable` | 网络、代理或探测传输错误，处于短退避 | 否，退避到期后重新探测 |
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
  ├─ transport error -> unreachable
  └─ confirmed anomaly -> degraded

unknown / healthy / suspect / degraded
  └─ transport error -> unreachable + backoff (30s doubling, max 10m)

unreachable
  ├─ backoff not expired -> unreachable
  ├─ probe pass -> healthy
  ├─ transport error again -> unreachable, backoff doubles
  ├─ managed proxy rotated -> unknown
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
6. 恢复探测网络错误：重新进入 cooldown + backoff，不写永久禁用，不计入质量 strike。

`auto_action=cooldown_then_disable`：

1. 第一次确认 `missing_thinking`：auth 写 `disabled=true`，state 写 `disable_kind=cooldown`、`cooldown_until=now+12h`，状态 `cooling`。
2. 冷却期内再次观测到异常：保持当前冷却，不重复延长或立即禁用。
3. 冷却到期：只有在 state 仍记录该账号由 guard 控制且未被管理员手动改动时，临时恢复。
4. 恢复探测成功：恢复 `disabled=false`，清除 cooldown，状态 `healthy`，保留 `strike_missing_thinking=true`。
5. 恢复探测再次确认 `missing_thinking`：auth 写 `disabled=true`，state 写 `disable_kind=permanent`，状态 `disabled_by_guard`。
6. 恢复探测网络错误：重新写回 cooldown，增加 backoff，不直接永久禁用，不计入质量 strike。

上游账号状态错误独立于质量 strike：`credential_invalid` 直接禁用并等待凭据更新；`quota_exhausted` 使用固定 24 小时冷却，因为 Grok 免费额度按滚动 24 小时窗口恢复。这两类处置不受 `auto_action: observe` 限制，避免失效凭据或耗尽额度继续参与路由。

传输错误语义（`probe_error` / `proxy_error`）：

- 不是质量信号，不增加质量 strike，不进入质量冷却，不写 `disabled=true`，也不受 `auto_action` 影响
- 未处于隔离状态的账号转为 `unreachable`，`probe_backoff_until` 从 30s 起，账号持续不可达时翻倍，最长 10m
- 退避到期后由探测调度器重新探测；健康探测直接恢复为 `healthy`
- 已处于 `cooling` 等隔离状态的账号保持原状态，只更新退避
- 达到 `consecutive_errors` 且绑定受管代理时轮换代理；轮换成功清除连续错误并回到 `unknown`，轮换失败只记录事件，不升级为质量冷却

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

1. 插件配置 `guard: transparent`
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

重放资格只控制失败后的失败恢复：可安全重放请求受 `max_attempts` 限制并允许换号/换代理；不可证明可安全重放时仍执行一次透明质量门，失败后不换号重试，按 `on_exhausted` 收尾。尚未拿到响应前的传输错误例外，不可重放请求也可换号。

### 6.3 候选账号

候选来源：

- `host.auth.list`
- `host.auth.get` 补全的物理 auth
- 插件状态中的冷却和禁用信息

过滤：

- provider/type 必须为 xAI/Grok
- 只允许 `healthy` / `unknown`
- 跳过 `suspect`、`degraded`、`unreachable`、`cooling` 及其他隔离状态
- 跳过 `disabled_by_guard`
- 跳过 `externally_disabled`
- 跳过 runtime unavailable
- 跳过没有有效 access token 的账号

主动探测标记只用于识别没有请求级 pending observation 的内部 probe usage，不得按 AuthID 过滤透明候选，也不得丢弃同一窗口内真实请求的 usage。

分层：

1. `healthy`（rank 0）整体优先于 `unknown`（rank 1）
2. 同一质量状态下，Auth Priority 数值越大越优先
3. 每个 `(quality, priority)` 组合是独立选择层，层内按 AuthID 稳定排序
4. 存在 healthy 层时，单次请求最多尝试 1 个 `unknown` 账号；全部为 `unknown` 时不限制
5. 当前层未耗尽时不进入下一层
6. 当前层全部失败后才 fallback 到下一层

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

    if no new generated output for attempt_timeout:
        close upstream stream
        next candidate

    if buffer > hold_max_buffer_bytes:
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
- 扣留期间每隔 `hold_keepalive` 向客户端发送 `: quality-hold` SSE 注释，不含内容也不结束扣留；开始 emit 后停止
- 缓冲超过 `hold_max_buffer_bytes`（默认 4 MiB）时放行已缓冲内容并记录 `transparent_overflow` 事件，不对该样本应用健康/异常判定，也不清零 strike
- 非推理请求在首个可见块后约 200ms 提前放行；空流仍会拦截，TPS 速度和缓冲上限在终态继续记录
- 判定记账在后台执行：失败尝试的 `transparent_attempt` 事件、账号判定（含交叉验证调度）以及成功样本的健康判定都不阻塞请求路径。每次判定都会完整落盘状态文件并获取账号锁，而正在运行的探测会在整个探测期间持有该锁，同步执行会让每次换号额外等待数秒到一次完整探测的时长，客户端在此期间收不到任何内容。成功响应先关闭客户端流再记账；插件停机时记账改为同步执行，保证在最终落盘前完成
- 判定只决定当前请求是否换号；同一请求内已失败的账号由选择器的 `MarkFailure` 立即排除，因此不依赖记账完成

#### 6.5.1 超时与请求预算

透明流使用三级超时，加载时强制 `hold_timeout + 10s <= attempt_timeout <= request_timeout`：

| 配置 | 默认值 | 作用 |
| --- | ---: | --- |
| `hold_timeout` | 30s | 单次扣留的上限：出现首个可判定输出前最多缓冲多久 |
| `attempt_timeout` | 60s | 单账号空闲计时：打开流或读取期间超过该时间没有新增生成输出即终止当前尝试并换号 |
| `request_timeout` | 120s | 单次请求的总预算，由该请求的所有账号尝试共享 |

规则：

- 每次尝试的扣留上限取 `hold_timeout` 与剩余请求预算的较小值
- 最小校验窗口为 `hold_timeout` 的一半，最多 3s；剩余预算小于该窗口时不再发起新尝试，当前尝试以 `inconclusive`（`budget_exhausted`）结束
- 扣留因预算被截短时，到期的 `missing_thinking` 改记为 `inconclusive`，不计入账号质量，避免一池降智账号把健康账号拖成误判
- `attempt_timeout` 是空闲计时，不缩短扣留；`request_timeout` 防止多个降智账号累计把客户端拖过自身超时

`shadow_mode=true` 时质量门只记录本应采取的结论（`quality_shadow` 事件），不扣留响应、不换号、不处置账号。
`debug_logging=true` 时每次透明判定额外写入 `quality_debug` 事件，带原因码与可见/输出/推理 Token、密文与下限、生成窗口、首 Token、TPS 和扣留是否到期，用于复核误判；默认关闭，避免事件量翻倍。

### 6.6 非流式请求

非流式请求可以完整缓冲响应后再分类：

- 通过：返回原始响应
- 异常：记录并尝试下一个账号
- 所有候选异常：返回 503 `quality_degraded`

非流式请求不涉及“首个 emit 前”的限制。

### 6.7 耗尽行为

所有尝试都异常、预算耗尽或没有候选时，按 `on_exhausted` 处理：

- `fail_unverified`（默认）：最后一次流式尝试的缓冲从未被确认降智（例如预算不足以 `inconclusive` 结束）时放行该缓冲；已确认降智的响应永不放行
- `fail_closed`：始终关闭为 503

不放行时：

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

- 不返回任何已确认异常的响应
- 不伪造 402 或 429
- 不把异常内容以 200 发出
- 不泄露账号、代理或上游响应细节

### 6.8 Resin 轮换与当前请求

仅对 `safeReplayRequest=true` 的请求执行换号或轮换。不可安全重放请求只执行一次，不进入轮换或换号。

`missing_thinking` 按账号级质量证据处理：

1. 记录异常并把该账号标为 `suspect` 或 `degraded`
2. 如果还有其他透明候选，立即切换账号，不在当前账号上做代理轮换
3. 只有单账号池才使用代理轮换作为兜底，同一账号最多轮换一次
4. 轮换后仍异常则按 `on_exhausted` 收尾；后续请求不会再次选择该账号
5. 总上游尝试次数仍受 `max_attempts`（默认 4）和 `request_timeout` 预算限制

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
- `unreachable`
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
- 请求级 `max_attempts` 与 `request_timeout` 仍是硬上限，达到后立即停止
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
- `healthy -> unreachable`：退出选择索引，健康探测后重新进入
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
4. 只要 A 仍为 `healthy`、属于当前最高层且当前没有进行中的请求，就继续使用 A；EWMA 评分导致桶变化不影响该判断
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
- 超过 `routing.affinity.max_entries` 时淘汰最旧绑定
- TTL 过期和超限淘汰使用按到期时间、绑定时间维护的最小堆；堆项惰性校验并定期压缩，避免每次请求全表扫描

没有稳定 session signal 时不创建绑定，请求仍享受分层、preferred 和并发分散。

#### 6.10.8 In-flight、评分桶与桶内策略

插件维护进程内：

```text
activeAttempts map[authID]int
```

计数时机：

- 在真正调用 host model execution 前占用
- 非流式、缓冲流、实时流结束或失败后释放
- auth 解析失败、执行前失败、Resin 轮换后失败和所有提前返回路径都必须释放
- 使用 `attempt.Release()` 统一释放，禁止在 selection lock 内调用上游网络
- 每次创建选择时清理超过 10 分钟仍未释放的占用，避免异常路径永久占住账号

健康账号按每个 `(质量, Priority)` 层内的 EWMA 分数降序排列，再按 `routing.scoring.max_accounts_per_bucket` 切分为不可变桶；`unknown` 不计算评分，按 Auth ID 排序后使用相同容量分桶。桶数由账号数动态计算，不写死。评分综合：

- 成功率
- 首 Token 耗时
- 输出 TPS
- 主动探测、透明请求、被动观测三类来源证据

半衰期统一由 `routing.scoring.half_life_hours` 配置，默认 168，允许 `1..8760`，所有证据来源共用。来源区别由证据权重表达，而不是使用互相不可比的衰减时间。

普通策略先在当前评分桶内执行 idle pass，再依次进入较低评分桶；本层所有桶的 idle pass 都没有候选后，才执行 busy pass：

1. idle pass：按评分桶顺序优先尝试未被占用且未尝试的 auth
2. busy pass：只有本层所有评分桶的 idle pass 都没有候选时，才允许按同一策略使用正在被其他请求占用的 auth

这避免了 preferred 正在使用时，普通 round-robin 又立刻选中同一个 auth，同时不会因为所有 auth 都忙而直接返回无候选。

桶内策略：

`round_robin`

- 每个评分桶维护独立游标
- 每个新请求从游标当前位置开始
- 游标在选号时原子推进，不等待网络请求完成
- session 或 preferred 命中不推进普通游标
- 回退顺序按桶内环形顺序遍历，不重复尝试

`random`

- 每次请求在当前桶随机选择一个起始下标
- 从该下标开始环形遍历
- 不生成完整随机排列
- 不使用有放回随机

`fill_first`

- 当前桶始终从排序后的第一个 auth 开始
- 如果第一个 auth 忙，idle pass 会继续寻找空闲 auth
- 当前桶全部不可用后才进入下一桶，本层全部不可用后才进入下一层
- 适合明确希望集中消耗账号的场景，不作为默认值

默认：

```yaml
routing:
  strategy:
    selection: round_robin
    prefer_last_success: true
  affinity:
    enabled: true
    ttl: 1h
    max_entries: 10000
  scoring:
    enabled: true
    max_accounts_per_bucket: 512
    half_life_hours: 168
    weights:
      success: 0.50
      ttft: 0.30
      tps: 0.20
    evidence:
      probe: 0.65
      transparent: 0.35
      passive: 0.0
    refresh:
      min_interval: 5m
      max_age: 30m
      drift_threshold: 0.03
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
      continue with scored buckets in this layer

  if preferred auth exists and this is the current top layer:
    if preferred is healthy and TryReservePreferred succeeds:
      try preferred even when it is in a lower score bucket
      if success: refresh preferred, return
      if hard account failure: clear preferred
      continue to next candidate in this layer
    else if preferred is healthy but busy:
      keep preferred
      skip it in idle pass

  for each bucket in layer.Buckets (score descending):
    start := strategyStart(bucket)
    for idle candidate in bucket:
      if excluded, tried, ineligible or busy: continue
      reserve idle attempt
      resolve auth
      if session binding was created by a concurrent request: switch to bound auth
      try candidate
      if success: bind/refresh session and update preferred if needed
      if retryable with Resin rotation: retry same candidate once
      mark excluded

  if no bucket had an idle candidate:
    for each bucket in layer.Buckets:
      for busy candidate in bucket:
        same strategy order, reserve shared attempt, try once

  release attempt
end for
return quality_degraded
```

请求不会复制完整 catalog，也不会在请求路径对账号重新打分或排序。索引重建为 `O(n log n)`；选择只遍历当前桶，并在桶内候选耗尽时继续向后。

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

非账号质量失败包括：

- host 暂时无容量或 `model_cooldown`（`ignored`）
- `rate_limited`
- `probe_error` / `proxy_error`（网络超时、TLS handshake timeout、SOCKS 代理失败等）

处理：

- `ignored` / `rate_limited` 可以触发当前请求换号，但不替换全局 preferred，也不在 `MarkFailure` 阶段主动删除 session 绑定
- `probe_error` / `proxy_error` 视为传输级硬失败：只要尚未拿到上游响应，即使请求体不可安全重放也换到下一账号；同时清除该 auth 的当前 session 绑定和匹配的全局 preferred，避免后续请求继续命中不可达连接
- 这些传输失败不是账号质量 strike；账号转为 `unreachable` 并按 30s..10m 退避重新探测，不进入质量冷却

Resin 轮换语义保持不变：

- 仅对可安全重放请求执行
- 同一 candidate 最多轮换一次
- 轮换后重新解析 auth，确保使用新的 `proxy_url`
- `missing_thinking` 存在其他候选时优先换号，不先轮换代理
- `proxy_error` / `probe_error` 达到 `consecutive_errors` 时，存在其他候选则先换号并异步轮换失败账号的代理
- 没有其他候选且连续传输错误达到 `proxy.rotate_after_errors`（默认 2，最大 10）时才同步轮换代理并重试同一 auth
- 单账号池才使用代理轮换作为兜底
- 轮换次数计入 `max_attempts`
- auth 未绑定插件管理的代理时跳过轮换；插件不依赖代理，没有任何代理来源、节点或绑定时，探测、透明保护和换号都走直连

#### 6.10.11 配置与热更新

新增运行时策略：

```yaml
routing:
  strategy:
    selection: round_robin
    prefer_last_success: true
  affinity:
    enabled: true
    ttl: 1h
    max_entries: 10000
  scoring:
    enabled: true
    max_accounts_per_bucket: 512
    half_life_hours: 168
    weights:
      success: 0.50
      ttft: 0.30
      tps: 0.20
    evidence:
      probe: 0.65
      transparent: 0.35
      passive: 0.0
    refresh:
      min_interval: 5m
      max_age: 30m
      drift_threshold: 0.03
```

校验：

- `routing.strategy.selection` 只能是 `round_robin`、`random`、`fill_first`
- `routing.affinity.ttl > 0` 且不超过 `168h`
- `routing.affinity.max_entries > 0` 且不超过 `100000`
- `routing.scoring.max_accounts_per_bucket` 为 `16..4096` 的整数
- `routing.scoring.half_life_hours` 为 `1..8760` 的整数
- 指标权重与证据权重均为 `0..1` 的有限数，且各自至少一项大于 `0`
- `routing.scoring.refresh.min_interval >= 10s`
- `routing.scoring.refresh.max_age` 位于 `min_interval..7d`
- `routing.scoring.refresh.drift_threshold` 位于 `0..1`
- 缺失字段使用默认值
- 不兼容旧的扁平 `routing` 字段；配置必须使用新的分层结构
- 管理页保存策略后必须同步更新 `p.policy.Routing` 和 `p.cfg.Routing`

每个账号的紧凑 `routing_stats` 随账号状态持久化；原始样本、preferred、session binding、桶游标和 in-flight 计数不写入 state.json。评分索引只由请求触发刷新：无流量时不空转，有流量时根据请求速率、评分漂移和最大陈旧时间在后台重建。

#### 6.10.12 并发安全要求

必须保证：

- `routingSelectionSnapshot` 发布后不可变
- 请求只读取加载到本次请求的原子快照
- 索引更新使用 copy-on-write，不修改旧快照
- preferred 的空闲检查和占用是原子的
- round-robin 游标推进是原子的
- session binding 的 `GetOrBind` 是原子的
- activeAttempts 的增减是原子的
- 不同评分桶游标互不影响
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
- EWMA 半衰期由 `routing.scoring.half_life_hours` 统一控制
- 新鲜证据提高评分，陈旧证据向中性分衰减
- 评分桶按容量动态切分，`unknown` 也遵守同一容量上限
- 累计评分漂移或最大索引年龄触发后台重建
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
- `suspect` / `degraded`：`probe.suspect_backoff`（默认 2m）后探测一次，失败 backoff 翻倍，最大 `probe.max_backoff`（默认 1h）
- `unreachable`：传输退避（30s 起翻倍，最长 10m）到期后探测一次
- `cooling`：冷却到期后探测一次
- `healthy`：`probe.healthy_interval_hours=0`（默认）时没有周期探测；大于 `0` 时，距上次主动探测达到该小时数后到期
- 管理员可随时手动触发 force probe

并发：

- 扫描阶段一次性过滤出到期账号，再放入有界队列；健康账号仅在 `probe.healthy_interval_hours` 启用且间隔已到或尚无 `last_probe_at` 时参与周期探测
- 队列容量由 `probe.queue_size` 控制，默认 10000，最大 1000000
- 单类探测或批量任务内部只启动与 `probe.concurrency`（默认 2）相同数量的 worker
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
- `missing_thinking`：探测本身就是确认步骤，阈值固定为 1；冷却后再次确认 -> `disabled_by_guard`
- 空流：15m 冷却
- probe 网络/超时/流不完整：`probe_error`，账号转 `unreachable` + 传输退避，不计入质量 strike
- 代理错误：`proxy_error`，同样转 `unreachable`；达到 `consecutive_errors` 且绑定受管代理时同步轮换代理，不进入质量冷却
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
- `账号`：搜索、状态筛选和分页、脱敏账号信息、统一 `verdict` 判定、真实思考证据、TPS/TTFT/output/reasoning/encrypted 指标、代理绑定、冷却和探测退避；详情抽屉提供脱敏判定依据，以及主动探测、冷却、恢复、禁用、绑定和轮换
- `代理管理`：内部按 `账号绑定` 和 `代理来源` 两个任务子视图组织。账号绑定子视图展示每个 Grok 账号、脱敏代理、代理账号和轮换代数，支持搜索、状态筛选、分页、单账号绑定/轮换/清除，以及批量绑定/清除。代理来源子视图统一展示节点、模板和 Resin，新增类型下拉同时包含三种来源。
- `任务中心`：统一展示账号、代理、Resin 清理和质量交叉验证等异步任务，包含进度、总数、处理中、等待、成功、失败、跳过、来源、时间和逐项结果；等待中或执行中的任务在状态旁提供取消操作，已结束任务在状态旁提供永久删除操作，删除会同时移除索引、归档和逐项明细。常驻队列只显示实际启用或仍有工作的队列。主动探测调度器以“质量检测队列”展示实时等待、处理中、成功和失败数量，自动探测不逐账号创建任务历史。历史任务列表从紧凑索引按页读取，逐项结果仅在打开任务时按需解压加载。`#tasks/<task-id>` 可直接打开任务详情
- `事件日志`：按关键词和判定分类（`quality`、`transport`、`account`、`inconclusive`、`ignored`）及代理、任务事件筛选并分页展示时间、auth ID、分类、代码、处置、动作、结果和脱敏详情
- `设置`：低频配置集中在 `守护策略`，按“运行模式与处置 / 透明鉴权选择与会话亲和 / 健康账号评分 / 质量阈值与思考证据 / 透明保护 / 主动探测 / 任务中心与历史记录”分组，每个字段通过 i 图标提示说明含义

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

状态码：200 成功、202 已接受异步任务、400 参数错误或 Resin 验证失败、403 动作不允许、404 auth/事件不存在、409 状态冲突、429 操作过于频繁、500 插件内部错误、502 Resin Admin API/代理不可达、503 CPA host 回调、上游不可用、目录未就绪或管理服务繁忙。所有可预期的管理 API 错误都必须返回 JSON envelope，不能让业务错误以非 JSON 502 形式出现。503 的 `catalog_loading` 表示账号目录仍在加载，`busy` 表示重动作超过并发闸门或时间预算；这两类错误携带 `Retry-After: 1`，客户端按退避重试。异步任务的最终失败通过 `tasks.get` 的 `error` 和 `error_status` 返回。任务状态包括 `queued`、`running`、`succeeded`、`failed`、`cancelled` 和重启后恢复的 `interrupted`。

### 10.4 Action 列表

| Action | 参数 | 说明 |
| --- | --- | --- |
| `overview` | 无 | 汇总状态和计数 |
| `accounts.list` | `page`、`page_size`、`state`、`query`、`summary`、`ids_only`、`all_ids` | 分页账号列表，每个账号附带 `verdict`；`ids_only` 返回当前筛选页的 ID，`ids_only + all_ids` 在单次扫描中返回全部命中 ID |
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
| `proxy.bindings.list` | 分页、筛选参数、`ids_only` | 账号绑定、external、drifted、unknown 筛选；`ids_only` 返回全部命中 ID |
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
| `events.list` | `limit`、`cursor`、`auth_id` | 事件列表；每条事件带 `category`、`code`、`decision` |
| `tasks.center.list` | `page`、`page_size`、`category`、`status`、`query` | 查询任务中心的任务索引；常驻队列使用独立的 `tasks.center.queues` |
| `tasks.center.queues` | 无 | 读取常驻队列的计数摘要 |
| `tasks.center.queue` | `queue_id`、`page`、`page_size` | 读取质量检测、自动绑定或代理健康检查队列的执行中与等待项 |
| `tasks.center.get` | `task_id`、`page`、`page_size`、`item_status` | 按页读取逐项结果；历史任务从归档懒加载 |
| `tasks.center.cancel` | `task_id` | 请求取消等待中或执行中的任务 |
| `tasks.center.delete` | `task_id` | 永久删除已结束任务的历史索引、归档和逐项明细 |
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

### 10.6 管理页性能、缓存与可靠性契约

本节约束账号、代理绑定、代理来源、任务中心和常驻队列等管理读取路径。目标是让交互请求的响应时间、峰值临时内存和失败方式具有上界，同时不引入随查询组合或使用时间增长的常驻内存。

#### 10.6.1 列表读取与内存边界

- 交互式列表的临时分配必须与 `page_size`、块大小同阶，不得与账号总数、代理来源总数、历史条目数或队列长度同阶。全量计数可以扫描全部数据，但不得为全量数据构造完整行视图、状态 map、下标切片或第二份排序结果。
- 账号目录发布时生成按账号 ID 升序的 `[]int32` 下标索引；`accounts.list` 沿该索引按 1024 个账号分块批量读取状态，只物化当前页，或 `ids_only` 所需的结果。不得在每次请求中复制整张状态 map、收集全表下标或全表排序。
- ID 序索引是目录的派生物，不写入 `state.json`、不升级 schema，可在目录刷新时重建；允许的常驻成本约为每账号 4 字节。不得再维护与状态桶并行的多份完整索引，除非基准证明它替代了全量扫描而不是增加常驻复杂度。
- `state_counts` 始终按全量账号计算，不受页码、状态筛选和查询条件影响。只有计数来自同一版本且完整可用时才允许提前结束扫描，否则必须完成计数扫描。
- 账号查询保持大小写不敏感的子串匹配，范围为显示名（邮箱、标签、ID 取第一个非空）、ID、标签和邮箱。ASCII 查询必须走零分配的大小写折叠比较；只有非 ASCII 输入才允许构造小规模小写字符串。查询首尾空白按既有规则裁剪。
- 列表排序和分页必须稳定：账号列表按账号 ID 升序；代理绑定和代理来源继续使用各自既有排序。空结果返回 `items: []`，不得返回 `null`。
- `ids_only + all_ids` 是显式批量选择的接口：服务端在同一趟流式扫描中收集全部命中 ID，调用方不得通过提高 `page_size` 或循环分页模拟全量选择。批量结果仍须携带完整 `total` 和筛选相关计数。
- 账号列表、代理摘要、代理绑定列表、代理来源列表和任务中心属于重管理动作，必须使用统一的并发闸门和时间预算。分块循环在每个块边界检查预算；不得依赖客户端取消信号终止已经进入同步 Host 或磁盘调用的工作。

#### 10.6.2 缓存边界

- 不缓存按“页码 + 页码大小 + 筛选条件 + 查询词 + 标志位”组合的整表列表响应。该键空间无界，常驻内存会随管理操作轨迹增长，并且 TTL 窗口内可能展示已经失效的状态或归因。
- 浏览器侧可以保留短时视图缓存，以减少同一 Tab 内的重复请求；服务端只缓存不承担账号归因的纯聚合结果。列表正确性的主要手段是版本化索引和分块扫描，不是响应缓存。
- 聚合缓存每个端点只保留一个条目，键至少包含目录 revision、状态 revision、账号数或绑定 revision 等必要版本；任一开始计算后版本发生变化，本次结果不得写入缓存。TTL 必须短，作为漏更新兜底而不是主要失效机制。
- `overview` 使用单条目、版本键控、默认 5 秒 TTL 的计数缓存；失败结果不得写入。`proxy.summary` 复用代理绑定计数的版本键控缓存，同样只在扫描期间 binding revision 未变化时回填。
- `tasks.center.queues` 只读取队列长度和整数聚合，不复制队列明细；队列深度读取为常数级，变更和清理摘要为线性整数累加但临时内存为常数级，因此不增加响应缓存。`tasks.center.list` 不得重复打包常驻队列摘要。
- 并发相同的重请求使用 single-flight 合并：键由 action、locale 和规范化参数组成，只共享正在执行的计算。结果完成后立即释放，不保留响应；失败和超时结果也不得转为缓存条目。
- 如果未来把聚合 TTL 暴露为配置，只允许设置短且有上限的秒数，不能把缓存键改为查询参数，也不能用 TTL 取代 revision 失效。未明确配置前，TTL 是实现内常量而不是用户可调参数。

#### 10.6.3 时间预算、错误与重试

- 管理 ABI 不保证传播客户端取消，因此插件内部必须设置时间预算。冷账号目录最多等待 2 秒；超时返回 HTTP 503、JSON 错误码 `catalog_loading` 和 `Retry-After: 1`。
- 重管理动作超过并发闸门或 2 秒预算时返回 HTTP 503、JSON 错误码 `busy` 和 `Retry-After: 1`。默认并发闸门为 4；阈值可以按现场负载调整，但必须有界，不能让慢请求无限排队并叠加内存。
- 所有可预期的业务错误必须返回 JSON envelope。前端收到非 JSON 的 502 时，应优先判断为宿主处理器 panic、宿主错误页或反向代理超时，不得把它当作插件常规业务错误或通过改文案掩盖。
- 前端只对 `catalog_loading` 和 `busy` 自动退避重试，默认最多 2 次；其他 4xx 不自动重试。重试耗尽后显示可操作的错误提示，不覆盖已有数据。
- 加载失败时必须保留旧表格、计数和滚动位置。被取消或被新请求取代的旧请求不得弹出错误 toast，也不得覆盖较新的响应。
- 冷目录失败、busy 失败、空结果和部分元数据不可读必须是不同状态；不得把读取失败当作 `unknown`、空列表或永久无数据。

#### 10.6.4 前端交互

- 搜索框和筛选下拉统一使用 200ms 防抖；同一类列表请求使用 `AbortController`，后发出的请求胜出，并用请求序号防止旧响应回写。
- 翻页、筛选或搜索变化时只刷新受影响的列表，不重建无关 Tab；强制刷新和普通轮询区分优先级，不能把多个慢请求串行叠成体感延迟。
- 账号全选使用服务端 `ids_only + all_ids`，账号绑定全选使用 `ids_only`；服务端负责一次完整命中扫描，前端只把 ID 加入选择集合。
- 加载中不得清空表格；错误提示放在表格上方，保留当前页数据和滚动锚点。筛选切换后若新结果为空，应显示空结果而不是上一筛选的残留行。

#### 10.6.5 队列、任务历史与响应本地化

- 常驻队列摘要只读取 active、waiting 和是否支持该队列等计数，不得复制、排序或序列化完整等待队列。常驻队列由独立 `tasks.center.queues` 提供，`tasks.center.list` 不重复返回同一批摘要。
- 任务中心历史每次列表请求只加载一次；清理机会合并进同一次加载，不得先加载一次做清理、再加载一次参与列表。
- 内存任务与历史条目按 `CreatedAt` 合并和排序，保持旧界面语义；计数、过滤和分页扫描在块边界检查请求预算。实现可以保留一次 `historyIndex` 浅拷贝和排序，但历史上限必须使该成本与页面大小解耦并有基准约束；若未来提高历史上限，应演进为按时间有序、不可变的索引。
- 历史归档保留策略、磁盘上限、清理时机和逐项解压边界不变；列表只读取紧凑索引，逐项结果只在打开任务时按页解压。
- 新列表端点应在构造行时直接生成本地化字段，包括 `last_reason`、`disable_reason`、`last_error` 等文案。不得在成功响应上对整棵响应树执行 marshal、decode 为 `any`、递归改写、再 marshal。
- `localizeResponseData` 只保留给低频且结构不固定的小响应；从列表路径迁移本地化时必须逐字段对照正文，行为差异视为回归。

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

      guard: passive              # off | passive | transparent
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
          half_life_hours: 168
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
        consecutive_missing_thinking: 2
        strike_decay_after: 48
        strike_decay_min_healthy: 10
        thinking_guard: true
        non_reasoning_models: []
        cross_verify: thinking       # off | soft | thinking | all
        min_generation_ms: 1000
        burst_guard: false
        min_passive_output_tokens: 32
        min_hold_output_tokens: 8
        hold_timeout: "30s"
        attempt_timeout: "60s"
        request_timeout: "120s"
        hold_keepalive: "5s"
        shadow_mode: false
        debug_logging: false
        hold_max_buffer_bytes: 4194304
        min_encrypted_bytes: 256
        encrypted_bytes_per_reasoning_token: 2
        missing_thinking_cooldown: "12h"
        empty_stream_cooldown: "15m"
        max_attempts: 4
        on_exhausted: "fail_unverified"  # fail_unverified | fail_closed

      probe:
        model: "grok-4.5"
        timeout: "90s"
        max_output_tokens: 768
        concurrency: 2
        queue_size: 10000
        scan_interval: "15s"
        healthy_interval_hours: 0
        unknown_delay: "30s"
        unknown_jitter: "30s"
        suspect_backoff: "2m"
        max_backoff: "1h"
        entry_protocol: "codex"
        exit_protocol: "codex"

      proxy:
        auto_rotate_after_quality_failure: true
        rotate_after_errors: 2
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

YAML 使用严格字段校验，未知字段会直接报错。旧 `resin.*` 注册字段、环境变量回退和单例 Resin 配置不再读取；已移除的 `passive_guard`、`transparent_guard`、`quality.soft_cross_verify`、`quality.thinking_cross_verify`、`quality.fail_closed`、`quality.stream_idle_timeout`、`quality.max_hold_total` 同样按未知字段拒绝。

### 11.2 配置来源与优先级

YAML 决定以下部署级字段：

- `enabled`
- `priority`
- 首次启动时的 `guard`、`auto_action`、`model_prefixes`
- `routing`、`quality`、`probe`
- `proxy.auto_rotate_after_quality_failure`、`proxy.rotate_after_errors`、`proxy.auto_bind.*`、`task_history.*`
- `state_file`、`events_limit`

`state_file` 是状态文件的启动引导路径。管理页可以通过 `state_file.update` 写入并切换状态文件路径，并在引导路径旁写入 `<state_file>.location` 指针；指针存在时，插件重启后使用其指向的实际状态文件。修改 YAML 的 `state_file` 会切换到新的引导位置。

运行时策略首次从 YAML 初始化到 state，之后由管理页修改：

- `guard`
- `auto_action`
- `model_prefixes`
- `routing`
- `quality`
- `probe`
- `proxy.auto_rotate_after_quality_failure`
- `proxy.rotate_after_errors`
- `task_history`
- `events_limit`

`policy.reset` 使用当前 YAML 值重新初始化运行时策略。`plugin.reconfigure` 不得静默覆盖用户在管理页保存的策略。

`model_prefixes` 采用严格前缀匹配：配置了 `grok-` 时，`my-grok-proxy` 不匹配；`gpt-` 不会匹配 `grok-4.5`。不以配置前缀开头的已发现别名只能通过 `host.auth.models` 缓存识别，不使用 `grok` 子串兜底。

### 11.3 默认值

- `guard=passive`
- `auto_action=observe`
- `quality.on_exhausted=fail_unverified`
- `quality.max_attempts=4`
- `routing.strategy.selection=round_robin`
- `routing.strategy.prefer_last_success=true`
- `routing.affinity.enabled=true`
- `routing.affinity.ttl=1h`
- `routing.affinity.max_entries=10000`
- `routing.scoring.enabled=true`
- `routing.scoring.max_accounts_per_bucket=512`
- `routing.scoring.half_life_hours=168`
- `routing.scoring.refresh.min_interval=5m`
- `routing.scoring.refresh.max_age=30m`
- `routing.scoring.refresh.drift_threshold=0.03`
- `quality.consecutive_soft=2`
- `quality.consecutive_errors=2`
- `quality.consecutive_missing_thinking=2`
- `quality.strike_decay_after=48`
- `quality.strike_decay_min_healthy=10`
- `quality.thinking_guard=true`
- `quality.non_reasoning_models=[]`
- `quality.cross_verify=thinking`
- `quality.burst_guard=false`
- `quality.hold_timeout=30s`
- `quality.attempt_timeout=60s`
- `quality.request_timeout=120s`
- `quality.hold_keepalive=5s`
- `quality.shadow_mode=false`
- `quality.debug_logging=false`
- `probe.healthy_interval_hours=0`
- `probe.concurrency=2`
- `probe.suspect_backoff=2m`
- `proxy.auto_rotate_after_quality_failure=true`
- `proxy.rotate_after_errors=2`
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

状态文件固定为 schema v2。加载规则：

- `schema_version` 必须严格等于 2。
- JSON 使用 `DisallowUnknownFields` 解码。
- 不接受旧 schema、旧单例 Resin 字段、旧账号 `resin` 字段或旧 token 引用字段。
- 不执行字段迁移、旧数据继承或兼容回退；旧 state 必须删除后重建。
- 保存时再次校验 schema，禁止把非 v2 内存状态写回磁盘。

结构示意：

```json
{
  "schema_version": 2,
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

- 状态写路径采用“锁内生成一致性快照、锁外编码、同步和临时文件替换”。编码、`Sync`、rename 不得在全局状态写锁内执行，读请求不得等待整份 state 落盘。
- 使用单调递增的 `dirtySeq` 判断快照是否仍为最新：写盘期间有新写入时不得清除 `dirty`，必须在锁外写盘完成后重新调度后台落盘。
- 同步写失败和后台 `Flush` 失败都必须保留内存中的新状态和 `dirty` 标记，并重新调度重试；不得把失败当成成功，也不得回滚用户刚完成的修改。
- 高频观测路径不得深拷贝整条账号状态。只有需要持久化和回滚的写路径可以复制必要快照；观测回调必须明确返回是否实际改变状态。
- 只有代理身份字段发生变化时才更新代理索引；普通质量观测不得触发覆盖全部账号的代理索引重建。
- 管理读请求不得顺带执行观测覆盖层修剪、索引重建、哈希校验等维护工作；修剪由目录刷新或后台调度完成。

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

- 状态读路径使用读锁；代理运行态观察集合使用读写锁，列表解析只持读锁，写入只做短临界区。
- 管理列表分块读取时，每块获取一次状态和绑定投影，不在整个请求期间持有全局写锁，也不复制完整状态表。
- 管理请求不得触发观测修剪；目录刷新和后台维护负责修剪、回收和重建派生物。
- 后台落盘使用单写者语义，编码和 fsync 在锁外执行；写盘结束后只有 `dirtySeq` 未变化才清除 `dirty`。
- 热重载、目录刷新、索引发布和缓存回填都必须以原子快照或版本号交接，避免列表读到半更新的目录、状态或绑定计数。

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
- 按输出 item 累加的 encrypted 达到 max(256, 2 * reasoning tokens)
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

#### 管理页性能与可靠性测试

- 账号列表在空查询、各状态筛选、跨字段搜索、无命中、越界页和空库场景下，`total`、`state_counts`、页内 ID 序列与旧语义一致；空结果必须是空数组。
- ID 序索引稳定性测试：相同目录多次发布得到相同顺序；目录刷新后按账号 ID 升序重建；索引不进入持久化状态。
- ASCII 与非 ASCII 搜索均大小写不敏感；显示名优先级为邮箱、标签、ID；查询首尾空白裁剪；跨字段命中只出现一次。
- `ids_only + all_ids` 返回完整命中集合，且只执行一次扫描；不得依赖循环分页或 `page_size` 放大。
- 代理绑定与来源列表的分块结果与旧全量视图一致；绑定状态枚举和漂移判断复用同一分类函数；来源排序与 ID 序索引一致。
- 常驻队列摘要的计数与队列明细数量一致；`tasks.center.queues` 不扫描任务历史，`tasks.center.list` 不重复返回 `resident_queues`。
- 任务中心只加载一次历史索引；计数、筛选、`CreatedAt` 排序和分页在内存任务与历史条目合并后仍保持一致。
- `overview` 和代理绑定计数缓存在目录、状态或 binding revision 改变后立即失效；失败结果不缓存；扫描期间版本改变不回填。
- 状态写失败以及后台 `Flush` 失败后保留 `dirty` 并重新调度；旧快照不得清除新写入。

### 16.2 集成测试

- mock CPA host callbacks
- mock xAI SSE
- mock Resin Admin API
- 透明模式首次 emit 前换号
- 所有候选耗尽按 `on_exhausted` 返回 503 `quality_degraded` 或放行未确认降智的缓冲
- 坏内容不 emit
- 请求预算不足时以 `inconclusive` 结束且不计入账号质量
- 传输错误使账号进入 `unreachable` 而不是质量冷却
- 无任何代理配置时透明保护与探测走直连
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

- 模拟冷目录延迟 30 秒，首次列表请求最多等待约 2 秒即返回 JSON `503`、`catalog_loading` 和 `Retry-After: 1`；目录就绪后下一请求可成功，且错误结果未被缓存。
- 3000 个以上账号的账号列表、绑定列表和来源列表不触发逐账号 Host 读取；分块扫描不复制完整状态 map，不执行全表排序。
- 重动作并发超过闸门时返回 JSON `503 busy` 和 `Retry-After: 1`；相同 action、locale、参数的并发请求通过 single-flight 只执行一次。
- 并发读列表与持续状态写入时无 race、无死锁；读请求不长时间等待状态落盘，管理请求不触发观测修剪。
- `tasks.center.queues` 在队列长度为 100、1000、10000 时分配量保持常数级，并与 `tasks.center.queue` 的明细数量一致。
- 前端快速切换筛选和搜索时，后发请求胜出；中止的旧请求不弹错、不覆盖新数据；失败时旧表格、计数和滚动位置保留。

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
- 非推理请求在首个可见内容后提前放行并跳过 missing-thinking 与 dump 类判定
- 插件 Executor
- 流式 hold / emit
- 多账号重试
- 后台分层选择索引和原子快照
- preferred last-success、session affinity 和 in-flight 跟踪
- round_robin / random / fill_first 层内策略
- 候选耗尽处理（`on_exhausted`）
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
2. `guard: passive` 能正确区分 reasoning stub 和真实 thinking。
3. 所有质量异常分类与默认阈值和本文一致。
4. 自动处置只操作 guard-owned auth。
5. 成功请求不清除 missing-thinking strike。
6. 冷却到期探测成功会解除冷却并保留 strike。
7. 冷却到期探测失败会永久禁用。
8. Resin 绑定和轮换保留 auth 中所有 token 字段。
9. 删除租约后不查询 IP，不承诺换 IP。
10. 透明模式仅在首个 emit 前换号。
11. 所有候选耗尽时按 `on_exhausted` 处理，已确认降智的响应永不下发。
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

22. 账号、代理绑定、代理来源和任务中心列表的单次临时分配与页面大小同阶；账号和来源的 ID 序索引只增加约每项 4 字节常驻内存。
23. 列表不缓存按查询参数组合的整表响应；聚合缓存单条目、版本键控、短 TTL 兜底，失败结果和版本变化中的结果不缓存。
24. 冷目录和重动作超时返回 JSON `503`，分别使用 `catalog_loading` 和 `busy`，并携带 `Retry-After: 1`；不得以非 JSON 502 表示可预期业务错误。
25. 搜索与筛选统一 200ms 防抖，列表请求后者胜出；加载失败保留旧表格、计数和滚动位置，`catalog_loading` 与 `busy` 最多自动重试 2 次。
26. 常驻队列通过 `tasks.center.queues` 只读取计数；任务中心历史每次列表只加载一次，不重复打包常驻队列。
27. 状态写入在锁外编码和落盘，`dirtySeq` 防止旧快照清除新写入；写失败保留 `dirty` 并重新调度。

## 19. 明确默认决策

| 决策 | 默认值 |
| --- | --- |
| 插件 ID | `grok-quality-guard` |
| 资源页完整 URL | `/v0/resource/plugins/grok-quality-guard/dashboard` |
| Action API 完整 URL | `/v0/management/plugins/grok-quality-guard/api` |
| guard | passive |
| auto_action | observe |
| 透明失败策略 | fail_unverified |
| 最大尝试次数 | 4 |
| 透明超时 | hold 30s，单账号 60s，单请求 120s |
| 传输错误 | `unreachable`，30s 起翻倍，最长 10m，不计质量 strike |
| 代理 | 可选；无代理时全部直连 |
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
| 状态文件版本 | 2 |
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
