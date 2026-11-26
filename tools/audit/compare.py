"""Join expected labels with replayed plugin verdicts and report the gaps."""
import argparse
import collections
import json


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("labels")
    parser.add_argument("verdicts", nargs="+")
    parser.add_argument("--details", type=int, default=20)
    args = parser.parse_args()

    labels = json.load(open(args.labels, encoding="utf-8"))
    expected = {s["id"]: s for s in labels["samples"]}
    actual = {}
    for path in args.verdicts:
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                if line.strip():
                    verdict = json.loads(line)
                    actual[verdict["id"]] = verdict

    matrix = collections.Counter()
    by_class = collections.Counter()
    by_variant = collections.defaultdict(collections.Counter)
    gaps = []
    for sample_id, sample in expected.items():
        verdict = actual.get(sample_id)
        if verdict is None or sample["expected"] == "transport" or verdict["decision"] == "skipped":
            continue
        want, got = sample["expected"], verdict["decision"]
        matrix[(want, got)] += 1
        variant = f"{sample['model']}:{sample['effort']}"
        if sample.get("synthetic"):
            variant += f" synthetic-{sample['synthetic']}"
        by_variant[variant][(want, got)] += 1
        if want != got:
            by_class[(want, got, verdict.get("class") or "-")] += 1
            gaps.append((sample, verdict))

    print("calibrated tasks:", labels["calibrated_tasks"])
    print("\nexpected -> plugin")
    for (want, got), count in sorted(matrix.items()):
        print(f"  {want:9s} -> {got:9s} {count}")
    print("\nper variant")
    for variant, counts in sorted(by_variant.items()):
        print(f"  {variant:32s}", {f"{w}->{g}": c for (w, g), c in sorted(counts.items())})
    print("\nmismatches by plugin class")
    for (want, got, cls), count in by_class.most_common():
        print(f"  expected {want:8s} got {got:8s} class={cls:28s} {count}")
    print(f"\nfirst {args.details} mismatches")
    for sample, verdict in gaps[:args.details]:
        print(f"  {sample['task']:8s} {sample['model']}:{sample['effort']:5s} correct={sample['correct']!s:5s} "
              f"rsn={sample['reasoning_tokens']} enc={sample['encrypted_bytes']} out={sample['output_tokens']} "
              f"-> {verdict['decision']} {verdict.get('class')} ({verdict.get('reason')})")

    accounts = labels.get("accounts", {})
    if accounts:
        print("\naccount labels:", dict(collections.Counter(a["label"] for a in accounts.values())))


if __name__ == "__main__":
    main()
