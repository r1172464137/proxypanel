#!/usr/bin/env python3
"""Run registered Go test selections, checking discovery and execution evidence."""

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import uuid

ROOT = Path(__file__).resolve().parent.parent
MANIFEST = ROOT / "scripts/backend-test-manifest.json"
EVIDENCE = ROOT / ".ai/evidence/backend-test"


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--list", action="store_true", help="show registered selections without executing Go")
    parser.add_argument("--check-manifest", action="store_true", help="compile/discover tests without running selected cases")
    parser.add_argument("--json", action="store_true", help="emit one JSON summary")
    parser.add_argument("--with-docker", action="store_true", help="allow explicitly registered Docker selections")
    parser.add_argument("filters", nargs="*", metavar="package[/keyword]")
    return parser.parse_args()


def load_selections(filters):
    entries = json.loads(MANIFEST.read_text())
    if not isinstance(entries, list) or not entries:
        raise ValueError("test manifest must be a nonempty array")
    ids = set()
    for entry in entries:
        if not isinstance(entry, dict):
            raise ValueError("manifest entry must be an object")
        name = entry.get("id")
        if not isinstance(name, str) or not name or name in ids:
            raise ValueError("selection IDs must be nonempty and unique")
        ids.add(name)
        for key in ("packages", "expected_tests", "stories", "requires"):
            values = entry.get(key)
            if not isinstance(values, list) or any(not isinstance(x, str) or not x for x in values):
                raise ValueError(f"{name}: invalid {key}")
        if not entry["packages"] or not entry["expected_tests"]:
            raise ValueError(f"{name}: packages and expected_tests cannot be empty")
        if len(entry["expected_tests"]) != len(set(entry["expected_tests"])):
            raise ValueError(f"{name}: duplicate expected test")
        if any(not re.fullmatch(r"\./(?:internal|cmd|tests)(?:/[A-Za-z0-9_./-]+)?", x)
               or ".." in x.split("/") for x in entry["packages"]):
            raise ValueError(f"{name}: invalid Go package scope")
        if not isinstance(entry.get("run"), str) or not entry["run"]:
            raise ValueError(f"{name}: empty test expression")
        unknown = set(entry["requires"]) - {"postgres", "redis", "xray", "docker", "unix"}
        if unknown:
            raise ValueError(f"{name}: unknown requirements: {sorted(unknown)}")
        tags = entry.get("build_tags", [])
        if (not isinstance(tags, list) or len(tags) != len(set(str(t) for t in tags))
                or any(not isinstance(t, str) or not re.fullmatch(r"[A-Za-z0-9_.]+", t) for t in tags)):
            raise ValueError(f"{name}: invalid build_tags")
        if "xrayreal" in tags and "xray" not in entry["requires"]:
            raise ValueError(f"{name}: xrayreal requires an explicit xray capability")
        timeout = entry.get("timeout_seconds", 120)
        if type(timeout) is not int or timeout < 1:
            raise ValueError(f"{name}: invalid timeout")
    if not filters:
        return entries
    selected = []
    for needle in filters:
        matches = [e for e in entries if needle.casefold() in e["id"].casefold()]
        if not matches:
            raise ValueError(f"no registered selection matches {needle!r}")
        for entry in matches:
            if entry not in selected:
                selected.append(entry)
    return selected


def command(entry, discover=False):
    tags = ["-tags=" + ",".join(entry["build_tags"])] if entry.get("build_tags") else []
    if discover:
        return ["go", "test", *tags, "-list", ".*", *entry["packages"]]
    return ["go", "test", *tags, "-json", "-count=1", "-p=1",
            f"-timeout={entry.get('timeout_seconds', 120)}s", "-run", entry["run"], *entry["packages"]]


def input_fingerprint():
    result = subprocess.run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
                            cwd=ROOT, capture_output=True, check=True)
    names = set(result.stdout.decode().split("\0"))
    names.update({"scripts/backend-test.py", "scripts/backend-test-manifest.json"})
    files = {}
    for name in sorted(names):
        if not name or name.startswith((".ai/", ".memory/", "web/")):
            continue
        if not (name.endswith((".go", ".sql", ".sh", ".service")) or name in
                {"go.mod", "go.sum", "scripts/backend-test.py", "scripts/backend-test-manifest.json"}):
            continue
        path = ROOT / name
        files[name] = hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else "deleted"
    digest = hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()
    return {"sha256": digest, "files": files}


def environment_missing(entry, allow_docker):
    missing = []
    for requirement in entry["requires"]:
        if requirement == "unix" and os.name != "posix":
            missing.append("Unix test environment")
        elif requirement == "postgres":
            if not os.environ.get("PANEL_TEST_DATABASE_URL") or os.environ.get("PANEL_TEST_DB_ISOLATED") != "1":
                missing.append("isolated PANEL_TEST_DATABASE_URL and PANEL_TEST_DB_ISOLATED=1")
        elif requirement == "redis" and not os.environ.get("PANEL_TEST_REDIS_URL"):
            missing.append("PANEL_TEST_REDIS_URL")
        elif requirement == "xray":
            binary = os.environ.get("XRAY_BIN", "")
            if not binary or not os.access(binary, os.X_OK):
                missing.append("executable XRAY_BIN")
        elif requirement == "docker" and not allow_docker:
            missing.append("explicit --with-docker permission")
    return missing


def run_logged(args, path, timeout):
    started = dt.datetime.now(dt.timezone.utc).isoformat()
    try:
        process = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, timeout=timeout,
                                 env={**os.environ, "PANEL_BASH": os.environ.get("PANEL_BASH", "/bin/bash")})
        output = process.stdout + process.stderr
        code = process.returncode
    except subprocess.TimeoutExpired as exc:
        output = ""
        for value in (exc.stdout, exc.stderr):
            if value:
                output += value.decode(errors="replace") if isinstance(value, bytes) else value
        output += "\nRunner wall timeout exceeded\n"
        code = 124
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as log:
        log.write(output)
    return {"command": args, "cwd": str(ROOT), "exit_code": code, "started_at": started,
            "ended_at": dt.datetime.now(dt.timezone.utc).isoformat(), "log": str(path)}, output


def check_go():
    if not shutil.which("go"):
        raise ValueError("go is not on PATH; load the project toolchain first")
    version = subprocess.run(["go", "version"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()
    wanted = re.search(r"^go (\d+)\.(\d+)(?:\.(\d+))?", (ROOT / "go.mod").read_text(), re.M)
    actual = re.search(r"go(\d+)\.(\d+)(?:\.(\d+))?", version)
    if not wanted or not actual or tuple(int(x or 0) for x in actual.groups()) < tuple(int(x or 0) for x in wanted.groups()):
        raise ValueError("installed Go does not satisfy go.mod")
    return version


def main():
    args = parse_args()
    selections = load_selections(args.filters)
    summary = {"status": "planned", "coverage": "registered_selections_only", "repository_full_suite": False,
               "results": [{**entry, "command": command(entry)} for entry in selections]}
    if args.list:
        print(json.dumps(summary, ensure_ascii=False, indent=2))
        return 0
    version = check_go()
    EVIDENCE.mkdir(parents=True, exist_ok=True)
    run_dir = EVIDENCE / (dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8])
    run_dir.mkdir(mode=0o700)
    before = input_fingerprint()
    results = []
    for number, entry in enumerate(selections):
        result = {"id": entry["id"], "expected_tests": entry["expected_tests"], "stories": entry["stories"]}
        results.append(result)
        if not args.check_manifest:
            missing = environment_missing(entry, args.with_docker)
            if missing:
                result.update(status="not_run", reason=missing)
                continue
        discovery, text = run_logged(command(entry, discover=True), run_dir / f"{number}-discovery.log", 180)
        result["discovery"] = discovery
        found = {line.strip() for line in text.splitlines() if re.fullmatch(r"Test\w+", line.strip())}
        missing = set(entry["expected_tests"]) - found
        if discovery["exit_code"] != 0 or missing:
            result.update(status="failed", reason="test discovery failed or expected cases missing", missing_tests=sorted(missing))
            continue
        if args.check_manifest:
            result["status"] = "manifest_valid"
            continue
        run, text = run_logged(command(entry), run_dir / f"{number}-tests.jsonl", entry.get("timeout_seconds", 120) + 60)
        result["execution"] = run
        events = []
        for line in text.splitlines():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if isinstance(event, dict) and event.get("Test"):
                events.append(event)
        ran = {e["Test"] for e in events if e.get("Action") == "run"}
        passed = {e["Test"] for e in events if e.get("Action") == "pass"}
        failed = {e["Test"] for e in events if e.get("Action") == "fail"}
        skipped = {e["Test"] for e in events if e.get("Action") == "skip"}
        missing = set(entry["expected_tests"]) - ran
        result.update(ran=sorted(ran), passed=sorted(passed), failed=sorted(failed), skipped=sorted(skipped), missing_tests=sorted(missing))
        if run["exit_code"] != 0 or failed or missing:
            result["status"] = "failed"
        elif skipped or not set(entry["expected_tests"]) <= passed:
            result["status"] = "incomplete"
        else:
            result["status"] = "passed"
    after = input_fingerprint()
    stable = before == after
    good = {"manifest_valid"} if args.check_manifest else {"passed"}
    complete = stable and all(r["status"] in good for r in results)
    summary = {"status": "passed" if complete else "failed", "mode": "manifest_check" if args.check_manifest else "test",
               "coverage": "registered_selections_only", "repository_full_suite": False, "go_version": version,
               "inputs_stable": stable, "inputs": before, "results": results, "report": str(run_dir / "result.json")}
    fd = os.open(run_dir / "result.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as output:
        json.dump(summary, output, ensure_ascii=False, indent=2)
    if args.json:
        print(json.dumps(summary, ensure_ascii=False))
    else:
        for result in results:
            print(f"{result['id']}: {result['status']}; ran={len(result.get('ran', []))}, skipped={len(result.get('skipped', []))}")
        print("Evidence:", summary["report"])
    return 0 if complete else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print(json.dumps({"status": "error", "error": str(error)}), file=sys.stderr)
        sys.exit(2)
