"""A small Python app with three errors, used by specs/run_probes_python.yml
and the SDK docs. The app logs to a FILE, as most services do:
  - a parse error it catches and logs with logger.exception, invisible on
    stderr: only `monitor run --probes` (or the SDK) sees it;
  - a worker thread that dies;
  - an uncaught crash with a cause.
"""
import json
import logging
import os
import tempfile
import threading

log_dir = os.environ.get("APP_LOG_DIR") or tempfile.gettempdir()
logging.basicConfig(filename=os.path.join(log_dir, "sdk-example-app.log"), level=logging.INFO)
log = logging.getLogger("billing")

print("booting")
log.info("loaded 3 plans")


def parse_invoice(text):
    return json.loads(text)


try:
    parse_invoice("{bad")
except ValueError:
    log.exception("invoice parse failed")

worker = threading.Thread(target=lambda: 1 / 0, name="reconciler")
worker.start()
worker.join()


def settle():
    try:
        parse_invoice("{bad")
    except ValueError as err:
        raise RuntimeError("settlement failed") from err


settle()
