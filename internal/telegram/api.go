package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func boolPtr(b bool) *bool { return &b }

type Sender struct{ Bot *bot.Bot }

func NewSender(token string) (*Sender, error) {
	b, err := bot.New(token, bot.WithSkipGetMe(), bot.WithHTTPClient(35*time.Second, &http.Client{Timeout: 40 * time.Second}))
	if err != nil {
		return nil, fmt.Errorf("initialize Telegram SDK")
	}
	return &Sender{b}, nil
}
func (s *Sender) Send(ctx context.Context, chatID int64, text string, kb *models.InlineKeyboardMarkup) (int64, error) {
	m, err := s.Bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: kb, LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: boolPtr(true)}})
	if err != nil {
		return 0, err
	}
	return int64(m.ID), nil
}
func (s *Sender) Edit(ctx context.Context, chatID, msgID int64, text string, kb *models.InlineKeyboardMarkup) error {
	_, err := s.Bot.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: chatID, MessageID: int(msgID), Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: kb, LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: boolPtr(true)}})
	return err
}
func (s *Sender) AnswerCallback(ctx context.Context, id, text string) error {
	_, err := s.Bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text})
	return err
}
func (s *Sender) IsAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	m, err := s.Bot.GetChatMember(ctx, &bot.GetChatMemberParams{ChatID: chatID, UserID: userID})
	if err != nil {
		return false, err
	}
	return m.Type == models.ChatMemberTypeOwner || m.Type == models.ChatMemberTypeAdministrator, nil
}

// PrivateDestination is a fail-closed public-username check, not proof of every group member's role.
func (s *Sender) PrivateDestination(ctx context.Context, id int64) (bool, error) {
	c, err := s.Bot.GetChat(ctx, &bot.GetChatParams{ChatID: id})
	if err != nil {
		return false, err
	}
	if c.Type == models.ChatTypePrivate {
		return true, nil
	}
	return (c.Type == models.ChatTypeGroup || c.Type == models.ChatTypeSupergroup || c.Type == models.ChatTypeChannel) && c.Username == "" && len(c.ActiveUsernames) == 0, nil
}
func (s *Sender) GetMe(ctx context.Context) (int64, string, error) {
	u, err := s.Bot.GetMe(ctx)
	if err != nil {
		return 0, "", err
	}
	return u.ID, u.Username, nil
}
func (s *Sender) WebhookURL(ctx context.Context) (string, error) {
	w, err := s.Bot.GetWebhookInfo(ctx)
	if err != nil {
		return "", err
	}
	return w.URL, nil
}
func EscapeHTML(s string) string { return html.EscapeString(s) }
func MessageLink(chatID, messageID, threadID int64) (string, error) {
	id := strconv.FormatInt(chatID, 10)
	if !strings.HasPrefix(id, "-100") || len(id) <= 4 || messageID <= 0 {
		return "", fmt.Errorf("message link unavailable: not a known supergroup ID")
	}
	internal, err := strconv.ParseInt(id[4:], 10, 64)
	if err != nil || internal <= 0 {
		return "", fmt.Errorf("invalid supergroup ID")
	}
	base := "https://t.me/c/" + id[4:] + "/"
	if threadID > 0 {
		base += strconv.FormatInt(threadID, 10) + "/"
	}
	return base + strconv.FormatInt(messageID, 10), nil
}
func RetryDelay(err error) time.Duration {
	var rate *bot.TooManyRequestsError
	if errors.As(err, &rate) {
		return time.Duration(rate.RetryAfter) * time.Second
	}
	return 10 * time.Second
}

// APIError never embeds request URLs (which contain the bot token) or response text.
type APIError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return fmt.Sprintf("Telegram API failure %d", e.Status) }

// Poller deliberately does not use SDK.Start: the caller must commit each update before acknowledging it.
type Poller struct {
	Endpoint string
	Client   *http.Client
}

func NewPoller(token string) *Poller {
	return &Poller{Endpoint: "https://api.telegram.org/bot" + token + "/getUpdates", Client: &http.Client{Timeout: 40 * time.Second}}
}
func (p *Poller) Fetch(ctx context.Context, offset int64) ([]json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{"offset": offset, "timeout": 25, "limit": 50, "allowed_updates": AllowedUpdates()})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("create Telegram poll request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Telegram polling transport failure")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil || len(data) > 4<<20 {
		return nil, fmt.Errorf("Telegram response unreadable or too large")
	}
	var r struct {
		OK         bool              `json:"ok"`
		Code       int               `json:"error_code"`
		Result     []json.RawMessage `json:"result"`
		Parameters struct {
			Retry int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err = json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("Telegram polling response invalid JSON")
	}
	if !r.OK {
		return nil, &APIError{Status: r.Code, RetryAfter: time.Duration(r.Parameters.Retry) * time.Second}
	}
	return r.Result, nil
}
