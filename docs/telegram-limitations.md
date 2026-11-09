# Возможности и ручная проверка Telegram

Официальные источники: [Bot API](https://core.telegram.org/bots/api), [FAQ](https://core.telegram.org/bots/faq), [Recent Actions](https://core.telegram.org/api/recent-actions). Поля messages/entities/updates сверены при реализации; использован SDK `github.com/go-telegram/bot v1.27.0`. Официальная документация не заменяет проверку на вашей тестовой группе.

- Подписка ровно на `message`, `edited_message`, `chat_member`, `my_chat_member`, `callback_query`.
- Для всех обычных сообщений нужен корректный Privacy Mode/статус бота. Админский статус нужен для `chat_member`; явно не нужны права выдавать санкции.
- У первого сообщения смысл **first observed since installation**, не новый аккаунт/первое за всю историю. История до подключения недоступна, coverage partial. После полной потери БД это знание теряется; восстановите backup, не заявляйте полное покрытие.
- Бот не читает произвольные ЛС участников. В админских ЛС обрабатываются только поддержанные команды; сообщения других приватных диалогов не сохраняются.
- Страна/IP/SIM-регион отсутствуют. `language_code` не геолокация и не фактор trust.
- Sender_chat/анонимные сообщения не превращаются в фиктивного пользователя (`from` может быть совместимым fake sender от Telegram). Forward сохраняет тип, не выдуманную личность скрытого автора. Quote/caption/entities учитываются, UTF-16 не индексируется как bytes.
- Для arbitrary `t.me/name` нет универсальной Bot API проверки username. Known bot objects/config → confirmed; suffix `bot`/start payload → heuristic; остальные → unverified. Наличие start payload само по себе не доказывает рекламу.
- `/mute`, `/ban`, сообщения другого бота и скриншоты — не подтверждение. Боты могут не видеть сообщения других ботов; доверенный интеграционный канал в этом MVP не реализован.
- Нет универсального deleted-message update для обычной группы. Недоступность permalink не доказывает удаление. Варны не имеют стандартного member-status.
- `chat_member.from` — actor события; old→new используются вместе. При restrict сохраняются sending-state/until_date, не всякая смена ограничений — mute. Неоднозначная связь с кейсом остаётся unknown.
- Ссылки строятся из стабильного supergroup ID `-100…`: `t.me/c/internal/message`, topic — `t.me/c/internal/thread/message`. Они требуют доступа к группе. Для обычной группы/невалидного ID кнопка не выдумывается. Public username links специально не используются.
- Telegram хранит недоставленные updates ограниченное время (обычно не более 24 часов). Нельзя гарантировать восстановление никогда не полученных событий после долгого outage; durable guarantees начинаются с успешного ingest.

## Перед production

1. Создать тестовую группу, ограничить админ-чат, добавить бота без restrict/delete прав.
2. Проверить обычное сообщение, caption + hidden text_link, edit, topic, sender_chat, reply report, реальное изменение member permissions.
3. Проверить `/status`, карточку, неадминский/устаревший callback и отзыв прав админа.
4. Каждый получатель ЛС сначала открывает диалог с ботом; закрытый admin destination остаётся обязательным fallback.
5. Убедиться, что webhook пуст, работает только один poller, живы DB/health/metrics. Измерить фактическую задержку; целевые 5–15 секунд в этой среде не подтверждены.
