package watcher

import (
	"fmt"
	"strings"
	"time"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

// Report is what one tick says: what changed, which acts were confirmed, which
// are still unconfirmed, and what cap would work on next. It is text; the
// watcher has no model client.
type Report struct {
	At              time.Time
	Changed         bool
	Changes         []Change
	Confirmed       []state.Act
	Pending         []state.Act
	Recommendations []Recommendation
}

// Change is one task whose holder state, lease, or newest report moved.
type Change struct {
	Task    string
	Project string
	Reason  string
}

// Recommendation is one actable task with the command that would run.
type Recommendation struct {
	Task      board.Task
	Reason    string
	Command   string
	Agent     string
	Knowledge string
	Gap       string
	Earlier   string
}

// String renders the report as plain text. A tick with no board change and no
// pending act returns an empty string: nothing is reported.
func (r Report) String() string {
	var b strings.Builder
	if r.Changed {
		b.WriteString("changes:\n")
		for _, c := range r.Changes {
			fmt.Fprintf(&b, "  %s/%s: %s\n", c.Project, c.Task, c.Reason)
		}
		if len(r.Recommendations) == 0 {
			b.WriteString("recommendations: none\n")
		} else {
			b.WriteString("recommendations:\n")
			for _, rec := range r.Recommendations {
				fmt.Fprintf(&b, "  %s %s %s | %s", rec.Task.Task, rec.Task.Project, rec.Task.Kind, rec.Reason)
				if rec.Command != "" {
					fmt.Fprintf(&b, " | command: %s", oneLine(rec.Command))
				}
				if rec.Gap != "" {
					fmt.Fprintf(&b, " | no command: %s", rec.Gap)
				}
				if rec.Earlier != "" {
					fmt.Fprintf(&b, " | earlier: %s", rec.Earlier)
				}
				b.WriteString("\n")
			}
		}
	}
	if len(r.Confirmed) > 0 {
		b.WriteString("confirmed acts:\n")
		for _, a := range r.Confirmed {
			fmt.Fprintf(&b, "  %s %s: holder %s appeared; knowledge %s\n", a.Task, a.Project, a.Session, a.Knowledge)
		}
	}
	if len(r.Pending) > 0 {
		b.WriteString("unconfirmed acts:\n")
		for _, a := range r.Pending {
			fmt.Fprintf(&b, "  %s %s: launched %s by knowledge %s; no holder yet\n",
				a.Task, a.Project, a.LaunchedAt.UTC().Format(time.RFC3339), a.Knowledge)
		}
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
