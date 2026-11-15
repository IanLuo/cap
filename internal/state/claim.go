package state

import (
	"encoding/json"
	"fmt"
	"time"
)

// ClaimLease is how long a claim holds without a write from its session.
const ClaimLease = 120 * time.Second

// Claim is the one session allowed to run cap's write verbs, and until when.
// It lives in cap:cap and is closed and rewritten on each holder write, like
// every cap record. A claim coordinates; it does not authenticate a session id.
type Claim struct {
	ID      int64     `json:"-"`
	Session string    `json:"session"`
	At      time.Time `json:"at"`
	Expires time.Time `json:"expires"`
}

// DecodeClaim reads one cap.claim row.
func DecodeClaim(r Row) (Claim, error) {
	var c Claim
	if err := json.Unmarshal([]byte(r.Text), &c); err != nil {
		return Claim{}, fmt.Errorf("decode cap.claim %d: %w", r.ID, err)
	}
	c.ID = r.ID
	return c, nil
}

// OpenClaim returns the one open claim, or nil. Rows are newest first; an older
// open claim would be a bug, and the newest wins.
func OpenClaim(rows []Row) (*Claim, error) {
	for _, r := range rows {
		if r.Kind != KindClaim || r.Closed {
			continue
		}
		c, err := DecodeClaim(r)
		if err != nil {
			return nil, err
		}
		return &c, nil
	}
	return nil, nil
}

// Live reports whether the claim still holds at now.
func (c *Claim) Live(now time.Time) bool {
	return c != nil && now.Before(c.Expires)
}

// ClaimCheck is the gate on a write verb. It returns "" when session may write,
// else the refusal, which names the holder and its expiry.
func ClaimCheck(cur *Claim, session string, now time.Time) string {
	if session == "" {
		return "no cap session: pass --session or set CAP_SESSION"
	}
	switch {
	case cur == nil:
		return "cap is unclaimed: run cap claim --session " + session + " --confirm after the user agrees"
	case cur.Session != session && cur.Live(now):
		return fmt.Sprintf("cap is held by session %s until %s", cur.Session, cur.Expires.UTC().Format(time.RFC3339))
	case cur.Session != session:
		return fmt.Sprintf("cap is stale, last held by session %s until %s: run cap claim --session %s --confirm to take it over",
			cur.Session, cur.Expires.UTC().Format(time.RFC3339), session)
	case !cur.Live(now):
		return fmt.Sprintf("cap claim for session %s lapsed at %s: run cap claim --session %s",
			session, cur.Expires.UTC().Format(time.RFC3339), session)
	}
	return ""
}

// DecideClaim says whether a claim attempt may write, and why not. confirm is
// the user's yes for an unclaimed or stale cap: without it nothing is written,
// so a takeover is deliberate.
func DecideClaim(cur *Claim, session string, now time.Time, confirm bool) (allowed bool, reason string) {
	if session == "" {
		return false, "no cap session: pass --session or set CAP_SESSION"
	}
	switch {
	case cur == nil && !confirm:
		return false, "cap is unclaimed: claim it with --confirm after the user agrees"
	case cur != nil && cur.Session == session:
		return true, ""
	case cur.Live(now):
		return false, fmt.Sprintf("cap is held by session %s until %s", cur.Session, cur.Expires.UTC().Format(time.RFC3339))
	case !confirm:
		return false, fmt.Sprintf("cap is stale, last held by session %s until %s: claim it with --confirm to take it over",
			cur.Session, cur.Expires.UTC().Format(time.RFC3339))
	}
	return true, ""
}
