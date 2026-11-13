package telegram

import (
	"encoding/json"
	"strings"

	"sentinel/internal/domain"
)

type mediaFile struct {
	FileID    string     `json:"file_id"`
	UniqueID  string     `json:"file_unique_id"`
	Size      int64      `json:"file_size"`
	Duration  int        `json:"duration"`
	Width     int        `json:"width"`
	Height    int        `json:"height"`
	MIME      string     `json:"mime_type"`
	Animated  bool       `json:"is_animated"`
	Video     bool       `json:"is_video"`
	Thumbnail *mediaFile `json:"thumbnail"`
}

func extractMedia(m *message) ([]domain.Media, bool) {
	out := []domain.Media{}
	partial := false
	add := func(f mediaFile, kind, format string) {
		if f.FileID == "" || f.UniqueID == "" || len(f.FileID) > 2048 || len(f.UniqueID) > 256 {
			return
		}
		x := domain.Media{Kind: kind, FileID: f.FileID, UniqueID: f.UniqueID, Format: format, Size: f.Size, Duration: f.Duration}
		if f.Thumbnail != nil && len(f.Thumbnail.FileID) <= 2048 {
			x.ThumbnailID = f.Thumbnail.FileID
		}
		if len(out) < domain.MaxMessageMedia {
			out = append(out, x)
		} else {
			partial = true
		}
	}
	var photos []mediaFile
	if json.Unmarshal(m.Photo, &photos) == nil && len(photos) > 0 {
		best := photos[0]
		for _, f := range photos[1:] {
			if f.Width > 0 && f.Width <= 10000 && f.Height > 0 && f.Height <= 10000 && f.Width*f.Height > best.Width*best.Height {
				best = f
			}
		}
		add(best, "photo", "image")
	}
	var f mediaFile
	if json.Unmarshal(m.Sticker, &f) == nil {
		format := "image"
		if f.Animated {
			format = "tgs"
		} else if f.Video {
			format = "video"
		}
		add(f, "sticker", format)
	}
	f = mediaFile{}
	if json.Unmarshal(m.Animation, &f) == nil {
		add(f, "animation", "video")
	}
	f = mediaFile{}
	if json.Unmarshal(m.Document, &f) == nil && (f.MIME == "image/gif" || strings.HasPrefix(f.MIME, "image/")) && f.MIME != "image/svg+xml" {
		add(f, "document", "image")
	}
	seen := map[string]bool{}
	for _, es := range [][]entity{m.Entities, m.CaptionEntities} {
		for i, e := range es {
			if i >= 100 {
				partial = true
				break
			}
			if e.Type != "custom_emoji" || e.CustomEmojiID == "" || len(e.CustomEmojiID) > 32 || strings.Trim(e.CustomEmojiID, "0123456789") != "" || seen[e.CustomEmojiID] {
				continue
			}
			seen[e.CustomEmojiID] = true
			if len(out) < domain.MaxMessageMedia {
				out = append(out, domain.Media{Kind: "custom_emoji", EmojiID: e.CustomEmojiID})
			} else {
				partial = true
			}
		}
	}
	return out, partial
}
