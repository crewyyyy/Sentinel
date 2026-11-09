# Sentinel: Telegram Anti-Spam Intelligence

Sentinel наблюдает за новыми сообщениями в подключённых Telegram-группах, оценивает риск и отправляет администраторам приватные карточки для проверки. Он не банит, не мутит, не удаляет сообщения и не выдаёт предупреждения. Кнопки и метки сохраняют решения модераторов, но не применяют санкции.

## Возможности

- Polling сохраняет updates в PostgreSQL до подтверждения Telegram. Inbox, журнал, оценки, AI jobs и outbox используют транзакции; медленный LLM не задерживает приём.
- Четыре обязательных триггера: `FIRST_OBSERVED`, `UNKNOWN_EXTERNAL_DOMAIN`, `TELEGRAM_BOT_LINK` и `COMPETITOR_PRICE_OR_ACCUSATION`. Trust и optional-бюджет их не отключают. Если назначение Telegram-ссылки неизвестно, сервис запускает review по сигналу `TELEGRAM_LINK_UNVERIFIED`.
- URL-детектор разбирает entities, captions, quotes и UTF-16 offsets; нормализует IDNA и PSL, отмечает скрытые ссылки и сокращатели, различает referral и UTM. Сервис не открывает неизвестные ссылки.
- Правила, оценки риска и trust версионируются. AI получает ограниченный контекст. Очередь поддерживает leases, fencing, backoff и circuit breaker.
- Кейсы объединяют повторы. Карточки можно обновлять; жалобы принимаются ответом на сообщение. Проверка прав выполняется при действии администратора.
- `chat_member` фиксирует подтверждённые переходы статуса. Telegram не связывает эти переходы с конкретным сообщением; ручная связь хранится отдельно. Подтверждение администратора помечается `admin_attested`.
- Журнал хранит до 1000 сообщений на чат и удаляет их по TTL 7 дней. Для кейсов и агрегатов действуют отдельные сроки хранения. В комплект входят локальные health/metrics endpoints, export и replay.

Сервис реализует Bot API MVP. Ограничения Telegram описаны в [справке по Bot API](docs/telegram-limitations.md), а оставшиеся требования перечислены в [статусе реализации](docs/implementation-status.md).

## Быстрый старт на Ubuntu VPS

Требуются Docker Engine + Compose v2; для разработки Go **1.27.3** (module minimum 1.26), PostgreSQL 17. Репозиторий не требует локальных абсолютных путей.

```bash
git clone https://github.com/crewyyyy/admWarnBot.git sentinel
cd sentinel
cp deploy/.env.example deploy/.env
chmod 600 deploy/.env
cp configs/config.example.yaml configs/config.yaml
openssl rand -hex 24  # вставьте результат как POSTGRES_PASSWORD
```

1. Создайте бота через BotFather. Добавьте его в управляемую группу и выдайте статус администратора. Для получения `chat_member` это обязательно. Не выдавайте права `can_restrict_members` и `can_delete_messages`. Если бот не администратор, отключите Privacy Mode в BotFather и добавьте бота в группу заново.
2. Замените `admin_chat_id` и `allowed_admin_ids` в YAML на свои числовые IDs. Для ЛС `admin_chat_id` должен совпадать с ID разрешённого пользователя. Сначала отправьте боту `/start`. При `auto_discover_chats: true` оставьте `chat_ids: []`: бот узнает ID группы из события Telegram и пришлёт запрос на подключение. Закрытый админ-чат должен быть отдельным от наблюдаемых групп; оператор отвечает за его состав. Username для авторизации не используется.
3. В `deploy/.env` укажите `TELEGRAM_BOT_TOKEN`, `POSTGRES_PASSWORD`, `AI_BASE_URL`, `AI_MODEL` и `AI_API_KEY`. Если провайдер требует `/v1`, включите его в `AI_BASE_URL`. Модель должна поддерживать Chat Completions, `response_format=json_object`, `temperature` и `max_tokens`. Название модели задаётся в конфигурации.

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d db
docker compose --env-file deploy/.env -f deploy/compose.yaml build sentinel
# Проверка конфигурации без запросов Telegram/LLM:
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm sentinel validate --config /app/configs/config.yaml
# Явное применение миграций; run также безопасно применяет отсутствующие миграции:
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm sentinel migrate
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d sentinel
docker compose --env-file deploy/.env -f deploy/compose.yaml logs --tail=100 sentinel
docker compose --env-file deploy/.env -f deploy/compose.yaml ps
```

Не запускайте два poller на один токен. Если Telegram webhook уже настроен, запуск завершится ошибкой; удалите webhook **явно**, не сбрасывая pending updates. Приложение не меняет webhook самовольно.

### Автоподключение группы

1. Разрешённый администратор открывает боту ЛС командой `/start`.
2. Тот же администратор добавляет бота в группу/супергруппу и выдаёт статус admin **без прав банить/удалять**.
3. Бот присылает в ЛС карточку с определённым `chat_id`. Перед подтверждением уведомите участников об обработке сообщений и внешнем AI; затем нажмите **«Подключить»**.
4. До подтверждения обычные сообщения этой группы не сохраняются и не уходят в AI. После него ID и согласие живут в БД, без правки YAML и restart. `/chats` показывает доступные чаты и повторно отправляет pending-карточки.

Удаление бота отключает чат после обработки события. Повторное добавление auto-чата требует нового подтверждения. Произвольное добавление посторонним человеком не подключает группу. [Подробности и ограничения](docs/chat-onboarding.md).

### Проверка получения событий

- Напишите **обычное** сообщение в тестовой группе. Затем `/status` в админ-чате. `messages`/inbox/AI pending видны также в БД и `/metrics` внутри сети Compose.
- Первое наблюдаемое «Привет» должно создать AI-job, но не обвинительную карточку. Временное отключение AI оставляет job pending.
- Ответьте `/report причина` на тестовое сообщение; должна появиться одна приватная карточка. Жалобы разных людей должны дополнять её.
- На отдельном тестовом участнике администратор вручную меняет права. Проверьте `chat_member` и источник в карточке. Текст `/mute` и кнопка «Спам» **не** подтверждают наказание.
- Проверка доступности health: `docker compose --env-file deploy/.env -f deploy/compose.yaml exec sentinel sentinel healthcheck`.

## Команды

В управляемой группе отправьте `/report`, `/report@your_bot`, `!репорт` или `!report` ответом на сообщение. Бот подтверждает жалобу нейтрально, без публичного диагноза. Повторная жалоба того же автора на то же сообщение не создаёт новую запись.

Первые пять жалоб в час от одного автора создают немедленные уведомления. Остальные сохраняются со статусом pending; администраторы могут открыть очередь командой `/reports`. Rate limit не отменяет AI-проверку. Высокий риск или обвинительный результат AI может вызвать срочную карточку.

В закрытом админ-чате или разрешённом ЛС команды проверяют allowlist и текущие права в исходной группе:

| Команда | Действие |
|---|---|
| `/start`, `/chats` | Инструкция подключения, доступные чаты, повторная доставка pending-заявок в ЛС |
| `/status`, `/stats` | Очереди, текущая модель, число сохранённых review/reports, observe-only |
| `/model [поиск]`, `/models [поиск]` | Каталог с постраничным выбором и подтверждением. Нужны права во всех управляемых чатах |
| `/case ID` | Обновить карточку кейса |
| `/reports [CHAT_ID]`, `/reportcase REPORT_ID` | Посмотреть pending-жалобы и открыть их кейс |
| `/resolve CASE_ID benign\|spam\|false_positive [текст]` | Явное разрешение конфликта меток с аудитом |
| `/feedback ID benign\|spam\|false_positive\|needs_review\|close [текст]` | Метка с аудитом, без санкции |
| `/review ID` | Довести актуальную ревизию до review; готовый review не оплачивается повторно |
| `/attest ID warn\|ban\|restrict\|delete [текст]` | Записать **утверждение** администратора, не вызвать санкцию |
| `/link CASE_ID EVENT_DB_ID` | Ручная связь события того же пользователя/чата; связь не приписывается Telegram |
| `/tune` | Инструкция безопасного изменения YAML; не online-learning |
| `/forget` | Инструкция операторского удаления с защитой незавершённых проверок |

Устаревшие кнопки закрытой карточки не меняют метки. Конфликтующие метки требуют `/resolve`; до арбитража кейс не попадает в dataset. Перед отправкой карточки бот проверяет приватность destination и права получателя ЛС. Оператор отвечает за состав закрытого админ-чата.

Для `/link` найдите ID события в таблице `moderation_events`; команды просмотра событий в Telegram пока нет. Карточка показывает риск, сигналы и статус AI review. Недоступность сообщения не считается доказательством удаления.

## Настройки AI, стоимости и отказов

Команда `/model` открывает каталог: шесть моделей на странице, поиск через `/model luna`, выбор и отдельное подтверждение. Кнопки листают страницы. Выбор сохраняется в PostgreSQL, действует после перезапуска и имеет приоритет над `AI_MODEL`. Новые попытки используют выбранную модель; начатые запросы продолжаются на прежней. Каталог загружается с настроенного endpoint. Изменить API-ключ через Telegram нельзя. Подробнее: [смена модели и ограничения](docs/runtime-models.md).

При `ai.enabled: false` обязательные jobs всё равно создаются и остаются pending. Для optional-проверок действует суточный лимит запросов по UTC. Каждый повторный вызов расходует отдельный запрос. Обязательные jobs этим лимитом не ограничены, но провайдер может отклонить запрос. Лимитов по токенам и деньгам пока нет.

Timeout, HTTP 429/5xx, неверный JSON или незавершённый ответ переводят job на повтор с backoff и записью попытки. Повторные ошибки открывают circuit breaker; для 429 учитывается `Retry-After`. При отсутствии usage сервис не указывает токены и стоимость. Цены задаются за миллион токенов. Нулевая цена в конфиге не означает, что провайдер не выставит счёт.

Жалобы вроде «у вас скам» или «у другого дешевле» имеют нулевой обвинительный вес, но запускают обязательную AI-проверку. Высокий trust её не отключает. Текст пользователя передаётся модели как недоверенные данные; это снижает риск prompt injection, но не гарантирует защиту от каждой модели.

## Разработка и dry run без ключей

```bash
make setup
make test
make lint
make race
make inspect  # нормализация + риск; явно не вызывает AI
make build
```

PostgreSQL-интеграция использует отдельную временную schema на каждый тест; без env тесты явно SKIP. Не указывайте production database: тесты требуют права CREATE/DROP SCHEMA.

```bash
docker run -d --name sentinel-test-db \
  -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=sentinel_test \
  -p 127.0.0.1:55432:5432 postgres:17-alpine
export TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55432/sentinel_test?sslmode=disable'
make integration
# После тестов (только созданный выше контейнер):
docker stop sentinel-test-db
docker rm sentinel-test-db
```

Fake Telegram/LLM HTTP servers и end-to-end transactional tests не обращаются к публичному Telegram и не расходуют AI-бюджет. CI запускает PostgreSQL + race + vet + сборку образа.

Для запуска вне Docker задайте `DATABASE_URL`, `TELEGRAM_BOT_TOKEN`, `AI_*` в окружении и выполните `make run`. Приложение не читает `.env` самостоятельно; Compose передаёт env. Не используйте shell tracing с секретами.

## Данные, tuning и эксплуатация

- [Архитектура](docs/architecture.md), [решения](docs/decisions/0001-bot-api-mvp.md).
- [Privacy/retention/forget](docs/privacy.md): исключение для pending mandatory evidence, ограничения автоматической деидентификации.
- [Операции: backup, restore, обновление, диагностика](docs/operations.md).
- [Экспорт, holdout, replay, rollback](docs/tuning.md).
- [Фактически проверено и отложено](docs/implementation-status.md).

Не публикуйте `.env`, настоящие IDs, дампы, dataset или session-файлы. Health и metrics не открывают административный HTTP API; Compose не публикует их host-порты.
