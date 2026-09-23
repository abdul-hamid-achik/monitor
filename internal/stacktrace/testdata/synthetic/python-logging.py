import logging

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger()


def flaky_parse(raw):
    if "bad" in raw:
        raise ValueError(f"flaky_parse: malformed payload near token {raw[:8]!r}")


def tick_errors():
    for p in ['{"ok":true}', "bad-payload-123"]:
        try:
            flaky_parse(p)
        except ValueError:
            logger.exception("flaky_parse failed")


def message_only():
    logger.error("connection pool exhausted: 0 of 10 connections available")


tick_errors()
message_only()
