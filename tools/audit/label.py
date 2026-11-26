"""Expected verdicts: what a careful reviewer would decide, before the plugin runs.

These labels deliberately avoid the signals the plugin itself uses as its
evidence gate (encrypted byte floors, TPS thresholds). They rest on two
things the plugin never sees: whether a verifiable answer is right, and how
the same request behaves on peer accounts.

Response label
  transport   non-200, no terminal event, or unreadable body
  deliver     the user got a usable answer
  withhold    wrong answer, no reasoning, while peers on the same task mostly
              reasoned and answered right: the degraded signature
Account label (over calibrated tasks only)
  degraded    fails at least two calibrated tasks that peers almost always pass
  healthy     otherwise, given at least two calibrated samples
  unknown     too few calibrated samples
"""
import argparse
import collections
import json

import responses
import tasks as taskbook

PEER_ACCURACY = 0.85   # a task counts as calibrated when peers get it right this often
PEER_REASONING = 0.80  # and reason on it this often


def load(paths):
    records = []
    for path in paths:
        with open(path, encoding="utf-8") as fh:
            records += [json.loads(line) for line in fh if line.strip()]
    return records


def sample_id(record):
    """Must match auditRecord.id in internal/app/audit_test.go."""
    key = record.get("trace_tail") or (f"{record.get('auth_index') or ''}:{record.get('task') or ''}:{record.get('rep') or 0}:"
                                       f"{record.get('model') or ''}:{record.get('effort') or ''}")
    return f"{record.get('channel')}:{key}"


def label(records, reference_variant, scaffold_path=None):
    book = {t["id"]: t for t in taskbook.build(scaffold_path=scaffold_path)}
    rows = []
    for record in records:
        facts = responses.facts_of(record)
        task = book.get(record.get("task"))
        graded = taskbook.grade(task, facts) if task else {"correct": None, "parsed": False}
        ok_transport = record.get("status") == 200 and facts.get("terminal") in ("response.completed", "completed")
        rows.append({"id": sample_id(record), "record": record, "facts": facts, "graded": graded, "transport_ok": ok_transport,
                     "reasoned": (facts.get("reasoning_tokens") or 0) > 0})

    # Peer baseline per task from the reference variant (production settings).
    peers = collections.defaultdict(lambda: {"n": 0, "correct": 0, "reasoned": 0})
    ref_model, ref_effort = reference_variant
    for row in rows:
        rec = row["record"]
        if (not row["transport_ok"] or rec.get("synthetic") or rec.get("model") != ref_model
                or rec.get("effort") != ref_effort):
            continue
        peer = peers[rec["task"]]
        peer["n"] += 1
        peer["correct"] += bool(row["graded"]["correct"])
        peer["reasoned"] += row["reasoned"]
    calibrated = {task for task, p in peers.items()
                  if p["n"] >= 3 and p["correct"] / p["n"] >= PEER_ACCURACY and p["reasoned"] / p["n"] >= PEER_REASONING}

    for row in rows:
        rec = row["record"]
        if not row["transport_ok"]:
            row["expected"] = "transport"
        elif rec.get("synthetic"):
            # Built by synth.py from a healthy stream with its reasoning removed.
            row["expected"] = "withhold"
        elif row["graded"]["correct"]:
            row["expected"] = "deliver"
        elif rec.get("task") in calibrated and not row["reasoned"]:
            row["expected"] = "withhold"
        else:
            row["expected"] = "deliver"

    accounts = collections.defaultdict(lambda: {"calibrated": 0, "failed": 0, "samples": 0, "reasoned": 0})
    for row in rows:
        acct = row["record"].get("auth_index")
        if not acct or not row["transport_ok"] or row["record"].get("synthetic"):
            continue
        stats = accounts[acct]
        stats["samples"] += 1
        stats["reasoned"] += row["reasoned"]
        if row["record"].get("task") in calibrated and (row["record"].get("model"), row["record"].get("effort")) == reference_variant:
            stats["calibrated"] += 1
            stats["failed"] += not row["graded"]["correct"]
    account_labels = {}
    for acct, stats in accounts.items():
        if stats["calibrated"] < 2:
            verdict = "unknown"
        elif stats["failed"] >= 2:
            verdict = "degraded"
        else:
            verdict = "healthy"
        account_labels[acct] = dict(stats, label=verdict)
    return rows, peers, calibrated, account_labels


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("captures", nargs="+")
    parser.add_argument("--reference", default="grok-4.7:xhigh")
    parser.add_argument("--scaffold", default=None, help="Codex scaffold used for agent tasks")
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    reference = tuple((args.reference.split(":") + [""])[:2])
    rows, peers, calibrated, accounts = label(load(args.captures), reference, args.scaffold)
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump({
            "reference": list(reference),
            "calibrated_tasks": sorted(calibrated),
            "peers": peers,
            "samples": [{"id": r["id"], "task": r["record"].get("task"), "model": r["record"].get("model"),
                         "effort": r["record"].get("effort"), "channel": r["record"].get("channel"),
                         "auth_index": r["record"].get("auth_index"), "stratum": r["record"].get("stratum"),
                         "synthetic": r["record"].get("synthetic"),
                         "expected": r["expected"], "correct": r["graded"].get("correct"), "answer": r["graded"].get("answer"),
                         "reasoning_tokens": r["facts"].get("reasoning_tokens"), "encrypted_bytes": r["facts"].get("encrypted_bytes"),
                         "output_tokens": r["facts"].get("output_tokens"), "items": r["facts"].get("items")} for r in rows],
            "accounts": accounts,
        }, fh, ensure_ascii=False, indent=1)
    print("calibrated tasks:", sorted(calibrated))
    print("expected:", dict(collections.Counter(r["expected"] for r in rows)))
    print("accounts:", dict(collections.Counter(a["label"] for a in accounts.values())))


if __name__ == "__main__":
    main()
