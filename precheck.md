Правильно — делать **fail-fast проверку на старте**, а не ловить `dimension mismatch` уже во время индексации или поиска.

И главное: **в проде лучше не использовать `assert` для критичных проверок**.
Причина: в Python `assert` может быть отключён при запуске с `-O`.
Для боевой проверки лучше явно выбрасывать `RuntimeError` / `ValueError`.

---

## Что именно нужно проверять на старте

Нужно сверить 3 вещи:

1. **ожидаемую размерность из конфигурации**
2. **фактическую размерность, которую возвращает embedding-модель**
3. **размерность коллекции / индекса в VectraDB**

Если хотя бы одно не совпадает — приложение должно **не стартовать**.

---

# Надёжная схема

## 1. Сделать один source of truth

Не разносить `1024` по коду руками в пяти местах.

Плохо:

```python
EMBED_DIM = 1024
# ...
create_collection(dim=1024)
# ...
if len(vec) != 1024:
    ...
```

Лучше:

```python
EMBED_DIM = 1024
```

и дальше использовать только это значение везде.

Но ещё лучше — не хардкодить вообще, а получать размерность из модели.

---

## 2. На старте получить тестовый embedding

Например, на строке `"healthcheck"` или `"dimension_probe"`.

Так ты проверяешь **реальное поведение модели**, а не только документацию.

---

## 3. Прочитать dimension коллекции из VectraDB

Нужно запросить метаданные коллекции/индекса и узнать, под какую длину вектора она создана.

---

## 4. Сравнить и аварийно завершить запуск

Если размерности не совпали — не принимать трафик, не запускать воркеры, не индексировать документы.

---

# Практический шаблон

Ниже безопасный вариант для Python.

```python
from dataclasses import dataclass


@dataclass(frozen=True)
class VectorConfig:
    embedding_model_name: str
    expected_dim: int
    collection_name: str


class StartupValidationError(RuntimeError):
    pass


def get_embedding_dimension(embedding_client) -> int:
    """
    Получаем реальную размерность от embedding-модели.
    """
    probe_text = "dimension_probe"
    vector = embedding_client.embed(probe_text)

    if not isinstance(vector, (list, tuple)):
        raise StartupValidationError(
            f"Embedding client returned invalid type: {type(vector)}"
        )

    if not vector:
        raise StartupValidationError("Embedding client returned empty vector")

    return len(vector)


def get_vectordb_dimension(vectordb_client, collection_name: str) -> int:
    """
    Здесь должен быть реальный вызов к VectraDB:
    например чтение схемы коллекции / индекса.
    """
    info = vectordb_client.get_collection_info(collection_name)

    db_dim = info.get("dimension")
    if not isinstance(db_dim, int) or db_dim <= 0:
        raise StartupValidationError(
            f"Invalid dimension returned by VectraDB for '{collection_name}': {db_dim}"
        )

    return db_dim


def validate_vector_dimensions(
    config: VectorConfig,
    embedding_client,
    vectordb_client,
) -> None:
    model_dim = get_embedding_dimension(embedding_client)
    db_dim = get_vectordb_dimension(vectordb_client, config.collection_name)

    if model_dim != config.expected_dim:
        raise StartupValidationError(
            f"Embedding model '{config.embedding_model_name}' returned dim={model_dim}, "
            f"but config expects dim={config.expected_dim}"
        )

    if db_dim != config.expected_dim:
        raise StartupValidationError(
            f"VectraDB collection '{config.collection_name}' has dim={db_dim}, "
            f"but config expects dim={config.expected_dim}"
        )

    if model_dim != db_dim:
        raise StartupValidationError(
            f"Dimension mismatch: embedding model dim={model_dim}, db dim={db_dim}"
        )
```

---

# Как вызывать на старте приложения

Например, при запуске FastAPI / worker / CLI.

```python
def startup():
    config = VectorConfig(
        embedding_model_name="my-embedding-model",
        expected_dim=1024,
        collection_name="documents",
    )

    embedding_client = build_embedding_client()
    vectordb_client = build_vectordb_client()

    validate_vector_dimensions(
        config=config,
        embedding_client=embedding_client,
        vectordb_client=vectordb_client,
    )

    print("Startup validation passed")
```

Если проверка не проходит — процесс падает сразу.

---

# Если очень хочется использовать `assert`

Можно, но только как **дополнительную dev-проверку** или в тестах.

```python
model_dim = get_embedding_dimension(embedding_client)
db_dim = get_vectordb_dimension(vectordb_client, "documents")

assert model_dim == 1024
assert db_dim == 1024
assert model_dim == db_dim
```

Но для прода правильнее так:

```python
if model_dim != db_dim:
    raise RuntimeError(f"Dimension mismatch: model={model_dim}, db={db_dim}")
```

---

# Ещё более правильный вариант

Лучше вообще не писать `expected_dim=1024`, если можно взять размерность из самой модели и сверять только БД против неё.

То есть модель становится источником истины.

```python
def validate_vector_dimensions_no_hardcode(
    embedding_client,
    vectordb_client,
    collection_name: str,
) -> None:
    model_dim = get_embedding_dimension(embedding_client)
    db_dim = get_vectordb_dimension(vectordb_client, collection_name)

    if model_dim != db_dim:
        raise StartupValidationError(
            f"Dimension mismatch for collection '{collection_name}': "
            f"model_dim={model_dim}, db_dim={db_dim}"
        )
```

Это особенно удобно, если модель может быть заменена через ENV.

---

# Что ещё стоит добавить кроме startup-check

## 1. Проверку перед созданием коллекции

Если коллекции нет, создавать её с размерностью, которую реально вернула модель.

```python
def ensure_collection_exists(embedding_client, vectordb_client, collection_name: str):
    model_dim = get_embedding_dimension(embedding_client)

    if not vectordb_client.collection_exists(collection_name):
        vectordb_client.create_collection(
            name=collection_name,
            dimension=model_dim,
        )
        return

    db_dim = get_vectordb_dimension(vectordb_client, collection_name)
    if db_dim != model_dim:
        raise StartupValidationError(
            f"Existing collection '{collection_name}' has dim={db_dim}, "
            f"but model returns dim={model_dim}"
        )
```

---

## 2. Проверку отдельно для ingestion и query

Иногда документы индексируются одной моделью, а запросы кодируются другой.
Это опасно, если кто-то случайно поменял только query encoder.

Нужно явно проверять обе стороны:

```python
doc_dim = len(doc_embedding_client.embed("probe"))
query_dim = len(query_embedding_client.embed("probe"))

if doc_dim != query_dim:
    raise StartupValidationError(
        f"Query/doc embedding dimensions differ: doc={doc_dim}, query={query_dim}"
    )
```

---

## 3. Логирование в healthcheck

Полезно писать в лог:

* имя модели
* фактическую размерность
* имя коллекции
* dimension в БД
* результат проверки

Например:

```python
print(
    {
        "embedding_model": config.embedding_model_name,
        "expected_dim": config.expected_dim,
        "collection": config.collection_name,
        "status": "ok",
    }
)
```

---

## 4. Отдельный readiness-check

Если у тебя сервис в Docker/Kubernetes, хорошо разделить:

* **liveness** — процесс жив
* **readiness** — модель и VectraDB согласованы

То есть сервис не должен считаться ready, пока dimension-check не прошёл.

---

# Что делать, если размерность поменялась

Если ты сменил embedding-модель, и она теперь выдаёт не `1024`, а `1536`, то нельзя просто продолжить работу поверх старой коллекции.

Правильные варианты:

## Вариант 1. Создать новую коллекцию

Например:

* `documents_v1_1024`
* `documents_v2_1536`

И переиндексировать данные.

## Вариант 2. Полная миграция

* останавливаешь ingestion
* создаёшь новый индекс
* пересчитываешь embeddings
* переключаешь трафик

## Вариант 3. Версионирование в конфиге

```python
COLLECTION_NAME = "documents_v2"
EMBEDDING_MODEL = "new-model"
```

Это безопаснее, чем “молча” перезаписать старую коллекцию.

---

# Минимально правильный production-паттерн

Если совсем коротко, то вот так:

```python
def startup_vector_guard(embedding_client, vectordb_client, collection_name: str) -> None:
    probe = "dimension_probe"

    model_vec = embedding_client.embed(probe)
    model_dim = len(model_vec)

    info = vectordb_client.get_collection_info(collection_name)
    db_dim = info["dimension"]

    if model_dim != db_dim:
        raise RuntimeError(
            f"Startup aborted: embedding dimension mismatch "
            f"(model={model_dim}, vectordb={db_dim}, collection={collection_name})"
        )
```

---

# Что я рекомендую тебе на практике

Самый правильный вариант:

1. **не использовать `assert` как единственную защиту**
2. **на старте получать реальный embedding probe**
3. **читать dimension из VectraDB**
4. **при несовпадении падать сразу**
5. **версионировать коллекции при смене модели**
6. **отдельно проверять query/doc embeddings, если они разные**
7. **не хардкодить dimension в нескольких местах**

---

# Итог

Да, защищаться нужно именно так:
**startup validation + fail-fast + единый source of truth + миграция при смене модели**.

Самая опасная ошибка — когда:

* модель уже новая
* коллекция старая
* приложение стартует
* а проблема вылезает только на боевых запросах

Правильная система должна это остановить ещё до старта.

Могу сразу дать тебе следующий шаг: **готовый production-ready модуль `startup_checks.py` для Python**, с логированием, retry, понятными ошибками и примером интеграции в FastAPI.
