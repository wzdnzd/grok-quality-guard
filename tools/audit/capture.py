"""Capture raw Grok responses for the quality audit.

Runs on the CPA host. Two channels, both bypassing the plugin:

native  POST /v1/responses on CPA with the plugin's probe header, so the
        plugin neither routes nor passively audits the request. CPA picks
        the account; every read is timestamped so the plugin's timing-based
        checks can be replayed. With --usage-db the serving account is
        resolved through the usage store, whose request_id is the tail of
        X-Cpa-Trace-Id.
pinned  CPA management api-call against one chosen account, non-streaming.
        Reaches any account, cooled ones included, but carries no timing.

Only token counts, text and event payloads are written; credentials and
management keys stay in memory. External paths and secrets come from explicit
flags or documented defaults. The only installation-layout assumption is the
optional --cpa-dir fallback for .env and config.yaml.
"""

import argparse
import collections
import concurrent.futures as cf
import hashlib
import http.client
import json
import os
import random
import sqlite3
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

import tasks as taskbook

DEFAULT_CPA_DIR = Path("/opt/applications/cliproxyapi")
DEFAULT_SCAFFOLD_FILE = Path("codex-scaffold.json")
DEFAULT_HEADERS_FILE = Path("upstream-constants.json")
MANAGEMENT_KEY_ENV = "CPA_MANAGEMENT_KEY"
CLIENT_KEY_ENV = "CPA_CLIENT_KEY"
UPSTREAM_URL = "https://cli-chat-proxy.grok.com/v1/responses"
PROBE_HEADER = "X-Grok-Quality-Probe"
# Identity headers CPA sends to the CLI chat proxy, as seen in its upstream
# request log. The two constant-valued auth headers come from the CPA source
# and are normally supplied with --headers from a 0600 file.
UPSTREAM_IDENTITY = {
    "User-Agent": "grok-shell/1.0.89 (linux; x86_64)",
    "X-Grok-Client-Identifier": "grok-shell",
    "X-Grok-Client-Version": "1.0.89",
}
_write_lock = threading.Lock()


def _clean(value):
    return (value or "").strip()


def _unquote(value):
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
    return value


def read_env_value(path, name):
    """Read one KEY=value entry from an env file, or return None if absent."""
    path = Path(path)
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except FileNotFoundError:
        return None
    except OSError as exc:
        raise SystemExit(f"cannot read env file {path}: {exc}") from exc
    for raw in lines:
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[len("export ") :].lstrip()
        key, separator, value = line.partition("=")
        if separator and key.strip() == name:
            value = _unquote(value)
            if value:
                return value
    return None


def _yaml_scalar(value):
    value = value.strip()
    if value.startswith("[") and value.endswith("]"):
        value = value[1:-1].strip()
        if not value:
            return None
        value = value.split(",", 1)[0].strip()
    value = _unquote(value)
    return value or None


def read_client_key(path):
    """First client key under access.api-keys in a CPA config."""
    path = Path(path)
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except FileNotFoundError:
        return None
    except OSError as exc:
        raise SystemExit(f"cannot read config file {path}: {exc}") from exc
    in_access, in_keys = False, False
    keys_indent = 0
    for raw in lines:
        stripped = raw.strip()
        if not stripped or stripped.startswith("#"):
            continue
        indent = len(raw) - len(raw.lstrip(" "))
        if indent == 0:
            in_access = stripped == "access:"
            in_keys = False
            continue
        if not in_access:
            continue
        if not in_keys:
            if stripped.startswith("api-keys:"):
                tail = stripped[len("api-keys:") :].strip()
                if tail:
                    return _yaml_scalar(tail)
                in_keys = True
                keys_indent = indent
            continue
        if indent > keys_indent and stripped.startswith("-"):
            key = _yaml_scalar(stripped[1:])
            if key:
                return key
            continue
        in_keys = False
    return None


def resolve_key(cli_value, env_name, file_path, read_file, label, flag):
    """Resolve a secret from CLI, then env, then file, then fail."""
    value = _clean(cli_value)
    if value:
        return value
    value = _clean(os.environ.get(env_name))
    if value:
        return value
    value = _clean(read_file(Path(file_path)))
    if value:
        return value
    raise SystemExit(
        f"{label} not found: pass {flag}, set {env_name}, or store it in {file_path}"
    )


def management_key(args):
    return resolve_key(
        args.cpa_management_key,
        MANAGEMENT_KEY_ENV,
        args.cpa_dir / ".env",
        lambda path: read_env_value(path, MANAGEMENT_KEY_ENV),
        "CPA management key",
        "--cpa-management-key",
    )


def client_key(args):
    return resolve_key(
        args.client_key,
        CLIENT_KEY_ENV,
        args.cpa_dir / "config.yaml",
        read_client_key,
        "CPA client key",
        "--client-key",
    )


def cpa_host():
    out = subprocess.check_output(
        [
            "docker",
            "inspect",
            "-f",
            "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}",
            "cliproxyapi",
        ]
    )
    return out.decode().split()[0]


def auth_index(file_name):
    return hashlib.sha256(f"xai:/root/.cli-proxy-api/{file_name}".encode()).hexdigest()[
        :16
    ]


def plugin_accounts(state_file):
    path = Path(state_file)
    try:
        state = json.loads(path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise SystemExit(f"cannot read state file {path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise SystemExit(f"invalid state JSON in {path}: {exc}") from exc
    accounts = state.get("accounts") if isinstance(state, dict) else None
    if not isinstance(accounts, dict):
        raise SystemExit(f"state file {path} has no accounts object")
    result = {}
    for name, value in accounts.items():
        if not isinstance(value, dict):
            raise SystemExit(
                f"state file {path} has a non-object account entry: {name}"
            )
        result[auth_index(name)] = {
            "file": name,
            "state": value.get("state"),
            "last_reason": value.get("last_reason") or "",
        }
    return result


def message_input(prompt):
    return [
        {
            "type": "message",
            "role": "user",
            "content": [{"type": "input_text", "text": prompt}],
        }
    ]


def client_body(task, job, stream):
    """The Responses request a client such as Codex would send for one job."""
    body = {
        "model": job["model"],
        "stream": stream,
        "store": False,
        "input": task.get("input") or message_input(task["prompt"]),
        "include": ["reasoning.encrypted_content"],
    }
    body.update(task.get("extra") or {})
    if job.get("effort"):
        body["reasoning"] = {"effort": job["effort"]}
    return body


def resolve_account(db_path, request_id, attempts=20):
    db = sqlite3.connect(Path(db_path).resolve().as_uri() + "?mode=ro", uri=True)
    try:
        for _ in range(attempts):
            row = db.execute(
                "select auth_index, output_tokens, reasoning_tokens from usage_events where request_id=? "
                "order by timestamp_ms desc limit 1",
                (request_id,),
            ).fetchone()
            if row:
                return {
                    "auth_index": row[0],
                    "usage_output": row[1],
                    "usage_reasoning": row[2],
                }
            time.sleep(1)
    finally:
        db.close()
    return {}


def capture_native(host, client_key, usage_db, task, job):
    body = client_body(task, job, stream=True)
    body["prompt_cache_key"] = str(uuid.uuid4())
    record = dict(job, channel="native", chunks=[], request=body)
    # Grok sends nothing until reasoning ends, and xhigh can think past five
    # minutes; a shorter read timeout throws away a response already paid for.
    conn = http.client.HTTPConnection(host, 8317, timeout=600)
    started = time.monotonic()
    try:
        conn.request(
            "POST",
            "/v1/responses",
            body=json.dumps(body, ensure_ascii=False).encode(),
            headers={
                "Authorization": "Bearer " + client_key,
                "Content-Type": "application/json",
                "Accept": "text/event-stream",
                PROBE_HEADER: "1",
                "Session-Id": str(uuid.uuid4()),
            },
        )
        resp = conn.getresponse()
        record["status"] = resp.status
        trace = resp.getheader("X-Cpa-Trace-Id") or ""
        record["trace_tail"] = trace[-36:]
        while True:
            data = resp.read1(65536)
            if not data:
                break
            record["chunks"].append(
                [
                    round((time.monotonic() - started) * 1000, 1),
                    data.decode("utf-8", "replace"),
                ]
            )
    except Exception as exc:  # noqa: BLE001 - record the failed attempt
        record["status"] = "error"
        record["error"] = f"{type(exc).__name__}: {exc}"[:200]
    finally:
        conn.close()
    record["elapsed_ms"] = round((time.monotonic() - started) * 1000, 1)
    if record.get("trace_tail") and usage_db is not None:
        record.update(resolve_account(usage_db, record["trace_tail"]))
    return record


def capture_pinned(host, mgmt_key, headers, task, job):
    upstream = client_body(task, job, stream=False)
    call_headers = dict(
        headers,
        **{
            "Authorization": "Bearer $TOKEN$",
            "X-Grok-Conv-Id": str(uuid.uuid4()),
            "Content-Type": "application/json",
            "Accept": "application/json",
        },
    )
    payload = {
        "auth_index": job["auth_index"],
        "method": "POST",
        "url": UPSTREAM_URL,
        "header": call_headers,
        "data": json.dumps(upstream, ensure_ascii=False),
    }
    request = urllib.request.Request(
        f"http://{host}:8317/v8/management/requests/api-call",
        method="POST",
        data=json.dumps(payload).encode(),
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + mgmt_key,
        },
    )
    record = dict(job, channel="pinned", request=upstream)
    started = time.monotonic()
    try:
        with urllib.request.urlopen(request, timeout=90) as resp:
            result = json.loads(resp.read())
        record["status"] = result.get("status_code")
        record["body"] = result.get("body") or ""
    except Exception as exc:  # noqa: BLE001 - record the failed attempt
        record["status"] = "error"
        record["error"] = f"{type(exc).__name__}: {exc}"[:200]
        if isinstance(exc, urllib.error.HTTPError):
            # The management API explains its own failures (timeout, proxy,
            # unknown auth) in the body; keep it to tell them apart.
            try:
                record["error_body"] = exc.read().decode("utf-8", "replace")[:500]
            except Exception:  # noqa: BLE001, S110 - error body is best-effort
                pass
    record["elapsed_ms"] = round((time.monotonic() - started) * 1000, 1)
    return record


def retry_jobs(path):
    """Jobs from a previous pinned run that ended in a transport error and never
    succeeded later, so a retry targets exactly the same accounts and tasks."""
    done, failed = set(), {}
    with open(path, encoding="utf-8") as fh:
        lines = fh.readlines()
    for line in lines:
        record = json.loads(line)
        key = (
            record.get("auth_index"),
            record.get("task"),
            record.get("model"),
            record.get("effort"),
            record.get("rep"),
        )
        if record.get("status") == 200:
            done.add(key)
        else:
            failed[key] = {
                k: record.get(k)
                for k in ("task", "model", "effort", "rep", "auth_index", "stratum")
            }
    return [job for key, job in failed.items() if key not in done]


def upstream_headers(constants_path):
    """Headers CPA sends upstream, minus anything per-account."""
    path = Path(constants_path)
    try:
        constants = json.loads(path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise SystemExit(f"cannot read headers file {path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise SystemExit(f"invalid headers JSON in {path}: {exc}") from exc
    if not isinstance(constants, dict) or not all(
        isinstance(key, str) and isinstance(value, str)
        for key, value in constants.items()
    ):
        raise SystemExit(f"headers file {path} must be a JSON object of string values")
    headers = dict(UPSTREAM_IDENTITY)
    headers.update(constants)
    return headers


def stratified_accounts(
    total,
    seed,
    state_file,
    quotas=None,
    exclude_states=("credential_invalid", "externally_disabled", "quota_exhausted"),
):
    """Sample accounts per plugin state. quotas maps a stratum to a fixed count
    and overrides the proportional share, so small strata can be oversampled."""
    accounts = plugin_accounts(state_file)
    strata = collections.defaultdict(list)
    for idx, info in accounts.items():
        if info["state"] in exclude_states:
            continue
        key = (
            info["state"]
            if info["state"] != "cooling"
            else "cooling:" + info["last_reason"]
        )
        strata[key].append(idx)
    rng = random.Random(seed)
    population = sum(len(v) for v in strata.values())
    picked = []
    for key, members in sorted(strata.items()):
        share = (quotas or {}).get(key)
        if share is None:
            share = max(3, round(total * len(members) / population))
        rng.shuffle(members)
        picked += [(key, idx) for idx in members[:share]]
    return picked, accounts


def run(jobs, worker, out_path, concurrency):
    done = 0
    with open(out_path, "a", encoding="utf-8") as fh, cf.ThreadPoolExecutor(
        max_workers=concurrency
    ) as pool:
        for future in cf.as_completed([pool.submit(worker, job) for job in jobs]):
            record = future.result()
            with _write_lock:
                fh.write(json.dumps(record, ensure_ascii=False) + "\n")
                fh.flush()
            done += 1
            print(
                f"[{done}/{len(jobs)}] {record.get('task')} {record.get('model')}/{record.get('effort')} "
                f"status={record.get('status')} acct={str(record.get('auth_index'))[:8]} {record.get('elapsed_ms')}ms",
                flush=True,
            )


def _positive_int(raw):
    try:
        value = int(raw)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("must be an integer") from exc
    if value < 1:
        raise argparse.ArgumentTypeError("must be at least 1")
    return value


def _nonnegative_int(raw):
    try:
        value = int(raw)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("must be an integer") from exc
    if value < 0:
        raise argparse.ArgumentTypeError("must be at least 0")
    return value


def _comma_list(raw):
    return [item.strip() for item in raw.split(",") if item.strip()]


def _parse_variants(parser, raw):
    variants = []
    for item in _comma_list(raw):
        model, _, effort = item.partition(":")
        model, effort = model.strip(), effort.strip()
        if not model:
            parser.error(f"invalid --variants item: {item!r}; want model[:effort]")
        variants.append((model, effort))
    if not variants:
        parser.error("--variants must contain at least one model")
    return variants


def _parse_quotas(parser, raw):
    quotas = {}
    for item in _comma_list(raw):
        key, separator, count = item.rpartition("=")
        if not separator or not key.strip():
            parser.error(f"invalid --quota item: {item!r}; want stratum=count")
        try:
            value = int(count)
        except ValueError:
            parser.error(f"invalid --quota count: {item!r}")
        if value < 0:
            parser.error(f"invalid --quota count: {item!r}; must be at least 0")
        quotas[key.strip()] = value
    return quotas


def _build_parser():
    parser = argparse.ArgumentParser(
        description="Capture real Grok responses for the quality audit.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("mode", choices=["native", "pinned"])
    parser.add_argument(
        "--tasks", default="", help="comma separated task ids; empty means every task"
    )
    parser.add_argument(
        "--skip", default="", help="comma separated task ids to leave out"
    )
    parser.add_argument(
        "--variants",
        default="grok-4.7:xhigh",
        help="model:effort,... ; empty effort omits reasoning",
    )
    parser.add_argument("--repeats", type=_positive_int, default=1)
    parser.add_argument(
        "--accounts",
        type=_nonnegative_int,
        default=0,
        help="pinned: stratified sample size",
    )
    parser.add_argument(
        "--quota",
        default="",
        help="pinned: stratum=count,... overriding the proportional share",
    )
    parser.add_argument(
        "--retry",
        type=Path,
        default=None,
        help="pinned: rerun the failed jobs of this capture file",
    )
    parser.add_argument("--seed", type=int, default=20261125)
    parser.add_argument(
        "--usage-db",
        type=Path,
        default=None,
        help="native: OMC usage SQLite database used to resolve the serving account; omit to record only trace_tail",
    )
    parser.add_argument(
        "--state-file",
        type=Path,
        default=None,
        help="pinned: plugin state.json used for stratified account sampling. required unless --retry",
    )
    parser.add_argument(
        "--cpa-dir",
        type=Path,
        default=DEFAULT_CPA_DIR,
        help="CPA install directory; key file fallback reads .env and config.yaml under it",
    )
    parser.add_argument(
        "--cpa-management-key",
        default=None,
        help="CPA management key; overrides CPA_MANAGEMENT_KEY and <cpa-dir>/.env",
    )
    parser.add_argument(
        "--client-key",
        default=None,
        help="CPA access key; overrides CPA_CLIENT_KEY and <cpa-dir>/config.yaml",
    )
    parser.add_argument(
        "--scaffold",
        type=Path,
        default=DEFAULT_SCAFFOLD_FILE,
        help="Codex instructions and tools for agent tasks; omit the file to skip those tasks",
    )
    parser.add_argument(
        "--headers",
        type=Path,
        default=DEFAULT_HEADERS_FILE,
        help="pinned: JSON file with constant upstream headers",
    )
    parser.add_argument("--concurrency", type=_positive_int, default=2)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--dry", action="store_true")
    return parser


def _require_file(parser, path, flag):
    if path is None:
        parser.error(f"{flag} is required for this mode")
    if not path.is_file():
        parser.error(f"{flag} file not found: {path}")
    return path


def _validate_args(parser, args):
    if args.mode == "native":
        if args.state_file is not None:
            parser.error("--state-file is only valid with pinned mode")
        if args.retry is not None:
            parser.error("--retry is only valid with pinned mode")
        if args.accounts or args.quota:
            parser.error("--accounts and --quota are only valid with pinned mode")
        if args.usage_db is not None:
            _require_file(parser, args.usage_db, "--usage-db")
    else:
        if args.usage_db is not None:
            parser.error("--usage-db is only valid with native mode")
        _require_file(parser, args.headers, "--headers")
        if args.retry is not None:
            _require_file(parser, args.retry, "--retry")
            if args.accounts or args.quota:
                parser.error("--retry cannot be combined with --accounts or --quota")
        else:
            _require_file(parser, args.state_file, "--state-file")
    if args.out.exists() and args.out.is_dir():
        parser.error(f"--out is a directory: {args.out}")
    if not args.out.parent.is_dir():
        parser.error(f"--out parent directory does not exist: {args.out.parent}")


def main(argv=None):
    parser = _build_parser()
    args = parser.parse_args(argv)
    _validate_args(parser, args)

    selected = _comma_list(args.tasks)
    skipped = set(_comma_list(args.skip))
    if not args.scaffold.is_file():
        print(
            f"warning: --scaffold file not found; agent tasks will be skipped: {args.scaffold}",
            file=sys.stderr,
        )
    book = {
        t["id"]: t
        for t in taskbook.build(selected or None, scaffold_path=args.scaffold)
        if t["id"] not in skipped
    }
    missing = sorted(set(selected) - set(book))
    if missing:
        parser.error(f"unknown or unavailable task ids: {', '.join(missing)}")
    variants = _parse_variants(parser, args.variants)

    jobs = []
    key = None
    headers = None
    if args.mode == "native":
        key = client_key(args)
        if args.usage_db is None:
            print(
                "warning: --usage-db not set; native captures will omit auth_index and usage data",
                file=sys.stderr,
            )
        for task_id in book:
            for model, effort in variants:
                for rep in range(args.repeats):
                    jobs.append(
                        {"task": task_id, "model": model, "effort": effort, "rep": rep}
                    )
    elif args.retry is not None:
        key = management_key(args)
        headers = upstream_headers(args.headers)
        jobs = [job for job in retry_jobs(args.retry) if job["task"] in book]
    else:
        quotas = _parse_quotas(parser, args.quota)
        picked, _ = stratified_accounts(
            args.accounts, args.seed, args.state_file, quotas
        )
        for stratum, idx in picked:
            for task_id in book:
                for model, effort in variants:
                    for rep in range(args.repeats):
                        jobs.append(
                            {
                                "task": task_id,
                                "model": model,
                                "effort": effort,
                                "rep": rep,
                                "auth_index": idx,
                                "stratum": stratum,
                            }
                        )
        print("strata:", dict(collections.Counter(s for s, _ in picked)))
        key = management_key(args)
        headers = upstream_headers(args.headers)

    random.Random(args.seed).shuffle(jobs)
    print(f"{len(jobs)} jobs over tasks {sorted(book)}", flush=True)
    if args.dry:
        return

    host = cpa_host()
    if args.mode == "native":
        worker = lambda job: capture_native(
            host, key, args.usage_db, book[job["task"]], job
        )
    else:
        worker = lambda job: capture_pinned(host, key, headers, book[job["task"]], job)
    run(jobs, worker, args.out, args.concurrency)


if __name__ == "__main__":
    sys.exit(main())
