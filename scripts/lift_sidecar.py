#!/usr/bin/env python3
"""Lift-compatible structured extraction sidecar for Levara.

HTTP contract:
  POST /extract
  {
    "filename": "invoice.pdf",
    "content_base64": "...",
    "schema": "{...}",
    "page_range": [1, 2]
  }

Response:
  {
    "extraction": {...},
    "metadata": {...},
    "raw": "...",
    "error": false,
    "token_count": 123
  }

The sidecar keeps Python/model dependencies out of the Go binary. Install
`lift-pdf` in the Python environment used to run this script.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import tempfile
import traceback
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Callable


Extractor = Callable[[str, str, str | None, int | None], Any]


class SidecarConfig:
    def __init__(
        self,
        method: str = "vllm",
        vllm_api_base: str | None = None,
        max_output_tokens: int | None = None,
    ) -> None:
        self.method = method
        self.vllm_api_base = vllm_api_base
        self.max_output_tokens = max_output_tokens


_model: Any = None


def _load_lift_model(method: str) -> Any:
    global _model
    if _model is None:
        from lift.model import InferenceManager

        _model = InferenceManager(method=method)
    return _model


def lift_extract(filepath: str, schema: str, page_range: str | None, max_output_tokens: int | None, config: SidecarConfig) -> Any:
    from lift import extract

    model = _load_lift_model(config.method)
    kwargs: dict[str, Any] = {}
    if config.vllm_api_base:
        kwargs["vllm_api_base"] = config.vllm_api_base
    return extract(
        filepath,
        schema,
        model=model,
        page_range=page_range,
        max_output_tokens=max_output_tokens,
        **kwargs,
    )


def page_range_to_lift(value: Any) -> str | None:
    """Convert Levara 1-based page numbers to lift's 0-based page range string."""
    if value is None or value == "":
        return None
    if isinstance(value, str):
        return value
    if not isinstance(value, list):
        raise ValueError("page_range must be a list of 1-based page numbers or a string")
    pages: list[int] = []
    for item in value:
        if not isinstance(item, int):
            raise ValueError("page_range list must contain integers")
        pages.append(max(item - 1, 0))
    pages = sorted(set(pages))
    return ",".join(str(p) for p in pages) if pages else None


def normalize_schema(value: Any) -> str:
    if value is None or value == "":
        raise ValueError("schema is required")
    if isinstance(value, str):
        return value
    return json.dumps(value, separators=(",", ":"))


def extension_for(filename: str) -> str:
    ext = Path(filename).suffix.lower()
    if ext in {".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".tiff", ".bmp"}:
        return ext
    return ".pdf"


def result_to_response(result: Any) -> dict[str, Any]:
    extraction = getattr(result, "extraction", None)
    raw = getattr(result, "raw", "")
    token_count = getattr(result, "token_count", 0)
    error = bool(getattr(result, "error", False))
    return {
        "extraction": extraction,
        "metadata": {},
        "raw": raw,
        "error": error,
        "token_count": token_count,
    }


def handle_extract_payload(payload: dict[str, Any], extractor: Extractor, config: SidecarConfig) -> tuple[int, dict[str, Any]]:
    filename = str(payload.get("filename") or "document.pdf")
    content_b64 = payload.get("content_base64")
    if not isinstance(content_b64, str) or not content_b64:
        return HTTPStatus.BAD_REQUEST, {"error": True, "detail": "content_base64 is required"}

    try:
        data = base64.b64decode(content_b64, validate=True)
    except Exception as exc:
        return HTTPStatus.BAD_REQUEST, {"error": True, "detail": f"invalid content_base64: {exc}"}

    try:
        schema = normalize_schema(payload.get("schema"))
        page_range = page_range_to_lift(payload.get("page_range"))
    except ValueError as exc:
        return HTTPStatus.BAD_REQUEST, {"error": True, "detail": str(exc)}

    max_output_tokens = payload.get("max_output_tokens", config.max_output_tokens)
    if max_output_tokens is not None and not isinstance(max_output_tokens, int):
        return HTTPStatus.BAD_REQUEST, {"error": True, "detail": "max_output_tokens must be an integer"}

    suffix = extension_for(filename)
    with tempfile.NamedTemporaryFile(prefix="levara-lift-", suffix=suffix, delete=False) as f:
        tmp_path = f.name
        f.write(data)

    try:
        result = extractor(tmp_path, schema, page_range, max_output_tokens)
        return HTTPStatus.OK, result_to_response(result)
    except ImportError as exc:
        return HTTPStatus.SERVICE_UNAVAILABLE, {
            "error": True,
            "detail": f"lift-pdf dependency is not installed: {exc}",
        }
    except Exception as exc:
        return HTTPStatus.INTERNAL_SERVER_ERROR, {
            "error": True,
            "detail": str(exc),
            "traceback": traceback.format_exc(limit=5),
        }
    finally:
        try:
            os.unlink(tmp_path)
        except OSError:
            pass


class LiftSidecarHandler(BaseHTTPRequestHandler):
    config = SidecarConfig()

    def do_GET(self) -> None:
        if self.path in {"/health", "/ready"}:
            self._json(HTTPStatus.OK, {"status": "ok"})
            return
        self._json(HTTPStatus.NOT_FOUND, {"error": True, "detail": "not found"})

    def do_POST(self) -> None:
        if self.path not in {"/", "/extract"}:
            self._json(HTTPStatus.NOT_FOUND, {"error": True, "detail": "not found"})
            return

        length = int(self.headers.get("Content-Length", "0") or "0")
        try:
            payload = json.loads(self.rfile.read(length))
        except Exception as exc:
            self._json(HTTPStatus.BAD_REQUEST, {"error": True, "detail": f"invalid JSON: {exc}"})
            return
        if not isinstance(payload, dict):
            self._json(HTTPStatus.BAD_REQUEST, {"error": True, "detail": "request body must be a JSON object"})
            return

        def extractor(path: str, schema: str, page_range: str | None, max_tokens: int | None) -> Any:
            return lift_extract(path, schema, page_range, max_tokens, self.config)

        status, body = handle_extract_payload(payload, extractor, self.config)
        self._json(status, body)

    def log_message(self, fmt: str, *args: Any) -> None:
        if os.getenv("LIFT_SIDECAR_QUIET", "true").lower() not in {"1", "true", "yes"}:
            super().log_message(fmt, *args)

    def _json(self, status: int, body: dict[str, Any]) -> None:
        raw = json.dumps(body, ensure_ascii=False).encode("utf-8")
        self.send_response(int(status))
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def main() -> None:
    parser = argparse.ArgumentParser(description="Levara lift structured extraction sidecar")
    parser.add_argument("--host", default=os.getenv("LIFT_SIDECAR_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.getenv("LIFT_SIDECAR_PORT", "8097")))
    parser.add_argument("--method", choices=["vllm", "hf"], default=os.getenv("LIFT_METHOD", "vllm"))
    parser.add_argument("--vllm-api-base", default=os.getenv("VLLM_API_BASE"))
    parser.add_argument("--max-output-tokens", type=int, default=int(os.getenv("LIFT_MAX_OUTPUT_TOKENS", "0")) or None)
    args = parser.parse_args()

    LiftSidecarHandler.config = SidecarConfig(
        method=args.method,
        vllm_api_base=args.vllm_api_base,
        max_output_tokens=args.max_output_tokens,
    )
    server = ThreadingHTTPServer((args.host, args.port), LiftSidecarHandler)
    print(f"lift sidecar listening on http://{args.host}:{args.port}/extract method={args.method}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
