package app

import (
	"context"
	"fmt"

	"sentinel/internal/domain"
)

// Ignore only a verified chat role, never an operator allowlist entry or a username.
func (e *Engine) ignoreAdminMessage(ctx context.Context, m domain.Message) (bool, error) {
	if !e.Config.Moderation.IgnoreAdminMessages || m.ChatID >= 0 || e.isAdminDestination(m.ChatID) {
		return false, nil
	}
	if m.SenderChatID != nil {
		// An anonymous administrator speaks as this group; another channel is not an admin.
		return *m.SenderChatID == m.ChatID && (m.ChatType == "group" || m.ChatType == "supergroup"), nil
	}
	if m.UserID == nil || *m.UserID <= 0 {
		return false, nil
	}
	admin, err := e.Admins.IsAdmin(ctx, m.ChatID, *m.UserID)
	if err != nil {
		return false, fmt.Errorf("check message author role: %w", err)
	}
	return admin, nil
}
