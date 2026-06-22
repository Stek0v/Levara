# Lift structured extraction sidecar

Levara can route table-heavy or visual-only PDFs to a schema-driven extraction
sidecar before ordinary ingest. The Go server stays dependency-light; Python,
`lift-pdf`, vLLM, and GPU/runtime choices live in the sidecar process.

## Start sidecar

```bash
pip install lift-pdf
python3 scripts/lift_sidecar.py --host 127.0.0.1 --port 8097 --method vllm
```

For local HuggingFace inference:

```bash
pip install 'lift-pdf[hf]'
python3 scripts/lift_sidecar.py --method hf
```

Useful env:

- `VLLM_API_BASE=http://localhost:8000/v1`
- `LIFT_METHOD=vllm`
- `LIFT_MAX_OUTPUT_TOKENS=12384`
- `LIFT_SIDECAR_PORT=8097`

## Wire Levara

Set one of:

```bash
export STRUCTURED_EXTRACT_ENDPOINT=http://127.0.0.1:8097/extract
# or
export LIFT_ENDPOINT=http://127.0.0.1:8097/extract
```

Or pass the endpoint explicitly at server startup:

```bash
./levara-server --structured-extract-endpoint http://127.0.0.1:8097/extract
```

Then upload a PDF with `schema`, `schema_id`, or `structured_schema`. Levara
runs its cheap PDF preflight first. If the PDF has table signals, or looks
visual-only, `/add` calls the sidecar and indexes a markdown projection of the
returned JSON.

The raw extraction JSON is saved under:

```text
<storagePath>/structured_extractions/<data_id>.json
```

## Sidecar contract

Request:

```json
{
  "filename": "invoice.pdf",
  "content_base64": "...",
  "schema": "{\"type\":\"object\",\"properties\":{\"total\":{\"type\":\"number\"}}}",
  "page_range": [1]
}
```

`page_range` from Levara is 1-based. The sidecar converts it to lift's 0-based
range before calling `lift.extract`.

Response:

```json
{
  "extraction": {"total": 155.5},
  "metadata": {},
  "raw": "{\"total\":155.5}",
  "error": false,
  "token_count": 33
}
```
