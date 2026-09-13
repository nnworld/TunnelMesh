#!/usr/bin/env python3
"""Regenerate the documentation record indexes.

docs/ 下的实施计划、设计规格、PR 记录和 ADR 会持续增长，手工维护索引必然漂移。
本脚本扫描这四个目录，按文件名中的日期与 feature slug 建立确定性交叉引用，
并重新生成四份 README.md 索引。脚本是幂等的：重复执行产出相同内容。

只生成索引文件本身，不修改任何记录正文。新增记录后执行：

    python3 scripts/gen_doc_index.py
"""

from __future__ import annotations

import re
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
GENERATED = "<!-- 本文件由 scripts/gen_doc_index.py 生成，请勿手工编辑。 -->"

DIRS = {
    "plan": ROOT / "docs/superpowers/plans",
    "spec": ROOT / "docs/superpowers/specs",
    "pr": ROOT / "docs/pull-requests",
    "adr": ROOT / "docs/architecture/adr",
}

KIND_LABEL = {"plan": "计划", "spec": "规格", "pr": "PR", "adr": "ADR"}
SLUG_SUFFIX = {"plan": ("-implementation",), "spec": ("-design",), "pr": (), "adr": ()}
FILENAME = re.compile(r"^(\d{4}-\d{2}-\d{2})-(.+)$")
BODY_REF = re.compile(r"(?:plans|specs|pull-requests|adr)/([A-Za-z0-9._-]+\.md)")
KIND_BY_DIRNAME = {"plans": "plan", "specs": "spec", "pull-requests": "pr", "adr": "adr"}


def relative(from_kind: str, to_kind: str, filename: str) -> str:
    """Return the markdown link target from one index directory to a record."""
    if from_kind == to_kind:
        return filename
    paths = {
        "plan": "docs/superpowers/plans",
        "spec": "docs/superpowers/specs",
        "pr": "docs/pull-requests",
        "adr": "docs/architecture/adr",
    }
    import os.path

    return os.path.relpath(f"{paths[to_kind]}/{filename}", paths[from_kind]).replace(os.sep, "/")


def scan(kind: str) -> list[dict]:
    directory = DIRS[kind]
    records = []
    for path in sorted(directory.glob("*.md")):
        if path.name == "README.md":
            continue
        text = path.read_text(encoding="utf-8")
        title = next((l[2:].strip() for l in text.split("\n") if l.startswith("# ")), path.stem)
        match = FILENAME.match(path.stem) if kind != "adr" else re.match(r"^(\d{4})-(.+)$", path.stem)
        if match:
            date, slug = match.group(1), match.group(2)
        else:
            date, slug = "", path.stem
        for suffix in SLUG_SUFFIX[kind]:
            if slug.endswith(suffix):
                slug = slug[: -len(suffix)]
        if kind == "adr":
            status = next(
                (m.group(1).strip() for m in [re.search(r"^-\s*Status:\s*(.+)$", text, re.M)] if m),
                "—",
            )
        else:
            status = ""
        records.append(
            {
                "kind": kind,
                "file": path.name,
                "date": date,
                "slug": slug,
                "title": title,
                "status": status,
                "retro": bool(re.search(r"^状态[:：]\s*补记", text, re.M)),
                "body_refs": set(BODY_REF.findall(text)),
            }
        )
    return records


def build_links(records: dict[str, list[dict]]) -> dict[tuple[str, str], set[str]]:
    """Map (kind, file) -> set of (target kind, target file) using slug and body evidence."""
    by_slug: dict[str, dict[str, dict]] = {}
    by_file: dict[str, dict[str, dict]] = {}
    for kind, items in records.items():
        for item in items:
            by_slug.setdefault(item["slug"], {})[kind] = item
            by_file.setdefault(kind, {})[item["file"]] = item

    links: dict[tuple[str, str], set[tuple[str, str]]] = {}
    for kind, items in records.items():
        for item in items:
            targets: set[tuple[str, str]] = set()
            for peer_kind, peer in by_slug.get(item["slug"], {}).items():
                if peer_kind != kind:
                    targets.add((peer_kind, peer["file"]))
            for ref in item["body_refs"]:
                for peer_kind, files in by_file.items():
                    if peer_kind != kind and ref in files:
                        targets.add((peer_kind, ref))
            links[(kind, item["file"])] = targets
    return links


def link_cell(from_kind: str, targets: set[tuple[str, str]], wanted: list[str]) -> str:
    parts = []
    for kind in wanted:
        for target_kind, filename in sorted(targets):
            if target_kind != kind:
                continue
            records = SCANNED[kind]
            title = next((r["title"] for r in records if r["file"] == filename), filename)
            parts.append(f"[{KIND_LABEL[kind]}：{shorten(title)}]({relative(from_kind, kind, filename)})")
    return "<br>".join(parts) if parts else "—"


def shorten(title: str, limit: int = 42) -> str:
    # 索引表已有“编号”列和“PR/规格”类型标签，标题里的同类前缀属于冗余信息。
    title = re.sub(r"^ADR\s*\d+[:：]\s*", "", title)
    title = re.sub(r"^(feat|fix|refactor|perf|docs|test|chore)(\([^)]*\))?[:：]\s*", "", title)
    title = re.sub(r"^PR\s*[:：]\s*", "", title)
    title = re.sub(r"\s*(Implementation Plan|实施计划|Design|设计)$", "", title)
    return title if len(title) <= limit else title[: limit - 1] + "…"


def table(headers: list[str], rows: list[list[str]]) -> str:
    out = ["| " + " | ".join(headers) + " |", "| " + " | ".join("---" for _ in headers) + " |"]
    out += ["| " + " | ".join(row) + " |" for row in rows]
    return "\n".join(out)


SCANNED: dict[str, list[dict]] = {}


def render_plan_index(links) -> str:
    items = sorted(SCANNED["plan"], key=lambda r: (r["date"], r["file"]), reverse=True)
    rows = [
        [
            r["date"],
            f"[{r['title']}]({r['file']})",
            link_cell("plan", links[("plan", r["file"])], ["spec"]),
            link_cell("plan", links[("plan", r["file"])], ["pr", "adr"]),
        ]
        for r in items
    ]
    retro = [r for r in items if r["retro"]]
    retro_block = ""
    if retro:
        retro_block = "\n## 补记计划\n\n以下计划按 AGENTS.md 的紧急修复条款先止损、后补记，正文已标注“补记计划”：\n\n"
        retro_block += "\n".join(f"- [{r['title']}]({r['file']})（{r['date']}）" for r in retro) + "\n"
    return f"""# 实施计划索引

{GENERATED}

本目录保存 `AGENTS.md`「Plan 与 PR 要求」规定的实施计划，文件名固定为
`YYYY-MM-DD-<feature-name>.md`。计划是实现前的设计契约：实现完成后不重写正文，
需要修正时新增计划或在正文标注补记。规则权威来源是
[AGENTS.md](../../../AGENTS.md)，文档组织约定见
[文档规范](../../development/documentation.md)。

共 {len(items)} 份计划，按日期倒序排列。

## 计划清单

{table(["日期", "实施计划", "关联规格", "关联 PR / ADR"], rows)}
{retro_block}
## 新增计划的步骤

1. 按 `YYYY-MM-DD-<feature-name>.md` 命名，正文包含 AGENTS.md 要求的全部小节。
2. 若已有设计规格或后续提交了 PR，在计划正文中直接链接对应文件，交叉引用会被本索引自动识别。
3. 执行 `python3 scripts/gen_doc_index.py` 重新生成本索引，并检查 `git diff`。
"""


def render_spec_index(links) -> str:
    items = sorted(SCANNED["spec"], key=lambda r: (r["date"], r["file"]), reverse=True)
    rows = [
        [
            r["date"],
            f"[{r['title']}]({r['file']})",
            link_cell("spec", links[("spec", r["file"])], ["plan"]),
            link_cell("spec", links[("spec", r["file"])], ["pr", "adr"]),
        ]
        for r in items
    ]
    return f"""# 设计规格索引

{GENERATED}

本目录保存实现前的设计规格（架构决策、接口契约、协议演进、安全边界），文件名固定为
`YYYY-MM-DD-<feature-name>-design.md`。规格记录“为什么这样设计”，与记录“怎么落地”的
[实施计划](../plans/README.md)分开维护。规则权威来源是
[AGENTS.md](../../../AGENTS.md)。

共 {len(items)} 份规格，按日期倒序排列。

## 规格清单

{table(["日期", "设计规格", "关联计划", "关联 PR / ADR"], rows)}

## 新增规格的步骤

1. 按 `YYYY-MM-DD-<feature-name>-design.md` 命名，写明背景、决策、替代方案和迁移影响。
2. 在正文中链接对应的实施计划或 PR，交叉引用会被本索引自动识别。
3. 执行 `python3 scripts/gen_doc_index.py` 重新生成本索引。
"""


def render_pr_index(links) -> str:
    items = sorted(SCANNED["pr"], key=lambda r: (r["date"], r["file"]), reverse=True)
    rows = [
        [
            r["date"],
            f"[{r['title']}]({r['file']})",
            link_cell("pr", links[("pr", r["file"])], ["plan"]),
            link_cell("pr", links[("pr", r["file"])], ["spec", "adr"]),
        ]
        for r in items
    ]
    return f"""# PR 记录索引

{GENERATED}

本目录保存 `AGENTS.md` 要求的 PR 描述文档，文件名固定为 `YYYY-MM-DD-<feature-name>.md`，
内容包含标题、目标分支、摘要、用户影响、API/Schema/配置影响、安全影响、测试证据、
发布与回滚步骤、Reviewer 关注点和集成状态。

共 {len(items)} 份记录，按日期倒序排列。

## 记录清单

{table(["日期", "PR 记录", "关联计划", "关联规格 / ADR"], rows)}

## 新增记录的步骤

1. 按 `YYYY-MM-DD-<feature-name>.md` 命名，覆盖 AGENTS.md 列出的全部小节。
2. 在正文中链接对应的实施计划与设计规格，交叉引用会被本索引自动识别。
3. 执行 `python3 scripts/gen_doc_index.py` 重新生成本索引。
"""


def render_adr_index(links) -> str:
    items = sorted(SCANNED["adr"], key=lambda r: r["file"])
    rows = [
        [
            r["date"],
            f"[{re.sub(r'^ADR\s*' + re.escape(r['date']) + r'[:：]\s*', '', r['title'])}]({r['file']})",
            r["status"] or "—",
            link_cell("adr", links[("adr", r["file"])], ["plan", "spec", "pr"]),
        ]
        for r in items
    ]
    return f"""# 架构决策记录（ADR）

{GENERATED}

重大架构变更必须留下 ADR，记录决策背景、决策内容、替代方案和迁移影响（见
[AGENTS.md](../../../AGENTS.md)「代码评审与演进」）。

## 约定

- 文件名 `NNNN-<short-title>.md`，四位编号单调递增、永不复用。
- 已接受的 ADR 不修改正文；决策被推翻时新增一条 ADR，并把旧条目 Status 改为 `Superseded by NNNN`。
- Status 取值：`Proposed`、`Accepted`、`Deprecated`、`Superseded by NNNN`。

## ADR 清单

{table(["编号", "标题", "状态", "关联记录"], rows)}

## 新增 ADR 的步骤

1. 复制现有 ADR 的结构，使用下一个可用编号。
2. 在正文中链接触发该决策的实施计划或设计规格。
3. 执行 `python3 scripts/gen_doc_index.py` 重新生成本索引。
"""


RENDERERS = {
    "plan": render_plan_index,
    "spec": render_spec_index,
    "pr": render_pr_index,
    "adr": render_adr_index,
}


def main() -> None:
    for kind in DIRS:
        SCANNED[kind] = scan(kind)
    links = build_links(SCANNED)
    for kind, renderer in RENDERERS.items():
        target = DIRS[kind] / "README.md"
        target.write_text(renderer(links).rstrip() + "\n", encoding="utf-8")
        print(f"generated {target.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
