#!/usr/bin/env bash
# Sequentially run 3 embed models through the full Levara cognify
# pipeline on Pi bench. Refuses to touch production levara.service.
#
# Usage:
#   bash run_all_models.sh              # all 3 models
#   bash run_all_models.sh potion       # just one
set -euo pipefail

PI_HOST="${PI_HOST:-10.23.0.53}"
PI_USER="${PI_USER:-stek0v}"
OUT_DIR="${OUT_DIR:-scripts/load-profiles/out}"
DEFAULT_MODELS=(potion granite jina)
MODELS_ARR=(${MODELS:-${DEFAULT_MODELS[@]}})
if [ $# -gt 0 ]; then
  MODELS_ARR=("$@")
fi

mkdir -p "$OUT_DIR"

safe_unit() {
  case "$1" in
    levara.service) echo "REFUSING to touch prod unit $1" >&2; exit 2 ;;
  esac
}

write_dropin() {
  local unit="$1" name="$2" content="$3"
  safe_unit "$unit"
  ssh "$PI_USER@$PI_HOST" "sudo mkdir -p /etc/systemd/system/${unit}.d && \
    printf '%s\n' '$content' | sudo tee /etc/systemd/system/${unit}.d/${name}.conf >/dev/null"
}

restart_bench() {
  ssh "$PI_USER@$PI_HOST" "sudo systemctl daemon-reload && \
    sudo systemctl restart embed-bench.service levara-bench.service"
}

stop_bench() {
  ssh "$PI_USER@$PI_HOST" "sudo systemctl stop levara-bench.service embed-bench.service || true"
}

run_one_model() {
  local short="$1"
  echo "=== model: $short ==="

  case "$short" in
    potion)  OPENAI="potion-code-16M";              DIM=256 ;;
    granite) OPENAI="granite-97m-multilingual-r2";  DIM=384 ;;
    jina)    OPENAI="jina-omni-nano";               DIM=512 ;;
    *) echo "unknown model: $short" >&2; exit 2 ;;
  esac

  stop_bench

  write_dropin embed-bench.service model "[Service]
Environment=EMBED_BENCH_MODEL=$short"
  write_dropin levara-bench.service embed "[Service]
Environment=EMBEDDING_MODEL=$OPENAI"
  write_dropin levara-bench.service dim "[Service]
ExecStart=
ExecStart=/home/stek0v/levara-bench/levara -standalone=true -port=8091 -grpc-port=0 -dim=$DIM -data-dir=/home/stek0v/levara-bench/data -node-id=pi-bench"

  restart_bench
  sleep 10

  python3 scripts/load-profiles/preflight_model.py --model "$short" --host "$PI_HOST" \
    || { echo "preflight failed for $short, skipping" >&2; stop_bench; return 1; }

  local TARGET_URL="http://$PI_HOST:8091"
  local COLL_P4="loadprofile_p4_main_$short"
  local COLL_P5="loadprofile_p5_main_$short"

  echo "[seed] $COLL_P4 + $COLL_P5"
  python3 scripts/load-profiles/seed_one.py --target-url "$TARGET_URL" --collection "$COLL_P4"
  python3 scripts/load-profiles/seed_one.py --target-url "$TARGET_URL" --collection "$COLL_P5"

  echo "[run] p4 / $short"
  python3 scripts/load-profiles/p4_memory_palace.py \
    --target-name bench --target-url "$TARGET_URL" \
    --model "$short" --embed-dim "$DIM" \
    --collection-override "$COLL_P4" \
    --out "$OUT_DIR/p4_$short.jsonl"

  echo "[run] p5 / $short"
  python3 scripts/load-profiles/p5_filtered_search.py \
    --target-name bench --target-url "$TARGET_URL" \
    --model "$short" --embed-dim "$DIM" \
    --collection-override "$COLL_P5" \
    --out "$OUT_DIR/p5_$short.jsonl"

  stop_bench
}

for m in "${MODELS_ARR[@]}"; do
  run_one_model "$m" || echo "model $m skipped" >&2
done

echo "=== analyze ==="
python3 scripts/load-profiles/analyze.py --by-model "$OUT_DIR"/p?_*.jsonl \
  > docs/load-profile-analysis-pi-multimodel.md

echo "OK. Output: docs/load-profile-analysis-pi-multimodel.md"
