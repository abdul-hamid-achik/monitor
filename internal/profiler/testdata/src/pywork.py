import json, threading, time, os, sys
def heavy_stringify(n):
    s = ""
    for i in range(n):
        o = {"a": i, "b": "x" * 10}
        s += json.dumps(o)              # line 6 hot
        if len(s) > 100000:
            s = ""
    return len(s)
def worker():
    while True:
        heavy_stringify(2000)
threading.Thread(target=worker, daemon=True, name="hot").start()
print("pid", os.getpid(), flush=True)
time.sleep(float(sys.argv[1]) if len(sys.argv) > 1 else 30)
