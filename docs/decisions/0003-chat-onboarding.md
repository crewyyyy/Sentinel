# ADR 0003 — обнаружение чатов с явным согласием

Дата: 2026-11-09. Статус: принято по запросу автоматического определения chat ID.

- Источник — настоящий `my_chat_member` от Bot API, не сообщение/username. Для нового auto-чата проверяются group/supergroup type, ID самого бота и numeric allowlisted actor.
- Обнаружение не равно включению наблюдения. Pending сохраняет только ограниченные provisioning-метаданные; сообщения/AI до согласия запрещены. Администратор получает карточку в своём ЛС; перед подтверждением обеспечивает privacy notice участникам.
- Callback требует owner+private destination+реальный Telegram receipt+revision+TTL, актуальные права администратора и admin-статус бота. Пересланная/поддельная/старая кнопка не активирует чат.
- Явный ID YAML остаётся deployment-time согласием; dynamic consent и inactive tombstone хранятся в PostgreSQL, объединяются с YAML без mutable Config/cache. Нет гонки обновления allowlist в памяти с poller.
- Prepare получает context и при сбое consent lookup не подтверждает update. Включение фиксируется транзакционно; queued pre-consent backlog не импортируется. Точность source timestamps — секунды; свежий edit определяется по edit_date.
- `left`/`kicked` отключают группу после обработки события, инвалидируют pending-кнопки. Повторный auto-join требует нового согласия; старые source events не возобновляют работу. Ранее принятые MUST jobs/evidence не удаляются.
- `/start`/`/chats` дают allowlisted bootstrap-путь без групп, повторно ставят private cards в outbox. Недоступные роли/ЛС не считаются успехом; pre-send errors освобождают lease для нормального retry. Ошибка проверки роли в onboarding callback не удерживает FIFO перед событием выдачи прав.
- Runtime `/model` учитывает все активные dynamic chats в RBAC. Старый configured `chat_ids[0]` больше не является обязательной точкой авторизации.
- Migration 0003 additive. Pending/title/receipt TTL 24h, connection audit 90d, одобренный consent/ID и inactive tombstone устойчивы к restart. Обычная message retention и приватность не ослабляются.

Проверки: fake Telegram/DB end-to-end, role/receipt/expiry failures, bootstrap, restart, DM retry, old backlog, removal/rejoin, global-model permissions, full race suite. Реального live-группового подключения без ручной проверки не заявляем.
