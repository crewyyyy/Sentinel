package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"testing"
)

func TestRateLimitedReportsStillReviewAndCanBeOpened(t *testing.T) {
	e := testEngine(t)
	for i := int64(1); i <= 6; i++ {
		target := msg(1000+i, 456+i, "обычная ссылка https://github.com/")
		r := msg(i, 123, "/report проверьте")
		r.ReplyID = target.ID
		r.Reply = &target
		deliver(t, e, domain.Update{ID: i, Message: &r})
	}
	if n := number(t, e, `SELECT count(*) FROM incidents`); n != 5 {
		t.Fatal("report notification limit failed", n)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs WHERE message_id=1006 AND priority='mandatory'`); n != 1 {
		t.Fatal("rate limit suppressed reported review", n)
	}
	reportID := number(t, e, `SELECT id FROM user_reports WHERE target_message_id=1006`)
	// Pending AI evidence must still let an administrator open a target pruned from the journal.
	if _, err := e.Store.Pool.Exec(context.Background(), `DELETE FROM message_journal WHERE message_id=1006`); err != nil {
		t.Fatal(err)
	}
	command := msg(50, 111, fmt.Sprintf("/reports %d", testChat))
	command.ChatID = adminChat
	deliver(t, e, domain.Update{ID: 50, Message: &command})
	command = msg(51, 111, fmt.Sprintf("/reportcase %d", reportID))
	command.ChatID = adminChat
	deliver(t, e, domain.Update{ID: 51, Message: &command})
	if n := number(t, e, `SELECT count(*) FROM incidents`); n != 6 {
		t.Fatal("report queue case not opened", n)
	}
}
func TestConflictArbitrationIsExplicitAndExportable(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "Купите мой API https://unknown.example?ref=1")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	id := number(t, e, `SELECT id FROM incidents`)
	for i, label := range []string{"spam", "benign"} {
		m = msg(int64(i+2), 111, fmt.Sprintf("/feedback %d %s", id, label))
		m.ChatID = adminChat
		deliver(t, e, domain.Update{ID: int64(i + 2), Message: &m})
	}
	var export bytes.Buffer
	if err := e.Export(ctx, &export); err != nil {
		t.Fatal(err)
	}
	if export.Len() != 0 {
		t.Fatal("conflicting label was treated as ground truth")
	}
	m = msg(4, 111, fmt.Sprintf("/resolve %d benign проверена добросовестная жалоба", id))
	m.ChatID = adminChat
	deliver(t, e, domain.Update{ID: 4, Message: &m})
	if err := e.Export(ctx, &export); err != nil {
		t.Fatal(err)
	}
	if export.Len() == 0 {
		t.Fatal("arbitration did not restore export")
	}
	var raw []byte
	if err := e.Store.Pool.QueryRow(ctx, `SELECT payload FROM incidents WHERE id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var c notify.Card
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Conflict || c.Label != "benign" {
		t.Fatal(c)
	}
	if n := number(t, e, `SELECT count(*) FROM moderation_events`); n != 0 {
		t.Fatal("arbitration created sanction")
	}
}
