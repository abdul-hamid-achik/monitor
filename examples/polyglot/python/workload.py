#!/usr/bin/env python3
import json
import os
import sys
import time
import threading
import traceback

START = time.time()
PID = os.getpid()


def heavy_stringify(items):
    out = []
    for item in items:
        doubled = item * 2
        s = ""
        for i in range(2500):
            s += json.dumps({"i": i, "item": item, "doubled": doubled, "pad": "x" * 64})  # HOT LINE (line 16)
        out.append(len(s))
    return out


def process_batch(n):
    items = list(range(n))
    return heavy_stringify(items)


def flaky_parse(raw):
    if "bad" in raw:
        raise ValueError(f"flaky_parse: malformed payload near token {raw[:8]!r}")
    return json.loads(raw)


def tick_errors():
    for p in ['{"ok": true}', "bad-payload-123", '{"ok": true}']:
        try:
            flaky_parse(p)
        except Exception as e:
            print(f"[python] caught in tick_errors: {e}", file=sys.stderr)
            traceback.print_exc()


def detonate():
    raise RuntimeError(f"python workload: intentional uncaught failure at t={time.time() - START:.1f}s")


print(f"[python] pid={PID} workload starting", file=sys.stderr)

# WORKLOAD_SECONDS shortens the run for CI/spec use (monitor run --'s
# mvp_errors_* specs); default 40s is unchanged when unset or invalid.
try:
    SECONDS = float(os.environ.get("WORKLOAD_SECONDS") or 40)
    if SECONDS <= 0:
        SECONDS = 40
except ValueError:
    SECONDS = 40
# Scales with SECONDS so a shortened run still produces several handled
# errors before the fatal one: at the default 40s this is exactly the
# original fixed 4s interval.
ERR_INTERVAL = max(0.05, SECONDS / 10)

stop = threading.Event()


def hot_loop():
    while not stop.is_set():
        process_batch(40)
        time.sleep(0.04)


def err_loop():
    while not stop.is_set():
        tick_errors()
        time.sleep(ERR_INTERVAL)


t1 = threading.Thread(target=hot_loop, daemon=True)
t2 = threading.Thread(target=err_loop, daemon=True)
t1.start()
t2.start()

time.sleep(SECONDS)
stop.set()
detonate()
