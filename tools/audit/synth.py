"""Synthetic positive controls built from healthy native captures.

None of the models these accounts may use answers without reasoning, so a
degraded response cannot be captured on demand. This strips the reasoning
item and its token bill from a healthy stream and keeps everything else, which
is how a degraded account looks to the gate. Two timings are produced:

fast  the answer starts 1.2 s after the request, as a model that skipped
      thinking would answer
slow  the original timing: the same silence, then an answer with no reasoning

Records carry synthetic=<timing> so label.py expects the gate to withhold them.
"""
import argparse
import json
import re

FAST_START_MS = 1200.0


def _events(chunks):
    """Split the stream into SSE blocks, each stamped with the arrival time of
    the chunk that completed it."""
    buffer, out = "", []
    for offset, text in chunks:
        buffer += text
        while "\n\n" in buffer:
            block, buffer = buffer.split("\n\n", 1)
            out.append((offset, block))
    if buffer.strip():
        out.append((chunks[-1][0], buffer))
    return out


def _strip(block, reasoning_index, sequence):
    data = re.search(r"^data: (.*)$", block, re.M)
    if not data:
        return block  # comments such as ": keepalive"
    event = json.loads(data.group(1))
    kind = event.get("type", "")
    item = event.get("item") or {}
    if item.get("type") == "reasoning" or kind.startswith("response.reasoning"):
        return None
    index = event.get("output_index")
    if isinstance(index, int) and reasoning_index is not None and index > reasoning_index:
        event["output_index"] = index - 1
    if kind == "response.completed":
        response = event["response"]
        response["output"] = [o for o in response.get("output") or [] if o.get("type") != "reasoning"]
        usage = response.get("usage") or {}
        details = usage.get("output_tokens_details") or {}
        billed = details.get("reasoning_tokens") or 0
        usage["output_tokens"] = max(0, (usage.get("output_tokens") or 0) - billed)
        usage["total_tokens"] = max(0, (usage.get("total_tokens") or 0) - billed)
        details["reasoning_tokens"] = 0
    if "sequence_number" in event:
        event["sequence_number"] = sequence
    return f"event: {kind}\ndata: {json.dumps(event, ensure_ascii=False)}"


def synthesize(record, timing):
    events = _events(record["chunks"])
    reasoning_index = None
    for _, block in events:
        data = re.search(r"^data: (.*)$", block, re.M)
        if data:
            event = json.loads(data.group(1))
            if (event.get("item") or {}).get("type") == "reasoning":
                reasoning_index = event.get("output_index")
                break
    if reasoning_index is None:
        return None
    first = events[0][0]
    chunks, sequence = [], 0
    for offset, block in events:
        stripped = _strip(block, reasoning_index, sequence)
        if stripped is None:
            continue
        if stripped.startswith("event:"):
            sequence += 1
        at = offset if timing == "slow" else FAST_START_MS + (offset - first)
        chunks.append([round(at, 1), stripped + "\n\n"])
    out = dict(record, chunks=chunks, synthetic=timing)
    out["trace_tail"] = f"{record.get('trace_tail')}~{timing}"
    out.pop("usage_reasoning", None)
    return out


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("captures", nargs="+")
    parser.add_argument("--tasks", default="", help="comma separated task ids to use; empty means all")
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    wanted = {t for t in args.tasks.split(",") if t}
    written = 0
    with open(args.out, "w", encoding="utf-8") as fh:
        for path in args.captures:
            for line in open(path, encoding="utf-8"):
                record = json.loads(line)
                if record.get("channel") != "native" or record.get("status") != 200:
                    continue
                if wanted and record.get("task") not in wanted:
                    continue
                for timing in ("fast", "slow"):
                    synthetic = synthesize(record, timing)
                    if synthetic:
                        fh.write(json.dumps(synthetic, ensure_ascii=False) + "\n")
                        written += 1
    print("synthetic records:", written)


if __name__ == "__main__":
    main()
