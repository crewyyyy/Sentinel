# ADR 0001 — durable observe-only Bot API MVP

Дата: 2026-11-08. Статус: принято для первой реализации.

- Go 1.27.3; модуль требует минимум Go 1.26 (актуальный x/net). Зависимости зафиксированы: go-telegram/bot v1.27.0, pgx/v5 v5.12.0, x/net v0.62.0, yaml.v3 v3.0.1.
- Один процесс, PostgreSQL 17, SQL + pgxpool, без ORM/broker. Domain DTO не зависят от SDK или pgx. Простые SQL transactions вместо большого количества repository interfaces.
- Только polling. SDK встроенный loop может подтвердить update до завершения durable handler, поэтому getUpdates — свой bounded HTTP loop с commit-controlled offset; исходящие методы — SDK. Webhook mode намеренно отклоняется, а не имитируется.
- Отдельный inbox, чтобы outage LLM/Telegram send не блокировал durable ingest. Классификатор и доставка — leased workers; SQL constraints, idempotency keys и fencing. Exactly-once для внешних side effects не обещается.
- Никаких enforcement calls, deleteMessage даже для своих карточек, MTProto или интеграционных webhook без отдельной реализованной авторизации. `admin_attested` и platform verified физически раздельны. Human-link relation не приписывается Telegram.
- Неизвестный Telegram destination также отправляется на обязательный review: это консервативный дополнительный триггер, не утверждение `is_bot`. Лимит URL extraction приводит к partial review, а не к ложной уверенности в безопасности.
- Retention text отдельно от message identity/version и first observed. Незавершённые mandatory jobs удерживают ограниченное redacted evidence; это явное privacy-исключение, требующее operational queue-lag monitoring.
- HTTPS для внешнего LLM endpoint; plaintext только loopback fake API. Только OpenAI-compatible chat-completions contract; model строкой конфигурации. Structured JSON validation, никакого tool-use.
- `/tune` read-only; operator-reviewed YAML + version + restart. Dataset export/replay не меняют модель/правила и требуют отдельного privacy review.

Дополнение 2026-11-09: `/reports` и `/reportcase` дают доступ к отложенным жалобам, `/resolve` выполняет явный аудируемый арбитраж конфликтов. Rate-limit уведомлений не отменяет review. Dataset использует label-time evidence snapshots. Для сохранения first-observed/order inbox обрабатывается FIFO; ошибка старейшего события задерживает derived processing, но не durable ingest. Админский destination проверяется на непубличность, получатели ЛС — также на актуальные права перед доставкой.

Отложены: webhook/MTProto/trusted integration, веб-интерфейс, token/currency budgets, provider-specific dialects, калибровка качества/нагрузки на реальных данных. См. implementation-status.md — эти пункты не считаются реализованными.
