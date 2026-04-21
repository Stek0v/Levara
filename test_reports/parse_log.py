#!/usr/bin/env python3
"""Parse `go test -json` output into markdown summary.

Usage: python3 parse_log.py <path-to-json-log> [> report.md]
"""
import json
import sys
from collections import defaultdict
from pathlib import Path


def main(path: str) -> int:
    events = []
    for line in Path(path).read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("exit="):
            continue
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            continue

    pkg_status: dict[str, str] = {}
    pkg_tests: dict[str, dict[str, str]] = defaultdict(dict)
    pkg_cover: dict[str, float] = {}
    failures: list[tuple[str, str, list[str]]] = []
    output_buf: dict[tuple[str, str], list[str]] = defaultdict(list)

    for ev in events:
        pkg = ev.get("Package", "")
        test = ev.get("Test")
        action = ev.get("Action")
        out = ev.get("Output", "")

        if action == "output" and out:
            output_buf[(pkg, test or "")].append(out)
            # Coverage line
            if "coverage:" in out and "of statements" in out:
                try:
                    pct = float(out.split("coverage:")[1].split("%")[0].strip())
                    pkg_cover[pkg] = pct
                except (ValueError, IndexError):
                    pass

        if action in ("pass", "fail", "skip"):
            if test is None:
                pkg_status[pkg] = action
            else:
                pkg_tests[pkg][test] = action
                if action == "fail":
                    failures.append((pkg, test, output_buf[(pkg, test)][-50:]))

    # --- render ---
    total_pkgs = len(pkg_status)
    passed = sum(1 for v in pkg_status.values() if v == "pass")
    failed = sum(1 for v in pkg_status.values() if v == "fail")
    skipped = sum(1 for v in pkg_status.values() if v == "skip")

    total_tests = sum(len(t) for t in pkg_tests.values())
    failed_tests = sum(1 for tests in pkg_tests.values() for s in tests.values() if s == "fail")

    print("# Test Run Summary\n")
    print(f"- Packages: **{total_pkgs}** (pass={passed}, fail={failed}, skip={skipped})")
    print(f"- Tests: **{total_tests}** (failed={failed_tests})")
    if pkg_cover:
        avg = sum(pkg_cover.values()) / len(pkg_cover)
        print(f"- Avg coverage (of reporting pkgs): **{avg:.1f}%** across {len(pkg_cover)} pkgs")
    print()

    print("## Per-package\n")
    print("| Package | Status | Tests | Coverage |")
    print("|---|---|---|---|")
    for pkg in sorted(pkg_status):
        tests = pkg_tests.get(pkg, {})
        tstr = f"{sum(1 for s in tests.values() if s=='pass')}/{len(tests)}" if tests else "-"
        cov = f"{pkg_cover[pkg]:.1f}%" if pkg in pkg_cover else "-"
        print(f"| `{pkg}` | {pkg_status[pkg]} | {tstr} | {cov} |")

    if failures:
        print("\n## Failures\n")
        for pkg, test, out in failures:
            print(f"### `{pkg}` :: `{test}`\n")
            print("```")
            for line in out:
                print(line.rstrip())
            print("```\n")

    return 1 if failed > 0 else 0


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        sys.exit(2)
    sys.exit(main(sys.argv[1]))
