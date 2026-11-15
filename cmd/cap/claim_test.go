package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IanLuo/cap/internal/state"
)

// seedClaim writes one claim row with the given expiry, as an earlier session
// would have left it.
func seedClaim(t *testing.T, dbPath, session string, expires time.Time) {
	t.Helper()
	m, err := state.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer m.CloseDB()
	text, err := state.Encode(state.Claim{Session: session, At: expires.Add(-state.ClaimLease), Expires: expires})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Put(state.KindClaim, state.CursorScope, "claim "+session, text); err != nil {
		t.Fatal(err)
	}
}

// claimRows reads every claim row, newest first, with its open state.
func claimRowList(t *testing.T, dbPath string) []state.Row {
	t.Helper()
	m, err := state.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := m.Recent(state.CursorScope, 1000)
	m.CloseDB()
	if err != nil {
		t.Fatal(err)
	}
	var out []state.Row
	for _, r := range rows {
		if r.Kind == state.KindClaim {
			out = append(out, r)
		}
	}
	return out
}

func TestClaimNeedsConfirmWhenUnclaimed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	t.Setenv("MEMSTORE_DB", dbPath)
	t.Setenv("CAP_SESSION", "test")

	var out, errOut bytes.Buffer
	if code := run([]string{"claim", "--session", "test"}, &out, &errOut); code != 1 {
		t.Fatalf("claim exit = %d, want 1; stderr %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "unclaimed") {
		t.Fatalf("stderr = %q, want the unclaimed refusal", errOut.String())
	}
	if n := len(claimRowList(t, dbPath)); n != 0 {
		t.Fatalf("claims = %d; a refusal writes nothing", n)
	}

	out.Reset()
	errOut.Reset()
	if code := run([]string{"claim", "--session", "test", "--confirm"}, &out, &errOut); code != 0 {
		t.Fatalf("claim --confirm exit = %d, stderr %s", code, errOut.String())
	}
	rows := claimRowList(t, dbPath)
	if len(rows) != 1 || rows[0].Closed {
		t.Fatalf("claims = %+v, want one open claim", rows)
	}
	if !strings.Contains(out.String(), "claimed by test") {
		t.Fatalf("stdout = %q, want the holder named", out.String())
	}
}

func TestClaimRefusesALiveHolderAndRenewsTheHolder(t *testing.T) {
	isolated(t)
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	t.Setenv("MEMSTORE_DB", dbPath)
	claimFor(t, "test")

	var out, errOut bytes.Buffer
	if code := run([]string{"claim", "--session", "other", "--confirm"}, &out, &errOut); code != 1 {
		t.Fatalf("claim by other exit = %d, want 1; stderr %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "held by session test") {
		t.Fatalf("stderr = %q, want the holder named", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := run([]string{"claim", "--session", "test"}, &out, &errOut); code != 0 {
		t.Fatalf("holder renew exit = %d, stderr %s", code, errOut.String())
	}
	rows := claimRowList(t, dbPath)
	open := 0
	for _, r := range rows {
		if !r.Closed {
			open++
		}
	}
	if open != 1 || len(rows) != 2 {
		t.Fatalf("claims = %+v, want one open row behind one closed row", rows)
	}
}

func TestClaimTakesAStaleClaimOnlyWithConfirm(t *testing.T) {
	isolated(t)
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	t.Setenv("MEMSTORE_DB", dbPath)
	seedClaim(t, dbPath, "old", time.Now().Add(-time.Minute))

	var out, errOut bytes.Buffer
	if code := run([]string{"claim", "--session", "new"}, &out, &errOut); code != 1 {
		t.Fatalf("stale claim exit = %d, want 1; stderr %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "stale") || !strings.Contains(errOut.String(), "old") {
		t.Fatalf("stderr = %q, want the stale holder named", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := run([]string{"claim", "--session", "new", "--confirm"}, &out, &errOut); code != 0 {
		t.Fatalf("takeover exit = %d, stderr %s", code, errOut.String())
	}
	rows := claimRowList(t, dbPath)
	if len(rows) != 2 || rows[0].Closed {
		t.Fatalf("claims = %+v, want the stale row closed behind the new one", rows)
	}
}

func TestWriteVerbsRefuseWithoutTheClaim(t *testing.T) {
	isolated(t)
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	t.Setenv("MEMSTORE_DB", dbPath)
	t.Setenv("CAP_TRACE", filepath.Join(t.TempDir(), "trace.log"))
	t.Setenv("CAP_SESSION", "")

	for _, args := range [][]string{
		{"act", "t-1"}, {"start"}, {"stop"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 1 {
			t.Fatalf("%v exit = %d, want 1", args, code)
		}
		if !strings.Contains(errOut.String(), "no cap session") {
			t.Fatalf("%v stderr = %q, want the no-session refusal", args, errOut.String())
		}
	}
}

func TestWriteVerbsRefuseAForeignHolder(t *testing.T) {
	isolated(t)
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	t.Setenv("MEMSTORE_DB", dbPath)
	t.Setenv("CAP_TRACE", filepath.Join(t.TempDir(), "trace.log"))
	claimFor(t, "test")
	t.Setenv("CAP_SESSION", "other")

	for _, args := range [][]string{
		{"act", "t-1"}, {"start"}, {"stop"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 1 {
			t.Fatalf("%v exit = %d, want 1", args, code)
		}
		if !strings.Contains(errOut.String(), "held by session test") {
			t.Fatalf("%v stderr = %q, want the holder named", args, errOut.String())
		}
	}
}

func TestWriteVerbsRefuseAStaleClaimOfTheirOwn(t *testing.T) {
	isolated(t)
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	t.Setenv("MEMSTORE_DB", dbPath)
	t.Setenv("CAP_TRACE", filepath.Join(t.TempDir(), "trace.log"))
	seedClaim(t, dbPath, "test", time.Now().Add(-time.Second))

	var out, errOut bytes.Buffer
	if code := run([]string{"stop"}, &out, &errOut); code != 1 {
		t.Fatalf("stop exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "lapsed") {
		t.Fatalf("stderr = %q, want the lapse named", errOut.String())
	}
}

func TestAHolderWriteRenewsTheClaim(t *testing.T) {
	isolated(t)
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	t.Setenv("MEMSTORE_DB", dbPath)
	seedClaim(t, dbPath, "test", time.Now().Add(30*time.Second))
	before := claimRowList(t, dbPath)

	var out, errOut bytes.Buffer
	if code := run([]string{"claim", "--session", "test"}, &out, &errOut); code != 0 {
		t.Fatalf("renew exit = %d, stderr %s", code, errOut.String())
	}
	after := claimRowList(t, dbPath)
	if len(after) != len(before)+1 {
		t.Fatalf("claims = %d, want one more than %d", len(after), len(before))
	}
	cur, err := state.OpenClaim(after)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil || cur.Session != "test" || !cur.Live(time.Now()) {
		t.Fatalf("claim = %+v, want test holding a live lease", cur)
	}
	if !cur.Expires.After(time.Now().Add(time.Minute)) {
		t.Fatalf("expires = %s, want a fresh 120 s lease", cur.Expires)
	}
}

func TestStatusNamesAStaleHolder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAP_LAUNCHCTL", fakeLaunchctl(t, filepath.Join(dir, "absent"), filepath.Join(dir, "argv.log")))
	t.Setenv("CAP_TRACE", filepath.Join(dir, "trace.log"))
	dbPath := filepath.Join(dir, "memory.db")
	t.Setenv("MEMSTORE_DB", dbPath)
	seedClaim(t, dbPath, "gone", time.Now().Add(-time.Minute))

	var out, errOut bytes.Buffer
	if code := run([]string{"status", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("status exit = %d, stderr %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"held":false`) || !strings.Contains(out.String(), `"session":"gone"`) {
		t.Fatalf("status = %s, want the stale holder named", out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := run([]string{"status"}, &out, &errOut); code != 0 {
		t.Fatalf("status exit = %d", code)
	}
	if !strings.Contains(out.String(), "cap claim: stale, last held by gone") {
		t.Fatalf("status = %q, want the stale line", out.String())
	}
}

func TestClaimJSON(t *testing.T) {
	isolated(t)
	t.Setenv("MEMSTORE_DB", filepath.Join(t.TempDir(), "memory.db"))

	var out, errOut bytes.Buffer
	if code := run([]string{"claim", "--confirm", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("claim exit = %d, stderr %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"held":true`) || !strings.Contains(out.String(), `"session":"test"`) {
		t.Fatalf("claim json = %s, want test holding", out.String())
	}
}
