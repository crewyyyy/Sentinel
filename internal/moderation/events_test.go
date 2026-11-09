package moderation

import (
	"sentinel/internal/domain"
	"testing"
)

func TestTransitions(t *testing.T) {
	for _, tc := range []struct {
		old, new     string
		send, member bool
		action       string
		mute         bool
	}{{"member", "kicked", false, false, "ban", false}, {"kicked", "kicked", false, false, "", false}, {"member", "left", false, false, "", false}, {"member", "restricted", true, true, "restrict", false}, {"member", "restricted", false, true, "restrict", true}, {"member", "restricted", false, false, "restrict", false}, {"restricted", "member", true, true, "unrestrict", false}} {
		e := FromMembership(domain.Membership{ActorID: 111, Old: domain.MemberState{Status: tc.old, UserID: 123}, New: domain.MemberState{Status: tc.new, UserID: 123, CanSendMessages: tc.send, IsMember: tc.member}}, 1)
		if tc.action == "" {
			if e != nil {
				t.Fatal(e)
			}
		} else if e == nil || e.Action != tc.action || e.SendingRestricted != tc.mute || e.EvidenceLevel != "verified" {
			t.Fatal(e)
		}
	}
	if e := FromMembership(domain.Membership{Old: domain.MemberState{Status: "member", UserID: 123}, New: domain.MemberState{Status: "kicked", UserID: 123}}, 1); e != nil {
		t.Fatal("invented actor", e)
	}
}
