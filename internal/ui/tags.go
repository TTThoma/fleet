package ui

import (
	"sort"

	tea "charm.land/bubbletea/v2"
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
	target := h.targetForCursor()
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
	h.rebuildFlatItems()
	if idx := target.find(h.flatItems); idx >= 0 {
		h.cursor = idx
	}
	h.syncViewport()
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
	target := h.targetForCursor()
	h.rebuildFlatItems()
	if idx := target.find(h.flatItems); idx >= 0 {
		h.cursor = idx
	}
	h.syncViewport()
	if t.IsZero() {
		h.setInfo("Tags cleared")
	} else {
		h.setInfo("Tagged " + teamLabel(t))
	}
}
