package telegram

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Embedded so deployments cannot lose the banner through a missing mount or public URL.
//
//go:embed assets/home-banner.png
var homeBanner []byte

func (s *Sender) SendBanner(ctx context.Context, chatID int64, caption string, kb *models.InlineKeyboardMarkup) (int64, error) {
	if s.emojiUnavailable.Load() {
		caption, kb = withoutCustomEmoji(caption, kb)
	}
	params := &bot.SendPhotoParams{
		ChatID: chatID, Caption: caption, ParseMode: models.ParseModeHTML,
		Photo: &models.InputFileUpload{Filename: "sentinel-banner.png", Data: bytes.NewReader(homeBanner)},
	}
	if kb != nil {
		params.ReplyMarkup = kb
	}
	m, err := s.Bot.SendPhoto(ctx, params)
	if emojiRejected(err) {
		params.Caption, kb = withoutCustomEmoji(caption, kb)
		params.ReplyMarkup = nil
		if kb != nil {
			params.ReplyMarkup = kb
		}
		// The first multipart request consumed the upload reader.
		params.Photo = &models.InputFileUpload{Filename: "sentinel-banner.png", Data: bytes.NewReader(homeBanner)}
		m, err = s.Bot.SendPhoto(ctx, params)
		if err == nil {
			s.emojiUnavailable.Store(true)
		}
	}
	if err != nil {
		return 0, err
	}
	return int64(m.ID), nil
}

func (s *Sender) EditCaption(ctx context.Context, chatID, msgID int64, caption string, kb *models.InlineKeyboardMarkup) error {
	if kb == nil {
		kb = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
	}
	if s.emojiUnavailable.Load() {
		caption, kb = withoutCustomEmoji(caption, kb)
	}
	params := &bot.EditMessageCaptionParams{ChatID: chatID, MessageID: int(msgID), Caption: caption, ParseMode: models.ParseModeHTML, ReplyMarkup: kb}
	_, err := s.Bot.EditMessageCaption(ctx, params)
	if emojiRejected(err) {
		params.Caption, params.ReplyMarkup = withoutCustomEmoji(caption, kb)
		_, err = s.Bot.EditMessageCaption(ctx, params)
		if err == nil || errors.Is(err, bot.ErrorBadRequest) && strings.Contains(err.Error(), "message is not modified") {
			s.emojiUnavailable.Store(true)
		}
	}
	return err
}
