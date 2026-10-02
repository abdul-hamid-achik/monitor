# Loaded through PYTHONPATH by `monitor run --probes`: the only file in its
# directory, so it shadows nothing but sitecustomize itself. It
#   1. takes its own directory back off sys.path, so the app sees the path
#      it would have had;
#   2. runs the sitecustomize it shadowed, if there is one (an error there
#      still reaches Python's own "Error in sitecustomize" report);
#   3. loads the monitorcli SDK in auto mode: an installed copy when the app
#      has one (so explicit and auto share one instance), else the copy
#      bundled next to this directory.
# It never prints and never lets the SDK break the app.


def _monitor_bootstrap():
    import importlib.machinery
    import importlib.util
    import os
    import sys

    here = os.path.dirname(os.path.abspath(__file__))
    sys.path[:] = [p for p in sys.path if os.path.abspath(p or os.curdir) != here]

    def load_sdk():
        try:
            try:
                import monitorcli
            except ImportError:
                init = os.path.join(os.path.dirname(here), "monitorcli", "__init__.py")
                spec = importlib.util.spec_from_file_location(
                    "monitorcli", init, submodule_search_locations=[os.path.dirname(init)])
                monitorcli = importlib.util.module_from_spec(spec)
                sys.modules["monitorcli"] = monitorcli
                try:
                    spec.loader.exec_module(monitorcli)
                except Exception:
                    sys.modules.pop("monitorcli", None)
                    raise
            auto = getattr(monitorcli, "_auto", None)
            if auto is not None:
                auto()
        except Exception:
            pass

    try:
        spec = importlib.machinery.PathFinder.find_spec("sitecustomize", sys.path)
        if spec is not None and spec.loader is not None:
            original = importlib.util.module_from_spec(spec)
            sys.modules["sitecustomize"] = original
            spec.loader.exec_module(original)
    finally:
        load_sdk()


_monitor_bootstrap()
del _monitor_bootstrap
