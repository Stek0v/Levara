"""Shared inference regression: stdlib runner, fake model, no GPU/downloads."""
from concurrent.futures import ThreadPoolExecutor
import threading
import time
import unittest
from unittest.mock import patch

with patch.dict("os.environ", EMBED_BENCH_MODEL="_fake"):
    from . import server


class SharedInferenceTests(unittest.TestCase):
    def test_parallel_requests_serialize_model_and_release_after_error(self):
        active = peak = 0
        count_lock = threading.Lock()
        kinds = []

        class Backend:
            dim = 2

            def __init__(self, dim):
                pass

            def embed(self, texts, kind="document"):
                nonlocal active, peak
                with count_lock:
                    active += 1
                    peak = max(peak, active)
                    kinds.append(kind)
                try:
                    time.sleep(0.01)  # allow competing requests to reach inference
                    if texts == ["fail"]:
                        raise ValueError("fake model error")
                    return [[1.0, 0.0] for _ in texts]
                finally:
                    with count_lock:
                        active -= 1

        with patch.dict("os.environ", EMBED_BENCH_MODEL="_fake"), patch.object(server, "_FakeBackend", Backend):
            app = server.build_app()
        endpoint = next(route.endpoint for route in app.routes if route.path == "/v1/embeddings")
        start = threading.Barrier(8)

        def call(index):
            start.wait(timeout=5)
            alias = "_fake:query" if index % 2 else "_fake:document"
            return endpoint(server.EmbedRequest(input=[str(index)], model=alias))

        with ThreadPoolExecutor(max_workers=8) as pool:
            results = list(pool.map(call, range(8)))
        self.assertEqual(peak, 1, "shared backend executed concurrently")
        self.assertEqual(kinds.count("query"), 4)
        self.assertEqual(kinds.count("document"), 4)
        self.assertTrue(all(row["data"] == [{"embedding": [1.0, 0.0], "index": 0}] for row in results))
        with self.assertRaisesRegex(ValueError, "fake model error"):
            endpoint(server.EmbedRequest(input="fail"))
        # A separate daemon thread bounds a broken lock's failure instead of hanging tests.
        done = threading.Event()
        result = []

        def after_failure():
            result.append(endpoint(server.EmbedRequest(input="ok")))
            done.set()

        threading.Thread(target=after_failure, daemon=True).start()
        self.assertTrue(done.wait(2), "inference lock was not released after error")
        self.assertEqual(result[0]["data"][0]["embedding"], [1.0, 0.0])


if __name__ == "__main__":
    unittest.main()
