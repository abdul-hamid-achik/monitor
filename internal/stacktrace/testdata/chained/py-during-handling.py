def root_cause():
    raise ValueError("root cause boom")


def wrap_it():
    try:
        root_cause()
    except ValueError:
        raise RuntimeError("work failed while handling")


wrap_it()
