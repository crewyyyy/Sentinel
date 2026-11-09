# Архитектура Bot API MVP

Один Go-процесс, один bot token на БД, PostgreSQL и фоновые workers. Redis/MTProto/web-панели нет. SQL миграция встроена в бинарник. Домен не импортирует SDK/pgx; Telegram-типы и SQL остаются на границах.

## Транзакционные границы

1. Собственный HTTP `getUpdates` с явными `allowed_updates`. Не используется SDK polling: тот продвигает offset до завершения durable handler. SDK отвечает за исходящие уведомления/проверки прав.
2. Нормализация, хэш исходного содержимого и локальное извлечение URL **до** redaction/truncation (UTF-16 offsets могут измениться). Хэш сохраняет различие правок за пределами preview; nil/empty entities и URL-флаги конфигурации не создают ложную ревизию. Затем редактирование секретов. Unmanaged message content отбрасывается до записи. Входной ответ ограничен 4 MiB, batch 50, нормализованный update 256 KiB.
3. `processed_updates + inbox + chat_cursor` — один commit. Только после него следующий запрос подтверждает update. Ошибка decode блокирует продвижение, не пропускает событие. Пустые/неиспользуемые updates сохраняют дедуп-ID без сторонних данных.
4. Один inbox worker в одной транзакции: journal/revision → chat-scoped stats → deterministic assessment → AI job → case/outbox → `inbox.done`. Порядок — время приёма, затем ID, не числовой update ID: Telegram может сменить диапазон после простоя. Старейшее retry-событие не обгоняется (FIFO/head-of-line blocking); ingest продолжает сохранять следующие updates, оператор контролирует backlog. Текст обработанного inbox немедленно очищается. Рестарт между шагами не оставляет полурезультат.
5. AI worker: `SKIP LOCKED` claim, attempt journal, optional budget reservation, fenced lease → HTTP вне транзакции → review + incident/outbox в одном commit. Результат старой ревизии сохраняется в audit, но не подменяет актуальную карточку. Mandatory всегда возвращается в pending; terminal discard для неё запрещён CHECK constraint.
6. Outbox worker: claim/fencing → send/edit вне транзакции → ID карточки + completion. Изменение кейса во время отправки оставляет outbox pending для следующего edit. Неоднозначный сетевой timeout может дать повторную внешнюю отправку; exactly-once не заявляется.

## Идемпотентность и жизненный цикл

- Update: `(bot_instance_id, update_id)`. Session-level advisory lock не разрешает два poller на один bot ID; миграции сериализованы отдельным transaction lock.
- Message: `(chat_id,message_id)`. Меняется именно содержимое/сущности/quote/URLs → новая версия; повторный edit не удваивает активность.
- `message_keys` хранит только ID, fingerprint и версию, отдельно от текстового журнала. Поэтому edit после удаления текста по TTL не считается новым сообщением и не переиспользует job-key предыдущей ревизии.
- AI: `(chat_id,message_id,review:revision)`, в том числе processing. Provider idempotency header включает хэш неизменного запроса; поддержка заголовка зависит от провайдера.
- Case: существующая связь с сообщением либо `(chat,user,campaign,window)`. Для sender_chat identity входит в fingerprint. Новые доказательства обновляют одну карточку на destination.
- Report: `(chat,reporter,target_message)`; жалоба не ground truth. Rate-limit ограничивает немедленные карточки, не обязательный review; `/reports [CHAT_ID]` и `/reportcase REPORT_ID` позволяют разобрать pending. Высокий риск/обвинительный AI может эскалировать независимо от лимита.
- Feedback: append-only idempotency key callback/update; label и moderation event физически раздельны. Конфликт исключает кейс из export до явного `/resolve`; export читает evidence snapshot выбранного feedback, не позднюю версию сообщения.
- Приватность доставки: закрытый destination без публичного username/aliases проверяется при старте и перед защищённой отправкой; личный получатель также повторно проходит allowlist/current-rights для исходного чата. Состав закрытой группы контролирует оператор.

## Анализ

Четыре MUST не зависят от score. Дополнительно неопределённые Telegram destinations и достижение лимита URL extraction вызывают обязательный review. `is_bot` не выдумывается: суффикс и deep link — отдельный heuristic flag. Домены сравниваются по точному host/поддомену и PSL, не substring allowlist. Не выполняется DNS/HTTP-инспекция ссылок.

Вес каждого кода учитывается один раз на сообщение; trust discount ≤20. Сигналы и версия сохраняются отдельно. Положительный trust: активные дни, наблюдаемая история, ограниченные ответы/участники обсуждения; отрицательная поправка — явные spam labels, не AI или предположения о стране. Повторы рекламы и ссылок учитываются в минутном окне. Автоматического обучения нет.

Контекст: reply + до трёх предыдущих авторских + до трёх ближайших сообщений того же чата/topic, без дублей. В durable job текст/URL/context ограничены; truncation помечается partial. На внешнем AI boundary убираются структурные Telegram IDs/usernames, авторы получают job-local pseudonyms. Свободный текст/ссылки всё ещё могут идентифицировать человека; автоматическая анонимность не обещается. Нет tools, секретов конфигурации или moderation calls у LLM.

## Достоверность

`chat_member` означает действие над пользователем, не доказательство причины/конкретного сообщения. Корреляция по умолчанию всегда relation unknown; `/link` — явно человеческое утверждение о связи. `restricted` без потери права писать не называется мутом. Voluntary `member→left` не считается kick. `admin_attested` не повышается до verified. Удаление остаётся unknown без проверенного источника.
