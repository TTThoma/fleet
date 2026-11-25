package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/workspace"
	"github.com/charmbracelet/x/ansi"
)

func showCreateTagged(t *testing.T, tasks []string, fromTask string) *SessionCreateDialog {
	t.Helper()
	d := NewSessionCreateDialog()
	d.SetSize(100, 40)
	d.ShowTagged("/repo/api", "api", agent.Claude, nil, tasks, fromTask)
	return d
}

// `A` from a plain row: both team rows start on none, and ⏎ creates an
// untagged session — exactly the long-standing behaviour.
func TestCreateDialogDefaultsUntagged(t *testing.T) {
	d := showCreateTagged(t, []string{"auth"}, "")
	msg := created(t, tap(&d, "enter"))
	if !msg.tags.IsZero() || msg.askTask {
		t.Errorf("tags = %+v askTask = %v, want untagged", msg.tags, msg.askTask)
	}
}

// `A` from a row on a task: the new session joins it as a worker, and the
// rows say so before anything is created.
func TestCreateDialogJoinsTheCursorRowsTask(t *testing.T) {
	d := showCreateTagged(t, []string{"auth", "billing"}, "billing")
	view := ansi.Strip(d.View())
	if !strings.Contains(view, "worker") || !strings.Contains(view, "billing") {
		t.Errorf("the rows don't show the inherited team:\n%s", view)
	}
	msg := created(t, tap(&d, "enter"))
	if msg.tags != (session.Tags{Role: session.RoleWorker, Task: "billing"}) {
		t.Errorf("tags = %+v, want worker on billing", msg.tags)
	}
}

func TestCreateDialogCyclesRoleAndTask(t *testing.T) {
	d := showCreateTagged(t, []string{"auth"}, "")
	tap(&d, "down") // Role (no Account row)
	tap(&d, "right")
	tap(&d, "down") // Task
	tap(&d, "right")
	msg := created(t, tap(&d, "enter"))
	if msg.tags != (session.Tags{Role: session.RoleOrchestrator, Task: "auth"}) {
		t.Errorf("tags = %+v, want orchestrator on auth", msg.tags)
	}
}

// "new task…" can't be typed here (the dialog holds no text input), so ⏎
// creates and asks for the name next — and the footer says that is what ⏎ does.
func TestCreateDialogNewTaskAsksForTheName(t *testing.T) {
	d := showCreateTagged(t, []string{"auth"}, "")
	tap(&d, "down")
	tap(&d, "down")
	tap(&d, "left") // wraps to the last option: new task…
	view := ansi.Strip(d.View())
	if !strings.Contains(view, createNewTaskLabel) || !strings.Contains(view, "then name task") {
		t.Errorf("new-task state not shown in the row and footer:\n%s", view)
	}
	msg := created(t, tap(&d, "enter"))
	if !msg.askTask || msg.tags.Task != "" {
		t.Errorf("askTask = %v task = %q, want askTask with no name yet", msg.askTask, msg.tags.Task)
	}
}

// The team rows are always present, so cycling them never changes the box.
func TestCreateDialogHeightIsStableAcrossTeamRows(t *testing.T) {
	d := showCreateTagged(t, []string{"a-very-long-task-name-that-fills-the-row-x"}, "")
	want := boxHeight(d.View())
	tap(&d, "down")
	tap(&d, "down")
	for i := 0; i < 3; i++ {
		tap(&d, "right")
		if got := boxHeight(d.View()); got != want {
			t.Fatalf("cycling the Task row changed the box: %d lines, want %d", got, want)
		}
	}
}

// After "new task…" the session exists and is selected, so the tag dialog
// opens on it with the Task field focused.
func TestNewTaskOpensTheTagDialogOnTheNewSession(t *testing.T) {
	h := tagsHome(t)
	s := session.NewSession("fresh", "/tmp/tg")
	s.Role = session.RoleWorker
	h.Update(sessionCreateResultMsg{session: s, askTask: true})
	if !h.tagDialog.IsVisible() || h.tagDialog.focus != tagFocusTask {
		t.Fatalf("tag dialog visible=%v focus=%d, want open on Task", h.tagDialog.IsVisible(), h.tagDialog.focus)
	}
	h.Update(tagsSetMsg{tags: session.Tags{Role: session.RoleWorker, Task: "payments"}})
	if got := s.GetTags(); got.Task != "payments" {
		t.Errorf("new session task = %q, want payments", got.Task)
	}
	rows, _ := h.storage.LoadSessions()
	if len(rows) != 1 || rows[0].Task != "payments" || rows[0].Role != "worker" {
		t.Errorf("stored = %+v", rows)
	}
}

func TestWorktreeInheritsTaskAsWorker(t *testing.T) {
	orch := session.NewSession("orch", "/tmp/tg")
	orch.SetTags(session.Tags{Role: session.RoleOrchestrator, Specialty: "infra", Task: "auth"})
	plain := session.NewSession("plain", "/tmp/tg")
	h := tagsHome(t, orch, plain)

	h.cursor = 0
	if got := h.worktreeInheritTags(); got != (session.Tags{Role: session.RoleWorker, Task: "auth"}) {
		t.Errorf("from the orchestrator: %+v, want worker on auth with no specialty", got)
	}
	h.cursor = 1
	if got := h.worktreeInheritTags(); !got.IsZero() {
		t.Errorf("from an untagged row: %+v, want untagged", got)
	}
}

// The tags ride the dialog's own messages, and the dialog shows them.
func TestWorktreeDialogCarriesInheritedTags(t *testing.T) {
	tags := session.Tags{Role: session.RoleWorker, Task: "auth"}
	d := NewWorktreeDialog()
	d.SetSize(120, 40)
	d.SetInherit(tags)
	d.Show([]workspace.WorkspaceInfo{{Name: "wt-a", Path: "/repo-wt-a"}}, nil, nil, "/repo", "origin/master", nil, nil)
	if !strings.Contains(ansi.Strip(d.View()), "joins as worker @auth") {
		t.Errorf("dialog doesn't show the inherited team:\n%s", ansi.Strip(d.View()))
	}

	// Existing worktree: the dialog opens on New branch; tab moves to the list.
	d.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on the worktree list produced nothing")
	}
	sel, ok := cmd().(workspaceSelectedMsg)
	if !ok || sel.tags != tags {
		t.Errorf("selected msg = %#v, want tags %+v", cmd(), tags)
	}

	// New worktree.
	d.SetInherit(tags)
	d.Show(nil, nil, nil, "/repo", "origin/master", nil, nil)
	for _, r := range "feat-x" { // opens on the New branch field
		d.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd = d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a new branch produced nothing")
	}
	cr, ok := cmd().(workspaceCreateMsg)
	if !ok || cr.tags != tags {
		t.Errorf("create msg = %#v, want tags %+v", cmd(), tags)
	}

	// A plain opener sets zero, and nothing is shown.
	d.SetInherit(session.Tags{})
	d.Show(nil, nil, nil, "/repo", "origin/master", nil, nil)
	if strings.Contains(ansi.Strip(d.View()), "joins as") {
		t.Error("an untagged opener still shows a team line")
	}
}
