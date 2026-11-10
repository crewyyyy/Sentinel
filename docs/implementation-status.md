# Состояние реализации

Проверки исходного кода и образа `sentinel:modular-check` выполнены **2026-11-10**. Это работающий Bot API MVP с инструментами Phase 5, не полная приёмка Task.md. Task.md/CLAUDE.md и их чекбоксы не изменялись.

| Фаза Task.md | Результат |
|---|---|
| 0 — фундамент | Go module, strict typed env/YAML, observe-only validation, lifecycle, Docker/Compose/CI, docs. Live Telegram checklist остаётся ручным |
| 1 — наблюдение | Controlled polling, durable dedup/inbox/cursor, нормализация, Postgres journal/revisions, TTL/cap, message identity keys, counters/reply graph, sender_chat. Restart/duplicate/edit/prune сценарии проверены локально |
| 2 — риск/trust | Общий модульный Analyzer для production/inspect/replay, каталог и YAML matchers/caps; URL/entities/caption/quote/IDNA/PSL; четыре MUST, unknown-Telegram/scan-limit review; ref-параметры и bot-компоненты; capped trust. 101 полная v1-оценка совпадает с исходным движком |
| 3 — LLM | Provider interface, compatible HTTP, versioned prompt, strict enums/required fields/limits, small context, redaction/pseudonyms, durable priority jobs/attempts/fencing, retry/429/circuit breaker, optional request budget, usage/estimated cost, `/model` с пагинацией/подтверждением и персистентной сменой для новых попыток. Не реализованы token/money hard budgets и отдельные provider-specific dialects |
| 4 — карточки/репорты/факты | Group/private destinations, короткие русские карточки, custom emoji с fallback, исправленное закрытие connection-карточки, немедленная доставка первых уведомлений. Stable supergroup/topic links, RBAC, reply-only dedup/rate-limited reports, platform transitions, attestation/manual relation. Полная матрица проверена fake API/DB; доставка approved-карточки подтверждена live |
| 5 — стабилизация | Feedback audit и явный `/resolve`, conflict exclusion, export label-time snapshots с privacy-review, campaign/time holdout, deterministic replay, status/stats/case/tune/forget, очередь `/reports` + `/reportcase`, race/backup-restore/container build. Полная latency observability и реальный corpus/quality measurement остаются эксплуатационной приёмкой |
| 6 — MTProto | Не реализован и не включён; отдельный будущий проект при доказанной необходимости |

## Фактически проверено

- `go test ./...`, `go vet ./...`, `go test -race -count=1 -timeout 180s ./...` с `TEST_DATABASE_URL` на локальном PostgreSQL 17, без платных запросов/публичного Telegram.
- Модульный движок: 101 неизменяемая полная v1-оценка, required-модули, validation, caps/trust без отмены mandatory, порядок/дедуп сигналов, immutable snapshot и concurrent reuse. `REF` в query keys, компоненты `ref123`, отрицательный `reference`; несколько сигналов дают одну durable job, restart/edit сохраняют старый payload.
- Старый и неизвестный будущий формат inbox не перепарсиваются по offsets замаскированного текста. Неполные признаки дают mandatory review без утверждения о рефералке; replay отдельно считает `IncompleteFeatures`. Metadata extraction не уходит внешнему AI.
- Privacy-регрессии для `password=@name` и URL в значении секрета: detached facts удаляются с сохранением coverage/review. Повторяющиеся обфусцированные ссылки не скрывают 33-й кандидат; malformed query сохраняет partial coverage.
- Реальные CLI `modules`, 12 `inspect` сценариев и `replay` на четырёх samples выполнены в контейнере `--read-only --network none`, без runtime credentials.
- Миграции 0001/0002/0003 up/down/up, повторный migrate и upgrade старой initial-схемы с сохранением pending inbox; FK/unique constraints; атомарный rollback dedup/cursor при неудачной вставке inbox.
- Auto-onboarding: запуск с пустым `chat_ids`, ограниченные кандидаты `my_chat_member`, private owner/receipt/RBAC/expiry confirmation, pre-consent и старый queued backlog не собираются, restart/DM retry, removal/stale event/rejoin, явный configured consent. Dynamic chat IDs участвуют в глобальной авторизации модели; нет mutable Config/cache.
- `/model`: пагинация, поиск, отдельное подтверждение, callback receipt/owner/current-rights/expiry, отказ при устаревшей настройке другого админа, сохранение после restart и новая модель для retry. Меню не доставляется в public destination или после отзыва прав. Каталог ограничен по размеру и не следует redirect; API body ошибок/ключи не выводятся.
- First observed после restart и очистки; независимость двух чатов; edit/replay без двойных counters; edit после prune получает следующую ревизию; late review не переписывает актуальный кейс.
- Четыре MUST даже при max trust; mandatory jobs claim при нулевом optional budget; конкурентные claims, lease expiry, stale-worker fencing, provider retry; LLM malformed schema/429/5xx/timeout, injection data isolation и usage unknown.
- Reports 15 авторов → один кейс; шестая rate-limited жалоба сохраняет mandatory review и открывается через `/reportcase` даже после journal prune. Уже завершённый review текущей ревизии не показывается как ложный pending и не дублируется.
- Неадмин, неверная карточка и отозванные права отклонены, включая новые команды очереди/арбитража. `/mute`, spam-label и `/resolve` не создают verified action; platform transition и admin-attested раздельны.
- Конфликт исключает export до явного арбитража; export использует snapshot на момент разметки, не позднейший edit. Replay проверен локально, precision без знаменателя остаётся null. YAML bounded 64 KiB/single document; ошибки не раскрывают значения scalar.
- Journal cap 1000 + TTL, pending evidence не теряется; unmanaged messages не сохраняются; credentials редактируются в journal/jobs/cards и перед внешним review.
- Fake SDK send и polling offset/429; deep links/UTF-16/IDN/hidden/caption entities; HTML card/negative callback paths через domain integration.
- Docker image собран, runtime user `sentinel`, offline `validate`/`inspect` проходят с `--read-only --network none`; Compose config валиден. Реальная DB-backed health handler проверена локально.
- Финальный image дважды применил миграции к новой disposable DB; `pg_dump -Fc` → `pg_restore --exit-on-error` в ещё одну новую DB восстановил три migration records, runtime-модель, одобренный chat consent, durable inbox event и cursor. Отдельно проверен upgrade с предыдущего Docker image (схема 0001/0002) до 0003. Это smoke restore, не доказательство production RPO/RTO.

## Что остаётся честным ограничением

1. Production обновлён до `v2-modular-ref`, `referral_cues` включён. Health и единственная bot advisory lock проверены; startup без ошибок. Снимок новой политики зарегистрирован, хеши прежних AI payload и оценок сохранены. Перед выкладкой pending AI jobs не было; сценарии их сохранения проверены интеграционными тестами. Обновление approved-карточки подключения доставлено через outbox после исправления empty keyboard. `getMe` и синтетический запрос к AI endpoint ранее прошли smoke. Полная ручная матрица BotFather/прав, всех карточек и platform transitions, качество классификации и биллинг этим не подтверждены.
2. В этом релизе polling only, без webhook, reactions, MTProto Admin Log и trusted-bot integration. Нет универсального deleted-message source; deletion unknown. Public username deeplinks заменены стабильными private supergroup links.
3. Report rate-limit сохраняет дополнительные жалобы pending и отправляет сводку, не отменяя AI review; `/reports [CHAT_ID]` показывает старейшие десять, `/reportcase REPORT_ID` открывает кейс. Веб-интерфейса и массовых операторских операций нет. ID platform event для ручного `/link` пока берётся из БД.
4. `/review` обеспечивает один review актуальной ревизии, не force-rerun уже готового результата. Hot tuning правил не поддержан; `/tune` — инструкция, изменение YAML требует validate/version/restart. Модель отдельно меняется через `/model` без restart с аудитом, привязкой кнопок и проверкой прав во всех чатах.
5. Export — не гарантия полной анонимности текста; replay оценивает deterministic threshold на зафиксированных URL features, не новую модель/allowlist pipeline. Нет 100–300 реальных human-labelled кейсов и достигнутых precision/recall/FP/FN targets.
6. Нет измеренного live SLO 5–15 секунд, полноценного нагрузочного теста, всех latency histograms или формального privacy/security-аудита. Внешние send/LLM могут повториться после неизвестного результата timeout; идемпотентность ограничена API провайдера.
7. Optional budget — только requests/UTC day, не токены/деньги. Pending jobs при длительном outage требуют monitoring; необработанные mandatory не выбрасываются ради TTL/бюджета. Совместимый endpoint должен поддерживать используемые JSON request параметры.
8. Cleanup периодический (до минуты отклонения от cap/TTL), first-observed metadata живёт дольше текста. Уже отправленные Telegram cards и backups очищаются отдельно администраторами. Forget ограничен retained evidence и не обещает удаления сторонних копий/всех упоминаний в чужом контексте.

Перед публичным релизом пройдите ручной checklist, настройте privacy notice/backups/monitoring и закройте нужные для вашего сценария пункты Phase 5. Ни одну санкцию или выполненную AI-проверку не следует выводить из обычного текста/метки/недоступной ссылки.
