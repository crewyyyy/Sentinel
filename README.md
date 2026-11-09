# Sentinel — Telegram Anti-Spam Intelligence

Go-сервис пассивного наблюдения за управляемыми Telegram-группами: объяснимый риск, обязательные AI-проверки, приватные карточки, жалобы и обратная связь. **Не банит, не мутит, не удаляет и не выдаёт варны.** Даже «Спам» — метка, не наказание.

## Что работает

- Polling с атомарными inbox + dedup + cursor в PostgreSQL. Обработка сообщения, ревизии, оценки, AI-job и outbox — транзакционная; LLM не задерживает приём updates.
- Четыре обязательных триггера: `FIRST_OBSERVED`, `UNKNOWN_EXTERNAL_DOMAIN`, `TELEGRAM_BOT_LINK`, `COMPETITOR_PRICE_OR_ACCUSATION`. Trust и optional-бюджет их не отменяют. Неопределённые Telegram-ссылки также идут на review, не объявляясь ботами.
- URL/entities/caption/quote, UTF-16, IDNA/PSL, скрытые ссылки, сокращатели, referral vs UTM. Неизвестные ссылки **не открываются**.
- Версионированные правила/риск/trust, ограниченный контекст, OpenAI-compatible HTTP transport, строгая схема, usage/cost unknown, leases/fencing/backoff и общий circuit breaker.
- Кейсы, объединение повторов, Telegram-карточки/редактирование, reply-only reports, актуальная проверка админских прав, append-only feedback.
- `chat_member` old→new: подтверждённые действия над пользователем отдельно от связи с сообщением. Ручное подтверждение — `admin_attested`, не platform verified.
- Журнал ≤1000 сообщений **на чат** после очередной очистки, TTL 7 дней, отдельные сроки доказательств/агрегатов, локальные health/metrics, export/replay.

Ограничения: [Bot API](docs/telegram-limitations.md), [состояние реализации и незакрытые требования](docs/implementation-status.md). Это тестируемый Bot API MVP, **не заявление о выполнении всех пунктов Phase 5 или production-приёмке**.

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

1. Создайте бота у BotFather. Добавьте его в управляемую группу; для `chat_member` нужен статус администратора. **Не выдавайте** `can_restrict_members`/`can_delete_messages`. Проверьте получение обычных сообщений; у неадминского бота отключите Privacy Mode и переподключите его к группе согласно FAQ Telegram.
2. В YAML замените фиктивные `chat_ids`, `admin_chat_id`, `allowed_admin_ids` на ваши числовые IDs. Закрытый админ-чат должен быть отдельным от управляемых групп; доступ к самому этому чату ограничьте администраторами. Username не используется для авторизации.
3. В `deploy/.env` задайте `TELEGRAM_BOT_TOKEN`, `POSTGRES_PASSWORD`, `AI_BASE_URL` (HTTPS endpoint **с `/v1`, если он требуется провайдером**), `AI_MODEL`, `AI_API_KEY`. Модель должна поддерживать chat completions, `response_format=json_object`, `temperature`, `max_tokens`; имена моделей не зашиты в код.

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

### Проверка получения событий

- Напишите **обычное** сообщение в тестовой группе. Затем `/status` в админ-чате. `messages`/inbox/AI pending видны также в БД и `/metrics` внутри сети Compose.
- Первое наблюдаемое «Привет» должно создать AI-job, но не обвинительную карточку. Временное отключение AI оставляет job pending.
- Ответьте `/report причина` на тестовое сообщение; должна появиться одна приватная карточка. Жалобы разных людей должны дополнять её.
- На отдельном тестовом участнике администратор вручную меняет права. Проверьте `chat_member` и источник в карточке. Текст `/mute` и кнопка «Спам» **не** подтверждают наказание.
- Проверка доступности health: `docker compose --env-file deploy/.env -f deploy/compose.yaml exec sentinel sentinel healthcheck`.

## Команды

В управляемой группе: `/report`, `/report@your_bot`, `!репорт`, `!report` **ответом** на сообщение. Подтверждение публично нейтральное. Повторная жалоба того же автора не размножается. Первые 5 жалоб/час на автора создают немедленные уведомления; остальные сохраняются pending, админам поступает сводка и доступна `/reports`. Их AI-проверка не отменяется; высокий риск/обвинительный AI-результат может вызвать срочную карточку независимо от лимита уведомлений.

В закрытом админ-чате/разрешённом ЛС, после проверки allowlist **и текущих прав в исходной группе**:

| Команда | Действие |
|---|---|
| `/status`, `/stats` | Очереди, текущая модель, число сохранённых review/reports, observe-only |
| `/model [поиск]`, `/models [поиск]` | Каталог моделей, страницы кнопок, выбор → подтверждение; нужны права во всех управляемых чатах |
| `/case ID` | Обновить карточку кейса |
| `/reports [CHAT_ID]`, `/reportcase REPORT_ID` | Посмотреть pending-жалобы и открыть их кейс |
| `/resolve CASE_ID benign\|spam\|false_positive [текст]` | Явное разрешение конфликта меток с аудитом |
| `/feedback ID benign\|spam\|false_positive\|needs_review\|close [текст]` | Метка с аудитом, без санкции |
| `/review ID` | Довести актуальную ревизию до review; готовый review не оплачивается повторно |
| `/attest ID warn\|ban\|restrict\|delete [текст]` | Записать **утверждение** администратора, не вызвать санкцию |
| `/link CASE_ID EVENT_DB_ID` | Ручная связь события того же пользователя/чата; связь не приписывается Telegram |
| `/tune` | Инструкция безопасного изменения YAML; не online-learning |
| `/forget` | Инструкция операторского удаления с защитой незавершённых проверок |

Закрытая карточка не меняет метки по устаревшим кнопкам. Конфликты требуют `/resolve` и до арбитража не входят в dataset. При отправке карточек проверяются непубличность destination и актуальные права получателя ЛС; состав закрытого админ-чата контролирует оператор. ID события для `/link` оператор смотрит в `moderation_events` (полноценного браузера событий в Telegram пока нет).

Пример смысла карточки: «риск 78/100; FIRST_OBSERVED; UNKNOWN_EXTERNAL_DOMAIN; AI pending; рекомендация проверить; удаление unknown». Она никогда не говорит «бот забанил».

## Настройки AI, стоимости и отказов

Модель меняется **без перезапуска** через `/model`: по 6 вариантов на страницу, `← Назад`/`Вперёд →`, поиск `/model luna`, затем отдельное «Применить». Выбор хранится в PostgreSQL и имеет приоритет над `AI_MODEL` после рестарта. Новые попытки (включая retries) используют новую модель; выполняющиеся запросы не прерываются. Каталог читается только с настроенного endpoint, ключи через Telegram не меняются. [Подробности и ограничения](docs/runtime-models.md).

При `ai.enabled: false` обязательные задачи всё равно создаются и остаются pending. Для optional установлен суточный UTC request-limit, резервируемый **на каждую попытку**, в том числе повторную. Mandatory не ограничиваются этим бюджетом; финансовый лимит провайдера может остановить их выполнение, но не удалить jobs. Отдельных token/money-hard-limits пока нет.

Timeout, 429/5xx, неверный JSON/enum и незавершённый ответ → backoff, pending, журнал попыток. После повторных ошибок открывается персистентный circuit breaker; 429 учитывает `Retry-After`. Нет необоснованного `safe` fallback. Usage отсутствует — токены/стоимость неизвестны. Цены на вход/выход задаёт оператор за миллион токенов; нули — **не доказательство бесплатности**.

Жалоба «у вас скам»/«дешевле» сама по себе имеет нулевой обвинительный вес, но обязательную AI-проверку. Высокий trust не даёт иммунитета. Prompt-injection из сообщений изолируется как данные; это снижает риск, но не доказывает неуязвимость любой внешней модели.

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

Не публикуйте `.env`, настоящие IDs, дампы, dataset или session-файлы. Health/metrics — без открытого админского HTTP API и без опубликованного host port в Compose.
