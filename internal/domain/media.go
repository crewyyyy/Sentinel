package domain

// Media contains Telegram references, never a public download URL or file bytes.
type Media struct {
	Kind        string `json:"kind"`
	FileID      string `json:"file_id,omitempty"`
	UniqueID    string `json:"unique_id,omitempty"`
	EmojiID     string `json:"emoji_id,omitempty"`
	ThumbnailID string `json:"thumbnail_id,omitempty"`
	Format      string `json:"format,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Duration    int    `json:"duration,omitempty"`
}

const MaxMessageMedia = 8

type MediaReview struct {
	Probability float64 `json:"nsfw_probability"`
	Verdict     string  `json:"verdict,omitempty"`
	Frames      int     `json:"frames"`
	Preview     bool    `json:"preview,omitempty"`
	Partial     bool    `json:"partial,omitempty"`
	Unavailable bool    `json:"unavailable,omitempty"`
	Model       string  `json:"model"`
}
