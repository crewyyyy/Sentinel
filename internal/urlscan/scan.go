// Package urlscan inspects links locally; it never resolves DNS or fetches destinations.
package urlscan

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
	"sentinel/internal/domain"
)

type Scanner struct{ allow, watch, deny, bots map[string]bool }

func set(ss []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		a, err := idna.Lookup.ToASCII(strings.ToLower(strings.TrimSpace(s)))
		if err == nil {
			m[a] = true
		}
	}
	return m
}
func New(allow, watch, deny, bots []string) *Scanner {
	knownBots := make(map[string]bool, len(bots))
	for _, name := range bots {
		name = strings.ToLower(strings.TrimSpace(name))
		if ValidBotUsername(name) {
			knownBots[name] = true
		}
	}
	return &Scanner{set(allow), set(watch), set(deny), knownBots}
}

func ValidBotUsername(name string) bool {
	return username.MatchString(strings.ToLower(name))
}

var links = regexp.MustCompile(`(?i)(?:https?://|tg://resolve\?|www\.|(?:t|telegram)\.me/)[^\s<>"\x{00ab}\x{00bb}]+|(?:[\pL\pN][\pL\pN-]*\.)+[\pL]{2,}(?:/[^\s<>"\x{00ab}\x{00bb}]*)?`)
var dotted = regexp.MustCompile(`(?i)\s*(?:\[\.\]|\(dot\)|\[dot\])\s*`)
var spaced = regexp.MustCompile(`(?i)([a-z0-9])\s+\.\s*([a-z])`)
var shorteners = set([]string{"bit.ly", "tinyurl.com", "t.co", "goo.gl", "clck.ru", "is.gd", "cutt.ly"})
var username = regexp.MustCompile(`^[a-z][a-z0-9_]{4,31}$`)

const (
	maxQueryKeys = 32
	maxLinks     = 32
)

func UTF16Slice(s string, off, length int) (string, bool) {
	u := utf16.Encode([]rune(s))
	if off < 0 || length < 0 || off > len(u) || length > len(u)-off {
		return "", false
	}
	end := off + length
	if off < len(u) && off > 0 && u[off] >= 0xdc00 && u[off] <= 0xdfff {
		return "", false
	}
	if end < len(u) && end > 0 && u[end] >= 0xdc00 && u[end] <= 0xdfff {
		return "", false
	}
	return string(utf16.Decode(u[off:end])), true
}
func (s *Scanner) Scan(m domain.Message) []domain.URL {
	urls, _ := s.scan(m)
	return urls
}

func (s *Scanner) scan(m domain.Message) ([]domain.URL, bool) {
	out := []domain.URL{}
	partial := false
	seen := map[string]bool{}
	add := func(raw, source string, confirmed bool, obfuscated bool) {
		if len(out) >= maxLinks || len(raw) > 4096 {
			partial = true
			return
		}
		u, ok := s.parse(raw, source)
		if !ok {
			partial = true
			return
		}
		u.Bot = u.Bot || confirmed
		u.Obfuscated = u.Obfuscated || obfuscated
		if !seen[u.Value] {
			seen[u.Value] = true
			out = append(out, u)
		}
	}
	scan := func(text string, es []domain.Entity) {
		if len(es) > maxLinks {
			partial = true
			es = es[:maxLinks]
		}
		for _, e := range es {
			switch e.Type {
			case "text_link":
				add(e.URL, "text_link", false, false)
			case "url":
				if raw, ok := UTF16Slice(text, e.Offset, e.Length); ok {
					add(raw, "entity", false, false)
				} else {
					partial = true
				}
			case "text_mention":
				if e.Bot && !seen["confirmed_bot_entity"] && len(out) < maxLinks {
					seen["confirmed_bot_entity"] = true
					out = append(out, domain.URL{Source: "text_mention", Telegram: true, Bot: true})
				}
			case "mention":
				if raw, ok := UTF16Slice(text, e.Offset, e.Length); ok && s.bots[strings.ToLower(strings.TrimPrefix(raw, "@"))] {
					add("https://t.me/"+strings.TrimPrefix(raw, "@"), "mention", true, false)
				}
			}
		}
		clean := strings.ReplaceAll(strings.ReplaceAll(text, "​", ""), "‍", "")
		clean = dotted.ReplaceAllString(clean, ".")
		clean = spaced.ReplaceAllString(clean, "$1.$2")
		clean = strings.ReplaceAll(clean, "hxxps://", "https://")
		clean = strings.ReplaceAll(clean, "hxxp://", "http://")
		candidates := links.FindAllString(clean, maxLinks+1)
		if len(candidates) > maxLinks {
			partial = true
			candidates = candidates[:maxLinks]
		}
		for _, raw := range candidates {
			add(raw, "text", false, clean != text)
		}
	}
	scan(m.Text, m.Entities)
	scan(m.Caption, m.CaptionEntities)
	scan(m.QuoteText, m.QuoteEntities)
	return out, partial
}
func (s *Scanner) parse(raw, source string) (domain.URL, bool) {
	raw = strings.TrimRight(strings.TrimSpace(raw), ".,;!?:)]}")
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	p, err := url.Parse(raw)
	if err != nil || p.Host == "" && p.Scheme != "tg" {
		return domain.URL{}, false
	}
	userinfo := p.User != nil
	p.User = nil
	port := p.Port()
	if p.Scheme != "https" && p.Scheme != "http" && p.Scheme != "tg" {
		return domain.URL{}, false
	}
	host, err := idna.Lookup.ToASCII(strings.ToLower(p.Hostname()))
	if err != nil {
		return domain.URL{}, false
	}
	host = strings.TrimSuffix(host, ".")
	domainName, _ := publicsuffix.EffectiveTLDPlusOne(host)
	if domainName == "" {
		domainName = host
	}
	p.Host = host
	if port != "" {
		p.Host = host + ":" + port
	}
	p.Fragment = ""
	u := domain.URL{Value: p.String(), Host: host, Domain: domainName, Source: source, Obfuscated: userinfo}
	u.Telegram = host == "t.me" || host == "telegram.me" || p.Scheme == "tg"
	u.Unknown = !u.Telegram && !s.allowed(host)
	u.Watch = s.watch[host] || s.watch[domainName]
	u.Denied = s.deny[host] || s.deny[domainName]
	u.Shortener = shorteners[domainName]
	name := strings.ToLower(strings.Trim(p.Path, "/"))
	q, queryErr := url.ParseQuery(p.RawQuery)
	u.QueryKeysPartial = queryErr != nil
	if p.Scheme == "tg" {
		name = strings.ToLower(q.Get("domain"))
	}
	if u.Telegram {
		if username.MatchString(name) && name != "share" && name != "proxy" && name != "login" && name != "socks" {
			u.TelegramUsername = name
		}
		u.Bot = s.bots[name] || source == "text_mention"
		u.BotHeuristic = !u.Bot && (strings.HasSuffix(name, "bot") || q.Has("start") || q.Has("startapp"))
	}
	keySet := make(map[string]bool)
	for key := range q {
		k := strings.ToLower(key)
		if k != "" && len(k) <= 64 && utf8.ValidString(k) {
			if !keySet[k] {
				keySet[k] = true
				u.QueryKeys = append(u.QueryKeys, k)
			}
		} else {
			u.QueryKeysPartial = true
		}
		switch k {
		case "ref", "referral", "affiliate", "start", "startapp", "invite":
			u.Referral = true
		}
		if strings.HasPrefix(k, "utm_") {
			u.Tracking = true
		}
	}
	sort.Strings(u.QueryKeys)
	if len(u.QueryKeys) > maxQueryKeys {
		u.QueryKeys = u.QueryKeys[:maxQueryKeys]
		u.QueryKeysPartial = true
	}
	for allow := range s.allow {
		if host != allow && !strings.HasSuffix(host, "."+allow) && strings.Contains(host, allow) {
			u.Obfuscated = true
		}
	}
	if strings.Contains(host, "xn--") {
		u.IDN = true
		unicodeHost, err := idna.Lookup.ToUnicode(host)
		if err == nil {
			similar := strings.NewReplacer("а", "a", "о", "o", "е", "e", "с", "c", "р", "p", "х", "x", "у", "y", "і", "i", "ѕ", "s").Replace(unicodeHost)
			if similar != host && s.allowed(similar) {
				u.Obfuscated = true
			}
		}
	}
	return u, true
}
func (s *Scanner) allowed(host string) bool {
	for a := range s.allow {
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}
