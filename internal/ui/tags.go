package ui

import (
	"path/filepath"
	"sort"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
)

// Team tags in the TUI: picking up tags another process wrote (`fleet tag`),
// and the helpers the sidebar, preview and tag dialog share. The policy for
// what a role does lives in internal/session/role.go.

// externalRetag is a known session whose stored tags differ from memory's.
type externalRetag struct {
	id string
	// from is what memory held when the worker diffed. The change is stale once
	// memory holds anything else: this TUI retagged the session since, and its
	// own write is the newer one.
	from session.Tags
	to   session.Tags
}

// externalRetagMsg carries tag changes another process made.
type externalRetagMsg struct{ changes []externalRetag }

// rowTags reads a stored row's tags the way FromRow does.
func rowTags(r *session.SessionRow) session.Tags {
	return session.Tags{Role: session.StoredRole(r.Role), Specialty: r.Specialty, Task: r.Task}
}

// snapshotTags reads memory's tags for every known session. The sync sweep
// takes it BEFORE loading rows, and the order is what makes the diff safe:
// applyTags writes SQLite and then memory, so a memory snapshot that already
// holds a TUI edit implies the row does too. Read the other way round, a row
// loaded just before an edit paired with memory read just after it would look
// like another process reverting the edit — and the handler would apply it.
func snapshotTags(known []*session.Session) map[string]session.Tags {
	out := make(map[string]session.Tags, len(known))
	for _, s := range known {
		out[s.ID] = s.GetTags()
	}
	return out
}

// retaggedElsewhere returns the sessions whose stored tags differ from the
// memory snapshot. A TUI edit landing between the snapshot and the row load
// shows up as a change too (from the old tags to the edit itself); the
// handler's `from` check drops it, since memory has moved on.
func retaggedElsewhere(rows []*session.SessionRow, mem map[string]session.Tags) []externalRetag {
	var out []externalRetag
	for _, r := range rows {
		from, ok := mem[r.ID]
		if !ok {
			continue // not known here; adoption handles it
		}
		if to := rowTags(r); to != from {
			out = append(out, externalRetag{id: r.ID, from: from, to: to})
		}
	}
	return out
}

// sendExternalRetags reports tag changes another process made. Worker goroutine.
func (h *Home) sendExternalRetags(rows []*session.SessionRow, mem map[string]session.Tags) {
	changes := retaggedElsewhere(rows, mem)
	// Same rendezvous hazard as the adopt send; the next sweep recomputes it.
	if len(changes) == 0 || h.isAttaching.Load() {
		return
	}
	h.send(externalRetagMsg{changes: changes})
}

// handleExternalRetags adopts tag changes found by the sync sweep. Arrives on a
// timer, so the cursor is re-found by identity: a retag can move a row in or
// out of an active filter, which matches task and specialty.
func (h *Home) handleExternalRetags(msg externalRetagMsg) (tea.Model, tea.Cmd) {
	changed := false
	for _, c := range msg.changes {
		s := h.sessionByID[c.id]
		if s == nil || s.GetTags() != c.from {
			continue
		}
		debuglog.Logger.Info("session retagged by another process", "id", s.ID, "role", string(c.to.Role))
		s.SetTags(c.to)
		changed = true
	}
	if !changed {
		return h, nil
	}
	// A retag can make the cursor's row vanish (a task header whose last member
	// left), so restore by identity with the same fallbacks the `g` toggle uses.
	h.rebuildKeepingCursor()
	return h, nil
}

// applyTags sets a session's tags in memory and SQLite together, in one Update
// step, so the sync sweep never mistakes this TUI's own change for another
// process's. Returns the storage error, leaving memory unchanged on failure.
func (h *Home) applyTags(s *session.Session, t session.Tags) error {
	if err := h.storage.UpdateTags(s.ID, string(t.Role), t.Specialty, t.Task); err != nil {
		return err
	}
	s.SetTags(t)
	return nil
}

// tasksInUse lists the distinct task names on known sessions, sorted, for the
// tag dialog's suggestions.
func tasksInUse(sessions []*session.Session) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range sessions {
		if t := s.GetTags().Task; t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// teamLabel renders tags compactly for the preview footer and the tag dialog:
// "worker·gh @auth-rework". "" when untagged. The same shape as `fleet status`'s
// TEAM column, so the two read alike. It avoids " · ", which separates the
// footer's chips.
func teamLabel(t session.Tags) string {
	out := string(t.Role)
	if t.Specialty != "" {
		if out != "" {
			out += "·"
		}
		out += t.Specialty
	}
	if t.Task != "" {
		if out != "" {
			out += " "
		}
		out += "@" + t.Task
	}
	return out
}

// openTagDialog opens the tag dialog on the session under the cursor. Reached
// via `T`, the palette or the `.` menu; like snooze, it re-captures the target
// and re-anchors from the cursor, since only the menu path had set them.
func (h *Home) openTagDialog() {
	s := h.selectedSession()
	if s == nil {
		h.setInfo("Select a session to tag")
		return
	}
	h.contextMenuTarget = h.targetForCursor()
	h.tagDialog.SetAnchor(h.contextMenuAnchor())
	h.tagDialog.Show("Tag "+s.Title, s.GetTags(), tasksInUse(h.sessions))
}

// setTagsSelected writes the dialog's tags to the session under the cursor.
func (h *Home) setTagsSelected(t session.Tags) {
	s := h.selectedSession()
	if s == nil {
		return
	}
	if s.GetTags() == t {
		return
	}
	// Free text never reaches the action log, which ships in bug reports.
	h.logAction("set tags", "", true)
	if err := h.applyTags(s, t); err != nil {
		h.setError("set_tags_failed", err)
		return
	}
	if t := t.Task; t != "" {
		// Retagging into a folded task must not hide the row being edited.
		h.repoExpanded[TaskExpandKey(t)] = true
	}
	h.rebuildKeepingCursor()
	if t.IsZero() {
		h.setInfo("Tags cleared")
	} else {
		h.setInfo("Tagged " + teamLabel(t))
	}
}

// openSessionCreateDialog opens the `A` dialog for repoPath, with its Task row
// defaulting to the task of the session under the cursor (and Role to worker),
// so adding a session next to a team member joins that team unless changed.
// Shared by `A` and the palette, so the two can't drift.
func (h *Home) openSessionCreateDialog(repoPath string) {
	fromTask := ""
	if s := h.selectedSession(); s != nil {
		fromTask = s.GetTags().Task
	}
	h.sessionCreateDialog.ShowTagged(repoPath, filepath.Base(repoPath), agent.Parse(h.cfg.GetDefaultAgent()),
		h.sessionCreateAccountRows(repoPath), tasksInUse(h.sessions), fromTask)
}

// worktreeInheritTags is what a new worktree session started with `w` from the
// cursor's row joins as: that session's task, as a worker. The specialty is
// deliberately not copied — an orchestrator's "infra" says nothing about what
// the worker it spawns will do. Zero when the row is not a session or has no
// task, which is the long-standing untagged behaviour.
func (h *Home) worktreeInheritTags() session.Tags {
	// On a task header (by-task view) the new worktree is that task's next
	// worker: `w` there means "add a worker to this team".
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		if item := h.flatItems[h.cursor]; item.IsTaskHeader {
			return session.Tags{Role: session.RoleWorker, Task: item.TaskKey}
		}
	}
	s := h.selectedSession()
	if s == nil {
		return session.Tags{}
	}
	task := s.GetTags().Task
	if task == "" {
		return session.Tags{}
	}
	return session.Tags{Role: session.RoleWorker, Task: task}
}

// groupByCommandName names the palette command for the view it switches to.
func (h *Home) groupByCommandName() string {
	if SidebarGroupBy == "task" {
		return "Group Sidebar by Repo"
	}
	return "Group Sidebar by Task"
}

// toggleSidebarGroupBy flips the sidebar between the repo tree and task groups
// (`g`, the palette, Settings → Layout). Persisted, like every Appearance
// setting.
func (h *Home) toggleSidebarGroupBy() {
	next := "task"
	if SidebarGroupBy == "task" {
		next = "repo"
	}
	h.cfg.SidebarGroupBy = next
	if next == "repo" {
		h.cfg.SidebarGroupBy = "" // the default, so config.json stays as it was
	}
	if err := h.cfg.Save(); err != nil {
		debuglog.Logger.Error("failed to save sidebar grouping", "err", err)
	}
	ApplyDisplayConfig(h.cfg)
	h.logAction("group sidebar", next, true)
	h.rebuildKeepingCursor()
	if next == "task" {
		if len(tasksInUse(h.sessions)) == 0 {
			h.setInfo("Grouped by task — no session has a task yet (T to tag one)")
			return
		}
		h.setInfo("Grouped by task")
		return
	}
	h.setInfo("Grouped by repo")
}

// rebuildKeepingCursor rebuilds the sidebar and puts the cursor back on the
// row it was on. A row that no longer exists — a task header after switching
// to the repo view, a checkout header hidden because every session in it now
// sits under a task — falls back to the first member of that task or the first
// session of that checkout or origin, and otherwise to a clamped index.
func (h *Home) rebuildKeepingCursor() {
	h.restoreCursorTo(h.targetForCursor())
}

// restoreCursorTo rebuilds and lands on target, with rebuildKeepingCursor's
// fallbacks.
func (h *Home) restoreCursorTo(target contextMenuTarget) {
	h.rebuildFlatItems()
	idx := target.find(h.flatItems)
	if idx < 0 {
		for i, it := range h.flatItems {
			if it.Session == nil {
				continue
			}
			if (target.taskKey != "" && it.Session.GetTags().Task == target.taskKey) ||
				(target.repoPath != "" && it.RepoPath == target.repoPath) ||
				(target.originKey != "" && it.OriginKey == target.originKey) {
				idx = i
				break
			}
		}
	}
	if idx >= 0 {
		h.cursor = idx
	} else {
		// Only when the row is gone: clampCursor steps off headers, which would
		// pull the cursor off a header it was legitimately still on.
		h.clampCursor()
	}
	h.syncViewport()
}
