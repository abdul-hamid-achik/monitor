"""Monitor's Python SDK.

Two ways in:

- auto: ``monitor run --probes -- python app.py`` loads this module through a
  bootstrap ``sitecustomize``. Nothing to install, no code to change.
- explicit: ``import monitorcli`` and call :func:`init`,
  :func:`capture_exception`, :func:`capture_message`, :func:`add_breadcrumb`
  and :func:`set_tag`.

Either way it only observes. It never prints, never adds a logging handler
(which would change what the app prints), never changes the exit status, and
never reads locals, arguments, environment variables or request data: only
the exception itself (type, message, and the traceback exactly as Python
formats it). Each event is one JSON file (``monitor.event.v1``) written
atomically into ``$MONITOR_EVENTS_DIR`` when ``monitor run`` set it, else into
the global inbox that ``monitor events drain`` and ``monitor issues`` read.
Parsing, scrubbing and grouping happen in monitor, not here.
"""

import collections
import json
import logging
import os
import sys
import threading
import time
import traceback

__all__ = [
    "init",
    "capture_exception",
    "capture_message",
    "add_breadcrumb",
    "set_tag",
    "set_tags",
    "flush",
    "__version__",
]

__version__ = "0.1.0"

_SCHEMA = "monitor.event.v1"
_MAX_BREADCRUMBS = 30
_MAX_CAUSES = 8
_MAX_TEXT = 8 * 1024
_MAX_STACK = 64 * 1024
# At most _RATE_MAX events per _RATE_WINDOW seconds; the rest are dropped,
# so an error loop never becomes a disk-filling loop.
_RATE_MAX = 50
_RATE_WINDOW = 10.0

_lock = threading.RLock()
_local = threading.local()
_state = {
    "mode": "",
    "service": None,
    "release": None,
    "environment": None,
    "dir": None,
    "before_send": None,
    "capture_logging": True,
    "hooked": False,
    "window_start": 0.0,
    "window_count": 0,
    "dropped": 0,
    "seq": 0,
}
_tags = {}
_breadcrumbs = collections.deque(maxlen=_MAX_BREADCRUMBS)
_made_dirs = set()

_LOG_LEVELS = {
    logging.CRITICAL: "fatal",
    logging.ERROR: "error",
    logging.WARNING: "warning",
    logging.INFO: "info",
    logging.DEBUG: "debug",
}


def _env(name):
    value = os.environ.get(name, "").strip()
    return value or None


def _clip(text, limit):
    text = text if isinstance(text, str) else str(text)
    return text[:limit] if len(text) > limit else text


def _safe_str(value):
    try:
        return str(value)
    except Exception:
        return "<unprintable>"


def _type_name(exc_type):
    # The same spelling Python's own traceback prints on its last line, so an
    # event and the stderr traceback of the same crash group together.
    module = getattr(exc_type, "__module__", None)
    name = getattr(exc_type, "__qualname__", None) or getattr(exc_type, "__name__", "Exception")
    if module in (None, "builtins", "__main__"):
        return name
    return module + "." + name


def _own_frame(filename):
    return os.path.abspath(filename) == os.path.abspath(__file__)


def _stack_text(exc):
    tb = getattr(exc, "__traceback__", None)
    if tb is not None:
        return "".join(traceback.format_exception(type(exc), exc, tb, chain=False))
    # Never raised (built and passed straight to capture_exception): use
    # where it was captured, which is the best line there is.
    frames = [f for f in traceback.extract_stack() if not _own_frame(f.filename)]
    lines = ["Traceback (most recent call last):\n"]
    lines.extend(traceback.format_list(frames))
    lines.extend(traceback.format_exception_only(type(exc), exc))
    return "".join(lines)


def _serialize(exc, depth, seen):
    if exc is None or depth >= _MAX_CAUSES or id(exc) in seen:
        return None
    seen.add(id(exc))
    out = {"type": _clip(_type_name(type(exc)), 256), "value": _clip(_safe_str(exc), _MAX_TEXT)}
    try:
        out["stack"] = _clip(_stack_text(exc), _MAX_STACK)
    except Exception:
        pass
    cause = getattr(exc, "__cause__", None)
    if cause is None and not getattr(exc, "__suppress_context__", False):
        cause = getattr(exc, "__context__", None)
    nested = _serialize(cause, depth + 1, seen)
    if nested:
        out["cause"] = nested
    return out


def _timestamp():
    now = time.time()
    return time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(now)) + ".%03dZ" % int((now % 1) * 1000)


def _inbox_dir():
    base = _env("XDG_STATE_HOME")
    if not base or not os.path.isabs(base):
        base = os.path.join(os.path.expanduser("~"), ".local", "state")
    return os.path.join(base, "monitor", "events", "inbox")


def _write_into(directory, name, data):
    if directory not in _made_dirs:
        os.makedirs(directory, mode=0o700, exist_ok=True)
        _made_dirs.add(directory)
    tmp = os.path.join(directory, "." + name + ".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            fh.write(data)
        os.replace(tmp, os.path.join(directory, name + ".json"))
    except Exception:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def _deliver(event):
    # Synchronous on purpose: an uncaught exception is the interpreter's
    # last moment, with no thread left to flush a queue.
    now = time.time()
    with _lock:
        if now - _state["window_start"] > _RATE_WINDOW:
            _state["window_start"] = now
            _state["window_count"] = 0
        if _state["window_count"] >= _RATE_MAX:
            _state["dropped"] += 1
            return False
        _state["window_count"] += 1
        seq = _state["seq"]
        _state["seq"] = seq + 1
    nanos = "%019d" % (int(now * 1000) * 1000000 + seq % 1000000)
    name = "%s-%d-%s" % (nanos, os.getpid(), event["event_id"])
    data = json.dumps(event, default=_safe_str)
    for directory in (_state["dir"], _env("MONITOR_EVENTS_DIR"), _inbox_dir()):
        if not directory:
            continue
        try:
            _write_into(directory, name, data)
            return True
        except Exception:
            _made_dirs.discard(directory)
    return False


def _build(kind, fields):
    event = {
        "$schema": _SCHEMA,
        "event_id": os.urandom(16).hex(),
        "timestamp": _timestamp(),
        "kind": kind,
        "runtime": "python",
        "sdk": {"name": "monitor.python", "version": __version__, "mode": _state["mode"] or "explicit"},
    }
    extra_tags = fields.pop("tags", None) or {}
    event.update(fields)
    with _lock:
        tags = dict(_tags)
        crumbs = list(_breadcrumbs)
    for key, value in extra_tags.items():
        if key is not None and value is not None:
            tags[_clip(_safe_str(key), 64)] = _clip(_safe_str(value), 256)
    if tags:
        event["tags"] = tags
    if crumbs:
        event["breadcrumbs"] = crumbs
    for key in ("service", "release", "environment"):
        if _state[key]:
            event[key] = _state[key]
    try:
        event["cwd"] = os.getcwd()
    except OSError:
        pass
    event["pid"] = os.getpid()
    launch = _env("MONITOR_LAUNCH_ID")
    if launch:
        event["launch_id"] = launch
    return event


def _send(event):
    # Never raises and never recurses: a failure inside capture (or a
    # before_send that logs an exception) is dropped, not captured again.
    if getattr(_local, "sending", False):
        return None
    _local.sending = True
    try:
        hook = _state["before_send"]
        if hook is not None:
            event = hook(event)
            if not event:
                return None
        return event["event_id"] if _deliver(event) else None
    except Exception:
        return None
    finally:
        _local.sending = False


def _capture_error(kind, exc, handled, level=None, tags=None):
    try:
        error = _serialize(exc, 0, set())
    except Exception:
        return None
    if not error:
        return None
    fields = {"error": error, "handled": bool(handled)}
    if level:
        fields["level"] = level
    if tags:
        fields["tags"] = tags
    return _send(_build(kind, fields))


def _push_breadcrumb(message, category=None, level=None):
    message = _clip(_safe_str(message), 1024).strip()
    if not message:
        return
    crumb = {"timestamp": _timestamp(), "message": message}
    if category:
        crumb["category"] = _clip(_safe_str(category), 64)
    if level:
        crumb["level"] = _clip(_safe_str(level), 16)
    with _lock:
        _breadcrumbs.append(crumb)


def _log_level(levelno):
    for threshold in (logging.CRITICAL, logging.ERROR, logging.WARNING, logging.INFO):
        if levelno >= threshold:
            return _LOG_LEVELS[threshold]
    return "debug"


def _on_log_record(record):
    if not _state["capture_logging"] or getattr(_local, "sending", False):
        return
    exc_info = record.exc_info
    if exc_info and exc_info[1] is not None and record.levelno >= logging.ERROR:
        _capture_error("logged", exc_info[1], True, level=_log_level(record.levelno))
    if record.levelno >= logging.INFO:
        try:
            message = record.getMessage()
        except Exception:
            message = _safe_str(record.msg)
        _push_breadcrumb(message, category=record.name, level=_log_level(record.levelno))


def _install_hooks():
    with _lock:
        if _state["hooked"]:
            return
        _state["hooked"] = True

    previous_excepthook = sys.excepthook

    def excepthook(exc_type, exc, tb):
        try:
            if not issubclass(exc_type, (KeyboardInterrupt, SystemExit)):
                _capture_error("uncaught", exc, False, level="fatal")
        except Exception:
            pass
        return previous_excepthook(exc_type, exc, tb)

    sys.excepthook = excepthook

    if hasattr(threading, "excepthook"):
        previous_thread_hook = threading.excepthook

        def thread_hook(args):
            try:
                if args.exc_type is not None and not issubclass(args.exc_type, SystemExit):
                    _capture_error("thread", args.exc_value, False, level="error")
            except Exception:
                pass
            return previous_thread_hook(args)

        threading.excepthook = thread_hook

    # Wrap Logger.handle instead of adding a handler: a handler on the root
    # logger switches off logging's lastResort printer and changes the app's
    # output. The app's record is handled first, then observed.
    original_handle = logging.Logger.handle

    def handle(self, record):
        try:
            return original_handle(self, record)
        finally:
            try:
                _on_log_record(record)
            except Exception:
                pass

    logging.Logger.handle = handle


def init(service=None, release=None, environment=None, tags=None, events_dir=None,
         capture_logging=None, before_send=None):
    """Set the context every later event carries and install the hooks
    (uncaught exceptions, thread exceptions, ``logger.exception(...)``).

    Safe to call more than once; the latest values win.
    """
    with _lock:
        _state["mode"] = "explicit"
        if service is not None:
            _state["service"] = _safe_str(service) or None
        if release is not None:
            _state["release"] = _safe_str(release) or None
        if environment is not None:
            _state["environment"] = _safe_str(environment) or None
        if events_dir is not None:
            _state["dir"] = _safe_str(events_dir) or None
        if capture_logging is not None:
            _state["capture_logging"] = bool(capture_logging)
        if before_send is not None:
            _state["before_send"] = before_send
    if tags:
        set_tags(tags)
    _install_hooks()


def capture_exception(exc=None, level=None, tags=None, handled=True):
    """Record an exception the app caught. With no argument, the one being
    handled right now (``sys.exc_info()``). Returns the event id, or None
    when nothing was written."""
    if exc is None:
        exc = sys.exc_info()[1]
    if exc is None:
        return None
    return _capture_error("captured", exc, handled, level=level or "error", tags=tags)


def capture_message(message, level="info", tags=None):
    """Record a message as an event (grouped by its text). Returns the event
    id, or None when nothing was written."""
    text = _clip(_safe_str(message), _MAX_TEXT)
    if not text.strip():
        return None
    fields = {"message": text, "level": level or "info", "handled": True}
    if tags:
        fields["tags"] = tags
    return _send(_build("message", fields))


def add_breadcrumb(message, category=None, level=None):
    """Record a step that later events will carry (the newest 30 are kept)."""
    _push_breadcrumb(message, category=category, level=level)


def set_tag(key, value):
    """Label every later event. ``None`` removes the tag."""
    if key is None:
        return
    key = _clip(_safe_str(key), 64)
    with _lock:
        if value is None:
            _tags.pop(key, None)
        else:
            _tags[key] = _clip(_safe_str(value), 256)


def set_tags(tags):
    for key, value in dict(tags).items():
        set_tag(key, value)


def flush(timeout=None):
    """Events are written synchronously, so there is never anything to
    flush; kept so code written for other SDKs keeps working."""
    return True


def _auto():
    """What the bootstrap sitecustomize calls under ``monitor run --probes``."""
    with _lock:
        if not _state["mode"]:
            _state["mode"] = "auto"
    _install_hooks()


def _stats():
    return {"dropped": _state["dropped"], "hooked": _state["hooked"], "mode": _state["mode"]}
