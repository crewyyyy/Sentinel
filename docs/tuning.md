# Offline evaluation и feedback

Метка администратора — strong label, санкция без установленной причины — только weak evidence, report — запрос на проверку, AI — prediction. Код не делает online fine-tuning, не меняет правила после бана и не обновляет permanent denylist автоматически.

```bash
mkdir -p exports
chmod 700 exports
# С настроенным env и YAML:
sentinel export --config configs/config.yaml > exports/review-required.jsonl
```

Экспорт включает только explicit benign/spam/false_positive feedback, исключает conflicting cases. Конфликт разрешается авторизованной командой `/resolve CASE_ID benign|spam|false_positive [обоснование]` с append-only аудитом; обычная следующая кнопка не заменяет арбитраж. Пример берётся из snapshot на момент человеческой разметки, а не из позднее отредактированной карточки. Telegram IDs/usernames из полей удалены; URLs в отдельных features теряют paths/payloads. **Текст требует дополнительного privacy review**: regex-redaction и удаление структурных IDs не гарантируют анонимность свободного текста. Не публикуйте export без проверки.

Group определяется доменами кампании либо нормализованным текстом; одинаковая группа не попадает одновременно в train/holdout. Группы с решениями последних 7 дней целиком относятся к holdout; это консервативный временной барьер, а не доказанная кластеризация всех похожих кампаний. У корпуса только ранних решений holdout может быть пуст — пополните его будущими кейсами.

```bash
# Проверить/обезличить экспорт вручную, затем выделить holdout:
jq -c 'select(.split == "holdout")' exports/review-required.jsonl > exports/holdout.jsonl
sentinel replay --config configs/config.yaml --file exports/holdout.jsonl
# Для сравнения нового YAML: новая ruleset_version, затем та же immutable holdout выборка:
sentinel replay --config configs/candidate.yaml --file exports/holdout.jsonl
```

Replay **локальный**, не вызывает LLM и не изменяет исходные оценки/БД. Он сравнивает threshold-предсказание deterministic rules с human labels: TP/FP/TN/FN, precision/recall (null, если нет знаменателя), число review-required. Это не итоговая accuracy LLM и не подтверждённая стоимость. Изменение allowlist требует replay URL extraction из исходных fixtures — текущий JSONL содержит зафиксированные URL flags и позволяет сравнивать прежде всего веса/пороги/trust-discount.

Модули, словари и caps настраиваются через `analysis` в YAML: [инструкция](analysis-modules.md). `modules`, `inspect` и `replay` работают без runtime-ключей. Поле `IncompleteFeatures` считает samples с неизвестной версией или неполным набором признаков; включённый `referral_cues` направляет такие samples на review, не утверждая наличие рефералки. Для проверки новых extractors нужны исходные синтетические fixtures или вручную проверенные samples текущего формата.

Для цены сравните AIRequired с вашими измеренными usage/job и тарифами; если usage неизвестен, не выдавайте точную сумму. Token/currency hard budgets ещё не реализованы; request-limit ограничивает только optional attempts.

Перед публикацией новой версии:

1. Собрать 100–300 разрешённых, размеченных примеров по рекламе, документации, честной критике, нейтральному сравнению, bot promo, phishing/API-key theft, цитатам рекламы.
2. Удалить дубли/похожие кампании между train и holdout, зафиксировать исходные данные и версии.
3. Отдельно измерить качество и нагрузку по категориям. Не подбирать веса на holdout и затем объявлять его независимой оценкой.
4. Дать человеку review диффа и false positives, сохранить предыдущий YAML/model/prompt, повысить version, validate + tests + restart.
5. При регрессии вернуть прошлый совместимый snapshot. Новые отзывы пополняют датасет; они не означают, что модель уже «научилась».
