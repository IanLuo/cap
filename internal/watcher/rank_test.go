package watcher

import (
	"testing"
	"time"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

func mustTime(t *testing.T, s string) *time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return &ts
}

// reportAt is one fs report on a task: who wrote it, what it said, and when.
func reportAt(t *testing.T, role, stateName, at string) board.Observed {
	return board.Observed{Role: role, Reported: stateName, At: mustTime(t, at), Source: "fs.event"}
}

func reviewTask(t *testing.T, id, parent, verdict, at string) board.Task {
	return board.Task{
		Task: id, Project: "p", Kind: "review", State: "open",
		ParentDev: parent, Agent: reportAt(t, "review", verdict, at),
	}
}

func devFix(t *testing.T, id, stateName, reported, at string) board.Task {
	return board.Task{
		Task: id, Project: "p", Kind: "dev", State: stateName,
		Agent: reportAt(t, "dev", reported, at),
	}
}

func recommended(t *testing.T, w *Watcher, s *state.MemStore, tasks []board.Task) []string {
	t.Helper()
	rows, err := s.Recent(state.Scope("p"), 1000)
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	recs, err := w.recommend(tasks, rows, w.Now())
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Task.Task)
	}
	return out
}

// AC7: a failed review is not recommended again until its parent dev reports a
// dev-done newer than the verdict. The fix makes the review actable again.
func TestFailedReviewWaitsForAFix(t *testing.T) {
	s, _ := newStorePath(t)
	c := newClock("2026-11-11T12:00:00Z")
	w := testWatcher(&fakeBoard{}, s, c)

	cases := []struct {
		name string
		want []string
		task []board.Task
	}{
		{
			name: "fail with an older dev-done: the dev is the next act",
			task: []board.Task{
				devFix(t, "t-dev", "open", "dev-done", "2026-11-11T10:00:00Z"),
				reviewTask(t, "t-rev", "t-dev", "fail", "2026-11-11T11:00:00Z"),
			},
			want: []string{"t-dev"},
		},
		{
			name: "a newer dev-done is the fix: the review ranks first again",
			task: []board.Task{
				devFix(t, "t-dev", "open", "dev-done", "2026-11-11T11:30:00Z"),
				reviewTask(t, "t-rev", "t-dev", "fail", "2026-11-11T11:00:00Z"),
			},
			want: []string{"t-rev", "t-dev"},
		},
		{
			name: "blocked waits exactly like fail",
			task: []board.Task{
				devFix(t, "t-dev", "open", "dev-done", "2026-11-11T10:00:00Z"),
				reviewTask(t, "t-rev", "t-dev", "blocked", "2026-11-11T11:00:00Z"),
			},
			want: []string{"t-dev"},
		},
		{
			name: "pass is not a verdict to wait on",
			task: []board.Task{
				devFix(t, "t-dev", "open", "dev-done", "2026-11-11T10:00:00Z"),
				reviewTask(t, "t-rev", "t-dev", "pass", "2026-11-11T11:00:00Z"),
			},
			want: []string{"t-rev", "t-dev"},
		},
		{
			name: "no parent dev: nothing proves a fix is owed",
			task: []board.Task{
				reviewTask(t, "t-rev", "", "fail", "2026-11-11T11:00:00Z"),
			},
			want: []string{"t-rev"},
		},
		{
			name: "parent dev closed: no fix can come",
			task: []board.Task{
				devFix(t, "t-dev", "done", "dev-done", "2026-11-11T10:00:00Z"),
				reviewTask(t, "t-rev", "t-dev", "fail", "2026-11-11T11:00:00Z"),
			},
			want: nil,
		},
		{
			name: "no parent dev-done at all: still waiting",
			task: []board.Task{
				devFix(t, "t-dev", "open", "progress", "2026-11-11T10:00:00Z"),
				reviewTask(t, "t-rev", "t-dev", "fail", "2026-11-11T11:00:00Z"),
			},
			want: []string{"t-dev"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recommended(t, w, s, tc.task)
			if len(got) != len(tc.want) {
				t.Fatalf("rank = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("rank = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
