# 通用代理节点、模板与 Resin 管理设计

> 版本：V4
> 状态：核心需求已实现
> 日期：2026-11-16
> 范围：模块抽取、通用代理配置与绑定、模板级联约束、自动绑定和 Dashboard 交互
> 本文保留完整设计约束与验收口径；实现位于 `internal/proxy`、`internal/app/proxy`、`internal/app/proxy/resin`、`internal/app` 装配层和 `internal/app/dashboard`

## 1. 评估结论与本次修订

原方案的三个方向成立：抽取独立代理模块、以模板机制复用 Resin、把 auth 的 `proxy_url` 作为实际路由依据。合法组合表也能可靠解决国家与州的组合限制。

但原文尚不能直接作为实现规格。主要缺陷不仅在交互，还包括随机语义、模块依赖、验证覆盖和跨文件一致性：

| 问题 | 影响 | 本次优化 |
| --- | --- | --- |
| 枚举按最少使用次数选择 | 偏离用户要求的随机选择，并引入长期计数和淘汰逻辑 | 枚举恢复随机；负载均衡只用于代理来源分配 |
| 直接随机选关联表的一整行 | 子项较多的国家获得更高概率，父级枚举的含义不明确 | 按父级到子级的顺序，在当前合法候选内随机选择 |
| 级联主要靠逐行填写 | 国家、州重复输入，修改父级值费力 | 默认使用父级选择与子级批量输入，表格作为高级编辑入口 |
| 树形和表格同时作为配置实体 | 数据可能不一致，转换、校验和保存逻辑重复 | 只持久化合法组合表，层级视图由相同数据派生 |
| 泛型包依赖 Resin 子包、子包又依赖泛型类型 | 存在 Go 循环导入风险 | `internal/app`装配两个包；代理包不导入 Resin 子包 |
| `Retire` 的语义不区分调用远端与排队 | 可能把 Resin API 放进绑定和请求路径 | 清理引用与持久化 outbox 明确分离，远端 worker 执行 |
| 绑定只有来源 ID，没有配置版本和清理快照 | 模板更新无法知道 auth 使用哪个版本，Resin 配置变化可能删错租约 | 增加来源版本、绑定指纹、清理快照引用 |
| 保存依靠有限样本验证，却声称覆盖全部值 | 超过样本上限后可能漏验长用户名或非法组合 | 完整静态验证全部配置，预览样本只用于展示 |
| 节点批量失败就自动回滚 | 已成功写入的 auth 可能被再次覆盖，回滚也可能失败 | 逐账号前向同步，保留成功结果，持久化失败进度 |
| 把不受管理的代理一律叫“冲突” | 用户已有代理并不一定错误，容易诱导清除 | 区分外部代理与受管理绑定漂移 |
| 四个二级 Tab 分别管理三种来源和绑定 | 跨类型比较和绑定选择跳转较多，容易理解为互斥模式 | 两个按任务划分的 Tab：账号绑定、代理来源 |
| 清除、修改、删除只写“auth 优先” | 崩溃窗口可能丢绑定元数据、暂停标记或租约删除任务 | 写入操作意图，再修改 auth，最后提交绑定与清理任务 |
| `require_healthy=true` 仍允许 unknown | 与字段含义不符 | 开启后只允许当前版本、未过期的健康检测结果 |
| 批量导入仍停在 10000 条和 2 MiB | 10 万节点上限无法实际使用，大文件缺少本地过滤与性能保护 | 提升到 100000 条和 16 MiB，增加防抖解析、边界提示和本地文件过滤 |

本文将这些修正合并为一套设计，不保留另一份与本文竞争的优化规格。

## 2. 已确认需求与范围

### 2.1 必须保留的约束

1. 支持多个静态代理节点和多个普通代理模板，可以同时启用和使用。
2. 节点最终格式为 `协议://用户名:密码@主机:端口`；无认证时为 `协议://主机:端口`。允许仅有用户名；密码可以省略或显式为空。
3. 协议只支持 `http`、`https`、`socks5`、`socks5h`。
4. 占位符只允许出现在模板的用户名中。密码、协议、主机、端口均为静态配置。
5. 每个用户占位符只有两种类型：枚举、随机。级联是枚举之间的约束，不增加第三种类型。
6. 枚举须配置分隔符和可选范围；生成时随机选择。随机类型须配置长度和是否区分大小写，使用英文字母、数字生成。
7. 模板必须通过配置验证才允许保存；保存接口自己执行验证。
8. 模板修改默认不回溯已绑定 auth，但提供模板级配置允许回溯。
9. 手动清除默认允许自动绑定重新接管，同时提供全局配置和单次操作覆盖。
10. 无 scheme 的节点输入直接忽略，不推断协议、不补 scheme。
11. 修改或删除静态节点需要同步已绑定 auth 的 `proxy_url`，不能提供绕过同步的普通保存选项。
12. Resin 复用模板渲染和绑定流程；其租约清理由 Resin adapter 处理。
13. 插件提供默认关闭的自动绑定选项，开启后将发现的未绑定 auth 加入有界后台队列。
14. Dashboard 保持现有配色、字体、卡片、表格和深浅主题风格。

本文统一使用 auth JSON 的 `proxy_url`。它不是 YAML 中常见的 `proxy-url`；实际写入由现有 Host auth 接口完成。

### 2.2 本阶段边界

不实现代理服务器、代理链、地理位置数据库、带宽统计或按请求切换模板。模板在绑定或轮换时生成最终 URL，正常请求继续使用 auth 中已保存的 URL。

国家和州是否合法，以用户配置的组合为准。系统不根据字段名推断地理知识，也不把“删除租约”等同于“必然更换出口 IP”。

## 3. 当前代码核对与保留行为

| 当前文件或入口 | 已确认行为 | 重构要求 |
| --- | --- | --- |
| `internal/app/proxy/resin.go` | 通用绑定路径调用 Environment、Host、StateStore | 移入通用 Manager 与 Resin adapter，`internal/app`保留装配 |

| `internal/app/proxy/resin.go`、`internal/app/proxy/resin/client.go` | Admin API、平台解析、SOCKS5 认证验证 | 分离配置验证和连通性检测，复用通用端点能力 |
| `internal/app/proxy/resin.go`、`internal/app/proxy/resin/cleanup.go` | 持久化租约删除队列、不可变快照、重试 | 保留去重与恢复能力，清理只使用 job 绑定的快照 |
| `internal/app/host_bridge.go:saveAuth` | 发送 Name 与完整 JSON，未传入条件版本 | 本地账号锁与最新读取能保护插件内修改；不能声称具备宿主原子 CAS |
| `internal/app/state/store.go`、`internal/app/state/proxy_store.go` | Update、UpdateIfChanged、观测延迟落盘有不同失败语义 | 代理关键提交必须明确使用同步持久化路径 |
| `internal/app/plugin.go`、`internal/app/catalog/catalog.go` | 定期刷新 catalog，支持文件代理元数据缓存 | 自动绑定复用刷新结果，不每轮重复逐 auth Host 查询 |
| `internal/app/probe/probe.go`、`internal/app/quality/*.go` | auth 修改使用账号锁，探测可能长时间持锁 | 所有代理写入接入相同账号锁，避免嵌套加锁 |
| `internal/app/executor/*.go` | 执行与重试读取 auth 的 `proxy_url` | 成功切换后立即失效相关缓存，不等下一轮目录刷新 |
| `internal/app/dashboard` | 顶层为概览、账号质量、代理管理、事件日志、设置 | 代理管理包含账号绑定与代理来源两个子视图，保留原位置和其余入口 |

Resin 本地轮换是其来源的 local_only 模式：仅改写已有代理身份，不需要 API，不参与创建新绑定。配置使用严格格式；插件不会读取或重写任何未显式绑定的历史 auth URL。

## 4. 通用模型与职责划分

### 4.1 代理来源

Source 表示可供绑定的配置来源，而不是一次生成的代理 URL。

| 来源类型 | 配置 | 新绑定 | 轮换 | 清理 |
| --- | --- | --- | --- | --- |
| `node` | 固定 Endpoint | 使用已有端点 | 在允许的节点范围中选择其他节点 | 本地解除引用 |
| `template` | 用户名模板与占位符规则 | 本地生成端点 | 在同一模板中重新生成 | 本地解除引用 |
| `resin` | 连接配置与内置模板 | API 模式本地生成租约身份 | 生成新租约身份 | 异步删除旧远端租约 |
| Resin 本地轮换 | 已有 URL 与原身份规则 | 不支持 | 仅本地改写 | 不执行远端删除 |

节点、模板和 Resin 都使用稳定 SourceID 建模，可并存多个实例；Resin 不是全局单例配置，也不存在旧配置迁移路径。

Source 保存业务配置；Provider interface 只处理真正变化的生成行为。启用、配额、来源版本、分配数量、管理视图由 Manager 管理，不要求每个 adapter 重复实现。

### 4.2 Endpoint

~~~go
type Endpoint struct {
    Scheme      string
    Host        string
    Port        uint16
    Username    string
    Password    string
    HasUser     bool
    HasPassword bool
}
~~~

`HasUser` 与 `HasPassword` 区分无认证、仅用户名、空密码和非空密码。不能仅靠空字符串判断，否则更新和格式规范化会丢语义。

内部保留解码后的值，URL 序列化时统一编码。`Endpoint` 不直接作为管理响应类型，避免默认 JSON 序列化泄露密码。

### 4.3 来源和版本

~~~go
type SourceInfo struct {
    ID          string
    Kind        string
    Name        string
    Enabled     bool
    Priority    int
    MaxBindings int
    ConfigVersion uint64
    Revision    uint64
}

type TemplateSpec struct {
    Endpoint        Endpoint
    UsernamePattern string
    Placeholders    []Placeholder
    ValueGroups     []ValueGroup
    UpdatePolicy    string
}
~~~

- SourceID 在编辑时保持稳定，不能用 URL 哈希充当来源 ID。
- config_version（v）是标准端点内容版本，仅在 `schema://用户名:密码@主机:端口` 的标准内容变化时递增。
- revision（r）是生成配置版本；节点随标准端点变化递增，模板在用户名模式、密码、占位符或关联组等生成内容变化时递增。
- 名称、启用状态、优先级、绑定上限和更新策略变化会保存，但不会推进 v 或 r；内容完全未变化的保存请求必须拒绝。
- 节点按标准 URL 去重；模板按标准端点和生成语义去重。占位符名称、定义顺序、枚举选项顺序及 `input`/`values` 表示、关联组 ID 与展示名、组顺序、列顺序和行顺序不参与判重；用户名中占位符的复用位置、实际值空间和关联组合集合必须一致。独立枚举只有一个值时等同字面量；值集合恰为 26 个小写字母加 10 个数字时等同长度 1、大小写不敏感的随机占位符，恰为 52 个大小写字母加 10 个数字时等同长度 1、大小写敏感的随机占位符。名称、启用状态等展示元数据同样不参与判重。新建和编辑都必须拒绝与同类其他来源重复的定义。
- 模板版本以不可变快照保留，已有绑定继续引用生成它的版本。
- `update_policy` 为 `keep_bound` 或 `rerender_bound`，默认 `keep_bound`。
- 降低配额不驱逐已有绑定；只暂停新的分配，直到数量低于配额。
- 不把 `password_set`、运行队列深度等展示字段混进生成配置。

### 4.4 Binding

~~~go
type Binding struct {
    AuthID          string
    SourceID        string
    SourceKind      string
    SourceRevision  uint64
    Generation      uint64
    EndpointHash    string
    ResolvedValues  map[string]string
    Cleanup         *CleanupRef
    BoundAt         time.Time
    UpdatedAt       time.Time
}

type CleanupRef struct {
    HandlerKey string
    SnapshotID string
    ResourceID string
}
~~~

`ResolvedValues` 只保存生成所需的占位符结果，不保存代理密码，默认不进入公共账号列表。它用于模板版本追踪、回溯计划和恢复，不用于给枚举施加使用次数均衡。

Resin 的 SnapshotID 指向不可变的 Admin URL、Platform UUID 和 Admin Token 快照；租约账号单独保存在 job 与 Binding.Lease 中。清理旧租约时读取这份快照，不从当前 Resin 配置拼接目标。

绑定指纹基于完整规范端点，包括协议与认证值，只在内部比较。它不是自动接管外部代理的凭证。

### 4.5 绑定状态和来源状态分开

| 绑定状态 | 含义 |
| --- | --- |
| `unbound` | 实际 URL 为空 |
| `bound` | 实际 URL 与插件绑定记录一致 |
| `external` | 实际 URL 非空，但不由插件管理 |
| `drifted` | 插件记录与实际 URL 不一致，需核对 |
| `unknown` | 无法确定实际 URL，例如读取失败 |
| `reconcile_required` | 存在未完成操作，需核对后再继续 |

同步进度是附加状态：`pending`、`running`、`partial`、`completed`、`needs_reconcile`。来源另有 active、disabled、deleting 等生命周期状态。

外部代理是正常可保留的配置。只有操作时的预期值与实际值不一致，才返回 `proxy_binding_conflict`。

## 5. URL 输入、解析与规范化

### 5.1 接受与忽略规则

| 输入 | 处理 |
| --- | --- |
| `http://host.example:8080` | 接受，无认证 |
| `https://user:pass@host.example:443` | 接受，HTTPS 表示先与代理服务器建立 TLS |
| `socks5://user@host.example:1080` | 接受，仅用户名；认证是否可用另行检测 |
| `socks5h://user:p%40ss@[2001:db8::1]:1080` | 接受，密码解码一次，IPv6 保留方括号 |
| `HTTP://host.example:8080/` | 接受，scheme 小写化，根路径斜杠去除 |
| `socks5h://proxy.example:1080:alice:secret` | 标准解析失败后自动按 `host:port:username:password` 解析 |
| `http://proxy.example:8080@alice:secret` | 标准解析失败后自动按 `host:port@username:password` 解析 |
| `http://host.example` | 报错，端口缺失 |
| `http://:pass@host.example:8080` | 报错，密码存在但用户名为空 |
| `http://host.example:8080/path` | 报错，非根路径不支持 |
| 含 query、fragment 的代理 URL | 报错，不静默丢掉可能有含义的数据 |
| `socks://host.example:1080`、`socks4://…` | 报错，不自动映射到 SOCKS5 |
| `host:1080`、`user:pass@host:1080`、`host:1080:user:pass` | 直接忽略，计入 missing_scheme；地址在前格式也必须带协议 |

单条添加无 scheme 时提示“已忽略：缺少代理协议”，不保存。批量导入报告 `imported / skipped / invalid / duplicate`，无 scheme 只计入 skipped，不阻断其他行。

支持标准 URL、地址在前格式和 `host:port@user:pass` 格式：始终先按标准 URL 解析，失败后才依次尝试两种非标准形式。所有回退都由解析器自动完成，不要求用户选择格式。特殊字符复杂的凭据使用分字段表单或百分号编码。

### 5.2 实现顺序

1. 去掉整行首尾空白；检测 scheme，先处理 missing_scheme。
2. 先使用 net/url 解析标准 URL；若标准解析失败，再尝试 host:port:username:password 与 host:port@username:password。
3. 验证四种 scheme、主机、显式端口、认证形式和禁止字段。
4. 使用 `net.SplitHostPort` 或等价标准逻辑处理 IPv6，不以“最后一个冒号”自行拆主机。
5. 用户名与密码只解码一次。分字段输入是字面量，不再次 URL 解码。
6. 通过 `url.User`、`url.UserPassword`、`net.JoinHostPort` 序列化。
7. 规范化 scheme 和域名大小写，保留用户名、密码大小写，保留显式端口。
8. 计算内部指纹，生成只含 scheme、host、port 的脱敏 URL。

不能移除 `:443` 等默认端口，因为目标规范要求显式端口。IDN 域名须采用统一 IDNA 规则；不允许 scoped IPv6 zone 或控制字符。

### 5.3 模板解析的额外规则

模板也使用同一 URL 结构解析。用户名中的未编码 `{name}` 是模板标记；先编译为 literal/variable token，再使用通用 Endpoint 序列化。

- 密码中未编码的 `{name}` 提示 placeholder_outside_username，不参与替换。
- 实际密码包含花括号时，在 URL 中用 `%7B`、`%7D` 表示字面值；分字段密码输入始终按字面值保存。
- 编码过的花括号不被再次识别为占位符，避免解码后意外启动模板替换。
- 模板字面量和替换值分别编码；已经编码的字符不重复编码。
- 每个用户名占位符只求值一次，重复出现复用相同值。

### 5.4 去重与凭据变化

完全相同的规范端点可提示重复；协议、密码、认证存在性不同的端点不自动合并。

发现“同地址、同用户名、不同密码”只给提醒，不静默覆盖或把它认定为同一节点。节点 ID 不随密码变化。

允许多个 auth 共享一个节点，也允许两个模板绑定偶然生成相同最终 URL。普通绑定不承诺模板实例唯一；轮换需要有实际变化，详见第 12 节。

### 5.5 批量节点导入与本地文件

批量节点导入的当前规模边界：

- 单次导入最多 100000 个有效节点。
- 单次导入有效节点内容最多 16 MiB。
- 代理来源总数最多 100000，包含静态节点和普通模板。
- 超过任一限制时明确拒绝，不静默截断后保存。

Dashboard 在提交前本地解析文本，忽略空行、以 `#` 或 `;` 开头的注释、缺少协议、不支持协议、非法 URL 和重复端点。输入解析使用短暂防抖，不在每次键入时重复扫描全部内容；页面不按行创建 DOM，因此大列表只受文本体积和节点数量限制。

“加载文件”支持 UTF-8 普通文本、`.txt` 和 `.list` 等列表文件。加载时只把有效且不重复的 URL 放入编辑区，并显示总行数、有效数、无效数、重复数和体积。文件读取、粘贴和提交使用同一 16 MiB 与 100000 条上限。

## 6. 占位符配置与随机语义

### 6.1 仅有枚举和随机两种类型

~~~go
type Placeholder struct {
    Name    string
    Kind    string
    Enum    *EnumSpec
    Random  *RandomSpec
    GroupID string
}

type EnumSpec struct {
    Separator string
    Input     *string
}

type RandomSpec struct {
    Length        int
    CaseSensitive bool
}
~~~

使用判别联合：enum 只能配置 EnumSpec，random 只能配置 RandomSpec。禁止字段混用、同名重复和用户名中未使用的定义。

占位符名称由用户定义，建议语法为 `[\p{L}_][\p{L}\p{N}_-]{0,63}`：支持 Unicode 字母、数字、下划线和连字符，首字符为字母或下划线。因此 `some-city`、`供应商` 都合法。同名占位符在一次生成中复用同一值；值中的花括号视为普通字符，不递归展开。

### 6.2 枚举

独立枚举示例：

~~~json
{
  "name": "country",
  "kind": "enum",
  "enum": {
    "separator": "|",
    "input": "us|cn"
  }
}
~~~

- 分隔符为字面字符串，使用 `strings.Split`，不解释成正则表达式。
- 分隔符必须非空，长度限制 1～8 个 Unicode 字符；输入长度另设上限。
- 拆分后去除值的首尾空白；空值报错，重复值给出定位提醒并规范化去重。
- 值保留原大小写。`US` 与 `us` 是不同供应商参数，不自动转换。
- 普通枚举从去重后的所有可选值中等概率随机选择。
- 范围只有一个值也合法，表示固定参数。
- 一个值本身包含分隔符时，须改用其他分隔符；首版不引入转义表达式或正则 DSL。

Dashboard 将输入解析成可编辑值标签，同时显示分隔符与实际拆分结果。更换分隔符先保留值列表再重新拼接；新分隔符出现在某个值中时提示冲突，避免重新切分后悄悄改变范围。

关联枚举也保留各自的分隔符，其范围来自所属合法组合表的列。此时 `enum.input` 不单独持久化；父级、子级批量输入仍按该占位符的分隔符拆分，然后更新组合表。详情可读显示派生范围，避免独立范围与关联表互相覆盖。

### 6.3 随机

~~~json
{
  "name": "session",
  "kind": "random",
  "random": {
    "length": 12,
    "case_sensitive": false
  }
}
~~~

| case_sensitive | 字符集 | 字符集大小 |
| --- | --- | ---: |
| false | `a-z0-9` | 36 |
| true | `A-Za-z0-9` | 62 |

- 长度范围 1～128，建议默认 12。
- 每位在选定字符集中等概率抽取，使用 `crypto/rand`，避免取模偏差。
- 该开关决定大小写字符是否同时参与生成，不用于改变供应商的账号比较规则。
- 不强制每个结果同时含字母和数字。
- 不增加“随机类型依赖国家”这一额外规则；有关联的地理参数使用枚举。
- 随机源作为内部可替换依赖，测试使用固定随机源；生成不写长期使用计数。

## 7. 模板编译与完整验证

### 7.1 编译而不是逐项字符串替换

保存前编译用户名为 literal/variable token，并编译关联表为前缀索引。正常绑定、轮换和预览共用同一生成器。

编译结果按 `source_id + revision` 缓存，不保存到磁盘。生成器接受当前操作的上下文，返回最终 Endpoint 和替换结果。

### 7.2 必须完整验证的内容

| 层次 | 检查 | 是否阻止保存 |
| --- | --- | --- |
| URL 结构 | scheme、主机、端口、认证、禁止路径与参数 | 是 |
| 变量结构 | 名称、未定义、重复定义、未使用、用户名以外变量 | 是 |
| 枚举与随机 | 分隔符、范围、空值、长度、字符集 | 是 |
| 关联表 | 列与变量对应、完整行、重复行、组重叠、非枚举成员 | 是 |
| 所有候选值 | 控制字符、字节长度、编码可序列化性 | 是 |
| 全部生成上界 | 重复变量、关联组合对最大用户名字节长度的影响 | 是 |
| 网络检测 | 实际连接、代理认证、隧道测试 | 普通来源可选；不替代结构验证 |
| Resin 管理验证 | Admin API、平台、代理认证 | API 模式保存生成配置时必需 |

关闭来源等不改变生成配置的元数据操作不强制重新访问网络，避免 Resin 不可达时无法禁用它。

### 7.3 不穷举跨组笛卡尔积

最终主机、端口和密码固定，只有用户名变化，因此可静态证明所有配置范围都满足格式与长度约束：

1. 计算用户名所有字面片段的 UTF-8 字节长度。
2. 统计每个占位符出现次数。
3. 独立枚举取最大值字节长度，乘以出现次数。
4. 随机变量使用配置长度，乘以出现次数。
5. 对每个关联组的每行，计算该行所有变量的长度贡献；取该组贡献最大的一行。
6. 将各独立贡献相加，得到最大用户名长度。
7. SOCKS5/SOCKS5H 的认证用户名和密码按解码后字节数限制，最大 255；有认证时实际握手还须满足服务端可用要求。
8. HTTP/HTTPS 设置合理的实现上限，例如用户名和密码各 1024 字节，且总配置体积受限。

关联变量一起计算，避免把不同非法行的最大值拼成“最长组合”；也不会漏掉重复变量或多字节汉字。

最大合法组合数可以计算为各独立枚举基数和关联表行数的乘积，不实际展开。含随机变量时另外标注“含随机空间”，不把组合数解释为唯一可用代理数。

### 7.4 预览和网络验证

预览默认生成 3 条，单次最多 20 条。它只展示结果与可读替换值，绝不承担“覆盖全部配置才能保存”的职责。

~~~json
{
  "valid": true,
  "errors": [],
  "warnings": [],
  "summary": {
    "placeholder_count": 3,
    "group_row_count": 7,
    "maximum_username_bytes": 40,
    "has_random": true
  },
  "samples": [
    {
      "masked_url": "socks5h://proxy.example.com:1080",
      "display_url": "socks5h://us-nyc:******@proxy.example.com:1080",
      "values": {"country": "us", "state": "CA", "session": "n3k8b6z1a4q7"}
    }
  ]
}
~~~

样例使用独立预览身份，不能计入绑定、配额或触发远端租约删除。`display_url` 保持标准代理 URL 结构并展示生成的用户名，只将密码替换为 `******`；`masked_url` 继续用于仅展示协议、主机和端口的列表场景。

普通来源默认只要求配置有效；网络检测可按需执行。`require_healthy=true` 的自动绑定策略需要对应来源当前版本、仍新鲜的检测成功结果，unknown 不算健康。模板样本网络成功只能代表该样本，不能承诺所有地理组合均可联网。

四种协议的解析与探测能力和 Host 执行能力分别验证。当前依赖 CLIProxyAPI v8.0.67：普通 HTTP/SSE 执行路径支持 HTTPS 到代理的 TLS，但将 SOCKS5 和 SOCKS5H 都交给使用远端 DNS 的 SOCKS5 拨号器。当前按宿主既有能力处理：保留 `socks5://` URL，允许按宿主的远端 DNS 方式使用，不改写为其他 scheme；`socks5h://` 同样使用远端 DNS。通用 `ProbeEndpoint` 本身可以区分两者，但不能据此宣称宿主具有 SOCKS5 本地 DNS 能力。原生 WebSocket 的 HTTPS 代理支持还受宿主拨号器限制，本次不将 HTTP/SSE 验证扩展为全部 WebSocket 传输保证。

### 7.5 结构化错误

错误包含 `code`、`path`、输入行号或单元格坐标、可读消息。需要定位密码错误时只返回字段路径，不回显密码内容。

输入失焦时做本地反馈；服务端保存再次完整验证。用户编辑后立刻废弃旧“验证通过”标记，不能拿上一次结果保存新版本。

当前实现边界：最多 32 个变量、单模板最多 100000 条关联行、配置体积最多 16 MiB。超过时返回明确错误，不截断后保存。

## 8. 级联限制的推荐模型与生成算法

### 8.1 用户定义优先

变量名、变量数量、哪些枚举需要联动、联合约束中的允许组合、条件取样顺序均由用户定义。实现根据配置中的变量 ID/名称和关系数据工作，没有 country/state 或其他业务字段的分支逻辑。

`columns` 的顺序用于随机生成与浏览；联合限制由完整合法行定义。它不等同于系统写死的一组 depends_on，也不意味着每个变量只能有一个业务父级。多字段共同约束见第 24.3 节。

### 8.2 选择：合法组合表 + 层级编辑视图

| 方案 | 能解决的问题 | 代价 | 决策 |
| --- | --- | --- | --- |
| 每变量独立随机 | 无关联参数 | 无法限制国家和州组合 | 保留为独立枚举 |
| 单父级条件映射 | 简单父子关系 | 多级和多字段联动时需要扩展规则 | 不单独建立第二套模型 |
| 通用条件表达式/DAG | 任意逻辑条件 | 冲突、无解与 UI 复杂度明显增加 | 首版不采用 |
| 合法组合表 | 任意有限联合约束 | 手工逐行编辑成本高 | 作为唯一持久化模型 |
| 由组合表派生的层级编辑 | 父级选择、批量维护子级 | 需要明确列的取样顺序 | 作为默认交互 |

关联组不限制字段必须是国家、州、城市；它可以表达地区、套餐、运营商等任意枚举组合。各组变量互不重叠，独立组之间自由组合；跨组限制需要合并成一个组。

### 8.3 配置示例

~~~json
{
  "placeholders": [
    {"name": "country", "kind": "enum", "enum": {"separator": "|"}, "group_id": "geo"},
    {"name": "state", "kind": "enum", "enum": {"separator": "|"}, "group_id": "geo"},
    {"name": "session", "kind": "random", "random": {"length": 12, "case_sensitive": false}}
  ],
  "value_groups": [
    {
      "id": "geo",
      "name": "国家与州",
      "columns": ["country", "state"],
      "rows": [
        ["us", "ID"],
        ["us", "CA"],
        ["us", "NV"],
        ["cn", "ZJ"],
        ["cn", "BJ"],
        ["cn", "SH"],
        ["cn", "GD"]
      ]
    }
  ]
}
~~~

每个组成员的完整范围由该列投影获得，当前父级下的条件范围由对应前缀的行获得。因此满足“每个占位符配置类型与范围”，同时避免维护两个可能不一致的全局值列表。

要求：组内至少两个变量；列不重复；每行所有列非空；每个成员必须为 enum；每个变量最多属于一个组；组内重复行规范化去重，不能通过重复行暗中增加权重。

### 8.4 默认随机语义：逐级均匀

以 `columns=["country","state"]` 为例：

~~~text
候选行 = 全部合法行
country = 从候选行中的去重 country 值随机选一个
候选行 = country 与选择结果相同的行
state = 从剩余行中的去重 state 值随机选一个
最后只可能得到合法表中的一行
~~~

于是 country 的 `us`、`cn` 各为 1/2；选中 us 后 ID、CA、NV 各为 1/3。`us + ZJ` 不会生成。

直接按整行均匀取样会得到 `P(us)=3/7`、`P(cn)=4/7`。这也是一种分布，但不采用为默认，因为用户配置枚举时更容易理解“先等概率选择国家，再等概率选择该国家允许的州”。

对于三列，同样按国家、州、城市逐级选。完整叶子组合可以具有不同概率；这是逐级条件枚举的明确结果，不用“均匀”一词掩盖。

### 8.5 索引和条件预览

保存时编译前缀 trie。运行时在每一层从有合法后续路径的不同值中随机选择，不以子树叶子数作为权重，复杂度随层数增长。

`columns` 的顺序决定条件取样顺序，并不宣称某列在业务上只能有一个父级。一个城市值可以出现在多个省的路径下；路径是身份，显示值不是全局唯一节点 ID。

预览允许用户固定 `country=us`。服务端先过滤与所有固定值相容的行，再随机生成未固定变量。请求固定 `country=cn,state=CA` 时返回约束不满足，不能静默改成另一组值。

### 8.6 树形与表格无需两套真实数据

层级编辑、表格和 CSV 都修改同一个前端 rows 草稿；提交只发送规范 columns/rows。

- 表格转层级是按列顺序构造前缀索引，不丢行。
- 层级转表格只是展开现有路径，不做独立 enum 范围的笛卡尔积。
- 任意完整合法行都能表示为前缀路径，不根据字段名判断“这个国家不应有这个州”。
- 编辑视图偏好可存前端 UI 设置，不与后端运行规则绑定。
- 中间层尚无子级的未完成分支是草稿错误，不能悄悄跳过后保存。
- 改变列顺序保持合法行集合，但改变随机分布；显示提示并视为生成配置变化。

### 8.7 固定字面量和更复杂约束

`country-us-state-ny-city-{city}` 只有 city 变量，直接配置 city 独立枚举即可；系统不为字面量 us、ny 添加隐式父级。

`country-us-state-{province}-city-{some-city}` 为 province 与 some-city 建关联组即可。

如 country、carrier、state 同时有关联，把它们放在同一合法表。首版不允许重叠组或表达式条件，以免两个各自合法的组拼出无解结果。

## 9. 级联配置的 Dashboard 操作方案

### 9.1 把“枚举”和“关联限制”分开呈现

编辑器从用户填写的用户名模式提取变量，按实际变量名生成表单，不预置国家/州字段。下列 country/state 只是一个自定义配置示例；换成 vendor/plan/profile 时，同一界面按该配置的名称、范围和关联组工作。

每个占位符首先选择类型：

~~~text
country   类型：枚举    分隔符：|    取值：us | cn
state     类型：枚举    分隔符：|    取值：按所选 country 配置
session   类型：随机    长度：12    包含大写字母：关闭

关联限制：country → state                         [编辑限制]
~~~

类型下拉只有“枚举 / 随机”。“建立关联限制”是独立操作，不把关联组伪装成第三种类型。

新增关联限制时选择多个枚举变量与取样顺序，默认名称由所选变量生成但可改。系统可以提示存在多变量，不能只因名字含 country 就擅自建立关系。

### 9.2 两级级联的默认表单

以用户例子为准，操作只需：

1. 确认 country、state 都是枚举。
2. 建立 country → state。
3. 填写国家 `us|cn`。
4. 选择 us，填州 `ID|CA|NV`。
5. 选择 cn，填州 `ZJ|BJ|SH|GD`。
6. 查看 7 条有效组合和随机预览。

~~~text
关联限制：国家与州
取样顺序：country → state

country                     当前 country = us
┌─────────────────────┐     ┌─────────────────────────────┐
│ us               3  │     │ state 分隔符：|              │
│ cn               4  │     │ ID|CA|NV                     │
│ [+ 添加父级]        │     │ [解析并更新此父级下的值]     │
└─────────────────────┘     │ ID   CA   NV                 │
                            └─────────────────────────────┘

有效组合 7 条    [条件预览] [高级表格] [导入 CSV/TSV]
~~~

state 的值标签可直接增加、删除、修改。切换父级不会丢未确认的输入，先提示应用或保留草稿。服务端错误定位到具体父级路径和字段。

### 9.3 三层及更多层级

桌面端最多同时展示三层：国家列表、当前国家的州列表、当前州的城市批量输入。更深层使用路径面包屑逐层深入，避免把整棵树完全展开。

路径示例：

~~~text
country=us / state=CA / city
城市分隔符：|
Los Angeles|San Francisco
~~~

相同 state 值在不同 country 下按路径管理，不全局合并。删除父级时展示将删除的完整组合数量；添加子项只增加选中路径下的行。

### 9.4 批量编辑和复制

- 在选中父级下用分隔符一次录入多个子值。
- “复制到其他父级”需要选择目标父级，并选择合并或替换；先展示影响条数。
- 修改父级名称一次更新全部相关行，不要求逐行改。
- 若新父级名称已存在，不静默合并分支；用户明确确认合并。
- 支持父级、子级搜索和按路径筛选，长列表分页或虚拟滚动。
- 不提供默认“把所有父级与所有子值自由组合”按钮，避免误生成国家与州的非法配对。

### 9.5 高级表格与 CSV/TSV

高级表格保留给已有组合清单、非树状业务关系及大批量数据。列名必须精确匹配用户名变量，导入允许重排列后对齐。

使用标准 CSV 解析，支持引号、值中逗号和 UTF-8；CSV 分隔符与各枚举的取值分隔符是不同概念。

CSV/TSV 文件上限为 16 MiB，单模板最多 100000 行关联数据。导入到草稿后显示新增、重复、非法、覆盖的行数，以及错误行/列。关联数据有非法行时禁止保存整份模板，不像节点导入那样默默只保存有效子集。导出同一份 rows，表格/层级之间切换不会触发后台另一次转换。

### 9.6 条件预览

选择某个 country 后，state 的下拉只出现它允许的值。选好任意部分条件，再点击生成预览补齐其余值。

预览结果显示脱敏端点与替换值，并明确“来自合法组合表”。不在页面承诺真实出口地理位置。

这一方式比单纯显示“随机生成三条”更容易让用户确认是否正确配置了级联限制。

## 10. 模块目录、依赖和 interface

### 10.1 推荐目录

~~~text
main.go

internal/
  abi/
    envelope.go
  app/
    plugin.go                   生命周期与子服务装配
    host_bridge.go              宿主回调适配
    registration.go             插件注册与配置 schema
    auth/
      metadata.go               账号身份与 JSON 元数据
    catalog/
      catalog.go                账号目录刷新
      service.go                目录服务与环境接口
      helpers.go
      exports.go
    dashboard/
      dashboard.go
      dashboard.html
    executor/
      rpc.go
      transparent.go
      stream.go
      request.go
      support.go
      errors.go
      service.go
      exports.go
    management/
      api.go
      accounts.go
      policy.go
      views.go
      http.go
      adapters.go
      service.go
      exports.go
    model/
      account.go
    passive/
      service.go                被动流量观测与 usage 质量审计
    action/
      request.go
      params.go
    probe/
      probe.go
      marker.go
      stream.go
      adapters.go
      service.go
      exports.go
    proxy/
      service.go
      binding.go
      management.go
      operations.go
      scheduler.go
      sources.go
      state.go
      resin.go
      types.go
      helpers.go
      exports.go
      resin/
        client.go
        provider.go
        cleanup.go
        url.go
    quality/
      service.go
      verdict.go
      guard.go
      reconcile.go
      helpers.go
      exports.go
    redact/
      redact.go
    routing/
      index.go
      selection.go
      session.go
      request.go
      service.go
      helpers.go
      exports.go
    state/
      store.go
      proxy_store.go
      proxy_accounts.go
      evidence.go
      constants.go
      path_api.go
      path_windows.go
      path_other.go
      resin_keys.go
      test_helpers.go
    tasks/
      tasks.go
  config/
    config.go
    proxy.go
    api.go
  model/
    model.go
  quality/
    signals.go
    parser.go
    analyzer.go
    verdict.go
    request.go
    api.go
  sse/
    normalizer.go
  proxy/
    endpoint.go
    template.go
    constraints.go
    model.go
    probe.go
    probe_test.go
~~~

目录边界按职责划分，不再以旧文件名前缀堆叠在 `internal/app` 根目录。`internal/app` 根目录只保留生命周期、宿主交互、注册信息、被动观测和子服务注入适配。

### 10.2 Go 导入关系

~~~text
main -> internal/abi, internal/app
internal/app -> internal/abi, internal/config, internal/model, internal/quality,
               internal/sse, internal/proxy, internal/app/*
internal/app/proxy -> internal/app/{auth,model,params,redact,state,tasks},
                      internal/app/proxy/resin, internal/config, internal/proxy
internal/app/proxy/resin -> internal/proxy
internal/app/quality -> internal/app/{auth,model,state,tasks},
                        internal/config, internal/quality
internal/app/catalog -> internal/app/{auth,model,proxy,state,tasks}, internal/model
internal/quality -> internal/config, internal/model
internal/config -> internal/proxy
internal/proxy -> Go 标准库及实际需要的基础库
~~~

`internal/proxy` 不导入 Resin 子包，所有子包均不导入 `main`。`internal/app` 创建各领域服务并注入宿主适配接口；`internal/app/proxy` 创建 Resin Provider，普通模板不需要知道 Resin 配置。
### 10.3 最小变化点

~~~go
type Provider interface {
    Generate(context.Context, GenerateRequest) (GeneratedProxy, error)
}

type GeneratedProxy struct {
    Endpoint Endpoint
    Values   map[string]string
    Cleanup  *CleanupRef
}

type CleanupHandler interface {
    Delete(context.Context, CleanupRef) error
}
~~~

Provider 绑定不可变来源版本，在`internal/app`注册。普通节点和普通模板使用本地 Provider；Resin Provider 复用同一用户名编译器，另外生成系统租约身份和 CleanupRef。

Generate 不写 auth，不消耗持久配额，不删除租约。Manager 负责分配与提交；CleanupHandler 只由远端清理 worker 调用。

不把 ID、管理视图、绑定数量、Health、Eligibility 都放到 Provider interface；这些信息已经属于来源配置或 Manager，不需要 adapter 重复管理。

### 10.4 Host 与状态 interface

~~~go
type AuthStore interface {
    Read(context.Context, string) (AuthSnapshot, error)
    PatchProxy(context.Context, ProxyPatch) (PatchResult, error)
}

type Repository interface {
    Snapshot(context.Context) (Snapshot, error)
    Commit(context.Context, ChangeSet) error
}
~~~

`ProxyPatch` 包含 authID、预期当前代理指纹、期望新 URL 或清除标记；`PatchResult` 必须区分成功、已提交但回执未知、条件冲突、未提交失败。

Repository.Commit 对一个 ChangeSet 同步持久化：绑定变更、操作状态、自动绑定排除标记和 cleanup outbox 必须能一并提交。观察指标仍可走原有合并写，不与代理关键提交混用。

只暴露代理域投影，保留 main 的质量状态。时钟和随机源用内部依赖函数/Reader 注入；事件随 ChangeSet 提交，不额外引入只转发消息的 EventPort。

账号锁由`internal/app`提供同一执行 gate。Manager 在 gate 内完成读取、准备、条件检查和 patch；PatchProxy 的锁内实现不能再次获取同一把非重入锁。

### 10.5 `internal/app`承担的适配责任

Host adapter 在锁内读取最新 JSON，仅修改 `proxy_url`，保留 disabled、access_token、refresh_token、未认识的 JSON 字段。成功后更新 catalog 的代理元数据、失效 auth 相关缓存并通知调度。

State adapter 使用当前 StateStore 的同步提交能力。事务回调不做 Host 调用、网络访问或获取 Manager 锁，避免跨 interface 的锁反转。

## 11. 持久化与操作意图

### 11.1 状态扩展

状态文件固定为 schema v1。代理来源、版本、操作、自动绑定策略和 cleanup outbox 都持久化。

实现同时使用相邻的 `<state_file>.proxy-journal` 保存小型代理提交。每条记录包含单调序号和校验和，并在触碰 auth 前 fsync。主 state 原子 checkpoint 包含已应用序号，成功替换后才截断 journal；重启幂等重放完整记录，移除未完成尾行。缺少主 checkpoint 但 journal 非空时拒绝初始化，避免丢失来源配置后误恢复。切换 state 路径时先保存当前路径，再将完整状态写入新路径，并拒绝覆盖目标现有 journal。备份或恢复应在插件停止后同时处理匹配的 state、journal 和 `.location` 指针。

~~~text
State
  policy / quality accounts / events
  proxy
    sources
    source_versions
    operations
    cleanup_outbox
    auto_bind_config
  accounts[authID]
    proxy_binding
    auto_bind_excluded
    auto_bind_retry
  resin
    integration_snapshots
    credential_refs
~~~

`internal/app`可同时引用通用状态类型与 Resin 状态类型，但通用包不知道 Resin 的具体设置。配置、绑定和展示字段分开，运行中的 queue_depth 不作为配置保存。

### 11.2 操作意图记录

每个会写 auth 的代理操作使用稳定 operation_id。记录：

- authID、操作种类、来源 ID 和目标 revision。
- 预期旧代理指纹、旧绑定 generation。
- 已生成的期望端点或可完整恢复它的模板版本与替换值。
- 待写入的新绑定、旧 CleanupRef。
- 本次清除采用的自动绑定排除策略。
- prepared / finalized / failed / needs_reconcile 状态。
- source change ID、时间和脱敏错误。

生成的随机会话值先记录，失败重试复用相同目标，不能每次重新随机生成另一节点。

恢复材料含认证信息时使用 state 的受保护存储与秘密引用，绝不出现在 tasks/events 响应。只保存哈希不足以重做操作。

### 11.3 单账号提交顺序

~~~text
检查来源、auth 和已有绑定
  -> 生成目标并预留配额
  -> 同步保存 prepared 操作意图
  -> 在账号 gate 内核对最新 auth
  -> patch 并保存 auth
  -> 同步提交新绑定、排除标记和 cleanup outbox，标记 finalized
  -> 唤醒 cleanup/auto-bind worker
~~~

这里新增的是“未生效操作意图”的前置持久化，实际路由仍只由 auth 决定；prepared 不表示绑定已生效。

- prepared 写入失败：不改 auth，释放预留。
- auth 明确保存失败：旧路由保留，操作标为 failed。
- auth 成功、finalize 失败：返回 `committed=true`、needs_reconcile；不再次生成、不把成功伪装成普通失败。
- Host 超时或回执解码失败：重新读实际 auth 确认，不能直接假定未提交。
- 存在未解决操作的 auth 暂停自动绑定和其他代理变更，防止故障扩大。

原有“auth 已写成功后 state 失败，不撤销路由”原则保留；新方案进一步区分前置持久化失败与事后失败。相关故障测试须按阶段注入。

### 11.4 重启恢复

恢复 worker 对每个 prepared 操作读取实际 auth：

| 实际代理 | 行为 |
| --- | --- |
| 与目标指纹相同 | 补提交绑定、排除策略和 cleanup outbox |
| 与预期旧代理相同 | 可重试相同已生成目标 |
| 与两者都不同 | 标记需核对，不覆盖、不根据当前全局配置猜测 |
| 无法读取 | 保留操作并退避，不当作空代理 |

恢复与正常任务共享 gate。source 更新任务保留固定目标版本，不追着最新配置反复改写。

### 11.5 状态不兼容策略

当前状态文件固定为 schema v1。加载时必须满足：

- `schema_version` 严格等于 1。
- JSON 严格解码，未知顶层或嵌套字段直接失败。
- 不接受旧 schema、旧 Resin 单例字段、旧账号 `resin` 字段或旧 token 引用字段。
- 不执行任何旧 schema 数据迁移、字段转换或兼容回退；旧 state 必须删除后重建。
- 保存前再次校验 schema，禁止内存状态以其他版本写回。

### 11.6 版本和凭据保留

历史模板版本在仍被 Binding 或 Operation 引用时保留；历史 Resin 快照在仍被 Binding、Operation、CleanupJob 引用时保留。只有全部引用释放后才 GC。

这保证修改凭据、禁用来源、重启或删除配置后，已排队的旧租约仍有确定的清理目标。

## 12. 绑定、清除、轮换与来源分配

### 12.1 分配范围

手动绑定可指定某个节点、模板或 Resin，也可选择一个包含多个来源的范围。自动绑定使用相同的选择器。

候选过滤包括 enabled、结构验证、deleting/syncing、配额、要求的检测状态和来源级退避。若使用 priority，先选最高可用优先级层，再在层内分散。

### 12.2 静态节点均衡

在当前允许范围内，使用 `已有绑定数 + 在途预留数` 最小的可用节点；同负载时随机选择。

~~~text
3 个节点、10 个 auth
理想数量分布：4 / 3 / 3
各节点在相同负载时的选择顺序由随机结果决定
~~~

预留配额是短临界区操作，Host 保存失败时释放。相同节点的版本同步不新增一个绑定数；source 与 auth 一起核对，避免满额节点无法修改自己的既有绑定。

漂移或读取失败的旧绑定先保守占用配额，核对后再释放。绑定数均衡不等于实际流量均衡，不提供不必要的带宽统计。

模板、Resin 的来源选择可使用相同数量策略，但它们内部的枚举与随机值仍按第 6～8 节生成，不建立组合使用次数库。

### 12.3 新绑定和幂等

- 实际 URL 为空才可普通绑定。
- 已由相同来源管理且没有显式轮换要求时，返回 already_bound。
- 实际 URL 非空且没有插件绑定，返回 external_proxy_present。
- 使用 operation_id 重试时返回或恢复同一个操作，不重新分配。
- 显式来源不可用时返回原因，不擅自绑定别的付费供应商。
- 自动选择范围内一个来源暂时失败时可以选择其他候选，并记录来源级退避。

手动接管是独立显式动作：对静态节点可在完整规范端点匹配时建立引用；普通模板默认选择替换绑定，因为从 URL 反推占位符可能有歧义；Resin 接管必须核对身份规则与清理快照。不能靠端点相同自动认领。

### 12.4 清除和自动绑定策略

~~~yaml
proxy:
  auto_bind:
    manual_clear_policy: rebind
~~~

`rebind` 为默认，`pause` 为清除成功后排除该 auth。单次 `exclude_auto_bind` 使用三态：省略/null 继承策略，true 暂停，false 允许重新绑定。

- 排除决策先写入操作意图，finalize 后与清除结果一起提交。
- finalize 失败时 prepared 操作阻止重新绑定，恢复后再应用排除决策。
- 修改全局策略只影响之后的手动清除，不重置过去显式排除的 auth。
- 提供“恢复自动绑定”动作；手动绑定本身不偷偷解除排除标记。
- 批量清除使用提交时的策略快照，不在每个 worker 中读取可能变化的策略。

对 managed 代理清除前核对指纹。对 external 代理必须显式允许清除；单行“清除外部代理”和“清除所选”确认都会设置 `allow_external`，只清本地 URL，不能执行 Resin 远端删除。

### 12.5 轮换

静态节点在明确的允许范围内避开当前规范端点；相同 URL 的重复节点不算有效轮换。模板与 Resin 默认留在原来源重新生成，跨来源轮换须明确选择。

普通模板不保证无限不同：

- 只有一个固定组合、没有随机变量时，返回 `rotation_unavailable`。
- 有多个结果时，按相同随机规则生成，有限次重试避开旧指纹。
- 达到重试上限仍无变化，返回未轮换，不增加 generation。
- 多个 auth 可以共享结果；不默认要求模板生成值全局唯一。
- Resin 对租约身份另做使用中/在途去重，并使用充足随机空间。

只有实际 auth 改变后 generation 才增加。失败保留旧 URL；远端旧租约在实际切换之后通过 outbox 删除。

### 12.6 质量失败后的自动轮换

保留现有质量失败分类和请求重试边界，只把动作委托给通用 Manager。统一配置使用 `proxy.auto_rotate_after_quality_failure`，不同时在 policy、Resin runtime 设置和代理模块里维护可独立变化的三个开关。

策略只从当前 policy/YAML 读取，不再维护多个可独立变化的开关。模板和节点同样可以轮换，但不能因普通限流、流已经对客户端输出或不可重放请求而额外增加重试。

## 13. CRUD、节点同步与模板回溯

### 13.1 节点创建与元数据编辑

新增节点先完成结构验证。名称、enabled、priority、max_bindings 等变化立即保存，不修改 auth。

Dashboard 添加节点页提供 `+ 增加一行` 追加条目，可一次提交多个名称与 URL，提交按钮按行数显示保存节点或保存 N 个节点。节点行将名称与代理 URL 同排展示，名称列使用紧凑宽度，URL 列占据剩余空间；长值在失焦时省略，聚焦后可横向滚动，并保留完整悬浮提示；enabled、priority、max_bindings 作为本次共享设置，经批量导入接口逐项报告 created、duplicate、invalid 和 skipped。编辑器帮助统一为标题后的 info 图标，tooltip 约束在抽屉边界内，提示文案不以标点结尾。

端点配置变化必须创建同步任务；API 不提供 `propagate=false` 绕过。无活跃绑定时可直接提交新版本。

### 13.2 节点端点同步：前向执行

1. 保存已经验证的目标版本和 source change。
2. 固定受影响 Binding 清单及每个 auth 的旧指纹、generation。
3. 暂停该节点的新分配，已有实际路由继续可用。
4. 每个目标通过通用单账号操作流程执行，写前再核对；预检不能代替写前核对。
5. 成功项更新绑定 revision，失败项保留旧版本并记录原因。
6. partial 状态只重试未完成项；重试复用原目标。
7. 完成后恢复该节点新分配。任务未结束时不接受同节点第二个端点变更。

不同 auth 暂时使用不同版本属于明确展示的同步过渡状态。成功项不自动回滚；需要撤销时创建另一个变更任务，核对当前指纹后前向应用旧配置。

### 13.3 模板回溯

`update_policy` 默认 `keep_bound`：

- 新绑定使用新版本。
- 既有 auth 保持 URL，引用旧版本。
- 改名、启用状态、配额或只切换 update_policy 不触发重新生成。

`rerender_bound`：

- 用户名模式、密码、端点、枚举、随机长度/大小写、关联表或取样顺序变化时，保存与固定目标版本的回溯任务一起持久化。
- 回溯对象是保存时仍属于该模板的绑定清单；后续新绑定不会被该任务再次重做。
- worker 检查来源、generation、指纹，已切换来源的 auth 标记 superseded。
- 每个新随机结果先持久化后写 auth，失败重试不换结果。
- 保存响应说明“配置已保存，X 个账号待同步”，而不是提前声称全部更新成功。
- 同一模板回溯未完成时拒绝第二个会改变生成规则的保存；元数据编辑仍可进行。

无论策略如何，都提供显式“应用当前模板到已绑定账号”。它可将旧版本绑定更新到当前版本；先预览数量和规则，再创建任务。

### 13.4 删除三种来源

统一默认语义：有绑定时先确认并清理受管理 auth，再完成删除，不无提示地把它们变成外部代理。

| 来源 | 本地处理 | 额外清理 |
| --- | --- | --- |
| 节点 | 清除对应 auth URL 和绑定 | 无 |
| 普通模板 | 清除对应 auth URL 和绑定 | 无 |
| Resin | 清除对应 auth URL 和绑定 | 旧租约进入 outbox |

删除中来源不参与新分配。失败项保留 deleting 状态，可重试。指纹已漂移的 auth 不覆盖，待管理员明确核对或解除旧管理关系后才能完成。

完成本地删除无需等待远端 Resin 删除成功；相应清理快照继续保留。删除后自动绑定按常规候选规则为未排除 auth 分配其他来源。

需要停止新分配而保留旧路由时用“禁用来源”，不必删除。

### 13.5 任务结果

~~~json
{
  "change_id": "change-001",
  "status": "partial",
  "target_revision": 3,
  "counts": {"total": 10, "succeeded": 8, "failed": 1, "conflict": 1},
  "items": [
    {"auth_id": "auth-1", "status": "succeeded", "committed": true},
    {"auth_id": "auth-2", "status": "conflict", "code": "proxy_binding_conflict"}
  ]
}
~~~

区分配置保存结果和 auth 同步结果；已提交但待恢复不计入“未写入失败”。取消批量任务只停止未开始项，不撤销已成功的修改。

## 14. 未绑定 auth 的自动绑定

### 14.1 配置与默认值

~~~yaml
proxy:
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
~~~

`source_ids` 非空时进一步限制 scope，不覆盖 scope；所选 ID 必须存在且类型在 scope 内。scope 可为 all、nodes、templates、resin。删除来源后保留失效 ID 的提醒，不能默默回退成全来源。

`health_max_age` 默认 10 分钟；开启 require_healthy 后，过期/未知的检测进入待检测状态，由独立、有界检测任务更新，成功后唤醒绑定队列，不占用账号提交 gate。

可以先开启自动绑定而暂时没有可用来源，状态显示等待来源。`enabled` 是明确代理写入授权，与质量观察模式独立。

### 14.2 候选与未知值

候选必须是当前 Grok/xAI auth、实际 URL 已确认为空、未排除、无未解决操作、退避到期，并符合禁用账号范围。

- 外部代理不参与。
- catalog 元数据读取失败为 unknown，不能把失败读成空 URL。
- 缺少文件 metadata 的 runtime auth 使用有界 Host 读取确认。
- worker 执行前在同一账号 gate 内再次读取最新 auth。
- 旧绑定实际为空时先完成核对/旧资源处理，再作为未绑定候选。

默认跳过所有已禁用账号；配置开启纳入时仍不修改其 disabled 标记或质量状态。

### 14.3 队列与公平调度

内存队列只保存 authID 与发现原因，去重集合覆盖 queued 和 running。不能仅去重 running，否则反复扫描会产生多个排队副本。

- 使用有界队列，满时保留扫描游标，下轮继续。
- 不为每个 auth 调一次 `startTask` 来创建独立 goroutine。
- 队列工作不是质量探测，不共用 probe 的并发额度。
- 提交时才选择来源和预留，不在入队时持有节点配额。
- 连续大目录采用轮转游标，避免每轮只尝试排序前 100 个 auth。
- scan、Host 读取与提交都有有界并发；没有来源或未到重试时间时使用 channel/timer 阻塞等待。

候选可从 catalog 重建，因此发现队列不用持久化。操作意图、排除标记、失败退避必须持久化，两者用途不同。

### 14.4 唤醒与退避

启动恢复完成、catalog 变化、创建/启用来源、来源检测恢复、排除解除、清除完成、配置更新和周期 timer 都可发送合并唤醒。

没有可用来源时停在 waiting_sources，不为每个 auth 增加失败次数。临时网络故障采用来源级退避；Host 保存失败采用 auth 级退避。指数退避从 1 分钟到 1 小时，加入小幅抖动。

来源恢复可释放来源导致的等待，但不任意清零无关 auth 的保存失败退避。成功清零本账号失败计数。

### 14.5 关闭、手动优先和观测

关闭后停止新派发、丢弃未开始队列项；已实际保存的操作仍完成 finalize。重启能由 prepared 意图恢复。

显式手动任务在调度层优先，但不强行打断正在持有账号锁的探测或保存。所有任务共享正确性 gate，不承诺 mutex 自动提供优先级。

自动绑定状态返回 enabled、running、waiting_reason、queue_depth、in_flight、最近扫描、成功/失败统计。空闲扫描不逐账号记录 skipped 事件；只记录结果、异常或状态变化，避免每 30 秒刷日志。

“立即扫描”在自动绑定开启时唤醒一次发现过程，不绕过开关、排除或退避。关闭状态下，“绑定全部未绑定”仍作为显式手动动作可用。

## 15. Resin 来源

### 15.1 模板形式

每个 Resin 来源都以 `Template` 的 `kind=resin` 持久化，连接差异（平台、Admin URL、AccountFormat、LocalOnly 和 Admin Token）保存在按来源 ID 索引的 Resin 配置中。可以同时存在多个 Resin 来源，因此同一 Resin 服务可以按平台、Admin 地址或账号生成规则拆分。单个来源使用：

~~~text
用户名模式：已解析的平台名.{account}
密码：固定 Proxy Token 的秘密引用
协议/主机/端口：固定端点
~~~

平台名先由配置解析成用户名 literal；`{account}` 是保留占位符，只替换为 auth 邮箱 `@` 前的本地部分并转小写。除 `{account}` 外，账号格式可以继续使用普通模板的 enum/random 占位符和关联组，例如 `xai-{account}-{region}`。Proxy Token 是固定密码，不是模板变量。

通用模板语法不保留 `{proxy_token}` 特殊变量；Resin Proxy Token 保存在来源配置中，并在构建模板时作为固定密码写入。普通用户变量仍只有 enum/random；`{account}` 的 reserved kind 只允许 Resin 模板使用，不出现在普通模板编辑器的生成方式下拉中。

管理页只暴露平台名之后的 username 后缀，用户不需要也不能重复填写平台前缀。协议固定为 SOCKS5H，密码固定引用 Proxy Token，代理主机和端口从 Admin API URL 解析；完整的 username template 仍作为内部持久化形式，用于生成、预览和租约清理。

### 15.2 生成与清理

平台名称与 UUID 在配置验证时解析，保存为不可变来源/集成版本。正常 Generate 使用已验证快照，不在每次绑定或请求轮换时访问平台 API。

Resin 没有显式创建租约的管理调用；绑定生成新身份并保存 URL，实际代理请求可能惰性创建租约。新身份保证不与使用中、在途租约冲突。默认账号格式为 `grok-{account}-{suffix}`，其中 `suffix` 是 random 变量；用户也可以自定义其他格式和更充足的随机空间。

旧租约删除任务从 Binding.Cleanup 构建，按 SnapshotID 读取旧 URL、UUID 和凭据。不能因为用户修改了 Resin 地址或平台，把旧租约删除请求发给新平台。

### 15.3 保存验证与本地模式

API 模式修改生成配置时顺序验证 Admin API、平台、代理认证。只做 SOCKS5 auth handshake 和 CONNECT 到测试目标是不同级别的检测，报告中明确区分。

普通通用节点支持四种协议；Resin 端点允许的协议还须满足实际 Resin 部署支持，不能仅凭通用 scheme 列表放行。

本地轮换继续保留“不需要 API、只处理已有 Resin 形式用户名、不新建绑定”的限制。新版本不得自动接管任意带点号用户名；旧管理证据不足时提供显式核对入口。

### 15.4 outbox 的生命周期

- 本地清除和切换成功后才允许任务派发。
- 删除按 SnapshotID 与租约身份去重；相同租约仍被其他绑定引用时延后。
- 204、404 视为幂等成功。
- 网络错误、429、5xx 退避；有 Retry-After 时优先采用，并有上限。
- 401/403 显示凭据问题、降低重试频率，提供更新旧快照凭据或手动重试。
- 参数错误等明确永久失败标记 needs_attention，不静默丢任务。
- 禁用或删除来源停止新绑定，但已经提交的旧清理任务继续使用旧快照。
- 清理 worker 随插件生命周期结束，可取消网络调用；重启继续处理。
- 任务完成前保留必要的凭据和快照引用。

Resin 停用不自动停止已授权的历史清理。需要暂停清理时用专门队列操作，并在状态里展示仍有待处理租约。

## 16. 管理 action 与明确的响应语义

继续使用现有 POST action 封装和 `tasks.get` / `tasks.cancel`，不新增一套 REST 或 WebSocket 请求层。

### 16.1 按任务组织 action

| action | 用途 |
| --- | --- |
| `proxy.summary` | 来源与绑定摘要、自动绑定状态 |
| `proxy.sources.list/get` | 分页来源列表（类型、启用、连通性筛选）、编辑视图 |
| `proxy.sources.validate/preview/test` | 配置验证、条件预览、网络检测 |
| `proxy.sources.test_selected/delete_selected` | 来源页多选后的异步队列式批量检测与清理删除，逐项返回结果 |
| `proxy.sources.create/update/delete` | 来源 CRUD，kind 判别具体配置 |
| `proxy.sources.import_nodes` | 带 scheme 节点批量导入，自动回退识别地址在前与 host:port@user:pass；最多 100000 条且内容不超过 16 MiB |
| `proxy.bindings.list` | 账号绑定、external、drifted、unknown 筛选 |
| `proxy.bindings.bind/clear/rotate/adopt` | 手动绑定、清除、轮换、接管 |
| `proxy.bindings.set_auto_bind` | 批量排除或恢复自动绑定 |
| `proxy.bind_unbound` | 显式批量绑定未绑定账号 |
| `proxy.template.apply` | 显式应用当前模板，支持 dry_run |
| `proxy.auto_bind.get/update/run` | 策略、开关与发现唤醒 |
| `proxy.changes.list/get/retry/cancel` | 同步与回溯任务进度 |
| `resin.cleanup.list/retry/pause` | Resin 远端清理管理 |

类型专用编辑器向来源 action 提交明确 kind DTO，服务端拒绝无关字段。共用 CRUD 不等于把所有类型字段堆进一个自由 map。

旧版 Resin 专用 action 已删除；绑定、清除、轮换和批量绑定统一使用 `proxy.bindings.*` 与 `proxy.bind_unbound`。

### 16.2 请求与保存

- 创建模板提交 URL、placeholders、value_groups、update_policy。
- 更新携带 `expected_version`，用于发现标准端点内容的并发编辑；`revision` 标识生成配置版本。
- 名称、启用、优先级、绑定上限和更新策略不推进 v/r；服务在锁内重新读取当前来源，并拒绝内容完全相同的保存。
- 验证返回规范化配置的非敏感摘要；保存再次完整验证，不接受客户端传来的 valid=true。
- dry_run 只返回影响范围，不预留、不写 state、不调用 Resin 删除 API。
- 大批量写操作返回 202 和 Task，任务引用持久化 change ID。
- 配置保存成功、同步未完成时返回 saved=true 与 change_id。

### 16.3 凭据编辑与 URL 回显

Dashboard 的节点和模板编辑器不提供独立的密码状态或 keep/set/clear 控件。完整 URL 是密码的唯一来源：URL 含密码时保存为有密码，URL 不含密码时保存为无密码。编辑表单直接回显完整 URL，用户可在 URL 中新增、修改或删除密码。

`proxy.sources.get` 仅在显式传入 `edit_fields:true` 时返回 `edit_url`。普通列表、普通详情、绑定响应、tasks/events 仍不显示凭据。Resin 的 Admin Token 与 Proxy Token 继续不回填。

密码只来自完整 URL；不存在 `password_update` 或独立的 keep/set/clear 密码协议。

Dashboard 的账号绑定列表默认只显示脱敏端点。用户点击眼睛时才调用 `proxy.bindings.endpoint` 按单个 auth 读取完整端点；完整值仅用于该行展开显示和鼠标悬浮标题，不进入普通列表响应。

### 16.4 关键请求示例

清除使用全局默认：

~~~json
{
  "action": "proxy.bindings.clear",
  "params": {"auth_ids": ["auth-1"], "exclude_auto_bind": null}
}
~~~

恢复某个 auth 自动绑定：

~~~json
{
  "action": "proxy.bindings.set_auto_bind",
  "params": {"auth_ids": ["auth-1"], "excluded": false}
}
~~~

条件预览：

~~~json
{
  "action": "proxy.sources.preview",
  "params": {"source_id": "tpl-geo", "assignments": {"country": "us"}, "count": 3}
}
~~~

### 16.5 稳定状态码和错误码

400 用于配置/输入错误，404 用于来源不存在，409 用于配置版本冲突、绑定条件冲突或同来源生成变更正在执行，502 用于外部验证调用失败，500 用于未发生 auth 提交前的持久化失败。

批量 partial 由任务结果返回，不在 200、207、409 之间任意选择。auth 已提交但 finalize 待恢复使用明确 committed 状态，避免 UI 自动重试成第二次轮换。

外部代理、无可用来源、无轮换结果、缺 scheme 都使用不同错误码，消息不包含完整 URL 或秘密。

## 17. Dashboard 信息架构与编辑流程

### 17.1 顶层保持五个入口

~~~text
概览 / 账号质量 / 代理管理 / 事件日志 / 设置
~~~

仅把“Resin 租约”替换为“代理管理”。不把日志和设置藏到新“高级”页，不改变用户已熟悉的位置。

### 17.2 推荐两个按工作任务划分的二级 Tab

~~~text
代理管理
  [账号绑定]  [代理来源]
~~~

账号绑定是默认 Tab，延续原 Resin 页以账号列表为主的工作流。代理来源集中管理三种来源，使用类型筛选：

~~~text
全部(12)  节点(8)  模板(3)  Resin(1)             [+ 添加来源 ▼]
~~~

筛选不是互斥工作模式。节点、模板、Resin 可以同时启用；新建下拉选择类型，不会切换插件的全局代理模式。

| 导航方案 | 优点 | 代价 | 决策 |
| --- | --- | --- | --- |
| 三个顶层类型 Tab | 类型入口显眼 | 导航膨胀，绑定操作重复 | 不采用 |
| 四个二级类型/绑定 Tab | 熟悉、易拆分实现 | 类型比较和混合绑定跳转多 | 可实现，但不是首选 |
| 两个任务 Tab + 来源类型筛选 | 绑定与配置职责清楚，跨类型比较集中 | 需要统一列表视图 | 推荐 |

统一列表只显示共同字段；打开后按来源类型展示专用编辑器。列表统一与编辑器专用可以兼得。

### 17.3 来源列表与编辑器

列表列控制在：选择、名称、类型、脱敏端点、启用、网络连通性、绑定数、版本、操作。配额、优先级、变量细节、旧版本引用进入详情。

- “启用状态”只表示来源是否允许参与新绑定；“网络连通性”只引用当前 revision 的最近网络隧道检测，状态统一为可连通、不可达、未检测。配置无效时仍保持未检测，并在说明中提示先修复配置，不增加“无法检测”或“不适用”状态。
- 节点和模板使用相同的连通性文案；模板检测会按当前模板生成一个临时端点并建立测试隧道，不保存临时节点，成功只代表该次生成样本可连通，不保证所有随机组合都可连通。本地 Resin 轮换没有独立端点，保持未检测并说明原因。
- 来源列表只展示已配置或已启用本地轮换的 Resin；仅有 `enabled` 但尚未配置凭据的 Resin 只保留内部运行记录，不作为列表项，也不显示“未配置”来源状态。
- 来源列表支持按类型、启用状态和网络连通性筛选；筛选只影响当前列表。
- 来源名称在节点、模板和已配置 Resin 之间必须唯一，比较时忽略首尾空白和英文大小写；创建、编辑和批量导入均执行校验。
- 来源列表支持当前页全选和跨页勾选，批量执行网络检测或强制清理删除。批量操作使用后台 Task 与有界并发队列，提交后返回 202；逐项结果保留失败原因，并可在任务详情中取消仍处于等待或执行状态的任务。
- 节点：小型抽屉，完整 URL 或分字段录入，解析反馈、启用、配额；有绑定的端点更新显示影响数量。
- 批量节点导入：支持多行粘贴和本地文件；前端先过滤无效与重复项，显示总数、有效、无效、重复和体积；单次最多 100000 条且 16 MiB，不逐行创建 DOM。
- 模板：宽编辑页/宽抽屉，三步创建流程；常用编辑直接定位段落。
- Resin：与模板共用列表行和编辑/检测/删除操作。新增类型下拉选择 Resin 后打开模板式编辑器，在通用账号格式、变量和关联组之外增加平台、Admin 地址与 Token、Proxy Token、本地轮换等连接字段；不提供独立于列表之外的配置入口或全局模式横幅。
- 三种来源均能跳到账号绑定并自动筛选该来源。
- 绑定选择器列出全部可用类型和不可用原因，支持多节点范围，不要求用户先跳到类型页。

原设置页中的 Resin 连接编辑迁入统一代理列表；设置页不保留第二份可独立修改的表单。列表中的 Resin 行用类型徽章和子行显示 API/本地模式，不在页面顶部重复全局模式状态。

动态变量名和取值使用 textContent 或现有转义函数输出；前端索引使用 Map 或无原型对象，避免把用户输入直接当 HTML、CSS 选择器或普通对象属性路径。

桌面端默认表格，保持原样式和批量管理密度；移动端以同行数据的简化卡片显示，不为桌面新增整套网格视图。

### 17.4 模板创建采用三步渐进配置

**第一步：代理格式**

填写名称与模板 URL。解析后显示协议、主机、端口、用户名模式和密码已设置状态。检测到的变量自动生成未配置项。

只有结构合法才进入下一步。显示“占位符只能在用户名中”；复制模板时保留规则，秘密字段默认保持为待填写状态，不复制到剪贴板。

**第二步：变量与关联限制**

每变量配置 enum/random；enum 填分隔符与值，random 配长度与大小写开关。需要级联时进入第 9 节的父级选择/子级输入编辑器。

独立变量和关联限制在同一页关联呈现。没有关联组时不显示空的“组配置步骤”，避免简单 session 模板也要走四五页。

**第三步：验证与保存**

展示字段错误、合法组合摘要、最大长度、条件预览。常用选项为启用与“保存修改时更新已绑定账号”；配额、优先级折叠到高级选项。

保存后若自动绑定已开启，页面明确提示新来源可能进入候选队列。若开启模板回溯，保存按钮旁展示影响的 auth 数量和异步任务语义。

### 17.5 编辑已有模板不强制重新走向导

使用与创建相同的数据模型和验证器，按“格式 / 变量与限制 / 更新策略”段落导航，可直接跳到错误或某个关联组。

周期刷新只更新列表和任务状态，不覆盖脏草稿。保存版本冲突时保留当前输入，展示新版本差异；不能为了刷新健康状态而清空密码字段或 CSV 草稿。

关联组切换视图保留同一 rows。预览请求附带草稿版本/请求序号，较晚到达的旧响应不能覆盖新的 country 条件和校验结果。

### 17.6 回溯交互

使用业务标签：

~~~text
保存修改时更新已绑定账号
默认：关闭
当前已有 18 个账号绑定此模板
~~~

开启后保存会创建任务；配置保存与全部 auth 同步分开显示。单纯开启策略而未改生成配置时，提示可点击“立即应用当前配置”，不偷偷重新生成全部账号。

有在途回溯时展示任务徽章和失败数量。重试只处理失败项；详情能看到仍用旧模板版本的账号。

### 17.7 账号绑定页与清除确认

列为账号、来源、端点、管理状态、操作。端点默认脱敏；眼睛按钮按需读取并切换完整端点，完整端点被列宽截断时通过鼠标悬浮标题显示。租约身份、generation、模板版本与替换值放到详情中，避免每一行都展示对普通节点无意义的“租约账号”。

顶部保留绑定所选、绑定全部未绑定、轮换、清除。外部代理显示“外部配置”，漂移显示“与记录不一致”，未知显示“读取失败”。

清除确认面板：

~~~text
将清除 8 个账号的代理

清除后自动绑定：
  遵循全局设置（当前：允许重新绑定）
  本次允许重新绑定
  本次暂停这些账号的自动绑定
~~~

提供明确“恢复自动绑定”入口，不要求用户理解 Excluded 数据字段。对外部代理另显示“仅清除当前本地代理，不删除 Resin 租约”。

### 17.8 自动绑定状态条和任务详情

顶部使用一条正常文档流的可折叠状态条：

~~~text
自动绑定：开启    等待 6 / 执行中 2    [立即扫描] [设置] [任务详情]
~~~

默认只占一行；待处理错误时自动展示提醒。队列、执行中、等待原因和最近扫描时间留在顶部状态条，完整成功/失败/跳过/排除统计通过状态条悬浮提示查看；设置面板只显示配置项，不混入运行状态。

不使用遮挡表格的悬浮卡，不展示两次自动绑定开关。同步和 Resin 清理通过此处及来源行徽章打开任务抽屉，复用现有 tasks 轮询；事件日志保留审计，不承担实时进度。

### 17.9 风格、响应式和可访问性

沿用现有 `--bg`、`--card`、`--surface`、`--line`、`--text`、`--muted`、状态色、`--radius`、字体与图标 sprite。继续使用 .tabs、.card、.field-grid、.toolbar、.table-wrap、.drawer。

- 桌面：级联两/三列编辑；窄屏：面包屑逐层编辑。
- 大表格仅自身容器横向滚动，不撑宽整页。
- 所有值有标签，错误定位字段/父级路径，键盘可完成组编辑。
- 不靠颜色单独表达验证或管理状态；长任务使用 aria-live。
- 关闭脏编辑器提示保存或放弃；源密码不进入浏览器持久草稿。
- Tab 有明确 role 和焦点管理；弹出面板关闭后恢复触发按钮焦点。
- 检查 375、768、1024、1440 宽度与深浅主题，不引入新 UI 框架和字体。

## 18. 敏感信息与外部修改保护

### 18.1 视图与秘密

- 普通来源列表、绑定列表、tasks/events 与错误都只显示 scheme、host、port。
- 授权配置编辑可读取用户名模式和规则，密码与 Token 返回 set 标记。
- 预览替换值来自独立预览身份；不回显完整真实凭据 URL。
- source snapshot、operation 与 cleanup payload 不直接 JSON 序列化为管理响应。
- 非标准导入格式的错误也不能回显整行密码；sanitizer 不能只处理 scheme://userinfo@。
- 管理 API 保持现有 Host 授权，不增加无鉴权的导出或揭示秘密入口。
- POSIX 保持受限目录与 0600 文件；Windows 使用适当 ACL，不能声称 chmod 数值在 Windows 等价。

### 18.2 外部并发的真实边界

账号 gate 能防止本插件的探测、冷却、绑定、清除相互覆盖；最新读取和预期代理指纹能发现已发生的外部修改。

当前 `host.auth.save` 没有原子 expected-version 写入：在最后读取与保存之间，另一个管理员仍可能更新 token 或代理。本设计不声称完全消除了该窗口。

实现时尽量缩小窗口，并做保存后核对。若需要跨管理员严格保证，须在 CPA Host 增加字段级 patch 或原子 CAS，再由 AuthStore adapter 使用；不能在本插件中用“先读后写”假装原子 CAS。

检测到漂移或未知回执进入 needs_reconcile，不能自动回滚成整份旧 JSON。

### 18.3 网络调用范围

普通生成和绑定不调用供应商网络；网络检测和 Resin 清理由独立任务执行。设置超时、TLS 正常验证、关闭不必要的重定向和环境代理。

SOCKS auth handshake 不等同于已建立可用隧道。HTTP CONNECT 200 与 407 也要区别成功和认证拒绝。可选完整连通性测试如果创建了 Resin 预览租约，应对该测试身份单独记录清理引用。

## 19. 性能、锁与规模边界

1. 来源、绑定索引按 ID 查找；来源同步从来源→绑定索引取目标，不扫描全部 auth 文件。
2. 编译缓存按生成 revision 发布不可变快照；保持旧版本到引用释放。
3. catalog 发现阶段用现有元数据缓存与增量结果，正常请求不执行代理目录扫描。
4. 所有排队、Host 查询、网络检测都有限并发；自动绑定、探测、清理分别限制资源。
5. 正常生成不维护历史组合计数，不预先展开多个组的笛卡尔积。
6. bulk change 的执行清单与逐账号意图按需创建，避免一次为百万 auth 生成完整随机 URL。
7. Snapshot/Commit 对代理域目标做局部投影与原子更新，不在每次轮询克隆全部质量记录。
8. 规模上限固定为：代理来源与单次节点导入最多 100000，单模板关联行最多 100000，单次导入与模板配置体积最多 16 MiB。达到上限时返回明确错误，不截断数据。

锁顺序统一：先账号 gate，Manager 预留锁只做短操作，释放预留锁后调用 Repository.Commit 或 Host。来源变更锁在发布版本/冻结新分配时使用，不持锁等待逐账号处理。

Repository 的事务回调不获取账号 gate，也不调用 Manager；定期 catalog 同步先取稳定快照再提交。避免原文两套不同锁顺序并存。

## 20. 验证与测试计划

测试以用户可见约束和故障恢复为目标，不只验证对象字段或函数存在。

| 范围 | 关键断言 |
| --- | --- |
| URL | 四种 scheme、仅用户名/空密码、编码一次、IPv6、保留端口、根斜杠规范化 |
| 批量导入 | 无 scheme 跳过；标准 URL 优先，失败后自动尝试地址在前与 host:port@user:pass 并正确处理 IPv4、IPv6 与百分号编码凭据；错误不回显整行凭据；100000 条与 16 MiB 边界准确；本地文件只保留有效去重项 |
| 自定义变量 | 英文、连字符、Unicode 名称均按相同规则处理，无 country/state 名字分支 |
| 枚举 | 按字面分隔符拆分，空项报错，重复规范化，大小写原样，单值合法 |
| 随机 | 36/62 字符集、指定长度、无取模偏差，不声称全局唯一 |
| 任意关联组 | 两列、三列、多字段联合约束，所有结果属于用户合法表 |
| 条件随机 | 父级候选随机而非按叶子数加权；改变取样顺序的分布明确 |
| 完整验证 | 全部行/值都检查；100000 行关联表通过边界验证；超过预览条数也不漏验；重复变量与多字节长度正确 |
| 层级/表格/CSV | 同一数据无损切换，不自动推断依赖，不丢未完成分支 |
| 绑定 | 空代理才能普通绑定；外部代理不自动接管；相同 operation 幂等 |
| 均衡 | 并发预留后节点配额不超限；有限节点集分配差距合理 |
| 轮换 | 有实际变化才增加 generation；只有一个结果时不伪造成功 |
| 同步/回溯 | 目标版本固定，成功项不回滚，失败重试复用已生成值 |
| 清除 | 全局默认、单次覆盖、已排除账号保持；finalize 失败也不抢先重新绑定 |
| 恢复 | prepared 前失败、auth 后失败、回执未知、重启、外部漂移分别处理 |
| Resin | 密码来自模板 URL/Proxy Token；清理使用不可变快照；删除去重和引用保护 |
| 自动绑定 | queued/running 同时去重，unknown 不当空值，满队列公平续扫，无来源不烧重试 |
| 生命周期 | 关闭停止新派发，已提交操作完成，重启继续恢复 |
| UI | 旧刷新不覆盖草稿，旧预览不覆盖新条件，任务结果与配置保存区分；大列表不按行创建 DOM，文件过滤结果与实际提交一致 |

国家/州例子的 `us+ZJ` 禁止，只因为它没有出现在该测试配置的合法表中。另用自定义 vendor/plan/profile、mode/pool 和中文变量测试，以防实现误写固定地理规则。

Resin 回归保留有效语义。状态写入故障测试按 prepared 与 finalize 两阶段重新注入，不能再要求“前置 state 已不可写时仍先改 auth”。

运行时协议支持需要 Host 集成测试：HTTP CONNECT、HTTPS 到代理的 TLS、SOCKS5/SOCKS5H DNS 路径、真实 auth 保存与缓存失效。配置单元测试无法代替这些验证。

文档阶段只检查规格一致性、示例数据与生成模型；正式编码时再运行对应 Go 测试、竞态测试与 UI 检查。

## 21. 推荐实施顺序

1. Endpoint、用户变量和合法关系表：先实现纯编译/生成与边界测试。
2. Resin adapter 抽取：`internal/app` 装配 TemplateProvider 和 CleanupHandler，使用通用模板密码。
3. 状态 v1、来源版本、prepared 操作与恢复：建立可靠单账号提交，再接入通用绑定。
4. 节点/模板 CRUD、前向同步和可配置模板回溯：复用同一提交路径。
5. Dashboard：先完成通用来源列表和手动绑定，再接入层级编辑、CSV 与条件预览。
6. 自动绑定 worker：复用已验证的发现、来源选择、配额与提交，补全排除与退避。
7. 租约清理、生命周期和故障恢复验收；更新 README、注册配置、管理 action。

Dashboard 与自动绑定按共用能力逐步集成，不等完成整个 UI 后才发现绑定模型缺字段。最终只保留通用绑定实现，不保留旧数据迁移或旧 action。

## 22. 验收标准

- 用户自定义任意合法变量名和关联组合，系统没有国家、州等内置依赖规则。
- 用户类型下拉只有 enum/random；关联是额外限制，随机不会被改成最少使用轮询。
- 父级下子项能批量配置，树形、表格、CSV 使用相同合法表。
- 所有配置通过完整验证后才保存，预览条数不影响验证覆盖。
- 四种协议保持声明的运行时语义，认证和 URL 编码无歧义。
- 无 scheme 的输入被忽略，系统不添加默认协议。
- 节点改动/删除同步受管理 auth，失败明确展示并可恢复。
- 模板默认保留旧绑定，开启策略后按固定版本回溯。
- 手动清除后的自动绑定符合全局默认与单次覆盖；暂停状态可显式解除。
- 自动绑定有界、去重、公平、可关闭，不覆盖外部代理、不将读取错误视为空。
- 批量节点导入和单模板关联行支持到 100000 条，16 MiB 边界明确可测。
- Resin 生成共用模板引擎，旧租约按旧清理快照删除。
- 占位符名称、清理身份、密码、来源版本与 UI 状态不混成一个含义不明的字段。
- 顶层入口与配色保持，代理管理以账号绑定/代理来源两项组织日常操作。

## 23. 已确定决策与扩展边界

本次评审沿用用户已确认的四项规则：用户名内占位符、默认不回溯但可配置、默认清除后可重绑但可配置、无 scheme 忽略。

新增澄清：占位符与关联关系完全由用户定义。示例名称只说明机制，不形成保留变量、字段字典或固定地理层级。

本设计推荐并已细化：合法组合表为单一数据源、逐级条件随机、任务型导航、来源版本快照、prepared 操作及前向同步。

暂不引入重叠关联组、脚本表达式、自动地理数据、全局唯一模板实例或动态请求级模板。若将来需要，这些属于单独扩展，不应隐藏在首版实现中。

外部并发的完整保护取决于 Host 原子 patch/CAS；文档已明确当前 adapter 能做到的范围，不能将本地锁扩大解释为宿主级事务。

## 24. 完整配置示例

### 24.1 独立枚举与随机 session

~~~json
{
  "kind": "template",
  "name": "region-session",
  "enabled": true,
  "spec": {
    "url": "socks5://test-region-{region}-sid-{session}-t-5:demo-pass@proxy.example.com:3000",
    "update_policy": "keep_bound",
    "placeholders": [
      {"name": "region", "kind": "enum", "enum": {"separator": "|", "input": "Rand|US"}},
      {"name": "session", "kind": "random", "random": {"length": 12, "case_sensitive": false}}
    ],
    "value_groups": []
  }
}
~~~

region 是用户自己的字段，Rand/US 也是用户填入的值，系统不解释它们的地理含义。

### 24.2 用户定义的国家/州关联

~~~json
{
  "kind": "template",
  "name": "geo-example",
  "enabled": true,
  "spec": {
    "url": "socks5h://xxx-country-{country}-state-{state}-sid-{session}:demo-pass@proxy.example.com:1080",
    "update_policy": "keep_bound",
    "placeholders": [
      {"name": "country", "kind": "enum", "enum": {"separator": "|"}, "group_id": "geo"},
      {"name": "state", "kind": "enum", "enum": {"separator": "|"}, "group_id": "geo"},
      {"name": "session", "kind": "random", "random": {"length": 12, "case_sensitive": false}}
    ],
    "value_groups": [
      {
        "id": "geo",
        "name": "用户定义的国家与州",
        "columns": ["country", "state"],
        "rows": [
          ["us", "ID"], ["us", "CA"], ["us", "NV"],
          ["cn", "ZJ"], ["cn", "BJ"], ["cn", "SH"], ["cn", "GD"]
        ]
      }
    ]
  }
}
~~~

将 update_policy 改为 rerender_bound，就会在生成配置修改后创建已绑定 auth 的更新任务。

### 24.3 非地理、多字段联合限制

~~~json
{
  "kind": "template",
  "name": "custom-relations",
  "enabled": true,
  "spec": {
    "url": "http://login-v-{vendor}-p-{plan}-x-{profile}:demo-pass@proxy.example.com:8080",
    "update_policy": "keep_bound",
    "placeholders": [
      {"name": "vendor", "kind": "enum", "enum": {"separator": ";"}, "group_id": "service"},
      {"name": "plan", "kind": "enum", "enum": {"separator": "|"}, "group_id": "service"},
      {"name": "profile", "kind": "enum", "enum": {"separator": ","}, "group_id": "service"}
    ],
    "value_groups": [
      {
        "id": "service",
        "name": "供应商、套餐和身份组合",
        "columns": ["vendor", "plan", "profile"],
        "rows": [
          ["A", "basic", "x1"],
          ["A", "pro", "x2"],
          ["B", "basic", "x2"],
          ["B", "pro", "x3"]
        ]
      }
    ]
  }
}
~~~

同样的 basic，在 vendor=A 时 profile=x1，在 vendor=B 时 profile=x2。因此 profile 受到 vendor 与 plan 共同限制；不是固定单父级地理模型。

用户名字段名也可由用户换成 mode、pool、some-city 或 Unicode 名称，只需保持模板与配置引用一致。

### 24.4 自动绑定与清除策略

~~~yaml
proxy:
  enabled: true
  auto_rotate_after_quality_failure: false
  auto_bind:
    enabled: true
    source_scope: all
    source_ids: []
    concurrency: 2
    queue_size: 1000
    max_per_cycle: 100
    scan_interval: "30s"
    require_healthy: false
    include_guard_disabled: false
    include_externally_disabled: false
    manual_clear_policy: rebind
    retry_initial: "1m"
    retry_max: "1h"
~~~

manual_clear_policy=pause 只改变之后手动清除的默认行为。已经显式排除的账号不会因切回 rebind 而自动解除排除。

### 24.5 Resin 的目标结构示意

~~~text
来源类型：resin
平台：Default，配置验证时解析 UUID
用户名模式：Default.{account}
代理端点：socks5h://resin:2260
密码来源：Proxy Token 固定秘密引用
清理目标：旧集成快照 + platform UUID + lease account
~~~

这里不把 Proxy Token 表示成密码占位符。普通模板中的 account/platform/session 等名字仍可由用户自行定义，只有 Resin 的内置身份生成器有专门语义。

## 25. 优化优先级与成本控制

| 优先级 | 工作 | 理由 |
| --- | --- | --- |
| P0 | 用户自定义变量/关系、随机语义、完整校验 | 直接决定配置正确性 |
| P0 | Go 无环依赖、Resin 密码映射、不可变清理快照 | 防止接口不可实现与误清理 |
| P0 | 配额预留、操作恢复、前向同步、排除原子提交 | 防止并发超额、丢元数据与清除后抢绑定 |
| P1 | 父级选择/批量子级输入、条件预览 | 减少配置错误与重复劳动 |
| P1 | 两个任务 Tab、专用编辑器、脏草稿保护 | 统一日常操作，保留现有风格 |
| P1 | CSV/TSV、失败项重试、来源筛选 | 覆盖实际批量管理场景 |
| 后续 | 可选权重、共享关系数据集、多个 Resin 实例 | 有独立产品价值，但不应延迟首版正确性 |

不为每个枚举建立持久频率计数，不同时保存树和表，不引入条件脚本引擎，不为列表视图再造一套前端框架。这些收敛点让首版的实现与测试成本可控。

## 26. 最终设计取舍

独立代理模块与合法关系表保留；原方案的随机行为、验证方式、清理 interface 和批量回滚被修正。

配置以用户定义的变量和联合关系为核心。默认层级编辑只是让有限关系数据更容易输入和浏览；相同合法表也支持非地理和多字段约束，不把 UI 的顺序误当成固定业务树。

Dashboard 用两个任务入口承载绑定和配置，三种来源可同时存在。可靠性通过来源版本、操作意图和可恢复的逐账号提交建立，原有 auth 路由事实和 Resin 异步清理行为保持明确。

## 27. 本次代码审查与实现修正

### 27.1 需求覆盖核对

| 需求 | 实现与验收重点 |
| --- | --- |
| 多个节点、多种 URL、统一端点 | `internal/proxy/endpoint.go` 统一解析、IDNA/IPv6/认证校验、编码和脱敏；标准 URL 优先，失败后自动回退地址在前与 host:port@user:pass；无 scheme 忽略；批量导入一次保存并报告 invalid/skipped/duplicate |
| 用户定义的模板变量 | 用户名内编译变量，枚举必须有分隔符与范围，随机使用无偏英文字母和数字；支持仅用户名、空密码及无认证模板 |
| 任意级联关系 | 用户指定组、列顺序和合法组合，按前缀索引逐级随机；没有国家/省份数据库或名称特判；Dashboard 支持多父路径、批量子项和 CSV/TSV |
| 完整验证后保存 | 保存重新执行静态校验，全部行和最坏用户名长度均验证；条件预览独立，不代替保存校验；错误带 code/path |
| Resin 复用与清理 | Resin Provider 共用编译模板；普通生成不访问平台 API；生成配置保存时验证平台和认证；旧租约按不可变快照清理 |
| CRUD、绑定、清除、轮换 | 来源 ID 稳定，编辑要求 expected_version；节点端点变化自动同步；删除失败保留来源；操作意图防止重复生成和跨动作误用 operation_id |
| 模板回溯 | 默认 keep_bound，显式 rerender_bound 或 proxy.template.apply 建立固定 revision 的任务；成功项不回滚，失败项保留原生成目标 |
| 自动绑定 | 默认关闭，有界队列同时去重 queued/running；不把未知读结果当空；保存前再核对开关、范围、禁用归属、来源和健康要求 |
| 手动清除策略 | 默认可重绑，可全局 pause 或单次 true/false/null 覆盖；已有明确排除不会因普通手动绑定被重置 |
| Dashboard | 账号绑定/代理来源两个任务 Tab，类型并存；分页、草稿版本保护、失效预览保护、来源选择、失败重试及清理暂停保留原配色 |

### 27.2 修正的关键问题

原实现的模板验证逐行渲染并反复扫描关系表，存在平方增长；URL 替换还可能把 `@`、`:` 等值拼入错误位置。现使用不可变编译器、完整静态验证与统一 Endpoint 序列化。相同 source ID/revision 的编译 Provider 在改名、启停和健康更新时复用。

原绑定路径多次复制整个质量状态和扫描所有账号。现在负载索引增量更新，分配同时计算已绑定、内存预留及未完成持久意图，并按 auth 去除重复计数。auth 已保存而 finalize 失败时不会释放持久配额；重启后仍占用，避免超过 max_bindings。账号目录发布为不可变切片，成功写入通过观察覆盖层立即可见。

绑定、清除、轮换和来源同步共用 prepare → 最新读取 → Host 保存 → 实际 URL 回读 → finalize。Host 回执损坏后先核对现实；回读未知或指纹漂移保留待核对操作，不排队删除旧租约。源同步按结果位置直接写入，成功项不重试，已有随机目标不重新生成。Resin 来源同样走这条路径。

Resin 的配置快照包含平台名称、UUID、地址和凭据；平台改名不会误复用旧用户名生成器。停用当前来源不停止历史清理。网络检测可能惰性创建测试租约，因此检测前持久化该测试身份的延迟清理任务；普通无网络预览不预留、不写 state。

### 27.3 内存与 CPU 控制

- 模板编译按关系行数线性增长；生成只访问编译后的候选索引，不扫描完整关系表，不展开跨组笛卡尔积。
- 常规绑定的来源选择成本与来源数量相关，与全部 auth 数量无关；不在正常模型请求里编译模板或扫描代理目录。
- 概览直接计数，绑定查询只构造当前页；发现扫描使用紧凑代理投影和轮转窗口，避免百万账号的完整 AccountState 数组。
- 代理关键提交只追加小型 fsynced journal；质量 state 的完整序列化由合并 checkpoint 负责，避免每个 auth 重复编码全部 state。
- 普通已完成意图保留约 256 条；来源任务成功项的秘密恢复材料及时释放。旧版本和快照按绑定、未完成操作、变更及清理引用保留。
- 已完成来源历史最多保留 100 条，每条最多保存 1000 个详情样本，并保留真实总数；未完成清单完整保留供重试。Task 列表只返回摘要，单任务批量结果最多 1000 条。
- 静态节点导入最多 100000 条且有效内容最多 16 MiB；单模板关联行最多 100000 条，配置体积最多 16 MiB。处理 worker、代理操作 slots、检测和发现队列均有上限。

### 27.4 验证范围与剩余边界

固定 10 个节点、改变账号数量的来源选择基准（Windows/amd64，Ryzen 7 5800H，单次 200ms）：

| 账号数量 | 每次选择耗时 | 每次分配 |
| ---: | ---: | ---: |
| 100 | 828 ns | 128 B / 1 次 |
| 10000 | 850 ns | 128 B / 1 次 |
| 100000 | 861 ns | 128 B / 1 次 |

这验证了选择路径不随全部 auth 数量增长；它不是端到端绑定吞吐承诺，后者仍包括 Host 调用和磁盘 fsync。纯模板生成在 100/1000/10000 行关系表上均保持约 696 B / 19 次分配；编译只发生在生成配置变化时。

回归覆盖真实提交后回执丢失、finalize 失败与 journal 重放、失败意图占用配额、关闭自动绑定阻止未提交写入、dry_run 不恢复写入、operation_id 跨动作拒绝、节点轮换默认范围、密码保留校验、未知 auth JSON、journal 尾行与版本 GC。纯模块另覆盖任意关系组、100000 行关联表边界、10001 节点导入、完整字节上限、认证和 TLS/CONNECT 探测。

Go 单元测试和 race 检查已运行；前端已完成隔离 V8 脚本解析、交互回归与真实 Chromium 布局检查。没有用隔离 DOM 测试或通用探测结果替代真实宿主所有传输的集成验证。Host auth 保存目前没有原子 CAS，插件只能保护本插件内部写入，并在外部竞争出现后报告待核对状态。
