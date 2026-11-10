#!/usr/bin/env python3
"""Syntax-check inline JavaScript in an HTML file with Node.js."""

from __future__ import annotations

import argparse
import subprocess
import sys
from html.parser import HTMLParser
from pathlib import Path


class InlineScriptParser(HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=False)
        self.scripts: list[str] = []
        self._current: list[str] | None = None

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        if tag.lower() != "script":
            return
        attributes = {key.lower(): value for key, value in attrs}
        self._current = [] if "src" not in attributes else None

    def handle_data(self, data: str) -> None:
        if self._current is not None:
            self._current.append(data)

    def handle_endtag(self, tag: str) -> None:
        if tag.lower() == "script" and self._current is not None:
            self.scripts.append("".join(self._current))
            self._current = None



def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("html_file", type=Path)
    args = parser.parse_args()

    try:
        source = args.html_file.read_text(encoding="utf-8")
    except OSError as exc:
        print(f"cannot read {args.html_file}: {exc}", file=sys.stderr)
        return 2

    html = InlineScriptParser()
    html.feed(source)
    scripts = [script for script in html.scripts if script.strip()]
    if not scripts:
        print(f"no inline JavaScript found in {args.html_file}", file=sys.stderr)
        return 1

    for index, script in enumerate(scripts, 1):
        result = subprocess.run(
            ["node", "--check"],
            input=script,
            text=True,
            capture_output=True,
            check=False,
        )
        if result.returncode != 0:
            print(f"JavaScript syntax error in inline script {index} of {args.html_file}:", file=sys.stderr)
            print(result.stderr, file=sys.stderr, end="")
            return result.returncode

    print(f"checked {len(scripts)} inline script(s) in {args.html_file}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
