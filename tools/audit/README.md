# audit

用真实 Grok 响应检验质量门的判定是否符合预期。不参与插件构建：这里只有 Python 脚本，Go 侧重放在 `internal/app/audit_test.go`，带 `audit` build tag，不加 `-tags audit` 时 `go build` / `go test ./...` 都不会编译它。

## 流程

1. **采集**（在 CPA 主机上运行 `capture.py`）
   - `native`：经 CPA `/v1/responses` 发流式请求，带 `X-Grok-Quality-Probe: 1`，插件既不接管路由也不做被动审计，线上账号状态不受影响。逐块记录到达时间，账号由 CPA 选择，通过 OMC 用量库里的 `request_id`（即 `X-Cpa-Trace-Id` 末 36 位）识别
   - `pinned`：经 CPA 管理接口 `api-call` 定向请求指定账号（可覆盖已冷却账号），非流式，无时序，管理接口有 60 秒上限
2. **打标**（`label.py`）：在运行插件之前给出期望判定，依据插件不使用的信号：可验证答案是否正确、同一题在同伴账号上的表现
3. **重放**（本地 Go 测试）：把每个样本按原始节奏喂给真实的透明质量门，记录放行或扣留及判定类别
4. **对比**（`compare.py`）：期望与实际的混淆矩阵和逐条差异

## 文件

| 文件 | 作用 |
| --- | --- |
| `tasks.py` | 题库与评分；算术题答案全部由程序计算，agent 题复用真实 Codex 指令和工具 |
| `capture.py` | 采集 |
| `responses.py` | 把流式或非流式响应解析成可比较的事实 |
| `label.py` | 期望判定 |
| `compare.py` | 期望与插件判定对比 |
| `data/` | 本地数据目录，已被 `.gitignore` 忽略 |

## 采集参数与密钥

`capture.py` 只通过显式参数定位外部文件，相对路径按当前工作目录解析；唯一的目录层级假设是密钥回退时读取 `--cpa-dir` 下的 `.env` 和 `config.yaml`。模式必需的文件缺失或参数非法时直接报错，`--scaffold` 缺失只跳过 agent 题。

| 参数 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--usage-db` | 否 | 无 | native 模式下用于反查 serving account；省略时只记录 `trace_tail` |
| `--state-file` | pinned 分层采样时必填 | 无 | 使用 `--retry` 时不需要 |
| `--cpa-dir` | 否 | `/opt/applications/cliproxyapi` | 仅用于密钥文件回退：读取其中的 `.env` 和 `config.yaml` |
| `--scaffold` | 否 | `codex-scaffold.json` | 文件不存在时跳过 agent 题 |
| `--headers` | pinned 时该文件必须存在 | `upstream-constants.json` | 上游固定 header 的 JSON 对象 |
| `--cpa-management-key` | 否 | 无 | CPA 管理密钥，优先级最高 |
| `--client-key` | 否 | 无 | CPA client key，优先级最高 |

密钥解析顺序严格为：命令行参数 -> 环境变量 `CPA_MANAGEMENT_KEY` / `CPA_CLIENT_KEY` -> `--cpa-dir` 下的 `.env` / `config.yaml` -> 报错。`--repeats`、`--concurrency` 必须大于等于 1，`--accounts` 必须大于等于 0，`--quota` 必须是 `stratum=count`。

## 运行

服务器上的实验目录放在 `/root/gqg-lab`（不要放 `/tmp`，重启会清空）。机器只有 2 核 2 GB，并发保持 2。

```sh
# 服务器
python3 capture.py native \
  --usage-db /opt/applications/cliproxyapi/data/ohmycpa/oh-my-cpa.db \
  --cpa-dir /opt/applications/cliproxyapi \
  --scaffold data/codex-scaffold.json \
  --skip trace --variants grok-4.7:xhigh --repeats 3 --out calib.jsonl
python3 capture.py pinned \
  --state-file /opt/applications/cliproxyapi/plugins/data/grok-quality-guard/state.json \
  --cpa-dir /opt/applications/cliproxyapi \
  --headers data/upstream-constants.json \
  --tasks modpow,lattice --accounts 160 --out screen.jsonl

# 本地
python tools/audit/label.py data/calib.jsonl --scaffold data/codex-scaffold.json --out data/labels.json
AUDIT_CAPTURES=$PWD/tools/audit/data/calib.jsonl \
AUDIT_OUT=$PWD/tools/audit/data/verdicts.jsonl \
AUDIT_POLICY=$PWD/tools/audit/data/policy.json \
go test -tags audit -run TestQualityGateAudit -timeout 3h ./internal/app/
python tools/audit/compare.py data/labels.json data/verdicts.jsonl
```

重放按样本的原始到达时间回放，长样本耗时等于原始响应时长。`AUDIT_SHARD=i/n` 可拆成多个进程并行。

对照样本（非推理模型、关闭推理等）重放时一律当作对 `grok-4.7` xhigh 请求的响应，用来模拟降智账号在插件视角下的样子；可用 `AUDIT_REQUEST_MODEL` / `AUDIT_REQUEST_EFFORT` 改变。
