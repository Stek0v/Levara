import base64
import importlib.util
from pathlib import Path
from types import SimpleNamespace


def load_sidecar():
    path = Path(__file__).resolve().parents[1] / "scripts" / "lift_sidecar.py"
    spec = importlib.util.spec_from_file_location("lift_sidecar", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def test_page_range_to_lift_converts_one_based_pages():
    sidecar = load_sidecar()
    assert sidecar.page_range_to_lift([1, 3, 3]) == "0,2"
    assert sidecar.page_range_to_lift("0-2") == "0-2"
    assert sidecar.page_range_to_lift([]) is None


def test_handle_extract_payload_success(tmp_path):
    sidecar = load_sidecar()
    seen = {}

    def fake_extract(path, schema, page_range, max_output_tokens):
        seen["path"] = path
        seen["schema"] = schema
        seen["page_range"] = page_range
        seen["max_output_tokens"] = max_output_tokens
        assert Path(path).read_bytes() == b"%PDF-1.4\n"
        return SimpleNamespace(
            extraction={"invoice_number": "INV-42", "total": 155.5},
            raw='{"invoice_number":"INV-42"}',
            token_count=12,
            error=False,
        )

    payload = {
        "filename": "invoice.pdf",
        "content_base64": base64.b64encode(b"%PDF-1.4\n").decode(),
        "schema": {"type": "object"},
        "page_range": [1, 2],
        "max_output_tokens": 99,
    }
    status, body = sidecar.handle_extract_payload(payload, fake_extract, sidecar.SidecarConfig())

    assert status == 200
    assert body["error"] is False
    assert body["extraction"]["invoice_number"] == "INV-42"
    assert body["token_count"] == 12
    assert seen["schema"] == '{"type":"object"}'
    assert seen["page_range"] == "0,1"
    assert seen["max_output_tokens"] == 99
    assert not Path(seen["path"]).exists()


def test_handle_extract_payload_rejects_missing_schema():
    sidecar = load_sidecar()
    payload = {
        "filename": "invoice.pdf",
        "content_base64": base64.b64encode(b"%PDF-1.4\n").decode(),
    }
    status, body = sidecar.handle_extract_payload(payload, lambda *_: None, sidecar.SidecarConfig())
    assert status == 400
    assert body["error"] is True
    assert "schema" in body["detail"]


def test_handle_extract_payload_rejects_bad_base64():
    sidecar = load_sidecar()
    payload = {
        "filename": "invoice.pdf",
        "content_base64": "not base64",
        "schema": "{}",
    }
    status, body = sidecar.handle_extract_payload(payload, lambda *_: None, sidecar.SidecarConfig())
    assert status == 400
    assert body["error"] is True
    assert "content_base64" in body["detail"]
