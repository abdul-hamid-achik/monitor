"""Tests for the monitorcli SDK. Run with:

    python3 -m unittest discover -s sdk/python/tests
"""

import json
import os
import subprocess
import sys
import tempfile
import textwrap
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
PKG = os.path.dirname(HERE)
BOOTSTRAP = os.path.join(PKG, "bootstrap")


def read_events(directory):
    if not os.path.isdir(directory):
        return []
    names = sorted(n for n in os.listdir(directory) if n.endswith(".json") and not n.startswith("."))
    out = []
    for name in names:
        with open(os.path.join(directory, name), encoding="utf-8") as fh:
            out.append(json.load(fh))
    return out


def run(script, auto=False, explicit=False, extra_path=None, env=None):
    """Run script in a fresh interpreter with its own events directory."""
    work = tempfile.mkdtemp(prefix="monitor-sdk-py-")
    app = os.path.join(work, "app.py")
    with open(app, "w", encoding="utf-8") as fh:
        fh.write(textwrap.dedent(script))
    events_dir = os.path.join(work, "events")
    child_env = dict(os.environ)
    child_env.pop("PYTHONPATH", None)
    child_env["MONITOR_EVENTS_DIR"] = events_dir
    paths = []
    if auto:
        paths.append(BOOTSTRAP)
    if extra_path:
        paths.append(extra_path)
    if explicit:
        paths.append(PKG)
    if paths:
        child_env["PYTHONPATH"] = os.pathsep.join(paths)
    child_env.update(env or {})
    res = subprocess.run([sys.executable, app], cwd=work, env=child_env, capture_output=True, text=True)
    return res, read_events(events_dir), work


APP = """
    import json, logging, threading
    log = logging.getLogger("app")
    print("starting")
    log.warning("cache cold")
    def parse(s):
        return json.loads(s)
    try:
        parse("{bad")
    except ValueError:
        log.exception("parse failed")
    t = threading.Thread(target=lambda: 1 / 0)
    t.start(); t.join()
    def load():
        try:
            parse("{bad")
        except ValueError as e:
            raise RuntimeError("config load failed") from e
    load()
"""


class AutoModeTest(unittest.TestCase):
    def test_output_and_exit_status_are_identical(self):
        plain, none, plain_dir = run(APP)
        probed, events, probed_dir = run(APP, auto=True)
        self.assertEqual(probed.returncode, plain.returncode)
        self.assertEqual(probed.stdout, plain.stdout)
        self.assertEqual(probed.stderr.replace(probed_dir, "<dir>"), plain.stderr.replace(plain_dir, "<dir>"))
        self.assertEqual(none, [])

        self.assertEqual([e["kind"] for e in events], ["logged", "thread", "uncaught"])
        logged, thread, uncaught = events
        self.assertTrue(logged["handled"])
        self.assertEqual(logged["error"]["type"], "json.decoder.JSONDecodeError")
        self.assertIn('in parse', logged["error"]["stack"])
        self.assertFalse(thread["handled"])
        self.assertEqual(thread["error"]["type"], "ZeroDivisionError")
        self.assertEqual(uncaught["level"], "fatal")
        self.assertEqual(uncaught["error"]["type"], "RuntimeError")
        self.assertEqual(uncaught["error"]["cause"]["type"], "json.decoder.JSONDecodeError")
        self.assertNotIn("The above exception", uncaught["error"]["stack"])
        self.assertEqual([b["message"] for b in uncaught["breadcrumbs"]], ["cache cold", "parse failed"])
        for ev in events:
            self.assertEqual(ev["$schema"], "monitor.event.v1")
            self.assertEqual(ev["runtime"], "python")
            self.assertEqual(ev["sdk"]["mode"], "auto")

    def test_the_shadowed_sitecustomize_still_runs(self):
        original = tempfile.mkdtemp(prefix="monitor-orig-site-")
        with open(os.path.join(original, "sitecustomize.py"), "w") as fh:
            fh.write("import sys\nsys.stderr.write('original ran\\n')\nMARK = 1\n")
        res, _, _ = run(
            """
            import sys, sitecustomize
            print(getattr(sitecustomize, "MARK", None))
            print(any(p.endswith("bootstrap") for p in sys.path))
            """,
            auto=True,
            extra_path=original,
        )
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(res.stderr, "original ran\n")
        self.assertEqual(res.stdout, "1\nFalse\n")


class ExplicitTest(unittest.TestCase):
    def test_capture_api_with_context(self):
        res, events, _ = run(
            """
            import monitorcli
            monitorcli.init(service="api", release="1.2.3", environment="dev", tags={"region": "mx"},
                            before_send=lambda ev: None if ev.get("message") == "drop me" else ev)
            monitorcli.add_breadcrumb("select users", category="db")
            try:
                {}["missing"]
            except KeyError:
                print(type(monitorcli.capture_exception(tags={"order": 42})).__name__)
            monitorcli.capture_exception(ValueError("never raised"))
            monitorcli.capture_message("cache rebuilt")
            monitorcli.capture_message("drop me", level="warning")
            print(monitorcli.capture_exception())
            """,
            explicit=True,
        )
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(res.stdout, "str\nNone\n")
        self.assertEqual(len(events), 3)
        err, never, msg = events
        self.assertEqual(err["kind"], "captured")
        self.assertTrue(err["handled"])
        self.assertEqual(err["service"], "api")
        self.assertEqual(err["release"], "1.2.3")
        self.assertEqual(err["tags"], {"region": "mx", "order": "42"})
        self.assertEqual([b["message"] for b in err["breadcrumbs"]], ["select users"])
        self.assertEqual(err["error"]["type"], "KeyError")
        # A never-raised exception gets the stack where it was captured.
        self.assertIn("Traceback (most recent call last)", never["error"]["stack"])
        self.assertIn("app.py", never["error"]["stack"])
        self.assertEqual(msg["kind"], "message")
        self.assertEqual(msg["level"], "info")

    def test_rate_limit_and_inbox_fallback(self):
        state = tempfile.mkdtemp(prefix="monitor-state-")
        blocker = os.path.join(state, "file")
        open(blocker, "w").close()
        res, _, _ = run(
            """
            import monitorcli
            for i in range(80):
                monitorcli.capture_message("loop %d" % i)
            """,
            explicit=True,
            env={"MONITOR_EVENTS_DIR": os.path.join(blocker, "launch"), "XDG_STATE_HOME": state},
        )
        self.assertEqual(res.returncode, 0, res.stderr)
        inbox = os.path.join(state, "monitor", "events", "inbox")
        self.assertEqual(len(read_events(inbox)), 50)
        self.assertEqual(os.stat(inbox).st_mode & 0o777, 0o700)


if __name__ == "__main__":
    unittest.main()
