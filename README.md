![Sentinel](generated-images/sentinel-banner-16x9.png)

# Sentinel

Telegram-бот для анализа сообщений и модерации чатов. Находит рекламу, реферальные ссылки, фишинг и нарушения настраиваемых правил. Отправляет администраторам карточки кейсов.

## Запуск на Ubuntu VPS

Нужны Docker Engine и Compose v2. Для разработки: Go 1.27.3 (минимум 1.26), PostgreSQL 17.

```bash
git clone https://github.com/crewyyyy/Sentinel.git sentinel
cd sentinel
cp deploy/.env.example deploy/.env
chmod 600 deploy/.env
cp configs/config.example.yaml configs/config.yaml
openssl rand -hex 24  # вставьте результат как POSTGRES_PASSWORD
```

1. Создайте бота через BotFather.
2. В YAML укажите числовые `admin_chat_id` и `allowed_admin_ids`. Для ЛС оба относятся к одному пользователю; закрытый админ-чат должен быть отдельным от наблюдаемых групп. Оставьте `chat_ids: []` и `auto_discover_chats: true` для подключения через бота.
3. В `deploy/.env` заполните `TELEGRAM_BOT_TOKEN`, `POSTGRES_PASSWORD`, `AI_BASE_URL`, `AI_MODEL`, `AI_API_KEY`. Включите `/v1` в URL, если этого требует провайдер. Модель должна поддерживать Chat Completions, `response_format=json_object`, `temperature` и `max_tokens`.

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d db
docker compose --env-file deploy/.env -f deploy/compose.yaml build sentinel
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm sentinel validate --config /app/configs/config.yaml
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm sentinel migrate
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d sentinel
docker compose --env-file deploy/.env -f deploy/compose.yaml logs --tail=100 sentinel
docker compose --env-file deploy/.env -f deploy/compose.yaml ps
```

Один токен обслуживает один poller. Существующий webhook нужно удалить перед запуском, сохранив pending updates.

## Подключить группу

1. Отправьте боту `/start`, выберите «Подключить чат».
2. Добавьте бота администратором без прав `can_restrict_members` и `can_delete_messages`.
3. Уведомите участников об обработке сообщений и внешнем AI. Нажмите «Подключить» в заявке, которую бот пришлёт в ЛС.

Сбор начинается после подтверждения. История до подключения недоступна. Подробности: [подключение](docs/chat-onboarding.md), [ограничения Bot API](docs/telegram-limitations.md).

Для проверки напишите сообщение в тестовой группе, затем ответьте на него `/report причина`. Администратору должна прийти карточка. Проверьте `/status` и health:

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml exec sentinel sentinel healthcheck
```

Если сообщения не приходят, проверьте статус администратора и Privacy Mode в BotFather; после его изменения переподключите бота. Для проверки `chat_member` измените права отдельного тестового участника. [Диагностика](docs/operations.md).

## Управление

`/start` открывает меню с баннером; переходы меняют подпись и кнопки того же сообщения. В «Модулях» выберите чат, настройте продажи, раздачи, услуги и поиск клиентов. Словари и дополнительные параметры открываются отдельно; изменения требуют сохранения и подтверждения. [Настройка модулей](docs/chat-modules.md).

В группе жалобы принимаются ответом на сообщение: `/report`, `/report@your_bot`, `!репорт`, `!report`. Дополнительные жалобы сверх пяти в час сохраняются в очереди `/reports`.

`moderation.ignore_admin_messages: true` исключает сообщения владельца и администраторов группы из автоматического анализа и журнала, включая анонимного администратора. Команды, кнопки и явные жалобы `/report` работают. При отсутствии параметра игнор выключен. После изменения нужен перезапуск бота; прежние кейсы и задания сохраняются.

Команды администратора работают в разрешённом ЛС или закрытом админ-чате:

| Команда | Действие |
|---|---|
| `/start`, `/chats` | Главное меню, список чатов и заявки |
| `/status`, `/stats` | Состояние и очереди |
| `/model [поиск]`, `/models [поиск]` | Выбор модели AI |
| `/modules [CHAT_ID]` | Правила выбранного чата |
| `/modulewords PANEL_ID MATCHER MODE слова \| фразы` | Заменить словарь в черновике |
| `/modulelimit PANEL_ID KEY N` | Изменить параметр в черновике |
| `/case ID` | Открыть кейс |
| `/reports [CHAT_ID]`, `/reportcase REPORT_ID` | Жалобы и их кейсы |
| `/feedback ID benign\|spam\|false_positive\|needs_review\|close [текст]` | Сохранить решение |
| `/resolve CASE_ID benign\|spam\|false_positive [текст]` | Разрешить конфликт меток |
| `/review ID` | Запросить AI-проверку |
| `/attest ID warn\|ban\|restrict\|delete [текст]` | Записать действие со слов администратора |
| `/link CASE_ID EVENT_DB_ID` | Связать кейс с событием из `moderation_events` |
| `/tune`, `/forget` | Настройки и инструкция удаления данных |

## AI и хранение

`/model` сохраняет выбор для всех чатов; нужны права администратора во всех них. Поиск: `/model luna`. Изменение действует на новые попытки, включая повторы. [Модели и доступ к каталогу](docs/runtime-models.md).

Обязательную AI-проверку создают первое наблюдаемое сообщение, неизвестный домен, ссылка на бота и сравнение цен/жалоба на сервис. Trust и лимит дополнительных запросов их не отменяют. При сбое или `ai.enabled: false` задания ждут в очереди. Лимит дополнительных запросов задаётся на сутки UTC; лимитов по токенам и деньгам пока нет. Стоимость рассчитывается при наличии usage и заданных цен.

Журнал: до 1000 сообщений на чат и до 7 дней. Остальные сроки, резервные копии и удаление данных описаны в [политике хранения](docs/privacy.md). Не публикуйте `.env`, реальные IDs, дампы, dataset и session-файлы. Health, metrics и PostgreSQL доступны только внутри сети Compose.

## Разработка

```bash
make setup
make test
make lint
make race
make inspect  # нормализация и риск без AI
make build
```

Интеграционные тесты требуют отдельной БД с правами CREATE/DROP SCHEMA. Без `TEST_DATABASE_URL` они пропускаются. Не используйте production.

```bash
docker run -d --name sentinel-test-db \
  -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=sentinel_test \
  -p 127.0.0.1:55432:5432 postgres:17-alpine
export TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55432/sentinel_test?sslmode=disable'
make integration
# Остановить и удалить только созданную выше тестовую БД:
docker stop sentinel-test-db
docker rm sentinel-test-db
```

Тесты используют fake Telegram/AI. CI запускает PostgreSQL, race, vet и сборку образа. Для запуска без Docker задайте `DATABASE_URL`, `TELEGRAM_BOT_TOKEN`, `AI_*` в окружении и выполните `make run`: приложение само `.env` не читает. Не включайте shell tracing с секретами.

## Документация

- [Архитектура](docs/architecture.md), [решения](docs/decisions/0001-bot-api-mvp.md).
- [Модули анализа и добавление детекторов](docs/analysis-modules.md).
- [Backup, restore, обновление и rollback](docs/operations.md).
- [Экспорт и replay](docs/tuning.md).
- [Реализованное и оставшиеся задачи](docs/implementation-status.md).
- [Оформление карточек и Premium](docs/telegram-limitations.md#оформление-карточек).
