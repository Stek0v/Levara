"""OpenAI-compatible embedding server for pplx-embed-context-v1-0.6b.
Uses late chunking: when multiple texts arrive in one request,
they are concatenated with [SEP] so each chunk's embedding
is aware of its neighbors. Single texts work as normal."""

import torch
from fastapi import FastAPI
from pydantic import BaseModel
from transformers import AutoTokenizer, AutoModel
import uvicorn

app = FastAPI()

MODEL_NAME = "perplexity-ai/pplx-embed-context-v1-0.6b"
MODEL_DIMS = 1024

if torch.cuda.is_available():
    DEVICE = "cuda"
    DTYPE = torch.bfloat16
elif torch.backends.mps.is_available():
    DEVICE = "mps"
    DTYPE = torch.float32  # MPS has limited bfloat16 support
else:
    DEVICE = "cpu"
    DTYPE = torch.float32

tokenizer = AutoTokenizer.from_pretrained(MODEL_NAME, trust_remote_code=True)
model = AutoModel.from_pretrained(
    MODEL_NAME, trust_remote_code=True, torch_dtype=DTYPE,
).to(DEVICE)
model.eval()

_compiled = False
if DEVICE == "cuda":
    try:
        model = torch.compile(model)
        _warmup = tokenizer("warmup", return_tensors="pt").to(DEVICE)
        with torch.no_grad():
            model(**{k: v for k, v in _warmup.items()})
        _compiled = True
        print("[embed-server] torch.compile + warmup done")
    except Exception as e:
        print(f"[embed-server] torch.compile failed, falling back to eager: {e}")
        model = AutoModel.from_pretrained(
            MODEL_NAME, trust_remote_code=True, torch_dtype=DTYPE,
        ).to(DEVICE)
        model.eval()

SEP_TOKEN_ID = tokenizer.sep_token_id or tokenizer.eos_token_id

_dtype_name = "bfloat16" if DTYPE == torch.bfloat16 else "float32"
print(f"[embed-server] model={MODEL_NAME} device={DEVICE} dtype={_dtype_name} compiled={_compiled} dims={MODEL_DIMS} sep_id={SEP_TOKEN_ID}")
if DEVICE == "cuda":
    print(f"[embed-server] GPU: {torch.cuda.get_device_name(0)}, VRAM: {torch.cuda.get_device_properties(0).total_memory / 1024**3:.1f} GiB")
elif DEVICE == "mps":
    print("[embed-server] Apple Silicon MPS accelerator")

# warmup
_warmup = tokenizer("warmup", return_tensors="pt").to(DEVICE)
with torch.no_grad():
    model(**{k: v for k, v in _warmup.items()})
print(f"[embed-server] warmup done ({DEVICE})")


class EmbeddingRequest(BaseModel):
    input: str | list[str]
    model: str = "text-embedding-3-small"
    dimensions: int | None = None


def embed_single(texts: list[str]) -> list[list[float]]:
    """Standard per-text embedding (no context)."""
    encoded = tokenizer(texts, padding=True, truncation=True, return_tensors="pt").to(DEVICE)
    with torch.no_grad():
        output = model(**encoded)
    mask = encoded["attention_mask"].unsqueeze(-1).float()
    hidden = output.last_hidden_state.float()
    vectors = (hidden * mask).sum(dim=1) / mask.sum(dim=1)
    vectors = torch.nan_to_num(vectors, nan=0.0, posinf=0.0, neginf=0.0)
    vectors = torch.nn.functional.normalize(vectors, p=2, dim=1)
    return vectors.cpu().tolist()


def embed_context(texts: list[str]) -> list[list[float]]:
    """Late chunking: concatenate texts with [SEP], run through model,
    then extract per-chunk embeddings using mean pooling between [SEP] positions."""
    # tokenize each chunk individually (without special tokens)
    chunk_ids = [tokenizer.encode(t, add_special_tokens=False) for t in texts]

    # build concatenated sequence: chunk1 [SEP] chunk2 [SEP] ... chunkN [SEP]
    input_ids = []
    boundaries = []  # (start, end) for each chunk
    for ids in chunk_ids:
        start = len(input_ids)
        input_ids.extend(ids)
        end = len(input_ids)
        boundaries.append((start, end))
        input_ids.append(SEP_TOKEN_ID)

    # truncate to model max length
    max_len = tokenizer.model_max_length or 32768
    input_ids = input_ids[:max_len]

    input_tensor = torch.tensor([input_ids], device=DEVICE)
    attention_mask = torch.ones_like(input_tensor)

    with torch.no_grad():
        output = model(input_ids=input_tensor, attention_mask=attention_mask)

    hidden = output.last_hidden_state[0].float()  # (seq_len, dims), FP16→FP32 for stable pooling

    # extract per-chunk embeddings via mean pooling within boundaries
    vectors = []
    for start, end in boundaries:
        if end > hidden.size(0):
            break
        chunk_hidden = hidden[start:end]
        vec = chunk_hidden.mean(dim=0)
        vec = torch.nan_to_num(vec, nan=0.0, posinf=0.0, neginf=0.0)
        vec = torch.nn.functional.normalize(vec, p=2, dim=0)
        vectors.append(vec.cpu().tolist())

    return vectors


@app.get("/health")
def health():
    info = {"status": "ok", "device": DEVICE, "model": MODEL_NAME, "dims": MODEL_DIMS, "mode": "context", "dtype": _dtype_name}
    if DEVICE == "cuda":
        info["gpu"] = torch.cuda.get_device_name(0)
    return info


@app.post("/v1/embeddings")
def embeddings(req: EmbeddingRequest):
    texts = [req.input] if isinstance(req.input, str) else req.input

    # filter empty strings, replace with placeholder to keep indices stable
    texts = [t if t and t.strip() else " " for t in texts]

    # single text → standard embedding; multiple texts → late chunking
    if len(texts) == 1:
        vectors = embed_single(texts)
    else:
        vectors = embed_context(texts)

    if req.dimensions and req.dimensions < MODEL_DIMS:
        vectors = [v[: req.dimensions] for v in vectors]

    return {
        "object": "list",
        "data": [
            {"object": "embedding", "embedding": v, "index": i}
            for i, v in enumerate(vectors)
        ],
        "model": req.model,
        "usage": {"prompt_tokens": 0, "total_tokens": 0},
    }


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=9001)
