package main

import (
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/session"
)

func TestParseTagArgs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
		wantSel []string
	}{
		{"role and session", []string{"--role", "worker", "fix-bug"}, "", []string{"fix-bug"}},
		{"several sessions", []string{"--task", "auth", "a", "b", "self"}, "", []string{"a", "b", "self"}},
		{"clear with none", []string{"--specialty", "none", "x"}, "", []string{"x"}},
		{"no flags", []string{"x"}, "nothing to change", nil},
		{"no session", []string{"--role", "worker"}, "no session given", nil},
		{"flag after session", []string{"--role", "worker", "x", "--task", "auth"}, "flags must come before", nil},
		{"bad role", []string{"--role", "boss", "x"}, "unknown role", nil},
		{"bad task", []string{"--task", "has space", "x"}, "invalid task", nil},
		{"specialty too wide", []string{"--specialty", "far-too-long-a-tag", "x"}, "limit is", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := parseTagArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if strings.Join(o.selectors, ",") != strings.Join(tc.wantSel, ",") {
				t.Errorf("selectors = %v, want %v", o.selectors, tc.wantSel)
			}
		})
	}
}

// A flag left out keeps that tag; "none" clears it. This is what lets an
// orchestrator retag a worker's task without wiping the specialty it set.
func TestTagFlagsKeepAbsentAndClearNone(t *testing.T) {
	base := session.Tags{Role: session.RoleWorker, Specialty: "gh", Task: "auth"}

	o, err := parseTagArgs([]string{"--task", "billing", "x"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := o.flags.apply(base)
	if got != (session.Tags{Role: session.RoleWorker, Specialty: "gh", Task: "billing"}) {
		t.Errorf("task only: %+v", got)
	}

	o, err = parseTagArgs([]string{"--role", "none", "--specialty", "none", "x"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = o.flags.apply(base)
	if got != (session.Tags{Task: "auth"}) {
		t.Errorf("clear role+specialty: %+v", got)
	}
}

func TestResolveTagTargetsSelf(t *testing.T) {
	rows := []*session.SessionRow{
		{ID: "aaaa1111-1700000000", Title: "orchestrator"},
		{ID: "bbbb2222-1700000001", Title: "worker one"},
	}
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "FLEET_INSTANCE_ID" {
				return v
			}
			return ""
		}
	}

	got, err := resolveTagTargets(rows, nil, []string{"self"}, env("aaaa1111-1700000000"))
	if err != nil || len(got) != 1 || got[0].ID != "aaaa1111-1700000000" {
		t.Fatalf("self = %v, %v", got, err)
	}

	if _, err := resolveTagTargets(rows, nil, []string{"self"}, env("")); err == nil ||
		!strings.Contains(err.Error(), "inside a fleet session") {
		t.Errorf("self without env: err = %v", err)
	}
	if _, err := resolveTagTargets(rows, nil, []string{"self"}, env("zzzz9999-1")); err == nil {
		t.Error("self naming an unknown session resolved")
	}

	// Duplicates collapse, so one session named twice is written once.
	got, err = resolveTagTargets(rows, nil, []string{"self", "aaaa1111-1700000000"}, env("aaaa1111-1700000000"))
	if err != nil || len(got) != 1 {
		t.Errorf("dedupe: %d targets, %v", len(got), err)
	}

	// One bad selector fails the whole call, before anything is written.
	if _, err := resolveTagTargets(rows, nil, []string{"worker one", "nope"}, env("")); err == nil {
		t.Error("an unknown selector among good ones resolved")
	}
}

func TestCreateCommandsTakeTagFlags(t *testing.T) {
	o, err := parseAddArgs([]string{"/tmp", "--role", "worker", "--task", "Auth", "--specialty", "gh"})
	if err != nil {
		t.Fatal(err)
	}
	if o.tags != (session.Tags{Role: session.RoleWorker, Specialty: "gh", Task: "auth"}) {
		t.Errorf("fleet add tags = %+v", o.tags)
	}
	if _, err := parseAddArgs([]string{"/tmp", "--role", "boss"}); err == nil {
		t.Error("fleet add accepted an unknown role")
	}

	w, err := parseWorktreeArgs([]string{"feat-x", "--role", "orchestrator"})
	if err != nil {
		t.Fatal(err)
	}
	if w.tags.Role != session.RoleOrchestrator {
		t.Errorf("fleet wt role = %q", w.tags.Role)
	}
	if _, err := parseWorktreeArgs([]string{"feat-x", "--no-session", "--task", "auth"}); err == nil ||
		!strings.Contains(err.Error(), "--no-session") {
		t.Errorf("--task with --no-session: err = %v", err)
	}
}

func TestStatusTagFilter(t *testing.T) {
	rows := []*session.SessionRow{
		{ID: "1", Role: "orchestrator", Task: "auth"},
		{ID: "2", Role: "worker", Task: "auth"},
		{ID: "3", Role: "worker", Task: "billing"},
		{ID: "4"},
	}
	ids := func(rs []*session.SessionRow) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		role, task, want string
	}{
		{"", "", "1,2,3,4"},
		{"worker", "", "2,3"},
		{"", "auth", "1,2"},
		{"worker", "auth", "2"},
		{"none", "", "4"},
		{"", "none", "4"},
	} {
		got := ids(tagFilter(rows, statusOpts{role: tc.role, task: tc.task}))
		if got != tc.want {
			t.Errorf("role=%q task=%q: got %s, want %s", tc.role, tc.task, got, tc.want)
		}
	}
	if _, err := parseStatusArgs([]string{"--role", "wroker"}); err == nil {
		t.Error("fleet status accepted an unknown role filter")
	}
}

func TestTeamCell(t *testing.T) {
	for _, tc := range []struct {
		in   session.Tags
		want string
	}{
		{session.Tags{}, "-"},
		{session.Tags{Role: session.RoleWorker}, "worker"},
		{session.Tags{Role: session.RoleWorker, Specialty: "gh", Task: "auth"}, "worker·gh @auth"},
		{session.Tags{Task: "auth"}, "@auth"},
		{session.Tags{Specialty: "gh"}, "gh"},
	} {
		if got := teamCell(tc.in); got != tc.want {
			t.Errorf("teamCell(%+v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
