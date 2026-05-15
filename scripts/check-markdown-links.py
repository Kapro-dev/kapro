#!/usr/bin/env python3
"""Check repo-local links in selected Markdown files."""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import unquote, urlsplit


DEFAULT_PATHS = ("README.md", "docs", "examples", "monitoring")
LINK_RE = re.compile(r"!?\[[^\]\n]*(?:\][^\[\]\n]*)?\]\(([^)\n]+)\)")
REF_RE = re.compile(r"^\s{0,3}\[[^\]\n]+\]:\s*(\S+)")
HTML_ATTR_RE = re.compile(r"""\b(?:href|src)=["']([^"']+)["']""", re.IGNORECASE)
SCHEME_RE = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*:")


@dataclass(frozen=True)
class Link:
    source: Path
    line: int
    target: str


def markdown_files(root: Path, paths: list[str]) -> list[Path]:
    files: list[Path] = []
    for raw in paths:
        path = root / raw
        if path.is_file() and path.suffix.lower() == ".md":
            files.append(path)
        elif path.is_dir():
            files.extend(sorted(path.rglob("*.md")))
    return sorted(set(files))


def iter_content_lines(path: Path):
    in_fence = False
    fence_marker = ""
    for line_no, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        stripped = line.lstrip()
        fence = re.match(r"(```+|~~~+)", stripped)
        if fence:
            marker = fence.group(1)[0]
            if in_fence and marker == fence_marker:
                in_fence = False
                fence_marker = ""
            elif not in_fence:
                in_fence = True
                fence_marker = marker
            continue
        if not in_fence:
            yield line_no, line


def extract_link_target(raw: str) -> str:
    target = raw.strip()
    if target.startswith("<") and ">" in target:
        return target[1 : target.index(">")].strip()
    return target.split()[0] if target else ""


def links_in_file(path: Path) -> list[Link]:
    links: list[Link] = []
    for line_no, line in iter_content_lines(path):
        for match in LINK_RE.finditer(line):
            links.append(Link(path, line_no, extract_link_target(match.group(1))))
        ref_match = REF_RE.match(line)
        if ref_match:
            links.append(Link(path, line_no, extract_link_target(ref_match.group(1))))
        for match in HTML_ATTR_RE.finditer(line):
            links.append(Link(path, line_no, match.group(1).strip()))
    return links


def should_skip(target: str, root: Path, source: Path) -> bool:
    if not target or target.startswith("#"):
        return True
    if target.startswith("//") or target.startswith("/"):
        return True
    if SCHEME_RE.match(target):
        return True

    candidate = normalized_target(root, source, target)
    try:
        candidate.relative_to(root)
    except ValueError:
        return True
    return False


def normalized_target(root: Path, source: Path, target: str) -> Path:
    parsed = urlsplit(target)
    path = unquote(parsed.path)
    return (root / path if path.startswith("/") else source.parent / path).resolve()


def missing_links(root: Path, files: list[Path]) -> tuple[list[Link], int]:
    missing: list[Link] = []
    checked = 0
    for file in files:
        for link in links_in_file(file):
            if should_skip(link.target, root, file):
                continue
            checked += 1
            if not normalized_target(root, file, link.target).exists():
                missing.append(link)
    return missing, checked


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "paths",
        nargs="*",
        default=list(DEFAULT_PATHS),
        help="Markdown file or directory paths to scan",
    )
    parser.add_argument(
        "--root",
        default=".",
        help="Repository root used to resolve scanned paths",
    )
    args = parser.parse_args()

    root = Path(args.root).resolve()
    files = markdown_files(root, args.paths)
    missing, checked = missing_links(root, files)

    if missing:
        print("Broken local Markdown links:", file=sys.stderr)
        for link in missing:
            rel_source = link.source.relative_to(root)
            print(f"  {rel_source}:{link.line}: {link.target}", file=sys.stderr)
        return 1

    print(f"Checked {checked} local Markdown links in {len(files)} files.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
