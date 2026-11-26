"""Summarize a pinned screening run per plugin state.

For each stratum (the state the plugin had put the account in) report how
many accounts answered, reasoned, met the encrypted-reasoning floor and got
the answer right. A stratum the plugin cooled for quality reasons should look
worse than the healthy one if the cooling was justified.
"""
import argparse
import collections
import json

import responses
import tasks as taskbook

ENCRYPTED_BYTES_PER_TOKEN = 2  # production policy encrypted_bytes_per_reasoning_token
MIN_ENCRYPTED_BYTES = 256      # production policy min_encrypted_bytes


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("captures", nargs="+")
    args = parser.parse_args()
    book = {t["id"]: t for t in taskbook.build()}
    strata = collections.defaultdict(collections.Counter)
    latest = {}
    for path in args.captures:
        for line in open(path, encoding="utf-8"):
            record = json.loads(line)
            if record.get("channel") != "pinned":
                continue
            # A retry replaces an earlier transport failure for the same account and task.
            key = (record.get("auth_index"), record.get("task"))
            if key in latest and latest[key].get("status") == 200 and record.get("status") != 200:
                continue
            latest[key] = record
    rows = []
    for record in latest.values():
        stats = strata[record.get("stratum")]
        stats["accounts"] += 1
        if record.get("status") != 200:
            stats["transport_error"] += 1
            continue
        facts = responses.facts_of(record)
        graded = taskbook.grade(book[record["task"]], facts)
        reasoning = facts.get("reasoning_tokens") or 0
        encrypted = facts.get("encrypted_bytes") or 0
        floor = max(MIN_ENCRYPTED_BYTES, reasoning * ENCRYPTED_BYTES_PER_TOKEN)
        stats["answered"] += 1
        stats["reasoned"] += reasoning > 0
        stats["floor_met"] += reasoning > 0 and encrypted >= floor
        stats["correct"] += bool(graded.get("correct"))
        stats["no_reasoning_and_wrong"] += reasoning == 0 and not graded.get("correct")
        rows.append((record.get("stratum"), record.get("auth_index"), reasoning, encrypted, graded.get("answer"),
                     graded.get("correct")))
    header = ["stratum", "accounts", "transport_error", "answered", "reasoned", "floor_met", "correct",
              "no_reasoning_and_wrong"]
    print(" | ".join(header))
    total = collections.Counter()
    for stratum, stats in sorted(strata.items()):
        total.update(stats)
        print(" | ".join([stratum or "-"] + [str(stats[h]) for h in header[1:]]))
    print(" | ".join(["TOTAL"] + [str(total[h]) for h in header[1:]]))
    suspicious = [r for r in rows if not r[5] or r[2] == 0]
    if suspicious:
        print("\nwrong or unreasoned answers")
        for stratum, account, reasoning, encrypted, answer, correct in sorted(suspicious, key=lambda r: str(r[0])):
            print(f"  {stratum:36s} {account} rsn={reasoning} enc={encrypted} answer={answer!r} correct={correct}")


if __name__ == "__main__":
    main()
