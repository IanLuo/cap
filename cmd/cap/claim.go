package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/IanLuo/cap/internal/state"
)

// claimRows bounds the listing that finds the open claim. The claim is the
// only kind in cap:cap besides the cursor; a thousand rows is history.
const claimRows = 1000

// claimSession is the caller's identity: --session, else $CAP_SESSION. cap
// records it; it does not prove it.
func (o options) claimSession() string {
	if o.session != "" {
		return o.session
	}
	return os.Getenv("CAP_SESSION")
}

// takeClaim is the gate on the write verbs. It refuses unless session holds a
// live claim, and a holder's write renews the lease. The refusal is written to
// stderr by the caller; nothing else happens.
func takeClaim(store *state.MemStore, session string, now time.Time) error {
	cur, err := openClaim(store)
	if err != nil {
		return err
	}
	if reason := state.ClaimCheck(cur, session, now); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	// A renewal keeps the pane the holder gave: the address is set once, when
	// the holder names it, and no later verb erases it.
	pane := ""
	if cur != nil && cur.Session == session {
		pane = cur.Pane
	}
	return writeClaim(store, cur, session, pane, now)
}

func openClaim(store *state.MemStore) (*state.Claim, error) {
	rows, err := store.Recent(state.CursorScope, claimRows)
	if err != nil {
		return nil, fmt.Errorf("read cap.claim: %w", err)
	}
	return state.OpenClaim(rows)
}

// writeClaim closes the old claim and writes the new one. Records are
// immutable: the renewal is a new row, never an edit.
func writeClaim(store *state.MemStore, cur *state.Claim, session, pane string, now time.Time) error {
	if cur != nil {
		if err := store.Close(cur.ID); err != nil {
			return fmt.Errorf("close cap.claim %d: %w", cur.ID, err)
		}
	}
	text, err := state.Encode(state.Claim{Session: session, Pane: pane, At: now, Expires: now.Add(state.ClaimLease)})
	if err != nil {
		return err
	}
	if _, err := store.Put(state.KindClaim, state.CursorScope, "claim "+session, text); err != nil {
		return fmt.Errorf("write cap.claim: %w", err)
	}
	return nil
}

// claimPane names the holder's address: --pane first, then the pane the
// harness started this caller in ($HERDR_PANE_ID), then the pane the same
// session's open claim already records. cap reads the harness's variable so a
// caller never has to ask herdr for its own id.
func claimPane(o options, cur *state.Claim, session string) string {
	if o.pane != "" {
		return o.pane
	}
	if p := os.Getenv("HERDR_PANE_ID"); p != "" {
		return p
	}
	if cur != nil && cur.Session == session {
		return cur.Pane
	}
	return ""
}

// claimCmd takes or renews the claim. An unclaimed or stale cap needs
// --confirm: the claim is a takeover, and the user's yes is what makes it one.
func claimCmd(o options, stdout, stderr io.Writer) int {
	store, err := state.Open("")
	if err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	defer store.CloseDB()

	now := time.Now()
	session := o.claimSession()
	cur, err := openClaim(store)
	if err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	allowed, reason := state.DecideClaim(cur, session, now, o.confirm)
	if !allowed {
		fmt.Fprintf(stderr, "cap: %s\n", reason)
		return 1
	}
	// The address: --pane, else the harness's pane id, else the holder's own.
	pane := claimPane(o, cur, session)
	if err := writeClaim(store, cur, session, pane, now); err != nil {
		fmt.Fprintf(stderr, "cap: %v\n", err)
		return 1
	}
	expires := now.Add(state.ClaimLease)
	if o.json {
		return emitJSON(stdout, stderr, claimJSON{Session: session, Pane: pane, Expires: expires.UTC().Format(time.RFC3339), Held: true})
	}
	if pane != "" {
		fmt.Fprintf(stdout, "cap claimed by %s in pane %s until %s\n", session, pane, expires.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintf(stdout, "cap claimed by %s until %s\n", session, expires.UTC().Format(time.RFC3339))
	}
	return 0
}
