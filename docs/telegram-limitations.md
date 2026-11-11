# Telegram: возможности и проверка

[Bot API](https://core.telegram.org/bots/api), [FAQ](https://core.telegram.org/bots/faq), [Recent Actions](https://core.telegram.org/api/recent-actions). SDK: `github.com/go-telegram/bot v1.27.0`.

- Подписка: `message`, `edited_message`, `chat_member`, `my_chat_member`, `callback_query`.
- Для `chat_member` нужен статус администратора. Права restrict/delete не нужны.
- Бот получает новые сообщения после подключения. Первое наблюдаемое сообщение учитывается по сохранённой истории; после потери БД восстановите backup.
- ЛС участников, их страна, IP и SIM-регион недоступны. `language_code` означает язык интерфейса и не участвует в trust.
- Анонимный автор, пересылка, quote и caption обрабатываются отдельно. Entities используют UTF-16 offsets.
- Тип произвольного `t.me/name` обычно неизвестен. Объект User и настроенный список ботов дают подтверждение; суффикс `bot` и start payload используются как эвристики.
- События модерации определяются по old/new состояниям `chat_member` и actor. Ручное подтверждение хранится как `admin_attested`; обычный текст `/mute` или `/ban` остаётся текстом.
- Универсального события удаления сообщения и стандартного статуса warn в Bot API нет. Связь санкции с кейсом устанавливается отдельно.
- Ссылки на супергруппы: `t.me/c/internal/message`, на темы: `t.me/c/internal/thread/message`. Получателю нужен доступ к группе. Для обычной группы такой ссылки нет.
- Telegram хранит недоставленные updates обычно не более 24 часов. Сохранение в PostgreSQL начинается с успешного приёма update.

## Оформление карточек

Custom emoji взяты из [Telegram iOS Icons](https://t.me/addemoji/tgiosicons). Текст использует HTML `tg-emoji`; кнопки — `icon_custom_emoji_id` и стили `primary`/`success`/`danger`.

[Условия Telegram](https://core.telegram.org/bots/api#html-style): дополнительный username через Fragment либо Premium у владельца для поддерживаемых сообщений. При явном отказе emoji адаптер повторяет запрос без них и сохраняет этот режим до restart. Ошибки 429/5xx и сетевые сбои обрабатываются очередью доставки.

Карточка содержит сообщение, риск/доверие, вывод AI и причины. Первые карточки, ответы AI и действия администратора доставляются сразу. `batch_window_seconds` объединяет только фоновые обновления.

## Проверка перед запуском

1. Создайте тестовую группу и закрытый админ-чат. Добавьте бота без restrict/delete прав; получатели ЛС должны открыть `/start`.
2. Проверьте обычное сообщение, caption, скрытый text_link, edit, topic, sender_chat и reply-report.
3. Измените права тестового участника и проверьте `chat_member`. Проверьте отказ неадмину, старой кнопке и администратору с отозванными правами.
4. Проверьте `/status`, health, metrics, пустой webhook и единственный poller. Измерьте задержку доставки в своей группе.
