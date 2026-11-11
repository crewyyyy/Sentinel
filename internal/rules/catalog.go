package rules

import (
	"sort"

	"sentinel/internal/rules/match"
)

// SignalSpec is the only source of weights, mandatory policy and signal families.
type SignalSpec struct {
	Code          string `json:"code"`
	DefaultWeight int    `json:"default_weight"`
	Mandatory     bool   `json:"mandatory_ai"`
	Family        string `json:"family"`
	LegacyWeight  bool   `json:"legacy_weight"`
}

type MatcherSpec struct {
	Default MatcherConfig `json:"default"`
	Modes   []string      `json:"modes"`
}

type ThresholdSpec struct {
	Default int `json:"default"`
	Min     int `json:"min"`
	Max     int `json:"max"`
}

// Spec describes a built-in module. Catalog returns independent copies; there is
// no mutable registry and no runtime registration or plugin mechanism.
type Spec struct {
	ID             string                   `json:"id"`
	Title          string                   `json:"title"`
	Description    string                   `json:"description"`
	Version        string                   `json:"version"`
	Required       bool                     `json:"required"`
	DefaultEnabled bool                     `json:"default_enabled"`
	Signals        []SignalSpec             `json:"signals"`
	Matchers       map[string]MatcherSpec   `json:"matchers"`
	Thresholds     map[string]ThresholdSpec `json:"thresholds"`
	build          func(buildContext) []stage
}

func literal(terms ...string) MatcherSpec {
	return MatcherSpec{Default: MatcherConfig{Mode: match.LegacySubstring, Terms: terms}, Modes: []string{match.LegacySubstring, match.Token, match.Phrase, match.Exact}}
}

func signal(code string, weight int, mandatory bool, family string) SignalSpec {
	return SignalSpec{Code: code, DefaultWeight: weight, Mandatory: mandatory, Family: family, LegacyWeight: true}
}

func phrases(terms ...string) MatcherSpec {
	return MatcherSpec{Default: MatcherConfig{Mode: match.Phrase, Terms: terms}, Modes: []string{match.Token, match.Phrase}}
}

// Every dictionary is exposed through ModuleConfig.Matchers. Defaults are literal
// words/phrases, not stems or a blacklist that treats a lone subject as an offer.
func policyMatchers(intent, subject []string) map[string]MatcherSpec {
	return map[string]MatcherSpec{
		"intent":  phrases(intent...),
		"subject": phrases(subject...),
		"negation": phrases(
			"не", "никогда", "нельзя", "запрещено", "запрещены", "запрещает", "запрещают", "запрещается", "запрещаются",
			"не надо", "не нужно", "не стоит", "не буду", "not", "never", "do not", "don't", "forbidden", "prohibited",
		),
		"solicitation": phrases(
			"пишите", "пиши", "напишите", "обращайтесь", "обращайся", "свяжитесь", "в личку", "в лс", "в личные сообщения",
			"за оплату", "за деньги", "платно", "принимаю заказы", "заказывайте", "купите", "покупайте", "переходите", "присоединяйтесь",
			"dm me", "message me", "contact me", "contact us", "buy now", "sign up", "join us", "for a fee", "paid service",
		),
		"question": phrases(
			"где", "кто", "зачем", "почему", "как", "можно ли", "правда ли", "where", "who", "why", "how", "can i", "can you", "do you", "are you",
		),
		"reported": phrases(
			"он написал", "она написала", "они написали", "мне написали", "мне прислали", "цитата", "цитирую", "сообщение спамера",
			"это спам", "жалоба на сообщение", "репорт", "someone wrote", "he said", "she said", "spam report", "reported message",
		),
	}
}

// Catalog centralizes metadata, defaults, settings and module construction.
func Catalog() []Spec {
	return []Spec{
		{ID: "observation", Title: "Наблюдение", Description: "Первое сообщение участника с момента подключения бота.", Version: "v1", Required: true, DefaultEnabled: true,
			Signals: []SignalSpec{signal("FIRST_OBSERVED", 12, true, "observation"), {Code: "ANALYSIS_MODULE_ERROR", Mandatory: true, Family: "observation"}}, build: buildObservation},
		{ID: "links", Title: "Ссылки", Description: "Домены, ссылки на ботов, рефералки и подмена адресов.", Version: "v1", Required: true, DefaultEnabled: true,
			Signals: []SignalSpec{
				signal("UNKNOWN_EXTERNAL_DOMAIN", 22, true, "links"), signal("TELEGRAM_BOT_LINK", 25, true, "links"), signal("TELEGRAM_LINK_UNVERIFIED", 0, true, "links"),
				signal("REFERRAL_LINK", 22, false, "links"), signal("SHORTENER", 10, false, "links"), signal("DOMAIN_IMPERSONATION", 25, false, "links"), signal("WATCH_DOMAIN", 10, false, "links"), signal("DENIED_DOMAIN", 35, false, "links"),
				{Code: "URL_SCAN_LIMIT", DefaultWeight: 0, Mandatory: true, Family: "links"},
			}, Thresholds: map[string]ThresholdSpec{"url_scan_limit": {Default: 32, Min: 1, Max: 32}}, build: buildLinks},
		{ID: "commerce", Title: "Сравнения и продвижение", Description: "Сравнения цен, жалобы на сервис и рекламные призывы.", Version: "v1", Required: true, DefaultEnabled: true,
			Signals: []SignalSpec{signal("COMPETITOR_PRICE_OR_ACCUSATION", 0, true, "commerce"), signal("EXTERNAL_PURCHASE_CTA", 22, false, "commerce"), signal("ALTERNATIVE_API_CHEAPER", 15, false, "commerce")},
			Matchers: map[string]MatcherSpec{
				"promotion": literal("купите", "покупайте", "переходи", "заходи", "пишите в лс", "пиши в лс", "мой api", "наш api", "мой сервис", "наш сервис", "buy now", "sign up", "dm me", "use my"),
				"criticism": literal("дешев", "дорого", "скам", "обман", "тариф", "китайский api", "cheaper", "scam", "fraud", "rip-off", "купите у", "купить у", "покупайте у", "buy from"),
			}, build: buildCommerce},
		{ID: "repetition", Title: "Повторные публикации", Description: "Повторяющееся продвижение и одинаковые ссылки в недавних сообщениях.", Version: "v1", DefaultEnabled: true,
			Signals:    []SignalSpec{signal("REPEATED_PROMO", 20, false, "repetition"), signal("MASS_IDENTICAL_LINKS", 18, false, "repetition")},
			Thresholds: map[string]ThresholdSpec{"recent_similar": {Default: 2, Min: 1, Max: 1000000}, "recent_links": {Default: 3, Min: 1, Max: 1000000}}, build: buildRepetition},
		{ID: "credentials", Title: "Запросы секретов", Description: "Просьбы передать API-ключ, токен или пароль.", Version: "v1", DefaultEnabled: true,
			Signals:  []SignalSpec{signal("CREDENTIAL_SOLICITATION", 35, false, "credentials")},
			Matchers: map[string]MatcherSpec{"request": literal("введите", "пришлите", "отправьте", "enter your", "send your"), "secret": literal("api key", "api-key", "api_key", "токен", "пароль", "password", "token")}, build: buildCredentials},
		{ID: "referral_cues", Title: "Реферальные признаки", Description: "Маркеры ref в параметрах ссылок и именах ботов.", Version: "v1", DefaultEnabled: false,
			Signals: []SignalSpec{{Code: "REFERRAL_REVIEW", DefaultWeight: 0, Mandatory: true, Family: "referral_cues"}},
			Matchers: map[string]MatcherSpec{
				"query_keys":     {Default: MatcherConfig{Mode: match.Exact, Terms: []string{"ref", "referral", "affiliate"}}, Modes: []string{match.Exact}},
				"bot_components": {Default: MatcherConfig{Mode: match.Token, Terms: []string{"ref", "referral"}}, Modes: []string{match.Token, match.Exact}},
			}, build: buildReferral},
		policyModule(policySpec{
			id: "sales", code: "SALES_REVIEW", order: 80,
			title: "Продажи", description: "Предложения продать доступ, подписку или аккаунт.",
			matchers: policyMatchers(
				[]string{"продам", "продаю", "продаем", "предлагаю купить", "купите у меня", "купите у нас", "покупайте у меня", "покупайте у нас", "i sell", "i am selling", "i'm selling", "we sell", "for sale"},
				[]string{"доступ", "доступы", "подписку", "подписки", "аккаунт", "аккаунты", "ключ", "ключи", "api", "токены", "инвайт", "инвайты", "кредиты", "товар", "товары", "access", "subscription", "subscriptions", "account", "accounts", "api key", "api keys", "credits", "tokens", "invite", "invites"},
			),
		}),
		policyModule(policySpec{
			id: "distribution", code: "DISTRIBUTION_REVIEW", order: 90,
			title: "Раздача и обмен доступом", description: "Раздача и обмен инвайтами, доступом и ключами.",
			matchers: policyMatchers(
				[]string{"раздаю", "раздаем", "подарю", "дарю", "делюсь", "отдам", "выдам", "рассылаю", "разошлю", "распространяю", "обменяю", "обмениваю", "i share", "i am sharing", "i give away", "giving away", "i distribute"},
				[]string{"инвайт", "инвайты", "инвайтами", "инвайтов", "приглашения", "приглашение", "доступ", "доступом", "аккаунт", "аккаунты", "ключ", "ключи", "ключами", "токены", "токенами", "подписку", "кредиты", "access", "invite", "invites", "invitations", "account", "accounts", "key", "keys", "tokens", "credits"},
			),
		}),
		policyModule(policySpec{
			id: "services", code: "SERVICES_REVIEW", order: 100,
			title: "Предложения услуг", description: "Предложения настройки, разработки и других технических услуг.",
			matchers: policyMatchers(
				[]string{"настрою", "настраиваю", "установлю", "устанавливаю", "подключу", "подключаю", "разверну", "разворачиваю", "сделаю", "разработаю", "создам", "починю", "предлагаю", "оказываю", "предлагаю услуги", "оказываю услуги", "i can configure", "i will configure", "i configure", "i can set up", "i will set up", "i build", "i develop", "i offer"},
				[]string{"сервер", "сервера", "серверы", "бот", "бота", "ботов", "api", "интеграцию", "интеграции", "сайт", "сайты", "автоматизацию", "разработку", "настройку", "услуги", "server", "servers", "bot", "bots", "website", "integration", "automation", "service", "services"},
			),
		}),
		policyModule(policySpec{
			id: "recruitment", code: "RECRUITMENT_REVIEW", order: 110,
			title: "Поиск клиентов и участников", description: "Поиск клиентов, партнёров и участников, приглашения в свой проект.",
			matchers: policyMatchers(
				[]string{"ищу", "ищем", "набираю", "набираем", "приглашаю", "приглашаем", "привлекаю", "привлекаем", "присоединяйтесь", "вступайте", "подписывайтесь", "i am looking for", "i'm looking for", "we are looking for", "i recruit", "we recruit", "join our", "subscribe to our"},
				[]string{"клиентов", "клиента", "заказчиков", "заказчика", "партнеров", "партнера", "участников", "команду", "сотрудников", "рефералов", "наш канал", "мой канал", "наш чат", "мой чат", "нашу группу", "наш проект", "customers", "clients", "partners", "members", "team", "affiliates", "channel", "community", "group", "project"},
			),
		}),
	}
}

// DefaultWeights includes all known signals. DefaultConfig deliberately retains
// the legacy map without the formerly unknown URL_SCAN_LIMIT or new policies.
func DefaultWeights() map[string]int {
	weights := map[string]int{}
	for _, spec := range Catalog() {
		for _, signal := range spec.Signals {
			weights[signal.Code] = signal.DefaultWeight
		}
	}
	return weights
}

func AllowedCodes() []string {
	codes := []string{}
	for _, spec := range Catalog() {
		for _, signal := range spec.Signals {
			codes = append(codes, signal.Code)
		}
	}
	sort.Strings(codes)
	return codes
}

func ModuleIDs() []string {
	ids := []string{}
	for _, spec := range Catalog() {
		ids = append(ids, spec.ID)
	}
	return ids
}
