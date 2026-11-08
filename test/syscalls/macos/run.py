#!/usr/bin/env python3
# Copyright 2026 The gVisor Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Run gVisor syscall test binaries under runsc on macOS (hvf).

Usage: run.py RUNSC ROOTFS OUTDIR TEST [TEST...]

Each test binary runs in one sandbox. If the binary does not finish (crash,
timeout), each of its cases that did not report a result runs again in its
own sandbox.
"""
import json, os, re, subprocess, sys, time, concurrent.futures as cf

RUNSC, ROOTFS, OUT = sys.argv[1], os.path.abspath(sys.argv[2]), os.path.abspath(sys.argv[3])
TESTS = sys.argv[4:]
BIN_TIMEOUT = int(os.environ.get("BIN_TIMEOUT", "600"))
CASE_TIMEOUT = int(os.environ.get("CASE_TIMEOUT", "120"))
JOBS = int(os.environ.get("JOBS", "4"))
ROOT = os.path.join(OUT, "state")

ALL_CAPS = [
    "CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH", "CAP_FOWNER",
    "CAP_FSETID", "CAP_KILL", "CAP_SETGID", "CAP_SETUID", "CAP_SETPCAP",
    "CAP_LINUX_IMMUTABLE", "CAP_NET_BIND_SERVICE", "CAP_NET_BROADCAST",
    "CAP_NET_ADMIN", "CAP_NET_RAW", "CAP_IPC_LOCK", "CAP_IPC_OWNER",
    "CAP_SYS_MODULE", "CAP_SYS_RAWIO", "CAP_SYS_CHROOT", "CAP_SYS_PTRACE",
    "CAP_SYS_PACCT", "CAP_SYS_ADMIN", "CAP_SYS_BOOT", "CAP_SYS_NICE",
    "CAP_SYS_RESOURCE", "CAP_SYS_TIME", "CAP_SYS_TTY_CONFIG", "CAP_MKNOD",
    "CAP_LEASE", "CAP_AUDIT_WRITE", "CAP_AUDIT_CONTROL", "CAP_SETFCAP",
    "CAP_MAC_OVERRIDE", "CAP_MAC_ADMIN", "CAP_SYSLOG", "CAP_WAKE_ALARM",
    "CAP_BLOCK_SUSPEND", "CAP_AUDIT_READ", "CAP_PERFMON", "CAP_BPF",
    "CAP_CHECKPOINT_RESTORE",
]

ENV = [
    "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
    "HOME=/root",
    "TEST_ON_GVISOR=hvf",
    "GVISOR_NETWORK=none",
    "GVISOR_RUNTIME=runsc",
    "IOURING_ENABLED=FALSE",
    "GVISOR_FUSE_TEST=FALSE",
    "GVISOR_SAVE_TEST=FALSE",
    # The same capabilities as the kvm platform.
    "GVISOR_PLATFORM_SUPPORT=32BIT:FALSE,ALIGNMENT_CHECK:TRUE,INT3:FALSE,MULTIPROCESS:TRUE,VSYSCALL:TRUE",
    "TEST_TMPDIR=/tmp",
    "RUNFILES_DIR=/runfiles",
    "TEST_SRCDIR=/runfiles",
]


def spec(args):
    caps = ALL_CAPS
    return {
        "ociVersion": "1.0.0",
        "process": {
            "user": {"uid": 0, "gid": 0},
            "args": args,
            "env": ENV,
            "cwd": "/",
            "capabilities": {k: caps for k in ("bounding", "effective", "inheritable", "permitted")},
        },
        "root": {"path": ROOTFS, "readonly": False},
        # The same values as testutil.NewSpecWithArgs and test/runner.
        "hostname": "runsc-test-hostname",
        "domainname": "runsc-test-domain",
        "mounts": [{"destination": "/tmp", "type": "tmpfs"}],
        "linux": {},
    }


def run(name, args, timeout):
    """Runs args in a new sandbox. Returns (exit code or None, output)."""
    bundle = os.path.join(OUT, "bundles", name)
    os.makedirs(bundle, exist_ok=True)
    with open(os.path.join(bundle, "config.json"), "w") as f:
        json.dump(spec(args), f)
    cid = re.sub(r"[^A-Za-z0-9_.-]", "_", name)[:200]
    # The runsc flags of test/runner that apply on macOS.
    cmd = [RUNSC, "--root=" + ROOT, "--network=none", "--allow-suid", "--net-raw",
           "--allow-packet-socket-write=true", "--watchdog-action=panic", "run", "--bundle", bundle, cid]
    try:
        p = subprocess.run(cmd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                           stderr=subprocess.STDOUT, timeout=timeout)
        return p.returncode, p.stdout.decode(errors="replace")
    except subprocess.TimeoutExpired as e:
        subprocess.run([RUNSC, "--root=" + ROOT, "kill", cid, "KILL"], capture_output=True)
        time.sleep(1)
        subprocess.run([RUNSC, "--root=" + ROOT, "delete", "--force", cid], capture_output=True)
        return None, (e.stdout or b"").decode(errors="replace") + "\n[TIMEOUT]\n"


RESULT = re.compile(r"^\[\s+(OK|FAILED|SKIPPED)\s+\] (\S+\.\S+?)(?: \(| *$)", re.M)


def parse(out):
    res = {}
    for m in RESULT.finditer(out):
        st, case = m.groups()
        if "," in case:
            continue
        res[case] = {"OK": "pass", "FAILED": "fail", "SKIPPED": "skip"}[st]
    return res


def list_cases(t, binpath):
    code, out = run(t + ".list", [binpath, "--gtest_list_tests"], 120)
    cases, suite = [], None
    for line in out.splitlines():
        if not line.strip() or line.startswith("[") or "WARNING" in line:
            continue
        if not line.startswith(" "):
            suite = line.split("#")[0].strip()
        elif suite:
            cases.append(suite + line.split("#")[0].strip())
    return cases


def one(t):
    binpath = "/runfiles/_main/test/syscalls/linux/" + t
    logdir = os.path.join(OUT, "logs")
    os.makedirs(logdir, exist_ok=True)
    cases = list_cases(t, binpath)
    start = time.time()
    code, out = run(t, [binpath], BIN_TIMEOUT)
    open(os.path.join(logdir, t + ".log"), "w").write(out)
    res = parse(out)
    note = "exit=%s" % code
    missing = [c for c in cases if c not in res]
    if missing and (code is None or code != 0 or "[  PASSED  ]" not in out):
        # The binary crashed or hung: run each missing case alone.
        note += " rerun=%d" % len(missing)
        for c in missing:
            ccode, cout = run(t + "." + c, [binpath, "--gtest_filter=" + c], CASE_TIMEOUT)
            open(os.path.join(logdir, t + "." + c.replace("/", "_") + ".log"), "w").write(cout)
            r = parse(cout).get(c)
            if r is None:
                r = "timeout" if ccode is None else "crash"
            res[c] = r
    for c in cases:
        res.setdefault(c, "missing")
    return t, {"cases": res, "note": note, "secs": round(time.time() - start, 1), "listed": len(cases)}


def main():
    os.makedirs(ROOT, exist_ok=True)
    results = {}
    with cf.ThreadPoolExecutor(JOBS) as ex:
        for t, r in ex.map(one, TESTS):
            results[t] = r
            c = {}
            for v in r["cases"].values():
                c[v] = c.get(v, 0) + 1
            print("%-28s %5.0fs %-18s %s" % (t, r["secs"], r["note"], c), flush=True)
            with open(os.path.join(OUT, "results.json"), "w") as f:
                json.dump(results, f, indent=1, sort_keys=True)


main()
