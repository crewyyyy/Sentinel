// Package domain defines network- and storage-independent evidence records.
package domain

import "time"

type Entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
	Bot    bool   `json:"bot,omitempty"`
}

// CurrentFeaturesVersion identifies canonical extracted evidence, not the policy used to score it.
const CurrentFeaturesVersion = 2

type Mention struct {
	Username  string `json:"username"`
	Source    string `json:"source"`
	BotStatus string `json:"bot_status"` // confirmed, heuristic, or unverified
	Offset    int    `json:"offset"`     // UTF-16 offset in the original source, never the redacted text
	Length    int    `json:"length"`
}

type Message struct {
	ChatID          int64      `json:"chat_id"`
	ID              int64      `json:"message_id"`
	ThreadID        int64      `json:"message_thread_id,omitempty"`
	UserID          *int64     `json:"user_id,omitempty"`
	SenderChatID    *int64     `json:"sender_chat_id,omitempty"`
	ChatType        string     `json:"chat_type"`
	ChatUsername    string     `json:"chat_username,omitempty"`
	Username        string     `json:"username,omitempty"`
	URLs            []URL      `json:"extracted_urls,omitempty"`
	URLsPartial     bool       `json:"urls_partial,omitempty"`
	Mentions        []Mention  `json:"mentions,omitempty"`
	FeaturesVersion int        `json:"features_version,omitempty"`
	FeaturesPartial bool       `json:"features_partial,omitempty"`
	Text            string     `json:"text"`
	Caption         string     `json:"caption,omitempty"`
	Entities        []Entity   `json:"entities,omitempty"`
	CaptionEntities []Entity   `json:"caption_entities,omitempty"`
	ReplyID         int64      `json:"reply_to_message_id,omitempty"`
	Reply           *Message   `json:"reply,omitempty"`
	Forwarded       bool       `json:"forwarded"`
	Quoted          bool       `json:"quoted"`
	QuoteText       string     `json:"quote_text,omitempty"`
	QuoteEntities   []Entity   `json:"quote_entities,omitempty"`
	ForwardType     string     `json:"forward_type,omitempty"`
	ContentType     string     `json:"content_type"`
	Media           []Media    `json:"media,omitempty"`
	MediaPartial    bool       `json:"media_partial,omitempty"`
	ContentHash     string     `json:"content_hash,omitempty"`
	Date            time.Time  `json:"date"`
	EditedAt        *time.Time `json:"edited_at,omitempty"`
	Version         int        `json:"edit_version"`
	Partial         bool       `json:"partial"`
	RecentSimilar   int        `json:"recent_similar"`
	RecentLinks     int        `json:"recent_links"`
}

func (m Message) Content() string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}

type URL struct {
	QueryKeys        []string `json:"query_keys,omitempty"`
	QueryKeysPartial bool     `json:"query_keys_partial,omitempty"`
	TelegramUsername string   `json:"telegram_username,omitempty"`
	Value            string   `json:"url"`
	Host             string   `json:"host"`
	Domain           string   `json:"domain"`
	Source           string   `json:"source"`
	Unknown          bool     `json:"unknown"`
	Watch            bool     `json:"watch"`
	Denied           bool     `json:"denied"`
	Referral         bool     `json:"referral"`
	Tracking         bool     `json:"tracking"`
	Shortener        bool     `json:"shortener"`
	Telegram         bool     `json:"telegram"`
	Bot              bool     `json:"bot"`
	BotHeuristic     bool     `json:"bot_heuristic"`
	Obfuscated       bool     `json:"obfuscated"`
	IDN              bool     `json:"idn"`
}
type Signal struct {
	Code           string  `json:"code"`
	Weight         int     `json:"weight"`
	Evidence       string  `json:"evidence"`
	Confidence     float64 `json:"confidence"`
	MandatoryAI    bool    `json:"mandatory_ai"`
	RulesetVersion string  `json:"ruleset_version"`
}
type PolicyCheck struct {
	Module string `json:"module"`
	Mode   string `json:"mode"` // review or prohibited by chat policy; not a finding of guilt
}

type Assessment struct {
	PolicyChecks   []PolicyCheck `json:"policy_checks,omitempty"`
	Band           string        `json:"risk_band"`
	Score          int           `json:"risk_score"`
	Trust          int           `json:"trust_at_detection"`
	Mandatory      bool          `json:"mandatory"`
	ReviewRequired bool          `json:"ai_review_required"`
	Signals        []Signal      `json:"signals"`
	URLs           []URL         `json:"normalized_urls"`
	Version        string        `json:"ruleset_version"`
}

func (a Assessment) ProhibitedPolicyMatch() bool {
	for _, check := range a.PolicyChecks {
		if check.Mode == "prohibited" {
			return true
		}
	}
	return false
}

type ReviewRequest struct {
	Model         string     `json:"model,omitempty"`
	JobID         int64      `json:"job_id"`
	Message       Message    `json:"message"`
	Assessment    Assessment `json:"assessment"`
	Context       []Message  `json:"context"`
	PromptVersion string     `json:"prompt_version"`
}
type ReviewResult struct {
	Category         string   `json:"category"`
	Severity         string   `json:"severity"`
	Confidence       float64  `json:"confidence"`
	Recommendation   string   `json:"recommendation"`
	ReasonCodes      []string `json:"reason_codes"`
	ExplanationRU    string   `json:"explanation_ru"`
	NeedsHumanReview bool     `json:"needs_human_review"`
}
type Usage struct {
	InputTokens   *int64   `json:"input_tokens,omitempty"`
	OutputTokens  *int64   `json:"output_tokens,omitempty"`
	EstimatedCost *float64 `json:"estimated_cost,omitempty"`
}
type ReviewResponse struct {
	Result   ReviewResult `json:"result"`
	Usage    Usage        `json:"usage"`
	Provider string       `json:"provider"`
	Model    string       `json:"model"`
}

type MemberState struct {
	Status          string `json:"status"`
	UserID          int64  `json:"user_id"`
	IsMember        bool   `json:"is_member"`
	CanSendMessages bool   `json:"can_send_messages"`
	UntilDate       int64  `json:"until_date"`
	Permissions     string `json:"permissions,omitempty"`
}
type Membership struct {
	ChatType  string      `json:"chat_type,omitempty"`
	ChatTitle string      `json:"chat_title,omitempty"`
	ChatID    int64       `json:"chat_id"`
	ActorID   int64       `json:"actor_id"`
	Date      time.Time   `json:"date"`
	Old       MemberState `json:"old"`
	New       MemberState `json:"new"`
	Self      bool        `json:"self"`
}
type ModerationEvent struct {
	Source            string    `json:"source"`
	SourceEventID     string    `json:"source_event_id"`
	ChatID            int64     `json:"chat_id"`
	TargetUserID      int64     `json:"target_user_id"`
	ActorUserID       *int64    `json:"actor_user_id,omitempty"`
	Action            string    `json:"action"`
	OccurredAt        time.Time `json:"occurred_at"`
	EvidenceLevel     string    `json:"evidence_level"`
	RelatedMessageID  *int64    `json:"related_message_id,omitempty"`
	SendingRestricted bool      `json:"sending_restricted"`
	UntilDate         int64     `json:"until_date"`
}
type Callback struct {
	ID        string `json:"id"`
	UserID    int64  `json:"user_id"`
	ChatID    int64  `json:"chat_id"`
	MessageID int64  `json:"message_id"`
	Data      string `json:"data"`
}
type Update struct {
	ID         int64       `json:"update_id"`
	Message    *Message    `json:"message,omitempty"`
	Membership *Membership `json:"membership,omitempty"`
	Callback   *Callback   `json:"callback,omitempty"`
}
