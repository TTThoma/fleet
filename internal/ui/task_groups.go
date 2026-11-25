package ui

import (
	"fmt"
	"sort"
	"strconv"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/session"
)

// The by-task sidebar view's header actions. BuildFlatItems draws the groups
// (sidebar.go); this file is what `⏎ d w z .` do on a task header, which has
// no single repo under it.

// taskMembers returns every session carrying task — from h.sessions, not the
// visible rows, so a collapsed task or a filtered-out member is still in scope.
func (h *Home) taskMembers(task string) []*session.Session {
	if task == "" {
		return nil
	}
	var out []*session.Session
	for _, s := range h.sessions {
		if s.GetTags().Task == task {
			out = append(out, s)
		}
	}
	return out
}

// taskOrigin is one origin a task's members run in, with the checkout a new
// worktree for it would be based on.
type taskOrigin struct {
	origin string // origin key
	label  string // "fleet"
	base   string // checkout to base a worktree on (originBaseRepo)
}

// taskOrigins lists the distinct origins of a task's members, sorted by label,
// for `w` on a task header.
func (h *Home) taskOrigins(task string) []taskOrigin {
	seen := make(map[string]bool)
	var out []taskOrigin
	for _, s := range h.taskMembers(task) {
		origin := h.originOf(session.GetRepoRoot(s.ProjectPath))
		if origin == "" || seen[origin] {
			continue
		}
		seen[origin] = true
		base := h.originBaseRepo(origin)
		if base == "" {
			base = session.GetRepoRoot(s.ProjectPath)
		}
		out = append(out, taskOrigin{origin: origin, label: labelForOrigin(origin), base: base})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].label != out[j].label {
			return out[i].label < out[j].label
		}
		return out[i].origin < out[j].origin
	})
	return out
}

// taskContextMenu is the `.` menu on a task header. Every id is an existing
// dispatch id; the handlers below are what make them mean "this task".
func (h *Home) taskContextMenu() (string, []ContextMenuItem) {
	item := h.flatItems[h.cursor]
	expand := "Expand"
	if item.Expanded {
		expand = "Collapse"
	}
	wt := ContextMenuItem{ID: "new_worktree", Label: "New Worktree Session", Shortcut: "w", Key: "w", Enabled: true}
	if len(h.taskOrigins(item.TaskKey)) > 1 {
		wt.Label = "New Worktree Session…"
	}
	items := []ContextMenuItem{
		{ID: "toggle_group", Label: expand, Shortcut: "⏎", Enabled: true},
		wt,
		h.snoozeMenuItem(),
		{ID: "delete_at_cursor", Label: "Delete Task's Sessions", Shortcut: "d", Key: "d", Enabled: true},
	}
	return "task: @" + item.TaskKey, items
}

// taskDeleteMsg confirms deleting a task's sessions. ids is what the dialog
// counted; the handler deletes only those still on the task when it runs.
type taskDeleteMsg struct {
	task string
	ids  []string
}

// confirmDeleteTask handles `d` on a task header: every session in the task,
// in every repo, goes through the ordinary deferred delete (so `u` restores
// each). Worktrees and folders stay. Checkbox-gated like the origin delete,
// because it reaches across repos at once.
func (h *Home) confirmDeleteTask(item SidebarItem) tea.Cmd {
	members := h.taskMembers(item.TaskKey)
	if len(members) == 0 {
		return nil
	}
	ids := make([]string, 0, len(members))
	repos := make(map[string]bool)
	running := 0
	for _, s := range members {
		ids = append(ids, s.ID)
		repos[session.GetRepoRoot(s.ProjectPath)] = true
		switch s.GetStatus() {
		case session.StatusRunning, session.StatusWaiting, session.StatusStarting:
			running++
		}
	}
	paths := make([]string, 0, len(repos))
	for r := range repos {
		paths = append(paths, r)
	}
	sort.Strings(paths)

	details := []string{
		fmt.Sprintf("%d session(s) in %d checkout(s)", len(members), len(paths)),
		"Worktrees and folders are kept",
	}
	if running > 0 {
		details = append(details, fmt.Sprintf("%d of them still running", running))
	}
	details = append(details, checkoutPathLines(paths)...)
	details = append(details, "Press u to undo each within 5s")

	// No task name in the action log: it is free text, and the log ships in bug
	// reports (the `set tags` rule).
	h.logAction("delete task", "", true)
	task := item.TaskKey
	h.confirmDialog.ShowDanger("Delete every session in this task?", "@"+task, details, func() tea.Msg {
		return taskDeleteMsg{task: task, ids: ids}
	})
	h.confirmDialog.RequireCheckbox("Yes, delete the whole task")
	return nil
}

// deferDeleteTask runs the confirmed delete. A session retagged out of the task
// while the dialog was open (`fleet tag`, the sync sweep) is spared: the dialog
// named the task, not the ids.
func (h *Home) deferDeleteTask(msg taskDeleteMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	deleted := 0
	for _, id := range msg.ids {
		s, ok := h.sessionByID[id]
		if !ok || s.GetTags().Task != msg.task {
			continue
		}
		_, cmd := h.deferDelete(sessionDeleteMsg{id: id})
		cmds = append(cmds, cmd)
		deleted++
	}
	if deleted < len(msg.ids) {
		h.setInfo(fmt.Sprintf("Deleted %d of %d — the rest left the task meanwhile", deleted, len(msg.ids)))
	}
	return h, tea.Batch(cmds...)
}

// taskRepoPickedMsg is the repo picker's answer for `w` on a multi-origin task.
type taskRepoPickedMsg struct{ repo string }

// openWorktreeAtCursor is `w`, the palette's and the `.` menu's "New Worktree
// Session". On a task header it resolves the repo from the task: one origin
// opens the dialog directly, several ask first. Either way the new session
// joins the task as a worker (worktreeInheritTags).
func (h *Home) openWorktreeAtCursor() tea.Cmd {
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		if item := h.flatItems[h.cursor]; item.IsTaskHeader {
			return h.openTaskWorktree(item)
		}
	}
	return h.openWorktreeFor(h.resolveWorktreeBaseRepo())
}

func (h *Home) openWorktreeFor(repoPath string) tea.Cmd {
	if repoPath == "" {
		h.setInfo("no repo selected")
		return nil
	}
	h.worktreeDialog.SetInherit(h.worktreeInheritTags())
	h.worktreeDialog.ShowLoading()
	return tea.Batch(h.fetchWorkspaceListForRepo(repoPath), spinnerTickCmd)
}

func (h *Home) openTaskWorktree(item SidebarItem) tea.Cmd {
	origins := h.taskOrigins(item.TaskKey)
	switch len(origins) {
	case 0:
		h.setInfo("no repo for this task")
		return nil
	case 1:
		return h.openWorktreeFor(origins[0].base)
	}
	// The picker hangs off the task header and re-finds it on pick, exactly as
	// the `.` menu does — so capture the target whether we came from `w` or
	// from the menu (whose own pick already restored the cursor to it).
	h.contextMenuTarget = h.targetForCursor()
	rows := make([]ContextMenuItem, 0, len(origins))
	for i, o := range origins {
		row := ContextMenuItem{ID: o.base, Label: o.label, Enabled: true}
		if i < 9 {
			row.Shortcut = strconv.Itoa(i + 1)
			row.Key = row.Shortcut
		}
		rows = append(rows, row)
	}
	h.contextMenu.SetAnchor(h.contextMenuAnchor())
	h.contextMenu.ShowPicker("new worktree for @"+item.TaskKey+" in", rows, func(id string) tea.Msg {
		return taskRepoPickedMsg{repo: id}
	})
	return nil
}

// handleTaskRepoPicked opens the worktree dialog for the picked repo, after
// putting the cursor back on the task header the picker was opened from (the
// inherit reads it).
func (h *Home) handleTaskRepoPicked(msg taskRepoPickedMsg) (tea.Model, tea.Cmd) {
	if !h.focusContextMenuTarget() {
		h.setInfo("That task is gone")
		return h, nil
	}
	return h, h.openWorktreeFor(msg.repo)
}
