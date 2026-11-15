package state

import (
	"strings"
	"testing"
	"time"
)

func TestClaimCheck(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	live := &Claim{Session: "a", At: now.Add(-time.Second), Expires: now.Add(30 * time.Second)}
	stale := &Claim{Session: "a", At: now.Add(-2 * time.Minute), Expires: now.Add(-time.Minute)}
	cases := []struct {
		name    string
		cur     *Claim
		session string
		want    string
	}{
		{"no session", live, "", "no cap session"},
		{"unclaimed", nil, "b", "cap is unclaimed"},
		{"held by another", live, "b", "held by session a until 2026-11-15T10:00:30Z"},
		{"stale to another", stale, "b", "stale, last held by session a"},
		{"own claim lapsed", stale, "a", "stale, last held by session a"},
		{"own claim live", live, "a", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClaimCheck(c.cur, c.session, now)
			if c.want == "" {
				if got != "" {
					t.Fatalf("ClaimCheck = %q, want allowed", got)
				}
				return
			}
			if !strings.Contains(got, c.want) {
				t.Fatalf("ClaimCheck = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDecideClaim(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	live := &Claim{Session: "a", Expires: now.Add(30 * time.Second)}
	stale := &Claim{Session: "a", Expires: now.Add(-time.Minute)}
	cases := []struct {
		name     string
		cur      *Claim
		session  string
		confirm  bool
		allowed  bool
		contains string
	}{
		{"no session", nil, "", true, false, "no cap session"},
		{"unclaimed without confirm", nil, "a", false, false, "unclaimed"},
		{"unclaimed with confirm", nil, "a", true, true, ""},
		{"own live claim renews", live, "a", false, true, ""},
		{"own stale claim without confirm", stale, "a", false, false, "stale, last held by session a"},
		{"own stale claim with confirm", stale, "a", true, true, ""},
		{"live holder, confirm changes nothing", live, "b", true, false, "held by session a"},
		{"stale holder without confirm", stale, "b", false, false, "stale, last held by session a"},
		{"stale holder with confirm", stale, "b", true, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			allowed, reason := DecideClaim(c.cur, c.session, now, c.confirm)
			if allowed != c.allowed {
				t.Fatalf("allowed = %v, want %v (reason %q)", allowed, c.allowed, reason)
			}
			if c.contains == "" && reason != "" {
				t.Fatalf("reason = %q, want none", reason)
			}
			if c.contains != "" && !strings.Contains(reason, c.contains) {
				t.Fatalf("reason = %q, want %q", reason, c.contains)
			}
		})
	}
}

func TestOpenClaimIgnoresClosedRows(t *testing.T) {
	now := time.Now()
	text, err := Encode(Claim{Session: "a", At: now, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	rows := []Row{{ID: 1, Kind: KindClaim, Text: text, Closed: true}}
	cur, err := OpenClaim(rows)
	if err != nil {
		t.Fatal(err)
	}
	if cur != nil {
		t.Fatalf("claim = %+v, want nil behind the closed row", cur)
	}
}
