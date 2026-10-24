# CPREV-001 frozen input

Target3435f1d..fe4bf8e; no current WIP.

```diff
diff --git a/scripts/backend-test-manifest.json b/scripts/backend-test-manifest.json
new file mode 100644
index 0000000..3ca022d
--- /dev/null
+++ b/scripts/backend-test-manifest.json
@@ -0,0 +1,176 @@
+[
+  {
+    "id": "api/static",
+    "packages": [
+      "./internal/panel/api"
+    ],
+    "run": "^Test(Static|ValidateWebRoot)",
+    "expected_tests": [
+      "TestStaticRoutes",
+      "TestStaticAPIOnly",
+      "TestValidateWebRoot",
+      "TestStaticSymlinksStayWithinRoot",
+      "TestValidateWebRootRejectsEscapingIndexSymlink",
+      "TestStaticRejectsNonRegularFileWithoutBlocking",
+      "TestValidateWebRootRejectsFifoIndex"
+    ],
+    "stories": [
+      "US-PR-001",
+      "US-PR-017"
+    ],
+    "requires": [
+      "unix"
+    ]
+  },
+  {
+    "id": "e2e/standalone-agent",
+    "packages": [
+      "./tests/e2e"
+    ],
+    "run": "^TestStandaloneAgent",
+    "expected_tests": [
+      "TestStandaloneAgentUnitMatchesInstalledBinary",
+      "TestStandaloneAgentUnitOmitsCAOverrideWithoutPrivateCA",
+      "TestStandaloneAgentRejectsUnsafeStatePath",
+      "TestStandaloneAgentAcceptsNormalStatePath",
+      "TestStandaloneAgentNormalizesTrailingSlashState",
+      "TestStandaloneAgentKeepConfigSkipsUnitAndCA",
+      "TestStandaloneAgentRunsWithoutBashSource"
+    ],
+    "stories": [
+      "US-PR-003",
+      "US-PR-016",
+      "US-PR-017"
+    ],
+    "requires": [
+      "unix"
+    ]
+  },
+  {
+    "id": "period/calendar",
+    "packages": [
+      "./internal/panel/period"
+    ],
+    "run": "^Test",
+    "expected_tests": [
+      "TestAnchorInstantAsiaShanghai",
+      "TestAnchorInstantClipsShortMonths",
+      "TestContainingAndNextBoundary",
+      "TestNextBoundaryIsStrictlyIncreasing",
+      "TestDSTGapTakesFirstValidInstant",
+      "TestDSTAmbiguousTakesEarlierInstant",
+      "TestAllocateSampleAcrossBoundary",
+      "TestAllocateSampleSinglePeriodIsExact",
+      "TestAllocateSampleRemainderGoesToLast",
+      "TestAllocateSampleZeroLengthIsDropped",
+      "TestLoadTimezoneRejectsEmptyAndUnknown",
+      "TestContainingUsesLocalCalendarMonth",
+      "TestPrevBoundaryUsesLocalCalendarMonth",
+      "TestDSTGapAtLargeOffsetAndSkippedDate",
+      "TestLoadTimezoneRejectsHostDependentLocal",
+      "TestPrevBoundaryAndCandidateWindows"
+    ],
+    "stories": [
+      "US-PR-008",
+      "US-PR-013"
+    ],
+    "requires": []
+  },
+  {
+    "id": "store/cycle",
+    "packages": [
+      "./internal/panel/store"
+    ],
+    "run": "^TestComputeAdvance",
+    "expected_tests": [
+      "TestComputeAdvanceKeepsRunningCycle",
+      "TestComputeAdvanceRollsEndedCycleToContainingWindow",
+      "TestComputeAdvanceAppliesPendingRuleAtBoundary",
+      "TestComputeAdvanceLeavesMissingCycleAlone",
+      "TestComputeAdvanceWithoutBindingOrRuleIsNoop",
+      "TestComputeAdvanceClipsShortMonths",
+      "TestComputeAdvanceRejectsUnknownTimezone",
+      "TestComputeAdvanceNeverOverlapsPreviousCycle",
+      "TestComputeAdvanceAppliesTimezoneEditAtFirstBoundaryExactly"
+    ],
+    "stories": [
+      "US-PR-005",
+      "US-PR-013"
+    ],
+    "requires": []
+  },
+  {
+    "id": "store/database-baseline",
+    "packages": [
+      "./internal/panel/store"
+    ],
+    "run": "^Test",
+    "expected_tests": [
+      "TestNodeMutationPublishesRevisionInSameCommit",
+      "TestMemberAndCredentialChangesPublishRevision",
+      "TestNodeMoveRevokesOnBothServers",
+      "TestEvaluateAccessAdvancesEndedCycle",
+      "TestSnapshotAdvancesCycleAndReturnsFreshRevision",
+      "TestAdvancePeriodsTick",
+      "TestSubscriptionRuleEditSchedulesImmediatelyForNextBoundary",
+      "TestUsageBatchBooksIntoAdvancedCycle",
+      "TestSnapshotAndEvidenceAreNotTruncatedBeyond200",
+      "TestSubscriptionRuleRepeatedEditsReplaceAndClearPending",
+      "TestSimulateUpdateNodeGroupAllowsNewlyAddedNode",
+      "TestDigestMembersIsOrderInsensitiveAndVersioned",
+      "TestRequestFingerprintBindsPreviewAndConfirmation",
+      "TestDiffNodes",
+      "TestApplySubscriptionPatchObjectValidation",
+      "TestOperationConstantsMatchContract",
+      "TestListBatchesReturnsNewestFirstWithCounters",
+      "TestListBatchesRespectsLimitAndHandlesEmpty",
+      "TestNormalizeRetentionDaysEnforcesFloor",
+      "TestNormalizeBoundsIsAlwaysBounded",
+      "TestCleanupNeverDeletesProtectedTables",
+      "TestCleanupStatementsAreBoundedAndUseThePolicyArguments",
+      "TestCleanupKeepsOpenAlertsAndUndeliveredNotifications",
+      "TestAlertDedupeKeyIsKindAndSubject",
+      "TestObservationSetCompleteness",
+      "TestTruncateMessage",
+      "TestShortDurationAndBytes",
+      "TestDeleteNodeRemovesGroupMembership",
+      "TestArchivedObjectsAreNotUpdatable",
+      "TestComputeAdvanceKeepsRunningCycle",
+      "TestComputeAdvanceRollsEndedCycleToContainingWindow",
+      "TestComputeAdvanceAppliesPendingRuleAtBoundary",
+      "TestComputeAdvanceLeavesMissingCycleAlone",
+      "TestComputeAdvanceWithoutBindingOrRuleIsNoop",
+      "TestComputeAdvanceClipsShortMonths",
+      "TestComputeAdvanceRejectsUnknownTimezone",
+      "TestComputeAdvanceNeverOverlapsPreviousCycle",
+      "TestComputeAdvanceAppliesTimezoneEditAtFirstBoundaryExactly",
+      "TestRecoveryGenerationIsFreshExactJSONInteger",
+      "TestRecoveryGenerationTransaction",
+      "TestRetryTaskReopensDispatchAndPublishesCurrentRevision",
+      "TestRetryTaskRefusals",
+      "TestRetryTaskIsAtomicOnFailure",
+      "TestBatchSetActiveDoesNotTouchProxyEnabled",
+      "TestUpdateMemberActiveDoesNotTouchProxyEnabled",
+      "TestDeleteSubscriptionRemovesTheRow",
+      "TestDeleteSubscriptionCascadesNodeGroupLinks",
+      "TestDeleteSubscriptionRefusesWhenMembersBound",
+      "TestDeleteSubscriptionRemovesArchivedOne",
+      "TestDeleteSubscriptionDetachesArchivedMembers",
+      "TestDeleteSubscriptionStillBlockedByActiveMember"
+    ],
+    "stories": [
+      "US-PR-005",
+      "US-PR-006",
+      "US-PR-009",
+      "US-PR-010",
+      "US-PR-013",
+      "US-PR-014",
+      "US-PR-016",
+      "US-PR-017"
+    ],
+    "requires": [
+      "postgres"
+    ],
+    "timeout_seconds": 300
+  }
+]
diff --git a/scripts/backend-test.py b/scripts/backend-test.py
new file mode 100644
index 0000000..0296d89
--- /dev/null
+++ b/scripts/backend-test.py
@@ -0,0 +1,229 @@
+#!/usr/bin/env python3
+"""Run registered Go test selections, checking discovery and execution evidence."""
+
+import argparse
+import datetime as dt
+import hashlib
+import json
+import os
+from pathlib import Path
+import re
+import shutil
+import subprocess
+import sys
+import uuid
+
+ROOT = Path(__file__).resolve().parent.parent
+MANIFEST = ROOT / "scripts/backend-test-manifest.json"
+EVIDENCE = ROOT / ".ai/evidence/backend-test"
+
+
+def parse_args():
+    parser = argparse.ArgumentParser(description=__doc__)
+    parser.add_argument("--list", action="store_true", help="show registered selections without executing Go")
+    parser.add_argument("--check-manifest", action="store_true", help="compile/discover tests without running selected cases")
+    parser.add_argument("--json", action="store_true", help="emit one JSON summary")
+    parser.add_argument("--with-docker", action="store_true", help="allow explicitly registered Docker selections")
+    parser.add_argument("filters", nargs="*", metavar="package[/keyword]")
+    return parser.parse_args()
+
+
+def load_selections(filters):
+    entries = json.loads(MANIFEST.read_text())
+    if not isinstance(entries, list) or not entries:
+        raise ValueError("test manifest must be a nonempty array")
+    ids = set()
+    for entry in entries:
+        if not isinstance(entry, dict):
+            raise ValueError("manifest entry must be an object")
+        name = entry.get("id")
+        if not isinstance(name, str) or not name or name in ids:
+            raise ValueError("selection IDs must be nonempty and unique")
+        ids.add(name)
+        for key in ("packages", "expected_tests", "stories", "requires"):
+            values = entry.get(key)
+            if not isinstance(values, list) or any(not isinstance(x, str) or not x for x in values):
+                raise ValueError(f"{name}: invalid {key}")
+        if not entry["packages"] or not entry["expected_tests"]:
+            raise ValueError(f"{name}: packages and expected_tests cannot be empty")
+        if len(entry["expected_tests"]) != len(set(entry["expected_tests"])):
+            raise ValueError(f"{name}: duplicate expected test")
+        if any(not re.fullmatch(r"\./(?:internal|cmd|tests)(?:/[A-Za-z0-9_./-]+)?", x)
+               or ".." in x.split("/") for x in entry["packages"]):
+            raise ValueError(f"{name}: invalid Go package scope")
+        if not isinstance(entry.get("run"), str) or not entry["run"]:
+            raise ValueError(f"{name}: empty test expression")
+        unknown = set(entry["requires"]) - {"postgres", "redis", "xray", "docker", "unix"}
+        if unknown:
+            raise ValueError(f"{name}: unknown requirements: {sorted(unknown)}")
+        timeout = entry.get("timeout_seconds", 120)
+        if type(timeout) is not int or timeout < 1:
+            raise ValueError(f"{name}: invalid timeout")
+    if not filters:
+        return entries
+    selected = []
+    for needle in filters:
+        matches = [e for e in entries if needle.casefold() in e["id"].casefold()]
+        if not matches:
+            raise ValueError(f"no registered selection matches {needle!r}")
+        for entry in matches:
+            if entry not in selected:
+                selected.append(entry)
+    return selected
+
+
+def command(entry, discover=False):
+    if discover:
+        return ["go", "test", "-list", ".*", *entry["packages"]]
+    return ["go", "test", "-json", "-count=1", "-p=1",
+            f"-timeout={entry.get('timeout_seconds', 120)}s", "-run", entry["run"], *entry["packages"]]
+
+
+def input_fingerprint():
+    result = subprocess.run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
+                            cwd=ROOT, capture_output=True, check=True)
+    names = set(result.stdout.decode().split("\0"))
+    names.update({"scripts/backend-test.py", "scripts/backend-test-manifest.json"})
+    files = {}
+    for name in sorted(names):
+        if not name or name.startswith((".ai/", ".memory/", "web/")):
+            continue
+        if not (name.endswith((".go", ".sql", ".sh")) or name in
+                {"go.mod", "go.sum", "scripts/backend-test.py", "scripts/backend-test-manifest.json"}):
+            continue
+        path = ROOT / name
+        files[name] = hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else "deleted"
+    digest = hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()
+    return {"sha256": digest, "files": files}
+
+
+def environment_missing(entry, allow_docker):
+    missing = []
+    for requirement in entry["requires"]:
+        if requirement == "unix" and os.name != "posix":
+            missing.append("Unix test environment")
+        elif requirement == "postgres":
+            if not os.environ.get("PANEL_TEST_DATABASE_URL") or os.environ.get("PANEL_TEST_DB_ISOLATED") != "1":
+                missing.append("isolated PANEL_TEST_DATABASE_URL and PANEL_TEST_DB_ISOLATED=1")
+        elif requirement == "redis" and not os.environ.get("PANEL_TEST_REDIS_URL"):
+            missing.append("PANEL_TEST_REDIS_URL")
+        elif requirement == "xray":
+            binary = os.environ.get("XRAY_BIN", "")
+            if not binary or not os.access(binary, os.X_OK):
+                missing.append("executable XRAY_BIN")
+        elif requirement == "docker" and not allow_docker:
+            missing.append("explicit --with-docker permission")
+    return missing
+
+
+def run_logged(args, path, timeout):
+    started = dt.datetime.now(dt.timezone.utc).isoformat()
+    try:
+        process = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, timeout=timeout,
+                                 env={**os.environ, "PANEL_BASH": os.environ.get("PANEL_BASH", "/bin/bash")})
+        output = process.stdout + process.stderr
+        code = process.returncode
+    except subprocess.TimeoutExpired as exc:
+        output = ""
+        for value in (exc.stdout, exc.stderr):
+            if value:
+                output += value.decode(errors="replace") if isinstance(value, bytes) else value
+        output += "\nRunner wall timeout exceeded\n"
+        code = 124
+    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
+    with os.fdopen(fd, "w") as log:
+        log.write(output)
+    return {"command": args, "cwd": str(ROOT), "exit_code": code, "started_at": started,
+            "ended_at": dt.datetime.now(dt.timezone.utc).isoformat(), "log": str(path)}, output
+
+
+def check_go():
+    if not shutil.which("go"):
+        raise ValueError("go is not on PATH; load the project toolchain first")
+    version = subprocess.run(["go", "version"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()
+    wanted = re.search(r"^go (\d+)\.(\d+)(?:\.(\d+))?", (ROOT / "go.mod").read_text(), re.M)
+    actual = re.search(r"go(\d+)\.(\d+)(?:\.(\d+))?", version)
+    if not wanted or not actual or tuple(int(x or 0) for x in actual.groups()) < tuple(int(x or 0) for x in wanted.groups()):
+        raise ValueError("installed Go does not satisfy go.mod")
+    return version
+
+
+def main():
+    args = parse_args()
+    selections = load_selections(args.filters)
+    summary = {"status": "planned", "coverage": "registered_selections_only", "repository_full_suite": False,
+               "results": [{**entry, "command": command(entry)} for entry in selections]}
+    if args.list:
+        print(json.dumps(summary, ensure_ascii=False, indent=2))
+        return 0
+    version = check_go()
+    EVIDENCE.mkdir(parents=True, exist_ok=True)
+    run_dir = EVIDENCE / (dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8])
+    run_dir.mkdir(mode=0o700)
+    before = input_fingerprint()
+    results = []
+    for number, entry in enumerate(selections):
+        result = {"id": entry["id"], "expected_tests": entry["expected_tests"], "stories": entry["stories"]}
+        results.append(result)
+        if not args.check_manifest:
+            missing = environment_missing(entry, args.with_docker)
+            if missing:
+                result.update(status="not_run", reason=missing)
+                continue
+        discovery, text = run_logged(command(entry, discover=True), run_dir / f"{number}-discovery.log", 180)
+        result["discovery"] = discovery
+        found = {line.strip() for line in text.splitlines() if re.fullmatch(r"Test\w+", line.strip())}
+        missing = set(entry["expected_tests"]) - found
+        if discovery["exit_code"] != 0 or missing:
+            result.update(status="failed", reason="test discovery failed or expected cases missing", missing_tests=sorted(missing))
+            continue
+        if args.check_manifest:
+            result["status"] = "manifest_valid"
+            continue
+        run, text = run_logged(command(entry), run_dir / f"{number}-tests.jsonl", entry.get("timeout_seconds", 120) + 60)
+        result["execution"] = run
+        events = []
+        for line in text.splitlines():
+            try:
+                event = json.loads(line)
+            except ValueError:
+                continue
+            if isinstance(event, dict) and event.get("Test"):
+                events.append(event)
+        ran = {e["Test"] for e in events if e.get("Action") == "run"}
+        passed = {e["Test"] for e in events if e.get("Action") == "pass"}
+        failed = {e["Test"] for e in events if e.get("Action") == "fail"}
+        skipped = {e["Test"] for e in events if e.get("Action") == "skip"}
+        missing = set(entry["expected_tests"]) - ran
+        result.update(ran=sorted(ran), passed=sorted(passed), failed=sorted(failed), skipped=sorted(skipped), missing_tests=sorted(missing))
+        if run["exit_code"] != 0 or failed or missing:
+            result["status"] = "failed"
+        elif skipped or not set(entry["expected_tests"]) <= passed:
+            result["status"] = "incomplete"
+        else:
+            result["status"] = "passed"
+    after = input_fingerprint()
+    stable = before == after
+    good = {"manifest_valid"} if args.check_manifest else {"passed"}
+    complete = stable and all(r["status"] in good for r in results)
+    summary = {"status": "passed" if complete else "failed", "mode": "manifest_check" if args.check_manifest else "test",
+               "coverage": "registered_selections_only", "repository_full_suite": False, "go_version": version,
+               "inputs_stable": stable, "inputs": before, "results": results, "report": str(run_dir / "result.json")}
+    fd = os.open(run_dir / "result.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
+    with os.fdopen(fd, "w") as output:
+        json.dump(summary, output, ensure_ascii=False, indent=2)
+    if args.json:
+        print(json.dumps(summary, ensure_ascii=False))
+    else:
+        for result in results:
+            print(f"{result['id']}: {result['status']}; ran={len(result.get('ran', []))}, skipped={len(result.get('skipped', []))}")
+        print("Evidence:", summary["report"])
+    return 0 if complete else 1
+
+
+if __name__ == "__main__":
+    try:
+        sys.exit(main())
+    except (ValueError, OSError, subprocess.SubprocessError) as error:
+        print(json.dumps({"status": "error", "error": str(error)}), file=sys.stderr)
+        sys.exit(2)

```

## scripts/backend-test.py
```text
1: #!/usr/bin/env python3
2: """Run registered Go test selections, checking discovery and execution evidence."""
3: 
4: import argparse
5: import datetime as dt
6: import hashlib
7: import json
8: import os
9: from pathlib import Path
10: import re
11: import shutil
12: import subprocess
13: import sys
14: import uuid
15: 
16: ROOT = Path(__file__).resolve().parent.parent
17: MANIFEST = ROOT / "scripts/backend-test-manifest.json"
18: EVIDENCE = ROOT / ".ai/evidence/backend-test"
19: 
20: 
21: def parse_args():
22:     parser = argparse.ArgumentParser(description=__doc__)
23:     parser.add_argument("--list", action="store_true", help="show registered selections without executing Go")
24:     parser.add_argument("--check-manifest", action="store_true", help="compile/discover tests without running selected cases")
25:     parser.add_argument("--json", action="store_true", help="emit one JSON summary")
26:     parser.add_argument("--with-docker", action="store_true", help="allow explicitly registered Docker selections")
27:     parser.add_argument("filters", nargs="*", metavar="package[/keyword]")
28:     return parser.parse_args()
29: 
30: 
31: def load_selections(filters):
32:     entries = json.loads(MANIFEST.read_text())
33:     if not isinstance(entries, list) or not entries:
34:         raise ValueError("test manifest must be a nonempty array")
35:     ids = set()
36:     for entry in entries:
37:         if not isinstance(entry, dict):
38:             raise ValueError("manifest entry must be an object")
39:         name = entry.get("id")
40:         if not isinstance(name, str) or not name or name in ids:
41:             raise ValueError("selection IDs must be nonempty and unique")
42:         ids.add(name)
43:         for key in ("packages", "expected_tests", "stories", "requires"):
44:             values = entry.get(key)
45:             if not isinstance(values, list) or any(not isinstance(x, str) or not x for x in values):
46:                 raise ValueError(f"{name}: invalid {key}")
47:         if not entry["packages"] or not entry["expected_tests"]:
48:             raise ValueError(f"{name}: packages and expected_tests cannot be empty")
49:         if len(entry["expected_tests"]) != len(set(entry["expected_tests"])):
50:             raise ValueError(f"{name}: duplicate expected test")
51:         if any(not re.fullmatch(r"\./(?:internal|cmd|tests)(?:/[A-Za-z0-9_./-]+)?", x)
52:                or ".." in x.split("/") for x in entry["packages"]):
53:             raise ValueError(f"{name}: invalid Go package scope")
54:         if not isinstance(entry.get("run"), str) or not entry["run"]:
55:             raise ValueError(f"{name}: empty test expression")
56:         unknown = set(entry["requires"]) - {"postgres", "redis", "xray", "docker", "unix"}
57:         if unknown:
58:             raise ValueError(f"{name}: unknown requirements: {sorted(unknown)}")
59:         timeout = entry.get("timeout_seconds", 120)
60:         if type(timeout) is not int or timeout < 1:
61:             raise ValueError(f"{name}: invalid timeout")
62:     if not filters:
63:         return entries
64:     selected = []
65:     for needle in filters:
66:         matches = [e for e in entries if needle.casefold() in e["id"].casefold()]
67:         if not matches:
68:             raise ValueError(f"no registered selection matches {needle!r}")
69:         for entry in matches:
70:             if entry not in selected:
71:                 selected.append(entry)
72:     return selected
73: 
74: 
75: def command(entry, discover=False):
76:     if discover:
77:         return ["go", "test", "-list", ".*", *entry["packages"]]
78:     return ["go", "test", "-json", "-count=1", "-p=1",
79:             f"-timeout={entry.get('timeout_seconds', 120)}s", "-run", entry["run"], *entry["packages"]]
80: 
81: 
82: def input_fingerprint():
83:     result = subprocess.run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
84:                             cwd=ROOT, capture_output=True, check=True)
85:     names = set(result.stdout.decode().split("\0"))
86:     names.update({"scripts/backend-test.py", "scripts/backend-test-manifest.json"})
87:     files = {}
88:     for name in sorted(names):
89:         if not name or name.startswith((".ai/", ".memory/", "web/")):
90:             continue
91:         if not (name.endswith((".go", ".sql", ".sh")) or name in
92:                 {"go.mod", "go.sum", "scripts/backend-test.py", "scripts/backend-test-manifest.json"}):
93:             continue
94:         path = ROOT / name
95:         files[name] = hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else "deleted"
96:     digest = hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()
97:     return {"sha256": digest, "files": files}
98: 
99: 
100: def environment_missing(entry, allow_docker):
101:     missing = []
102:     for requirement in entry["requires"]:
103:         if requirement == "unix" and os.name != "posix":
104:             missing.append("Unix test environment")
105:         elif requirement == "postgres":
106:             if not os.environ.get("PANEL_TEST_DATABASE_URL") or os.environ.get("PANEL_TEST_DB_ISOLATED") != "1":
107:                 missing.append("isolated PANEL_TEST_DATABASE_URL and PANEL_TEST_DB_ISOLATED=1")
108:         elif requirement == "redis" and not os.environ.get("PANEL_TEST_REDIS_URL"):
109:             missing.append("PANEL_TEST_REDIS_URL")
110:         elif requirement == "xray":
111:             binary = os.environ.get("XRAY_BIN", "")
112:             if not binary or not os.access(binary, os.X_OK):
113:                 missing.append("executable XRAY_BIN")
114:         elif requirement == "docker" and not allow_docker:
115:             missing.append("explicit --with-docker permission")
116:     return missing
117: 
118: 
119: def run_logged(args, path, timeout):
120:     started = dt.datetime.now(dt.timezone.utc).isoformat()
121:     try:
122:         process = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, timeout=timeout,
123:                                  env={**os.environ, "PANEL_BASH": os.environ.get("PANEL_BASH", "/bin/bash")})
124:         output = process.stdout + process.stderr
125:         code = process.returncode
126:     except subprocess.TimeoutExpired as exc:
127:         output = ""
128:         for value in (exc.stdout, exc.stderr):
129:             if value:
130:                 output += value.decode(errors="replace") if isinstance(value, bytes) else value
131:         output += "\nRunner wall timeout exceeded\n"
132:         code = 124
133:     fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
134:     with os.fdopen(fd, "w") as log:
135:         log.write(output)
136:     return {"command": args, "cwd": str(ROOT), "exit_code": code, "started_at": started,
137:             "ended_at": dt.datetime.now(dt.timezone.utc).isoformat(), "log": str(path)}, output
138: 
139: 
140: def check_go():
141:     if not shutil.which("go"):
142:         raise ValueError("go is not on PATH; load the project toolchain first")
143:     version = subprocess.run(["go", "version"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()
144:     wanted = re.search(r"^go (\d+)\.(\d+)(?:\.(\d+))?", (ROOT / "go.mod").read_text(), re.M)
145:     actual = re.search(r"go(\d+)\.(\d+)(?:\.(\d+))?", version)
146:     if not wanted or not actual or tuple(int(x or 0) for x in actual.groups()) < tuple(int(x or 0) for x in wanted.groups()):
147:         raise ValueError("installed Go does not satisfy go.mod")
148:     return version
149: 
150: 
151: def main():
152:     args = parse_args()
153:     selections = load_selections(args.filters)
154:     summary = {"status": "planned", "coverage": "registered_selections_only", "repository_full_suite": False,
155:                "results": [{**entry, "command": command(entry)} for entry in selections]}
156:     if args.list:
157:         print(json.dumps(summary, ensure_ascii=False, indent=2))
158:         return 0
159:     version = check_go()
160:     EVIDENCE.mkdir(parents=True, exist_ok=True)
161:     run_dir = EVIDENCE / (dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8])
162:     run_dir.mkdir(mode=0o700)
163:     before = input_fingerprint()
164:     results = []
165:     for number, entry in enumerate(selections):
166:         result = {"id": entry["id"], "expected_tests": entry["expected_tests"], "stories": entry["stories"]}
167:         results.append(result)
168:         if not args.check_manifest:
169:             missing = environment_missing(entry, args.with_docker)
170:             if missing:
171:                 result.update(status="not_run", reason=missing)
172:                 continue
173:         discovery, text = run_logged(command(entry, discover=True), run_dir / f"{number}-discovery.log", 180)
174:         result["discovery"] = discovery
175:         found = {line.strip() for line in text.splitlines() if re.fullmatch(r"Test\w+", line.strip())}
176:         missing = set(entry["expected_tests"]) - found
177:         if discovery["exit_code"] != 0 or missing:
178:             result.update(status="failed", reason="test discovery failed or expected cases missing", missing_tests=sorted(missing))
179:             continue
180:         if args.check_manifest:
181:             result["status"] = "manifest_valid"
182:             continue
183:         run, text = run_logged(command(entry), run_dir / f"{number}-tests.jsonl", entry.get("timeout_seconds", 120) + 60)
184:         result["execution"] = run
185:         events = []
186:         for line in text.splitlines():
187:             try:
188:                 event = json.loads(line)
189:             except ValueError:
190:                 continue
191:             if isinstance(event, dict) and event.get("Test"):
192:                 events.append(event)
193:         ran = {e["Test"] for e in events if e.get("Action") == "run"}
194:         passed = {e["Test"] for e in events if e.get("Action") == "pass"}
195:         failed = {e["Test"] for e in events if e.get("Action") == "fail"}
196:         skipped = {e["Test"] for e in events if e.get("Action") == "skip"}
197:         missing = set(entry["expected_tests"]) - ran
198:         result.update(ran=sorted(ran), passed=sorted(passed), failed=sorted(failed), skipped=sorted(skipped), missing_tests=sorted(missing))
199:         if run["exit_code"] != 0 or failed or missing:
200:             result["status"] = "failed"
201:         elif skipped or not set(entry["expected_tests"]) <= passed:
202:             result["status"] = "incomplete"
203:         else:
204:             result["status"] = "passed"
205:     after = input_fingerprint()
206:     stable = before == after
207:     good = {"manifest_valid"} if args.check_manifest else {"passed"}
208:     complete = stable and all(r["status"] in good for r in results)
209:     summary = {"status": "passed" if complete else "failed", "mode": "manifest_check" if args.check_manifest else "test",
210:                "coverage": "registered_selections_only", "repository_full_suite": False, "go_version": version,
211:                "inputs_stable": stable, "inputs": before, "results": results, "report": str(run_dir / "result.json")}
212:     fd = os.open(run_dir / "result.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
213:     with os.fdopen(fd, "w") as output:
214:         json.dump(summary, output, ensure_ascii=False, indent=2)
215:     if args.json:
216:         print(json.dumps(summary, ensure_ascii=False))
217:     else:
218:         for result in results:
219:             print(f"{result['id']}: {result['status']}; ran={len(result.get('ran', []))}, skipped={len(result.get('skipped', []))}")
220:         print("Evidence:", summary["report"])
221:     return 0 if complete else 1
222: 
223: 
224: if __name__ == "__main__":
225:     try:
226:         sys.exit(main())
227:     except (ValueError, OSError, subprocess.SubprocessError) as error:
228:         print(json.dumps({"status": "error", "error": str(error)}), file=sys.stderr)
229:         sys.exit(2)
```

## scripts/backend-test-manifest.json
```text
1: [
2:   {
3:     "id": "api/static",
4:     "packages": [
5:       "./internal/panel/api"
6:     ],
7:     "run": "^Test(Static|ValidateWebRoot)",
8:     "expected_tests": [
9:       "TestStaticRoutes",
10:       "TestStaticAPIOnly",
11:       "TestValidateWebRoot",
12:       "TestStaticSymlinksStayWithinRoot",
13:       "TestValidateWebRootRejectsEscapingIndexSymlink",
14:       "TestStaticRejectsNonRegularFileWithoutBlocking",
15:       "TestValidateWebRootRejectsFifoIndex"
16:     ],
17:     "stories": [
18:       "US-PR-001",
19:       "US-PR-017"
20:     ],
21:     "requires": [
22:       "unix"
23:     ]
24:   },
25:   {
26:     "id": "e2e/standalone-agent",
27:     "packages": [
28:       "./tests/e2e"
29:     ],
30:     "run": "^TestStandaloneAgent",
31:     "expected_tests": [
32:       "TestStandaloneAgentUnitMatchesInstalledBinary",
33:       "TestStandaloneAgentUnitOmitsCAOverrideWithoutPrivateCA",
34:       "TestStandaloneAgentRejectsUnsafeStatePath",
35:       "TestStandaloneAgentAcceptsNormalStatePath",
36:       "TestStandaloneAgentNormalizesTrailingSlashState",
37:       "TestStandaloneAgentKeepConfigSkipsUnitAndCA",
38:       "TestStandaloneAgentRunsWithoutBashSource"
39:     ],
40:     "stories": [
41:       "US-PR-003",
42:       "US-PR-016",
43:       "US-PR-017"
44:     ],
45:     "requires": [
46:       "unix"
47:     ]
48:   },
49:   {
50:     "id": "period/calendar",
51:     "packages": [
52:       "./internal/panel/period"
53:     ],
54:     "run": "^Test",
55:     "expected_tests": [
56:       "TestAnchorInstantAsiaShanghai",
57:       "TestAnchorInstantClipsShortMonths",
58:       "TestContainingAndNextBoundary",
59:       "TestNextBoundaryIsStrictlyIncreasing",
60:       "TestDSTGapTakesFirstValidInstant",
61:       "TestDSTAmbiguousTakesEarlierInstant",
62:       "TestAllocateSampleAcrossBoundary",
63:       "TestAllocateSampleSinglePeriodIsExact",
64:       "TestAllocateSampleRemainderGoesToLast",
65:       "TestAllocateSampleZeroLengthIsDropped",
66:       "TestLoadTimezoneRejectsEmptyAndUnknown",
67:       "TestContainingUsesLocalCalendarMonth",
68:       "TestPrevBoundaryUsesLocalCalendarMonth",
69:       "TestDSTGapAtLargeOffsetAndSkippedDate",
70:       "TestLoadTimezoneRejectsHostDependentLocal",
71:       "TestPrevBoundaryAndCandidateWindows"
72:     ],
73:     "stories": [
74:       "US-PR-008",
75:       "US-PR-013"
76:     ],
77:     "requires": []
78:   },
79:   {
80:     "id": "store/cycle",
81:     "packages": [
82:       "./internal/panel/store"
83:     ],
84:     "run": "^TestComputeAdvance",
85:     "expected_tests": [
86:       "TestComputeAdvanceKeepsRunningCycle",
87:       "TestComputeAdvanceRollsEndedCycleToContainingWindow",
88:       "TestComputeAdvanceAppliesPendingRuleAtBoundary",
89:       "TestComputeAdvanceLeavesMissingCycleAlone",
90:       "TestComputeAdvanceWithoutBindingOrRuleIsNoop",
91:       "TestComputeAdvanceClipsShortMonths",
92:       "TestComputeAdvanceRejectsUnknownTimezone",
93:       "TestComputeAdvanceNeverOverlapsPreviousCycle",
94:       "TestComputeAdvanceAppliesTimezoneEditAtFirstBoundaryExactly"
95:     ],
96:     "stories": [
97:       "US-PR-005",
98:       "US-PR-013"
99:     ],
100:     "requires": []
101:   },
102:   {
103:     "id": "store/database-baseline",
104:     "packages": [
105:       "./internal/panel/store"
106:     ],
107:     "run": "^Test",
108:     "expected_tests": [
109:       "TestNodeMutationPublishesRevisionInSameCommit",
110:       "TestMemberAndCredentialChangesPublishRevision",
111:       "TestNodeMoveRevokesOnBothServers",
112:       "TestEvaluateAccessAdvancesEndedCycle",
113:       "TestSnapshotAdvancesCycleAndReturnsFreshRevision",
114:       "TestAdvancePeriodsTick",
115:       "TestSubscriptionRuleEditSchedulesImmediatelyForNextBoundary",
116:       "TestUsageBatchBooksIntoAdvancedCycle",
117:       "TestSnapshotAndEvidenceAreNotTruncatedBeyond200",
118:       "TestSubscriptionRuleRepeatedEditsReplaceAndClearPending",
119:       "TestSimulateUpdateNodeGroupAllowsNewlyAddedNode",
120:       "TestDigestMembersIsOrderInsensitiveAndVersioned",
121:       "TestRequestFingerprintBindsPreviewAndConfirmation",
122:       "TestDiffNodes",
123:       "TestApplySubscriptionPatchObjectValidation",
124:       "TestOperationConstantsMatchContract",
125:       "TestListBatchesReturnsNewestFirstWithCounters",
126:       "TestListBatchesRespectsLimitAndHandlesEmpty",
127:       "TestNormalizeRetentionDaysEnforcesFloor",
128:       "TestNormalizeBoundsIsAlwaysBounded",
129:       "TestCleanupNeverDeletesProtectedTables",
130:       "TestCleanupStatementsAreBoundedAndUseThePolicyArguments",
131:       "TestCleanupKeepsOpenAlertsAndUndeliveredNotifications",
132:       "TestAlertDedupeKeyIsKindAndSubject",
133:       "TestObservationSetCompleteness",
134:       "TestTruncateMessage",
135:       "TestShortDurationAndBytes",
136:       "TestDeleteNodeRemovesGroupMembership",
137:       "TestArchivedObjectsAreNotUpdatable",
138:       "TestComputeAdvanceKeepsRunningCycle",
139:       "TestComputeAdvanceRollsEndedCycleToContainingWindow",
140:       "TestComputeAdvanceAppliesPendingRuleAtBoundary",
141:       "TestComputeAdvanceLeavesMissingCycleAlone",
142:       "TestComputeAdvanceWithoutBindingOrRuleIsNoop",
143:       "TestComputeAdvanceClipsShortMonths",
144:       "TestComputeAdvanceRejectsUnknownTimezone",
145:       "TestComputeAdvanceNeverOverlapsPreviousCycle",
146:       "TestComputeAdvanceAppliesTimezoneEditAtFirstBoundaryExactly",
147:       "TestRecoveryGenerationIsFreshExactJSONInteger",
148:       "TestRecoveryGenerationTransaction",
149:       "TestRetryTaskReopensDispatchAndPublishesCurrentRevision",
150:       "TestRetryTaskRefusals",
151:       "TestRetryTaskIsAtomicOnFailure",
152:       "TestBatchSetActiveDoesNotTouchProxyEnabled",
153:       "TestUpdateMemberActiveDoesNotTouchProxyEnabled",
154:       "TestDeleteSubscriptionRemovesTheRow",
155:       "TestDeleteSubscriptionCascadesNodeGroupLinks",
156:       "TestDeleteSubscriptionRefusesWhenMembersBound",
157:       "TestDeleteSubscriptionRemovesArchivedOne",
158:       "TestDeleteSubscriptionDetachesArchivedMembers",
159:       "TestDeleteSubscriptionStillBlockedByActiveMember"
160:     ],
161:     "stories": [
162:       "US-PR-005",
163:       "US-PR-006",
164:       "US-PR-009",
165:       "US-PR-010",
166:       "US-PR-013",
167:       "US-PR-014",
168:       "US-PR-016",
169:       "US-PR-017"
170:     ],
171:     "requires": [
172:       "postgres"
173:     ],
174:     "timeout_seconds": 300
175:   }
176: ]
```
