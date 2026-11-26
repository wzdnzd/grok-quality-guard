"""Parse captured Responses API payloads into comparable facts."""
import json
import re

_EVENT = re.compile(r"^data: (.*)$", re.M)


def sse_events(text):
    events = []
    for raw in _EVENT.findall(text or ""):
        raw = raw.strip()
        if not raw or raw == "[DONE]":
            continue
        try:
            events.append(json.loads(raw))
        except ValueError:
            continue
    return events


def summarize_output(output):
    """Facts from a completed response.output array."""
    facts = {"items": [], "text": "", "encrypted_bytes": 0, "reasoning_items": 0, "summary_chars": 0, "tool_calls": []}
    for item in output or []:
        kind = item.get("type")
        facts["items"].append(kind)
        if kind == "reasoning":
            facts["reasoning_items"] += 1
            facts["encrypted_bytes"] += len(item.get("encrypted_content") or "")
            facts["summary_chars"] += sum(len(p.get("text") or "") for p in item.get("summary") or [])
        elif kind == "message":
            facts["text"] += "".join(p.get("text") or "" for p in item.get("content") or [] if isinstance(p, dict))
        elif kind in ("function_call", "custom_tool_call"):
            facts["tool_calls"].append({"name": item.get("name"), "arguments": item.get("arguments") or item.get("input") or ""})
    return facts


def usage_facts(usage):
    usage = usage or {}
    return {"output_tokens": usage.get("output_tokens"),
            "reasoning_tokens": (usage.get("output_tokens_details") or {}).get("reasoning_tokens"),
            "input_tokens": usage.get("input_tokens")}


def from_stream(text):
    events = sse_events(text)
    terminal = next((e for e in reversed(events) if e.get("type") in ("response.completed", "response.incomplete", "response.failed")), None)
    response = (terminal or {}).get("response") or {}
    facts = summarize_output(response.get("output"))
    if not facts["text"]:
        facts["text"] = "".join(e.get("delta") or "" for e in events if e.get("type") == "response.output_text.delta")
    facts.update(usage_facts(response.get("usage")))
    facts["terminal"] = (terminal or {}).get("type")
    facts["reasoning_delta_events"] = sum(1 for e in events if "reasoning" in (e.get("type") or "") and e.get("type", "").endswith(".delta"))
    return facts


def from_json(text):
    try:
        response = json.loads(text or "")
    except ValueError:
        return {"terminal": None, "items": [], "text": "", "encrypted_bytes": 0, "reasoning_items": 0,
                "summary_chars": 0, "tool_calls": [], "output_tokens": None, "reasoning_tokens": None, "input_tokens": None}
    facts = summarize_output(response.get("output"))
    facts.update(usage_facts(response.get("usage")))
    facts["terminal"] = response.get("status")
    facts["reasoning_delta_events"] = 0
    return facts


def facts_of(record):
    if record.get("channel") == "native":
        return from_stream("".join(chunk[1] for chunk in record.get("chunks") or []))
    return from_json(record.get("body"))
