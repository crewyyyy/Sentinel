// Package telegram owns all Bot API wire formats and outgoing read/notification calls.
package telegram

import (
	"encoding/json"
	"fmt"
	"sentinel/internal/domain"
	"time"
)

func AllowedUpdates() []string {
	return []string{"message", "edited_message", "chat_member", "my_chat_member", "callback_query"}
}

// Wire types mirror the explicitly consumed official Bot API fields. Entity offsets remain UTF-16.
type user struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"is_bot"`
}
type chat struct {
	Title    string `json:"title"`
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Username string `json:"username"`
}
type entity struct {
	Type          string `json:"type"`
	Offset        int    `json:"offset"`
	Length        int    `json:"length"`
	URL           string `json:"url"`
	User          *user  `json:"user"`
	CustomEmojiID string `json:"custom_emoji_id"`
}
type message struct {
	ID              int64           `json:"message_id"`
	ThreadID        int64           `json:"message_thread_id"`
	From            *user           `json:"from"`
	SenderChat      *chat           `json:"sender_chat"`
	Chat            chat            `json:"chat"`
	Date            int64           `json:"date"`
	EditDate        int64           `json:"edit_date"`
	Text            string          `json:"text"`
	Caption         string          `json:"caption"`
	Entities        []entity        `json:"entities"`
	CaptionEntities []entity        `json:"caption_entities"`
	Reply           *message        `json:"reply_to_message"`
	Forward         json.RawMessage `json:"forward_origin"`
	Quote           *struct {
		Text     string   `json:"text"`
		Entities []entity `json:"entities"`
	} `json:"quote"`
	Photo     json.RawMessage `json:"photo"`
	Sticker   json.RawMessage `json:"sticker"`
	Document  json.RawMessage `json:"document"`
	Video     json.RawMessage `json:"video"`
	Voice     json.RawMessage `json:"voice"`
	Audio     json.RawMessage `json:"audio"`
	Animation json.RawMessage `json:"animation"`
}
type member struct {
	Status    string `json:"status"`
	User      user   `json:"user"`
	IsMember  bool   `json:"is_member"`
	CanSend   bool   `json:"can_send_messages"`
	UntilDate int64  `json:"until_date"`
	CanPhotos bool   `json:"can_send_photos"`
	CanVideos bool   `json:"can_send_videos"`
	CanOther  bool   `json:"can_send_other_messages"`
}
type memberUpdate struct {
	Chat chat   `json:"chat"`
	From user   `json:"from"`
	Date int64  `json:"date"`
	Old  member `json:"old_chat_member"`
	New  member `json:"new_chat_member"`
}
type callback struct {
	ID      string   `json:"id"`
	From    user     `json:"from"`
	Data    string   `json:"data"`
	Message *message `json:"message"`
}

func Decode(raw json.RawMessage) (domain.Update, error) {
	var w struct {
		ID       *int64        `json:"update_id"`
		Message  *message      `json:"message"`
		Edited   *message      `json:"edited_message"`
		Member   *memberUpdate `json:"chat_member"`
		Self     *memberUpdate `json:"my_chat_member"`
		Callback *callback     `json:"callback_query"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return domain.Update{}, fmt.Errorf("decode Telegram update: %w", err)
	}
	if w.ID == nil {
		return domain.Update{}, fmt.Errorf("missing update_id")
	}
	u := domain.Update{ID: *w.ID}
	m := w.Message
	if w.Edited != nil {
		m = w.Edited
	}
	if m != nil {
		x := normalizeMessage(m, 0)
		u.Message = &x
	}
	cm := w.Member
	if w.Self != nil {
		cm = w.Self
	}
	if cm != nil {
		u.Membership = &domain.Membership{ChatType: cm.Chat.Type, ChatTitle: cm.Chat.Title, ChatID: cm.Chat.ID, ActorID: cm.From.ID, Date: time.Unix(cm.Date, 0).UTC(), Old: normalizeMember(cm.Old), New: normalizeMember(cm.New), Self: w.Self != nil}
	}
	if c := w.Callback; c != nil {
		x := domain.Callback{ID: c.ID, UserID: c.From.ID, Data: c.Data}
		if c.Message != nil {
			x.ChatID = c.Message.Chat.ID
			x.MessageID = c.Message.ID
		}
		u.Callback = &x
	}
	return u, nil
}
func normalizeMessage(m *message, depth int) domain.Message {
	x := domain.Message{ChatID: m.Chat.ID, ID: m.ID, ThreadID: m.ThreadID, ChatType: m.Chat.Type, ChatUsername: m.Chat.Username, Text: m.Text, Caption: m.Caption, Date: time.Unix(m.Date, 0).UTC(), Version: 1, Partial: depth > 0, ContentType: "other", Forwarded: len(m.Forward) > 0, Quoted: m.Quote != nil}
	if m.Quote != nil {
		x.QuoteText = m.Quote.Text
	}
	if m.Forward != nil {
		var origin struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(m.Forward, &origin); err == nil {
			x.ForwardType = origin.Type
		}
	}
	if m.From != nil && m.SenderChat == nil {
		id := m.From.ID
		x.UserID = &id
		x.Username = m.From.Username
	}
	if m.SenderChat != nil {
		id := m.SenderChat.ID
		x.SenderChatID = &id
	}
	if m.EditDate > 0 {
		t := time.Unix(m.EditDate, 0).UTC()
		x.EditedAt = &t
	}
	if m.Reply != nil {
		x.ReplyID = m.Reply.ID
		if depth == 0 {
			r := normalizeMessage(m.Reply, depth+1)
			x.Reply = &r
		}
	}
	es := func(es []entity) []domain.Entity {
		out := []domain.Entity{}
		for i, e := range es {
			if i >= 100 {
				break
			}
			out = append(out, domain.Entity{Type: e.Type, Offset: e.Offset, Length: e.Length, URL: e.URL, Bot: e.User != nil && e.User.Bot})
		}
		return out
	}
	x.Entities = es(m.Entities)
	x.CaptionEntities = es(m.CaptionEntities)
	if m.Quote != nil {
		x.QuoteEntities = es(m.Quote.Entities)
	}
	switch {
	case x.Text != "":
		x.ContentType = "text"
	case len(m.Photo) > 0:
		x.ContentType = "photo"
	case len(m.Sticker) > 0:
		x.ContentType = "sticker"
	case len(m.Video) > 0:
		x.ContentType = "video"
	case len(m.Document) > 0:
		x.ContentType = "document"
	case len(m.Voice) > 0:
		x.ContentType = "voice"
	case len(m.Audio) > 0:
		x.ContentType = "audio"
	case len(m.Animation) > 0:
		x.ContentType = "animation"
	}
	if depth == 0 {
		x.Media, x.MediaPartial = extractMedia(m)
	}
	return x
}
func normalizeMember(m member) domain.MemberState {
	return domain.MemberState{Status: m.Status, UserID: m.User.ID, IsMember: m.IsMember, CanSendMessages: m.CanSend, UntilDate: m.UntilDate, Permissions: fmt.Sprintf("photos=%t videos=%t other=%t", m.CanPhotos, m.CanVideos, m.CanOther)}
}
