#!/usr/bin/env python3
"""Vision OCR + Entity Extraction benchmark for Levara.

Usage:
    python3 run_benchmark.py --platform mac --ollama-url http://localhost:11434
    python3 run_benchmark.py --platform pi --ollama-url http://10.23.0.53:11434
"""

import argparse, base64, json, os, time, subprocess, sys
from pathlib import Path
from datetime import datetime, timezone

SCRIPT_DIR = Path(__file__).parent
IMAGES_DIR = SCRIPT_DIR / "images"
TEXTS_DIR = SCRIPT_DIR / "texts"
RESULTS_DIR = SCRIPT_DIR / "results"

# Models to test
VISION_MODELS = [
    {"id": "moondream:1.8b", "name": "moondream", "size_gb": 1.7, "platforms": ["mac", "pi"]},
    {"id": "granite3.2-vision:2b", "name": "granite3.2-vision", "size_gb": 1.5, "platforms": ["mac", "pi"]},
    {"id": "llava:7b", "name": "llava", "size_gb": 4.7, "platforms": ["mac"]},
    {"id": "minicpm-v:8b", "name": "minicpm-v", "size_gb": 4.9, "platforms": ["mac"]},
]

TEXT_MODELS = [
    {"id": "qwen3:0.6b", "name": "qwen3-0.6b", "size_gb": 0.4, "platforms": ["mac", "pi"]},
]

ENTITY_PROMPT_TEMPLATE = 'Extract entities and relationships from the following text.\nReturn ONLY valid JSON in this exact format:\n{{"entities": [{{"name": "...", "type": "..."}}], "relationships": [{{"from": "...", "to": "...", "type": "..."}}]}}\n\nText: {text}'

VISION_PROMPT = "Extract ALL text from this image. Preserve structure (tables, lists, headings). Include all numbers, dates, names exactly as shown."


def ollama_chat(base_url: str, model: str, prompt: str, images: list[str] | None = None, timeout: int = 120) -> tuple[str, float]:
    """Send chat request to Ollama. Returns (response_text, duration_seconds)."""
    import urllib.request

    msg = {"role": "user", "content": prompt}
    if images:
        msg["images"] = images

    body = json.dumps({
        "model": model,
        "messages": [msg],
        "stream": False,
        "options": {"num_predict": 4000}
    }).encode()

    req = urllib.request.Request(
        f"{base_url}/api/chat",
        data=body,
        headers={"Content-Type": "application/json"},
    )

    t0 = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            data = json.loads(resp.read())
    except Exception as e:
        return f"ERROR: {e}", time.monotonic() - t0

    elapsed = time.monotonic() - t0
    text = data.get("message", {}).get("content", "")
    return text, elapsed


def get_memory_mb(base_url: str) -> int:
    """Get Ollama process memory (approximate)."""
    try:
        result = subprocess.run(["ps", "-o", "rss=", "-C", "ollama"], capture_output=True, text=True)
        if result.stdout.strip():
            return sum(int(x) for x in result.stdout.strip().split("\n")) // 1024
    except:
        pass
    return 0


def run_vision_test(base_url: str, model: dict, image_path: Path, platform: str) -> dict:
    """Run one vision OCR test."""
    test_id = f"{image_path.stem}_{model['name']}_{platform}"
    print(f"  [{test_id}] ", end="", flush=True)

    # Read and encode image
    img_data = image_path.read_bytes()
    img_b64 = base64.b64encode(img_data).decode()

    mem_before = get_memory_mb(base_url)

    # Run inference
    text, elapsed = ollama_chat(base_url, model["id"], VISION_PROMPT, images=[img_b64], timeout=180)

    mem_after = get_memory_mb(base_url)

    is_error = text.startswith("ERROR:")
    word_count = len(text.split()) if not is_error else 0

    print(f"{'FAIL' if is_error else 'OK'} {elapsed:.1f}s {word_count} words")

    return {
        "test_id": test_id,
        "model": model["id"],
        "model_name": model["name"],
        "platform": platform,
        "test_type": "vision_ocr",
        "image": image_path.name,
        "image_size_bytes": len(img_data),
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "timing": {
            "total_seconds": round(elapsed, 2),
        },
        "memory": {
            "before_mb": mem_before,
            "after_mb": mem_after,
        },
        "result": {
            "extracted_text": text[:5000],
            "text_length": len(text),
            "word_count": word_count,
            "is_error": is_error,
        },
    }


def run_entity_test(base_url: str, model: dict, text_path: Path, platform: str) -> dict:
    """Run one entity extraction test."""
    test_id = f"{text_path.stem}_{model['name']}_{platform}"
    print(f"  [{test_id}] ", end="", flush=True)

    source_text = text_path.read_text()
    prompt = ENTITY_PROMPT_TEMPLATE.format(text=source_text)

    # Run inference
    text, elapsed = ollama_chat(base_url, model["id"], prompt, timeout=60)

    # Try parse JSON
    json_valid = False
    entities_count = 0
    relationships_count = 0
    try:
        # Find JSON in response
        start = text.find("{")
        end = text.rfind("}") + 1
        if start >= 0 and end > start:
            parsed = json.loads(text[start:end])
            json_valid = True
            entities_count = len(parsed.get("entities", []))
            relationships_count = len(parsed.get("relationships", []))
    except:
        pass

    print(f"{'OK' if json_valid else 'FAIL'} {elapsed:.1f}s entities={entities_count} rels={relationships_count}")

    return {
        "test_id": test_id,
        "model": model["id"],
        "model_name": model["name"],
        "platform": platform,
        "test_type": "entity_extraction",
        "text_file": text_path.name,
        "source_text_length": len(source_text),
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "timing": {
            "total_seconds": round(elapsed, 2),
        },
        "result": {
            "response": text[:3000],
            "json_valid": json_valid,
            "entities_count": entities_count,
            "relationships_count": relationships_count,
        },
    }


def print_summary(results: list[dict]):
    """Print summary table."""
    print("\n" + "=" * 80)
    print("SUMMARY")
    print("=" * 80)

    # Vision results
    vision = [r for r in results if r["test_type"] == "vision_ocr" and not r["result"]["is_error"]]
    if vision:
        print("\n--- Vision OCR ---")
        print(f"{'Model':<25} {'Avg Time':>10} {'Avg Words':>10} {'Tests':>6}")
        print("-" * 55)

        models = {}
        for r in vision:
            mn = r["model_name"]
            if mn not in models:
                models[mn] = {"times": [], "words": []}
            models[mn]["times"].append(r["timing"]["total_seconds"])
            models[mn]["words"].append(r["result"]["word_count"])

        for mn, data in sorted(models.items()):
            avg_t = sum(data["times"]) / len(data["times"])
            avg_w = sum(data["words"]) / len(data["words"])
            print(f"{mn:<25} {avg_t:>9.1f}s {avg_w:>9.0f} {len(data['times']):>6}")

    # Entity results
    entity = [r for r in results if r["test_type"] == "entity_extraction"]
    if entity:
        print("\n--- Entity Extraction ---")
        print(f"{'Model':<25} {'Avg Time':>10} {'JSON OK':>8} {'Avg Ent':>8} {'Avg Rel':>8}")
        print("-" * 65)

        models = {}
        for r in entity:
            mn = r["model_name"]
            if mn not in models:
                models[mn] = {"times": [], "valid": 0, "total": 0, "ents": [], "rels": []}
            models[mn]["times"].append(r["timing"]["total_seconds"])
            models[mn]["total"] += 1
            if r["result"]["json_valid"]:
                models[mn]["valid"] += 1
            models[mn]["ents"].append(r["result"]["entities_count"])
            models[mn]["rels"].append(r["result"]["relationships_count"])

        for mn, data in sorted(models.items()):
            avg_t = sum(data["times"]) / len(data["times"])
            avg_e = sum(data["ents"]) / len(data["ents"])
            avg_r = sum(data["rels"]) / len(data["rels"])
            print(f"{mn:<25} {avg_t:>9.1f}s {data['valid']}/{data['total']:>5} {avg_e:>7.1f} {avg_r:>7.1f}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--platform", default="mac", choices=["mac", "pi"])
    parser.add_argument("--ollama-url", default="http://localhost:11434")
    parser.add_argument("--vision-only", action="store_true")
    parser.add_argument("--entity-only", action="store_true")
    parser.add_argument("--model", help="Run only this model (substring match)")
    args = parser.parse_args()

    RESULTS_DIR.mkdir(exist_ok=True)

    all_results = []
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")

    # Collect test images
    images = sorted(IMAGES_DIR.glob("*.png"))
    texts = sorted(TEXTS_DIR.glob("*.txt"))

    print(f"Platform: {args.platform}")
    print(f"Ollama: {args.ollama_url}")
    print(f"Images: {len(images)}")
    print(f"Texts: {len(texts)}")
    print()

    # Vision OCR tests
    if not args.entity_only:
        for model in VISION_MODELS:
            if args.platform not in model["platforms"]:
                print(f"SKIP {model['name']} (not supported on {args.platform})")
                continue
            if args.model and args.model not in model["name"]:
                continue

            print(f"\n{'='*60}")
            print(f"Vision: {model['name']} ({model['size_gb']} GB)")
            print(f"{'='*60}")

            for img in images:
                result = run_vision_test(args.ollama_url, model, img, args.platform)
                all_results.append(result)

                # Save individual result
                out_path = RESULTS_DIR / f"{result['test_id']}.json"
                out_path.write_text(json.dumps(result, indent=2, ensure_ascii=False))

    # Entity extraction tests
    if not args.vision_only:
        for model in TEXT_MODELS:
            if args.platform not in model["platforms"]:
                continue
            if args.model and args.model not in model["name"]:
                continue

            print(f"\n{'='*60}")
            print(f"Entity: {model['name']} ({model['size_gb']} GB)")
            print(f"{'='*60}")

            for txt in texts:
                result = run_entity_test(args.ollama_url, model, txt, args.platform)
                all_results.append(result)

                out_path = RESULTS_DIR / f"{result['test_id']}.json"
                out_path.write_text(json.dumps(result, indent=2, ensure_ascii=False))

    # Save combined results
    combined_path = RESULTS_DIR / f"benchmark_{args.platform}_{timestamp}.json"
    combined_path.write_text(json.dumps(all_results, indent=2, ensure_ascii=False))
    print(f"\nResults saved: {combined_path}")

    print_summary(all_results)


if __name__ == "__main__":
    main()
