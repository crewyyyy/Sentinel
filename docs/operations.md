# Эксплуатация

Все команды выполняются из корня репозитория. Не открывайте PostgreSQL, health/metrics или Docker socket в интернет. Используйте SSH, firewall, шифрование диска и отдельное хранение `.env`/backups. Один bot token/DB/process; отдельные инстансы — отдельные БД.

## Состояние и диагностика

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml ps
docker compose --env-file deploy/.env -f deploy/compose.yaml logs --tail=100 sentinel
docker compose --env-file deploy/.env -f deploy/compose.yaml exec sentinel sentinel healthcheck
# Очереди без текста сообщений и секретов:
docker compose --env-file deploy/.env -f deploy/compose.yaml exec db \
  psql -U sentinel -d sentinel -c "SELECT priority,status,count(*),min(created_at) FROM ai_jobs GROUP BY 1,2;"
```

`/healthz` проверяет DB, **не** успешность LLM/Telegram доставки. `/metrics` доступен только внутри сети/на локальном bind; admin HTTP endpoints отсутствуют. Есть durable counters updates/messages/triggers/required AI/attempts/successes/tokens/estimated micro-cost/false-positive feedback/reports, gauges pending/inbox/queue lag и retained counts. `*_retained` уменьшаются после очистки; это не lifetime counters. Пока нет полного набора latency histograms и SLO.

| Симптом | Проверка |
|---|---|
| Не видит обычные сообщения | BotFather Privacy Mode, членство/статус бота, переподключение после смены privacy |
| Нет chat_member | Бот должен быть админом; explicit allowed_updates включён. Проверьте реальное old→new в тестовой группе |
| Startup отказал из-за webhook | Другой consumer использует токен. Явно удалите webhook без drop_pending_updates, только затем polling |
| Callback отклонён | Allowlisted user ID + актуальное членство admin/owner в исходной группе + привязка к реально отправленной карточке |
| ЛС не доставляются | Получатель сначала открывает диалог с ботом и сохраняет актуальные права администратора исходного чата. Delivery повторно проверяет права и непубличность destination; `recipient_not_authorized` означает отказ, не доставку |
| `/model`: 401 / 403 | Проверьте `AI_API_KEY`, `AI_BASE_URL` и доступ ключа к каталогу; после правки env пересоздайте контейнер |
| AI pending растёт | ai.enabled, совместимость endpoint/model/JSON parameters, ключ/лимит провайдера, ai_attempts.error_code, circuit breaker |
| Optional jobs не идут | UTC request-budget расходуется и на retries; mandatory имеет отдельную приоритетность |
| Telegram 429 | Сохраняется outbox, backoff учитывает retry_after; не перезапускайте бесконечно |
| Карточка продублирована | Timeout мог произойти после фактической отправки. Отправка не exactly-once; IDs и результат требуют ручной проверки |
| Deep link недоступен | Нужен доступ к супергруппе/topic; обычная группа не имеет поддержанной private link схемы |
| Inbox retries / decode blocked | Событие не подтверждается/не выбрасывается. Проверьте код/схему; не ставьте done вручную без расследования |

## Очередь жалоб и арбитраж

`/reports [CHAT_ID]` показывает старейшие десять pending-жалоб с состоянием AI; `/reportcase REPORT_ID` открывает/обновляет кейс, включая доступное ограниченное evidence из job после очистки журнала. При отсутствии содержимого оно явно unknown/partial. Текущие права проверяются для исходного чата каждой жалобы.

Первые пять жалоб в час от одного автора создают немедленные уведомления. Последующие остаются pending со сводкой; review не отменяется, высокий риск/обвинительный результат может дать срочную карточку. После явной неконфликтующей обработки связанные reports получают reviewed. Конфликт меток требует `/resolve CASE_ID benign|spam|false_positive [обоснование]`; арбитраж пишется в аудит, не считается санкцией.

Inbox намеренно FIFO: ошибка старейшего события задерживает дальнейший deterministic processing, чтобы не перепутать first observed и порядок правок. Durable ingest остаётся независимым; при росте backlog расследуйте старейший retry, не пропускайте его вручную.

По умолчанию jobs не удаляются после N ошибок. Это сознательный выбор для observe-only/MUST. Оператор видит pending и исправляет конфигурацию/совместимость, а не объявляет jobs проверенными. Не публикуйте payload ошибок/SQL rows с текстом в issue.

## Backup и restore

Планируйте регулярные encrypted off-host backups, ограничьте срок их хранения. `.env` и ключи сохраняйте **отдельно**, не внутрь публичного dump. PostgreSQL volume сам по себе не backup.

```bash
umask 077
mkdir -p backups
docker compose --env-file deploy/.env -f deploy/compose.yaml exec -T db \
  pg_dump -U sentinel -d sentinel -Fc > backups/sentinel.dump
# На отдельной тестовой БД, не поверх рабочей:
docker compose --env-file deploy/.env -f deploy/compose.yaml exec db \
  createdb -U sentinel sentinel_restore_test
docker compose --env-file deploy/.env -f deploy/compose.yaml exec -T db \
  pg_restore -U sentinel -d sentinel_restore_test --exit-on-error < backups/sentinel.dump
docker compose --env-file deploy/.env -f deploy/compose.yaml exec db \
  psql -U sentinel -d sentinel_restore_test -c "SELECT count(*) FROM schema_migrations; SELECT count(*) FROM ai_jobs; SELECT count(*) FROM inbox;"
```

Не подключайте восстановленную копию к тому же Telegram-токену параллельно с production. Проверьте first observed, cursor, jobs, alert IDs, FK/unique constraints и сроки данных. Отработайте переключение в maintenance window. Повторите restore-test после обновления PostgreSQL и изменений миграций. Перед удалением тестовой БД убедитесь в её имени и назначении.

## Обновление и rollback

1. Backup + restore-test, сохранить прежний binary/image и YAML.
2. Остановить worker, проверить новый `validate`, unit/integration/race tests и offline replay.
3. Новые веса/allowlists/trust/thresholds → новая `ruleset_version`, существующие оценки не переписываются. Не изменять prompt под старым именем версии.
4. `docker compose ... build sentinel` и `up -d sentinel`. Проверить health, pending lag, delivery, отсутствие второго poller. SIGTERM останавливает workers; незавершённая транзакция откатывается, leases истекают.
5. Для rollback кода вернуть совместимый image/config. Не применять down к production вслепую.

Runner применяет упорядоченные `0001_init`, `0002_runtime_models`, `0003_chat_onboarding`, `0004_module_settings`, `0005_control_panels` транзакционно под advisory lock, пропускает уже записанные версии. Проверены up/down/up и upgrade с сохранением pending inbox. Новые миграции регистрируйте в `migrations.Ordered()`, не меняйте уже применённый SQL.

Down SQL — **разрушительный** rollback, только на тестовой БД после подтверждённого backup; порядок обратный: `0005`, `0004`, `0003`, `0002`, затем `0001`. Удаление соответствующих version rows на тесте требуется для повторного up. `0002` добавляет runtime-модель, панели/audit и model/prompt metadata попыток; `0003` — managed-chat consent, private onboarding requests и audit. Откат теряет соответствующие настройки, но не выполняет санкции. `0004` добавляет настройки модулей, панели и аудит; `0005` добавляет главную панель. При откате к старому приложению отдельно обработайте новые `module_panel`/`control_panel` задания outbox после остановки workers: старый код не понимает эти типы. AI jobs и принятые updates не удаляйте.

## Подключение без ручного chat_id

При `auto_discover_chats: true` можно начать с `chat_ids: []`. `/start` в ЛС от allowlisted admin → добавление/повышение бота до admin в группе → privacy notice участникам → подтверждение карточки в ЛС. `/chats` повторно отправляет pending-заявки, если ЛС ранее не было доступно. До подтверждения сообщений группы в journal/AI нет. Удаление бота отключает auto-чат, повторное добавление требует нового подтверждения. Сохранённые consent/IDs входят в backup; changing discovery flag не удаляет уже одобренные чаты. См. [onboarding](chat-onboarding.md).

## Настройки без исходников

`moderation.ignore_admin_messages: true` исключает владельца и администраторов группы из автоматического анализа; без параметра действует `false`. Статус проверяется через `getChatMember`, независимо от `allowed_admin_ids`; анонимный администратор определяется по `sender_chat`, совпадающему с самой группой. Сообщения других каналов не исключаются. При ошибке проверки update не подтверждается до повтора; уже записанный inbox остаётся pending. Нужен перезапуск после изменения параметра. Явные `/report`, команды и кнопки работают, старые задания не отменяются. Пока игнор включён, пропущенные сообщения не добавляют статистику активности и ответов администратора.

Меняйте `configs/config.yaml`, выполняйте `sentinel validate`, повышайте ruleset_version при изменении detection policy и перезапускайте контейнер. Hot reload базового YAML и автоматический тюнинг отсутствуют. Баннер `/start` встроен в бинарник, внешний URL или volume для него не нужен. Новые панели используют фото с подписью; прежние текстовые продолжают редактироваться до следующего `/start`. Тип сообщения сохраняется в `outbox.result.banner`, новая SQL-миграция не нужна. `/modules [CHAT_ID]` позволяет менять необязательные модули выбранного чата через черновик и подтверждение без restart; настройка версионируется, старые оценки и jobs не пересчитываются. `/model [поиск]` → страницы → выбор → подтверждение сохраняют модель без restart; нужны права во всех чатах. Runtime-выбор приоритетнее `AI_MODEL`, usage другой модели не оценивается по исходным ценам. См. [смену модели](runtime-models.md). `watch_message_edits` и mandatory budget bypass нельзя отключить. Для мягкого shadow-старта повышайте **alert_threshold**, не отключайте обязательные AI-задачи.
