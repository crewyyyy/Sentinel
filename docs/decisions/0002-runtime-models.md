# ADR 0002 — runtime-модель и безопасные панели

Дата: 2026-11-09. Статус: принято по запросу runtime-выбора модели через Telegram.

- Модель общая для процесса/БД. Любая её смена требует allowlist и актуальных admin-прав во всех managed chats, не только в первом.
- Каталог берётся с настроенного OpenAI-compatible `/models`, без redirect, с timeout/body/model-count limits. Ключ и endpoint из Telegram не меняются. Metadata-каталог не доказывает совместимость или subscription-тариф.
- `/model [поиск]` создаёт 30-минутный DB snapshot каталога. Шесть кнопок на страницу; отдельное подтверждение. Callback содержит индексы, owner/receipt/revision проверяются сервером.
- `runtime_ai_model` — durable override исходного `AI_MODEL`. Версия настройки и advisory lock не допускают, чтобы подготовленная ранее панель перезаписала чужой свежий выбор.
- Claim фиксирует model/prompt_version на попытке. Provider получает модель в immutable request, не меняет общий Config; retries после смены используют новый выбор, выполняющиеся HTTP не прерываются.
- Готовые reviews не перезапускаются; mandatory/budget инварианты остаются. Модель не fallback-ится на другой тариф при ошибке. При смене снимается provider pause старых ошибок; pending jobs не удаляются.
- Цены исходной модели нельзя переносить на другой ID: usage известен при наличии, cost другого ID unknown.
- Prompt `review_v2` и user envelope `format=json` исправляют несовместимость subscription-шлюза, проверяющего JSON-mode только по user input. Старый prompt-файл неизменён; реальная версия пишется на каждой новой попытке.
- Миграция 0002 добавляется поверх неизменной 0001; проверены upgrade с pending inbox, повторный migrate и полный up/down/up.

Кнопки и смены проверены fake Telegram/LLM + PostgreSQL, включая revoked rights и stale confirmations. Live synthetic review на настроенном subscription-маршруте подтвердил контракт JSON, но не production-качество, биллинг или доставку в реальную группу.
