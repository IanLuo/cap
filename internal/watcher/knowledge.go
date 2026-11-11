package watcher

import (
	"sort"
	"strings"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

// A dispatch knowledge record is a kind=knowledge memory whose text names one
// command with markers on their own lines:
//
//	cap.dispatch: herdr start --task {task}
//	cap.agent: herdr
//	cap.task-kind: dev
//	cap.project: cap, balcony
//
// cap.task-kind and cap.project are optional; absent means the record applies
// to every kind or every project. cap.project takes one or more comma-separated
// names. Placeholders {task}, {project}, {path} and {kind} are substituted at
// launch.
// cap never edits knowledge. A missing or ambiguous set of records is a
// question, never a guess.
type knowledgeChoice struct {
	Command string
	Agent   string
	ID      int64
}

type parsedKnowledge struct {
	commands  []string
	agent     string
	taskKinds []string
	projects  []string
}

func resolve(store state.Store, task board.Task) (knowledgeChoice, []knowledgeChoice, error) {
	rows, err := store.SearchKnowledge("cap.dispatch", knowledgeLimit)
	if err != nil {
		return knowledgeChoice{}, nil, err
	}
	return choose(task, rows)
}

func choose(task board.Task, rows []state.Row) (knowledgeChoice, []knowledgeChoice, error) {
	byKey := map[string]knowledgeChoice{}
	for _, r := range rows {
		p := parseKnowledge(r)
		if len(p.taskKinds) > 0 && !containsFold(p.taskKinds, task.Kind) {
			continue
		}
		if len(p.projects) > 0 && !containsFold(p.projects, task.Project) {
			continue
		}
		for _, command := range p.commands {
			agent := p.agent
			if agent == "" {
				agent = "unspecified"
			}
			key := command + "\x00" + agent
			if prev, ok := byKey[key]; !ok || r.ID < prev.ID {
				byKey[key] = knowledgeChoice{Command: command, Agent: agent, ID: r.ID}
			}
		}
	}
	out := make([]knowledgeChoice, 0, len(byKey))
	for _, c := range byKey {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	switch len(out) {
	case 0:
		return knowledgeChoice{}, nil, nil
	case 1:
		return out[0], out, nil
	default:
		return knowledgeChoice{}, out, nil
	}
}

func parseKnowledge(r state.Row) parsedKnowledge {
	var p parsedKnowledge
	for _, line := range strings.Split(r.Title+"\n"+r.Text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "cap.dispatch:"):
			if v := markerValue(line, len("cap.dispatch:")); v != "" {
				p.commands = append(p.commands, v)
			}
		case strings.HasPrefix(lower, "cap.agent:"):
			if v := markerValue(line, len("cap.agent:")); v != "" && p.agent == "" {
				p.agent = v
			}
		case strings.HasPrefix(lower, "cap.task-kind:"):
			v := markerValue(line, len("cap.task-kind:"))
			for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
				if f != "" {
					p.taskKinds = append(p.taskKinds, f)
				}
			}
		case strings.HasPrefix(lower, "cap.project:"):
			v := markerValue(line, len("cap.project:"))
			for _, f := range strings.Split(v, ",") {
				if f = strings.TrimSpace(f); f != "" {
					p.projects = append(p.projects, f)
				}
			}
		}
	}
	return p
}

func markerValue(line string, n int) string {
	v := strings.TrimSpace(line[n:])
	if len(v) >= 2 && strings.HasPrefix(v, "`") && strings.HasSuffix(v, "`") {
		v = strings.TrimSpace(v[1 : len(v)-1])
	}
	return v
}

func containsFold(xs []string, want string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, want) {
			return true
		}
	}
	return false
}

func render(command string, t board.Task) string {
	return strings.NewReplacer(
		"{task}", t.Task,
		"{project}", t.Project,
		"{path}", t.ProjectPath,
		"{kind}", t.Kind,
	).Replace(command)
}
