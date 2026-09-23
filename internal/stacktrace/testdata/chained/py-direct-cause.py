def root_cause():
    raise ValueError("root cause boom")


def wrap_it():
    try:
        root_cause()
    except ValueError as err:
        raise RuntimeError("work failed") from err


wrap_it()
