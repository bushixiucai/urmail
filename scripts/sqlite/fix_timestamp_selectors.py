#!/usr/bin/env python3
"""B5b helper: rewrite remaining `x.Valid` / `x.Time` selector uses of store
timestamp fields that are now plain int64 unix microseconds.

The compiler names the exact selector, so this only touches the sites it
reports: `.Valid` becomes `!= 0`, `.Time` becomes `timeutil.FromMicro(x)`.
Run with --apply; it loops until no such error remains.
"""
import argparse
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
ERR = re.compile(r'^(.+?\.go):(\d+):(\d+): (.+?)\.(Valid|Time) undefined \(type int64')
IMPORT = '\t"github.com/bushixiucai/urmail/internal/timeutil"\n'


def build_errors():
    """Compiler and vet diagnostics, with vet's "vet: " prefix removed.

    vet type-checks test files too, so one pass covers production and tests.
    """
    proc = subprocess.run(["go", "vet", "./..."], cwd=ROOT,
                          capture_output=True, text=True)
    lines = (proc.stdout + proc.stderr).split("\n")
    return [ln[5:] if ln.startswith("vet: ") else ln for ln in lines]


def ensure_import(path):
    s = path.read_text()
    if "internal/timeutil" in s:
        return s
    # insert after the first import (
    return s.replace("import (\n", "import (\n" + IMPORT, 1)


UNUSED = re.compile(r'^(.+?\.go):\d+:\d+: "(.+?)" imported and not used$')


def clean_imports(lines):
    """Drop imports the compiler reports as unused; safe because it names them."""
    removed = 0
    for line in lines:
        m = UNUSED.match(line)
        if not m:
            continue
        path = ROOT / m.group(1)
        imp = m.group(2)
        s = path.read_text()
        for form in (f'\t"{imp}"\n', f'\t{imp.split("/")[-1]} "{imp}"\n'):
            if form in s:
                path.write_text(s.replace(form, "", 1))
                removed += 1
                break
    return removed


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--apply", action="store_true")
    args = ap.parse_args()
    total = 0
    for _ in range(6):
        hits = 0
        periods = set()
        for line in build_errors():
            m = ERR.match(line)
            if not m:
                continue
            path = ROOT / m.group(1)
            lineno, col = int(m.group(2)), int(m.group(3))
            recv, field = m.group(4), m.group(5)
            lines = path.read_text().split("\n")
            text = lines[lineno - 1]
            # `!x.Valid` is a negated validity test, not a comparison.
            negated = text.rstrip().endswith("!") or f"!{recv}.Valid" in text
            old = f"{recv}.{field}"
            if old not in text:
                # compiler may report a shorter receiver chain; try the
                # selector suffix on the reported line
                idx = max(0, col - 1)
                if field == "Valid":
                    pat = re.compile(r"([A-Za-z_][\w.]*)\.Valid")
                else:
                    pat = re.compile(r"([A-Za-z_][\w.]*)\.Time")
                m2 = pat.search(text, idx)
                if not m2:
                    print("unmatched:", line, file=sys.stderr)
                    continue
                recv = m2.group(1)
                old = m2.group(0)
            if field == "Valid":
                if f"!{recv}.Valid" in text:
                    lines[lineno - 1] = text.replace(f"!{recv}.Valid", f"{recv} == 0")
                    path.write_text("\n".join(lines))
                    periods.add(path)
                    hits += 1
                    continue
                new = f"{recv} != 0"
            else:
                new = f"timeutil.FromMicro({recv})"
            lines[lineno - 1] = text.replace(old, new)
            path.write_text("\n".join(lines))
            periods.add(path)
            hits += 1
        total += hits
        if hits == 0:
            if clean_imports(build_errors()):
                subprocess.run(["gofmt", "-w", "internal"], cwd=ROOT, check=False)
                continue
            break
        for p in periods:
            p.write_text(ensure_import(p))
        subprocess.run(["gofmt", "-w", "internal"], cwd=ROOT, check=False)
        if clean_imports(build_errors()):
            continue
        if not args.apply:
            print(f"would rewrite {hits} sites")
            return 0
    print(f"rewrote {total} sites")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())