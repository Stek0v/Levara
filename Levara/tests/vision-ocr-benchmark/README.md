# Vision OCR Model Benchmark — Test Plan

## Цель
Выбрать оптимальную модель для извлечения текста из изображений на Mac и Pi.
Дополнительно: протестировать LFM2.5-350M как замену qwen3:0.6b для entity extraction.

## Модели для тестирования

### Vision/OCR модели
| # | Модель | Размер | Ollama ID | Платформа |
|---|--------|--------|-----------|-----------|
| 1 | moondream 1.8b | 1.7 GB | `moondream:1.8b` | Mac + Pi |
| 2 | granite3.2-vision 2b | 1.5 GB | `granite3.2-vision:2b` | Mac + Pi |
| 3 | llava 7b | 4.7 GB | `llava:7b` | Mac only |
| 4 | minicpm-v 8b | 4.9 GB | `minicpm-v:8b` | Mac only |

### Text extraction модели (не vision, для сравнения cognify quality)
| # | Модель | Размер | Ollama ID | Платформа |
|---|--------|--------|-----------|-----------|
| 5 | LFM2.5-350M | ~350 MB | `hf.co/LiquidAI/LFM2.5-350M-GGUF` | Mac + Pi |
| 6 | qwen3:0.6b (baseline) | 400 MB | `qwen3:0.6b` | Mac + Pi |

## Тестовые сеты

### Set A: Скриншоты UI (5 изображений)
- A1: Скриншот WebUI dashboard Levara (статистика, таблица)
- A2: Скриншот терминала с кодом Go (синтаксис, цвета)
- A3: Скриншот Grafana dashboard (графики, метрики)
- A4: Скриншот Telegram чата (текст, аватары, timestamps)
- A5: Скриншот GitHub PR с diff (добавления/удаления, номера строк)

### Set B: Документы (5 изображений)
- B1: Фото страницы книги (печатный текст, русский)
- B2: Фото рукописного текста (заметки, почерк)
- B3: Скан визитки (контакты, логотип, мелкий шрифт)
- B4: Фото таблицы/чека (числа, столбцы, суммы)
- B5: Фото whiteboard (маркер, схема, стрелки)

### Set C: Технические диаграммы (3 изображения)
- C1: Архитектурная диаграмма (блоки, стрелки, подписи)
- C2: UML/ER диаграмма (сущности, связи)
- C3: Мем/инфографика (текст + изображение, смешанный контент)

### Set D: Entity extraction тексты (для LFM2.5 vs qwen3, не vision)
- D1: Абзац о Levara архитектуре (EN)
- D2: Описание sync механизма (RU)
- D3: Changelog с версиями и датами
- D4: Список зависимостей с версиями
- D5: Описание бага с техническими деталями

## Метрики

### Для Vision/OCR (Sets A, B, C)
1. **Время ответа** (ms) — от отправки до получения текста
2. **Полнота извлечения** (%) — сколько текста из изображения извлечено
3. **Точность** (%) — правильность извлечённого текста (орфография, числа)
4. **Структура** — сохранение таблиц, списков, иерархии
5. **RAM usage** (MB) — потребление памяти моделью
6. **Ошибки** — галлюцинации, выдуманный текст

### Для Entity Extraction (Set D)
1. **Время ответа** (ms)
2. **Entities extracted** — количество
3. **Precision** — % правильных сущностей
4. **Recall** — % найденных из ожидаемых
5. **JSON validity** — парсится ли structured output
6. **RAM usage** (MB)

## Процедура тестирования

### Шаг 1: Подготовка тестовых данных
```bash
# Создать директорию
mkdir -p tests/vision-ocr-benchmark/{images,texts,results}

# Сгенерировать скриншоты Sets A, C через playwright
# Подготовить фото Sets B вручную (или скачать примеры)
# Подготовить тексты Set D
```

### Шаг 2: Установка моделей
```bash
# Mac
ollama pull moondream:1.8b
ollama pull granite3.2-vision:2b
ollama pull llava:7b
ollama pull minicpm-v:8b
ollama run hf.co/LiquidAI/LFM2.5-350M-GGUF  # первый запуск скачает

# Pi (только маленькие)
ssh stek0v@10.23.0.53 'ollama pull moondream:1.8b'
ssh stek0v@10.23.0.53 'ollama pull granite3.2-vision:2b'
ssh stek0v@10.23.0.53 'ollama run hf.co/LiquidAI/LFM2.5-350M-GGUF'
```

### Шаг 3: Запуск бенчмарка
```bash
# Для каждой модели × каждого изображения:
# 1. Замерить RAM до
# 2. Отправить запрос с base64 image
# 3. Замерить время ответа
# 4. Сохранить результат
# 5. Замерить RAM после
# 6. Оценить качество (automated + manual review)

python3 tests/vision-ocr-benchmark/run_benchmark.py \
  --platform mac \
  --ollama-url http://localhost:11434 \
  --output-dir tests/vision-ocr-benchmark/results
```

### Шаг 4: Анализ результатов
- Таблица: модель × метрика × тестовый сет
- Графики: время vs качество
- Рекомендация: какая модель для Mac, какая для Pi

## Формат лога

Каждый тест генерирует JSON:
```json
{
  "test_id": "A1_moondream_mac",
  "model": "moondream:1.8b",
  "platform": "mac",
  "image": "A1_dashboard.png",
  "image_size_bytes": 245632,
  "timestamp": "2026-04-01T12:00:00Z",

  "timing": {
    "model_load_ms": 1200,
    "inference_ms": 8500,
    "total_ms": 9700
  },

  "memory": {
    "before_mb": 2048,
    "during_mb": 3800,
    "after_mb": 2100
  },

  "result": {
    "extracted_text": "Dashboard\nVectors: 129\nMemories: 97\n...",
    "text_length": 342,
    "word_count": 48
  },

  "quality": {
    "completeness_pct": 85,
    "accuracy_pct": 92,
    "structure_preserved": true,
    "hallucinations": false,
    "notes": "Missed small footer text, numbers correct"
  }
}
```

## Ожидаемый output

```
╔══════════════════════╦═══════════╦═══════════╦═══════════╦═══════════╗
║ Model                ║ Avg Time  ║ Accuracy  ║ Complete  ║ RAM (MB)  ║
╠══════════════════════╬═══════════╬═══════════╬═══════════╬═══════════╣
║ moondream:1.8b (Mac) ║    5.2s   ║   78%     ║   72%     ║   1,700   ║
║ moondream:1.8b (Pi)  ║   18.5s   ║   78%     ║   72%     ║   1,700   ║
║ granite3.2-v:2b(Mac) ║    6.1s   ║   85%     ║   80%     ║   1,500   ║
║ granite3.2-v:2b(Pi)  ║   22.0s   ║   85%     ║   80%     ║   1,500   ║
║ llava:7b (Mac)       ║    8.3s   ║   91%     ║   88%     ║   4,700   ║
║ minicpm-v:8b (Mac)   ║   12.1s   ║   94%     ║   92%     ║   4,900   ║
╠══════════════════════╬═══════════╬═══════════╬═══════════╬═══════════╣
║ LFM2.5-350M (Mac)    ║    0.8s   ║   --      ║   --      ║    350    ║
║ qwen3:0.6b (Mac)     ║    1.2s   ║   --      ║   --      ║    400    ║
╚══════════════════════╩═══════════╩═══════════╩═══════════╩═══════════╝

Recommendation:
  Pi:  granite3.2-vision:2b (best quality/size ratio)
  Mac: llava:7b (best quality, acceptable speed)
  Entity extraction: LFM2.5-350M (faster, better instruction following than qwen3)
```
