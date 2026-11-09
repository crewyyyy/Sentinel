// Package moderation interprets evidence, never textual claims or admin labels, as platform events.
package moderation

import (
	"sentinel/internal/domain"
	"strconv"
)

func FromMembership(m domain.Membership, updateID int64) *domain.ModerationEvent {
	if m.Self || m.ActorID == 0 || m.New.UserID == 0 || m.Old.UserID != m.New.UserID {
		return nil
	}
	action := ""
	switch {
	case m.New.Status == "kicked" && m.Old.Status != "kicked":
		action = "ban"
	case m.Old.Status == "restricted" && m.New.Status == "member":
		action = "unrestrict"
	case m.New.Status == "restricted" && (m.Old.Status != "restricted" || m.Old.CanSendMessages != m.New.CanSendMessages || m.Old.Permissions != m.New.Permissions || m.Old.UntilDate != m.New.UntilDate || m.Old.IsMember != m.New.IsMember):
		action = "restrict"
	}
	// member -> left also occurs for voluntary departures; it is not proof of a kick.
	if action == "" {
		return nil
	}
	actor := m.ActorID
	return &domain.ModerationEvent{Source: "platform_chat_member", SourceEventID: strconv.FormatInt(updateID, 10), ChatID: m.ChatID, TargetUserID: m.New.UserID, ActorUserID: &actor, Action: action, OccurredAt: m.Date, EvidenceLevel: "verified", SendingRestricted: action == "restrict" && m.New.IsMember && !m.New.CanSendMessages, UntilDate: m.New.UntilDate}
}
