package watcher

import (
	"strings"
	"testing"
)

func TestShortCommandKeepsShortTextWhole(t *testing.T) {
	if got := ShortCommand("echo  t-1"); got != "echo t-1" {
		t.Fatalf("ShortCommand = %q, want the one-line form", got)
	}
}

func TestShortCommandCutsLongText(t *testing.T) {
	long := "P=$(herdr pane split --current --direction right --cwd /tmp --no-focus | jq -r '.result.pane.pane_id'); " +
		"herdr agent start cap-t-1 --kind claude --pane \"$P\""
	got := ShortCommand(long)
	if n := len([]rune(got)); n > 80 {
		t.Fatalf("ShortCommand length = %d, want at most 80", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("ShortCommand = %q, want a visible cut", got)
	}
	if !strings.HasPrefix(long, strings.TrimSuffix(got, "…")) {
		t.Fatalf("ShortCommand = %q, want a prefix of the command", got)
	}
}

func TestReportTextNamesTheCommandShortly(t *testing.T) {
	long := strings.Repeat("herdr agent prompt cap-t-1 ", 10)
	r := Report{
		Changed: true,
		Changes: []Change{{Task: "t-1", Project: "p", Reason: "new dev task"}},
		Recommendations: []Recommendation{{
			Task:    tickTask("t-1", "p", "dev", "open"),
			Reason:  "unheld dev",
			Command: long,
		}},
	}
	text := r.String()
	if !strings.Contains(text, "command: "+strings.TrimSuffix(ShortCommand(long), "…")) {
		t.Fatalf("report does not name the command: %s", text)
	}
	if strings.Contains(text, long) {
		t.Fatalf("report pasted the whole command: %s", text)
	}
	if !strings.Contains(text, "full commands: cap tick --json") {
		t.Fatalf("report does not point at the full text: %s", text)
	}
}
