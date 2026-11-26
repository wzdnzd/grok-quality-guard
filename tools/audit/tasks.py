"""Candidate tasks for the quality audit.

Every answer is computed here, never written by hand, so a grader mistake
cannot masquerade as a model mistake. Tasks favour multi-step work that a
reasoning model gets right and a model answering without thinking usually
gets wrong; calibration keeps only the ones that actually separate the two.
"""
import datetime
import heapq
import itertools
import json
import os
import random
import re

ANSWER_RULE = "只在最后一行输出：最终答案：<答案>，不要输出其他格式的最终行。"


def _rng(seed):
    return random.Random(seed)


def mul6():
    a, b = 738291, 604157
    return f"计算 {a} × {b} 的精确值。", str(a * b)


def mul4():
    # Short enough for the 60 s management api-call cap at low effort, still
    # easy to get wrong without working it out.
    a, b = 4729, 8361
    return f"计算 {a} × {b} 的精确值。", str(a * b)


def modpow():
    return "计算 13 的 1234 次方除以 1000 的余数。", str(pow(13, 1234, 1000))


def strings():
    count = 0
    for word in itertools.product("ABC", repeat=8):
        if all(word[i] != word[i + 1] for i in range(7)) and word.count("A") == 3:
            count += 1
    return ("由字母 A、B、C 组成、长度为 8 的字符串中，相邻两个字母都不相同，并且恰好包含 3 个 A。"
            "这样的字符串一共有多少个？"), str(count)


def date_shift():
    start = datetime.date(2026, 11, 25)
    end = start + datetime.timedelta(days=1000)
    names = "一二三四五六日"
    return (f"{start:%Y年%m月%d}日是星期{names[start.weekday()]}。从这一天起再过 1000 天是哪一天、星期几？"
            "按 YYYY-MM-DD 星期X 的格式回答。"), f"{end:%Y-%m-%d} 星期{names[end.weekday()]}"


def unique_number():
    def ok(n):
        d = [int(c) for c in str(n)]
        return (len(set(d)) == 4 and sum(d) == 17 and n % 7 == 0 and n % 11 == 0
                and d[0] > d[3] and d[1] % 2 == 0 and d[2] > d[1])
    hits = [n for n in range(1000, 10000) if ok(n)]
    assert len(hits) == 1, hits
    return ("找出唯一满足以下全部条件的四位数：四个数字互不相同；各位数字之和为 17；能被 7 整除；能被 11 整除；"
            "千位数字大于个位数字；百位数字是偶数；十位数字大于百位数字。"), str(hits[0])


TRACE_SOURCE = """def f(n):
    s = 0
    for i in range(1, n + 1):
        if i % 3 == 0 or '7' in str(i):
            s += i * (i % 5 + 1)
        else:
            s -= i // 2
    return s

print(f(137))"""


def code_trace():
    scope = {}
    exec(TRACE_SOURCE.replace("print(f(137))", "result = f(137)"), scope)
    return "下面这段 Python 代码运行后会输出什么？\n```python\n" + TRACE_SOURCE + "\n```", str(scope["result"])


def permutation():
    perm = list(itertools.islice(itertools.permutations("ABCDEFG"), 2024, 2025))[0]
    return "把字母 ABCDEFG 的所有排列按字典序从小到大编号，第 1 个是 ABCDEFG。第 2025 个排列是什么？", "".join(perm)


def shortest_path():
    rng = _rng(7)
    nodes = "ABCDEFGHI"
    edges = set()
    for i in range(len(nodes) - 1):
        edges.add((nodes[i], nodes[i + 1], rng.randint(4, 15)))
    while len(edges) < 17:
        a, b = rng.sample(nodes, 2)
        if a > b:
            a, b = b, a
        if any(e[0] == a and e[1] == b for e in edges):
            continue
        edges.add((a, b, rng.randint(2, 20)))
    graph = {n: [] for n in nodes}
    for a, b, w in edges:
        graph[a].append((b, w))
        graph[b].append((a, w))
    dist = {n: float("inf") for n in nodes}
    dist["A"] = 0
    queue = [(0, "A")]
    while queue:
        d, n = heapq.heappop(queue)
        if d > dist[n]:
            continue
        for m, w in graph[n]:
            if d + w < dist[m]:
                dist[m] = d + w
                heapq.heappush(queue, (dist[m], m))
    listing = "；".join(f"{a}-{b} 长度 {w}" for a, b, w in sorted(edges))
    return f"无向图的边如下：{listing}。求从 A 到 I 的最短路径长度。", str(dist["I"])


def base7():
    n, digits = 987654321, ""
    value = n
    while value:
        digits = str(value % 7) + digits
        value //= 7
    return f"把十进制数 {n} 转换成七进制。", digits


def subset_sum():
    values = [3, 7, 11, 14, 18, 22, 25, 29, 31, 36, 40, 43, 47, 52]
    target = 120
    count = sum(1 for r in range(len(values) + 1) for c in itertools.combinations(values, r) if sum(c) == target)
    return f"集合 {values} 中有多少个子集的元素之和恰好等于 {target}？", str(count)


def lattice():
    blocked = {(3, 2), (5, 4)}
    grid = [[0] * 7 for _ in range(9)]
    for x in range(9):
        for y in range(7):
            if (x, y) in blocked:
                continue
            if x == 0 and y == 0:
                grid[x][y] = 1
                continue
            grid[x][y] = (grid[x - 1][y] if x else 0) + (grid[x][y - 1] if y else 0)
    return ("在平面网格上从 (0,0) 走到 (8,6)，每步只能向右 (x+1) 或向上 (y+1)，并且不能经过点 (3,2) 和 (5,4)。"
            "一共有多少条不同的路径？"), str(grid[8][6])


def letter_count():
    rng = _rng(11)
    words = ["error", "never", "between", "secret", "element", "reference", "seventeen", "tree", "level", "deep"]
    text = " ".join(rng.choice(words) for _ in range(28))
    return f"下面这段文本中，小写字母 e 一共出现了多少次？\n{text}", str(text.count("e"))


CANDIDATES = {
    "mul6": mul6, "mul4": mul4, "modpow": modpow, "strings": strings, "date": date_shift, "unique": unique_number,
    "trace": code_trace, "perm": permutation, "path": shortest_path, "base7": base7, "subset": subset_sum,
    "lattice": lattice, "letters": letter_count,
}

# Agent tasks reuse the real Codex instructions and tool list, so the request
# has the same shape as production traffic. The tool history is synthetic but
# follows Codex's exec output format.
_EXEC_OK = ("Chunk ID: 5e21ab\nWall time: 1.4108 seconds\nProcess exited with code 0\nOriginal token count: 96\nOutput:\n\n"
            "Name           Length\n----           ------\nalpha.txt        1834\nbeta.txt          977\nchangelog.txt   12650\n"
            "draft.txt        4402\nnotes.txt         238\nreport.txt      30915\ntodo.txt          561\n")
_EXEC_SIZES = [1834, 977, 12650, 4402, 238, 30915, 561]
_EXEC_FAIL = ("Chunk ID: 9c04f7\nWall time: 1.1983 seconds\nProcess exited with code 1\nOriginal token count: 74\nOutput:\n"
              "Get-ChildItem : 找不到路径“C:\\work\\data”，因为该路径不存在。\n所在位置 行:1 字符: 1\n"
              "+ Get-ChildItem -Path C:\\work\\data -Filter *.txt | Select-Object Name,Length\n"
              "+ ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~\n"
              "    + CategoryInfo          : ObjectNotFound: (C:\\work\\data:String) [Get-ChildItem], ItemNotFoundException\n")
_LIST_CALL = '{"cmd":"pwsh.exe -NoLogo -NoProfile -Command \\"Get-ChildItem -Path C:\\\\work\\\\data -Filter *.txt | Select-Object Name,Length\\""}'


def _user(text):
    return {"type": "message", "role": "user", "content": [{"type": "input_text", "text": text}]}


def _call(output):
    return [{"type": "function_call", "call_id": "call_lab_1", "name": "exec_command", "arguments": _LIST_CALL},
            {"type": "function_call_output", "call_id": "call_lab_1", "output": output}]


def agent_tasks(scaffold):
    prefix = scaffold["developer"]
    extra = {"tools": scaffold["tools"], "tool_choice": "auto", "parallel_tool_calls": True}
    ask_sum = "统计 C:\\work\\data 目录下所有 .txt 文件的总大小（字节）。最后一行输出：最终答案：<总字节数>"
    return [
        {"id": "a_list", "check": "tool", "extra": extra,
         "input": prefix + [_user("列出 C:\\work\\data 目录下所有 .txt 文件的文件名和大小。")]},
        {"id": "a_sum", "check": "answer_or_tool", "answer": str(sum(_EXEC_SIZES)), "extra": extra,
         "input": prefix + [_user(ask_sum)] + _call(_EXEC_OK)},
        {"id": "a_fail", "check": "recover", "extra": extra,
         "input": prefix + [_user(ask_sum)] + _call(_EXEC_FAIL)},
        {"id": "a_html", "check": "html", "extra": extra,
         "input": prefix + [_user("帮我生成一个HTML文件，内容是通过SVG绘制一个鹈鹕骑自行车的2D动画，要求车轮转动和脚蹬动作保持物理连贯")]},
    ] + [
        # Real production requests captured from CPA, replayed verbatim.
        {"id": f"r{n}", "check": "recover", "extra": extra, "input": real["input"]}
        for n, real in enumerate(scaffold.get("real") or [], start=1)
    ]


def build(task_ids=None, scaffold_path=None):
    tasks = []
    for task_id, make in CANDIDATES.items():
        if task_ids and task_id not in task_ids:
            continue
        question, answer = make()
        tasks.append({"id": task_id, "check": "answer", "prompt": question + "\n" + ANSWER_RULE, "answer": answer})
    if scaffold_path and os.path.exists(scaffold_path):
        with open(scaffold_path, encoding="utf-8") as fh:
            scaffold = json.load(fh)
        tasks += [t for t in agent_tasks(scaffold) if not task_ids or t["id"] in task_ids]
    return tasks


_FINAL = re.compile(r"最终答案\s*[:：]\s*(.+)")


def extract(text):
    hits = _FINAL.findall(text or "")
    return hits[-1].strip() if hits else None


def normalize(value):
    if value is None:
        return None
    value = value.strip().strip("`*").strip()
    value = re.sub(r"[，,\s]", "", value)
    return value.upper()


def _valid_call(call):
    try:
        args = json.loads(call.get("arguments") or "")
    except ValueError:
        return False
    return isinstance(args, dict) and any(str(v).strip() for v in args.values())


def grade(task, facts):
    """Grade one response. facts comes from responses.facts_of."""
    text = facts.get("text") or ""
    calls = facts.get("tool_calls") or []
    check = task.get("check", "answer")
    if check == "tool":
        ok = any(_valid_call(c) for c in calls)
        return {"answer": calls[0]["name"] if calls else None, "correct": ok, "parsed": bool(calls)}
    if check == "recover":
        ok = any(_valid_call(c) for c in calls) or len(text.strip()) >= 10
        return {"answer": "tool" if calls else text[-40:], "correct": ok, "parsed": True}
    if check == "html":
        blob = text + "".join(c.get("arguments") or "" for c in calls)
        ok = "</svg>" in blob.lower() or "</html>" in blob.lower()
        return {"answer": f"{len(blob)} chars", "correct": ok, "parsed": True}
    got = extract(text)
    if check == "answer_or_tool" and got is None and any(_valid_call(c) for c in calls):
        # Checking the listing again is a reasonable next step, not a wrong answer.
        return {"answer": "tool", "correct": True, "parsed": True}
    if got is None:
        return {"answer": None, "correct": False, "parsed": False}
    return {"answer": got, "correct": normalize(got) == normalize(task["answer"]), "parsed": True}


if __name__ == "__main__":
    for task in build():
        print(task["id"], "=>", task["answer"])
