package telegram

import (
	"context"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"sentinel/internal/domain"
	"sentinel/internal/visual"
)

type MediaSource struct {
	Bot      *bot.Bot
	Client   *http.Client
	FileBase string // Test seam; production always uses api.telegram.org.
}

func (s *MediaSource) Fetch(ctx context.Context, m domain.Media, maxBytes int64) ([]byte, bool, int, error) {
	if m.EmojiID != "" {
		stickers, err := s.Bot.GetCustomEmojiStickers(ctx, &bot.GetCustomEmojiStickersParams{CustomEmojiIDs: []string{m.EmojiID}})
		if err != nil {
			return nil, false, 0, &visual.Error{Code: "emoji_lookup_unavailable"}
		}
		if len(stickers) != 1 || stickers[0].CustomEmojiID != m.EmojiID {
			return nil, false, 0, &visual.Error{Code: "emoji_not_found", Permanent: true}
		}
		f := stickers[0]
		m.FileID, m.Size, m.Format = f.FileID, int64(f.FileSize), "image"
		if f.IsAnimated {
			m.Format = "tgs"
		} else if f.IsVideo {
			m.Format = "video"
		}
		if f.Thumbnail != nil {
			m.ThumbnailID = f.Thumbnail.FileID
		}
		m.Duration = 3
	}
	preview := m.Format == "tgs"
	if preview {
		if m.ThumbnailID == "" {
			return nil, true, 0, &visual.Error{Code: "animated_preview_missing", Permanent: true}
		}
		m.FileID = m.ThumbnailID
		m.Size = 0
	}
	if m.FileID == "" || len(m.FileID) > 2048 {
		return nil, preview, 0, &visual.Error{Code: "media_reference_invalid", Permanent: true}
	}
	if m.Size > maxBytes {
		return nil, preview, 0, &visual.Error{Code: "media_file_limit", Permanent: true}
	}
	file, err := s.Bot.GetFile(ctx, &bot.GetFileParams{FileID: m.FileID})
	if err != nil {
		return nil, preview, 0, &visual.Error{Code: "media_download_unavailable"}
	}
	p := file.FilePath
	if p == "" || len(p) > 1024 || strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.Contains(p, "..") || strings.ContainsAny(p, "\\%?#:\r\n") {
		return nil, preview, 0, &visual.Error{Code: "media_path_invalid", Permanent: true}
	}
	if int64(file.FileSize) > maxBytes {
		return nil, preview, 0, &visual.Error{Code: "media_file_limit", Permanent: true}
	}
	base := "https://api.telegram.org"
	if s.FileBase != "" {
		base = strings.TrimRight(s.FileBase, "/")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/file/bot"+s.Bot.Token()+"/"+p, nil)
	if err != nil {
		return nil, preview, 0, &visual.Error{Code: "media_request_invalid", Permanent: true}
	}
	client := http.Client{Timeout: 20 * time.Second}
	if s.Client != nil {
		client = *s.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(request)
	if err != nil {
		return nil, preview, 0, &visual.Error{Code: "media_download_unavailable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, preview, 0, &visual.Error{Code: "media_download_unavailable"}
	}
	if resp.ContentLength > maxBytes {
		return nil, preview, 0, &visual.Error{Code: "media_file_limit", Permanent: true}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, preview, 0, &visual.Error{Code: "media_download_unavailable"}
	}
	if int64(len(data)) > maxBytes {
		return nil, preview, 0, &visual.Error{Code: "media_file_limit", Permanent: true}
	}
	return data, preview, m.Duration, nil
}
