# Vision/OCR benchmark protocol

This directory contains a runner for OCR and entity-extraction experiments.
It does not contain an accepted ranking of models. Earlier sample accuracy,
latency and RAM numbers were illustrative, not measurements, and have been
removed. Current product validation is described in
[testing](../../docs/testing.md).

## Prepare inputs and a target

Use a local or explicitly selected isolated Ollama instance and synthetic or
approved test documents. The runner sends image/text contents to that instance.
Pin the model artifacts and record their digests; a mutable model tag alone
is not enough to reproduce a comparison.

The runner reads `images/*.png` and `texts/*.txt` below this directory.
Suggested corpus, with a manually checked ground-truth transcription for each:

| Set | Inputs | What to check |
|-----|--------|---------------|
| A. UI and code | dashboard, terminal, diff, chat | small text, symbols, line ordering |
| B. Documents | printed Russian/English, handwriting, receipt, table | exact numbers, names, columns and missing content |
| C. Diagrams | architecture and entity relationships | labels, edges and invented text |
| D. Entity extraction | technical paragraphs with expected entities/relations | valid JSON, precision and recall against labels |

Keep input digests, language, image resolution and labels with the report.
Do not infer accuracy from output length or a successful HTTP response.

## Run the existing CLI

First inspect options without sending requests:

```bash
python3 tests/vision-ocr-benchmark/run_benchmark.py --help
```

After selecting and preparing a model supported by the runner:

```bash
export LEVARA_OCR_TEST_URL=http://127.0.0.1:11434
python3 tests/vision-ocr-benchmark/run_benchmark.py \
  --platform mac --ollama-url "$LEVARA_OCR_TEST_URL" \
  --vision-only --model moondream
```

`--model` is a substring filter on names hardcoded in `VISION_MODELS` or
`TEXT_MODELS`, not an arbitrary model selector. The source currently lists
moondream, granite3.2-vision, llava and minicpm-v for vision, and qwen3:0.6b
for text. These are experiment candidates, not hardware compatibility or
quality recommendations. LFM2.5 is not in the current runner.

`--platform` selects the runner's predefined candidate list; it does not detect
hardware or validate that the model fits. `--entity-only` runs text cases.
There is **no `--output-dir` option**: the runner writes individual JSON files
and `benchmark_<platform>_<timestamp>.json` into this directory's `results/`.
Individual case filenames can be overwritten on a later run, so retain the
combined artifact for each experiment.

## What the runner actually measures

| Field | Actual behavior | Limit |
|-------|-----------------|-------|
| `timing.total_seconds` | wall-clock HTTP request duration | includes any loading inside the request; no separate load/inference timing |
| OCR text and word count | response text length, words, error flag | response stored only up to 5,000 characters; length is not accuracy |
| `memory.before_mb` / `after_mb` | local `ps -C ollama` RSS snapshots | not model peak RAM, not the remote host; unsupported commands can yield zero |
| Entity JSON validity | parses a JSON-looking response substring | counts entities/relations but does not judge correctness |
| Summary | means of successful OCR timing/word counts; entity JSON counts | excludes failed OCR calls from timing means; report failures separately |

The runner does **not** calculate OCR accuracy/completeness, table fidelity,
hallucination rate, entity precision/recall, peak memory or model-load time.
Measure these separately against the labelled corpus. A reported memory zero
means the probe may be unavailable; it does not mean zero memory use.

## Report template

Leave unmeasured values blank. Attach raw results and manually scored examples.

| Model artifact | Hardware | Corpus digest | Successful / attempted | Request p50/p95 | OCR CER/WER | Exact numbers / expected | Table checks | Peak RAM |
|----------------|----------|---------------|------------------------|-----------------|-------------|--------------------------|--------------|----------|
| — | — | — | — | — | — | — | — | — |

For entity extraction, add per-case expected/found/correct entities and
relations, precision, recall and invalid JSON count. Distinguish cold and warm
requests, record software versions and commands, and repeat on the actual
target hardware before making a model recommendation.
