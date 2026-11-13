# Message pipeline diagram

Create an exceptionally clear, publication-ready RUSSIAN LANGUAGE process diagram for a GitHub README explaining the Telegram bot SENTINEL. Requested landscape 1536 × 1024 pixels. This is a factual FLOWCHART, not a poster, marketing dashboard, mock UI, screenshot or decorative collage. All diagram text must be crisp and correctly spelled in Cyrillic. Prioritize readable type, truthful routing and generous whitespace over decoration.

Visual family: white/off-white background, deep cobalt blue (#182CC4), near-black type, pale blue connectors, a few carefully machined silver details. Inspired by precise Swiss editorial diagrams and the project's white/cobalt/chrome banner. Use mostly flat 2D shapes with thin clean borders. A very small chrome shield may appear beside SENTINEL in the heading, but no large illustration. No neon, robot faces, padlocks, police emblems, gradients behind text, tiny pseudo-labels, irrelevant numbers or subtitles about sanctions. Do not include any claims about automatic bans, mutes, deleting messages, online learning or guaranteed safety.

Title at top: «Как Sentinel проверяет сообщения».

Build a legible diagram with a clear MAIN FLOW arranged in three horizontal rows. Use numbered stages and distinct arrowheads, no ambiguous crossings. The main path reads LEFT TO RIGHT across each row, with a long routed connector from a row's last stage down to the next row's first stage. Stage labels should be the largest text inside nodes; sublabels no smaller than about 22–24 pixels on this canvas. Keep every label contained and every arrow outside text.

ROW 1 — intake, four connected stages:
1 «Сообщение» — sublabel «Новое или отредактированное».
2 «Доступ к обработке» — sublabel «Подключённый чат · игнор админов по настройке».
3 «Подготовка» — sublabel «Текст, ссылки, цитаты · маскирование секретов».
4 «Сохранение» — sublabel «Очередь и журнал · защита от дублей».
A small secondary branch from stage 2 ends at «Вне обработки», meaning a disconnected chat or an explicitly excluded administrator. It must NOT flow into AI.

ROW 2 — analysis, four connected stages:
5 «Правила и риск» — sublabel «Ссылки · предложения · повторы · история автора».
6 Decision diamond «Нужна AI-проверка?».
7 «Очередь AI» — sublabel «Задание сохраняется до проверки».
8 «AI и проверка ответа» — sublabel «Краткий контекст · вывод · проверка формата».
The YES branch from stage 6 goes to stage 7 and then stage 8. The NO branch from stage 6 ends at a quieter node «Только журнал» and does not send a card merely because a message was received.
A compact readable callout beside this row, connected to the decision without obstructing arrows, lists FOUR MAIN MANDATORY REASONS, with exact text:
«Первое наблюдаемое сообщение»
«Неизвестный домен»
«Ссылка на бота»
«Сравнение цен или жалоба на сервис»
Below this callout, one short line: «Также: высокий риск или ручной запрос».
From stage 8, a visibly secondary return loop labelled «Ошибка API или формата: повтор» goes back to stage 7. It must not go to the successful-result path.

ROW 3 — escalation and moderator workflow, three connected stages:
9 «Кейс» — sublabel «Причины · риск · вывод AI».
10 «Приватная карточка» — sublabel «ЛС или закрытый админ-чат».
11 «Решение модератора» — sublabel «Метка и история кейса».
From stage 8 to stage 9, label the successful escalation connector «Если нужен разбор». From stage 8, an optional subdued branch to «Только журнал» is allowed for a result with no reason to escalate; DO NOT depict every AI answer as generating an alert.
A dashed cobalt SHORTCUT connects the LOCAL analysis stage 5 directly to case stage 9, labelled «Высокий риск · правило «Запретить» · /report». Next to this shortcut, a small caption says «Карточка может появиться до ответа AI». This is essential: local high risk, prohibited-policy matches and manual reports can open a case before AI completes.
Beside stage 9, include a tiny but readable secondary input «События Telegram», with an arrow pointing INTO the case, representing independently received moderation events. Do not draw an arrow from the AI or a decision label toward Telegram actions.

Important correctness constraints: all main arrows must follow the numbered chronology; gates can exclude messages before processing; not all messages need AI; the four main triggers bypass optional budget/trust; no failed AI response becomes a successful classification; a pending case can precede AI and then be updated; administrator decisions are stored after delivery, with NO feedback arrow implying automatic model retraining. Keep the diagram comprehensible at README width. A compact legend may distinguish solid main-flow arrows, dashed early-case path and retry loop, but do not add explanatory paragraphs. Render the finished diagram as a single opaque PNG.
