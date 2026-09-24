# Embed-сервис: инциденты и правила эксплуатации

Обслуживает `com.stek0v.levara-embed` (embed_bench, порт 9101). Сейчас:
`embeddinggemma-300m` (unsloth-зеркало), dim 768, transformers-бэкенд на MPS.

## Наблюдавшиеся отказы и их подписи

| Отказ | Подпись | Лечение |
|---|---|---|
| HANG: MPS busy-loop (2026-09-24, 3 ч) | процесс жив, 600%+ CPU, health таймаутит, TERM не работает | `kill -9`; watchdog делает это автоматически |
| Сломанные эмбеддинги молча (fp16-MPS / отсутствие BOS) | health OK, но косинусы деградируют/инвертируются | самотест пары похожих текстов (см. ниже) |
| Краш на старте (CPU-тензоры против MPS-модели) | exit 78/1 сразу, лог: "Passed CPU tensor to MPS op" | входы должны ехать на device модели; probe до .to("mps") |

## Правила

1. **Один хозяин MPS.** В прод-окне никакие эксперименты не грузят модели на
   тот же MPS-девайс (триггер инцидента 2026-09-24 — A/B-прогон параллельно
   прод-эмбеддеру). Эксперименты — `device=cpu`.
2. **Watchdog обязателен**: `scripts/ops/embed_watchdog.sh` + LaunchAgent
   `com.stek0v.levara-embed-watchdog` (каждые 2 мин; grace 90 c на загрузку).
3. **Circuit breaker в levera** (`pkg/embed/breaker.go`): 3 подряд неудачи →
   30 c fail-fast вместо 30-секундных ожиданий на каждый вызов.
4. **После любого рестарта embed** — самотест:
   ```bash
   curl -s localhost:9101/health                       # dim правильный
   # похожая пара > 0.7, непохожая < 0.6:
   python3 - <<'EOF'
   import json,urllib.request,math
   e=lambda t: json.load(urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:9101/v1/embeddings',data=json.dumps({'input':t}).encode(),headers={'Content-Type':'application/json'})))['data'][0]['embedding']
   a,b,c=e('нужно настроить docker compose для postgres'),e('настрой docker compose под postgres'),e('рецепт борща со свёклой')
   cos=lambda x,y: sum(i*j for i,j in zip(x,y))/(math.sqrt(sum(i*i for i in x))*math.sqrt(sum(j*j for j in y)))
   print(f'similar={cos(a,b):.3f} dissimilar={cos(a,c):.3f}')
   EOF
   ```
5. Каскад-детекция со стороны потребителей: recallMemory стал ждать ~30 c и
   падать TaskGroup-ошибками = почти наверняка embed (см. инцидент).
