package ui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

func tagsHome(t *testing.T, sessions ...*session.Session) *Home {
	t.Helper()
	storage, err := session.Open(filepath.Join(t.TempDir(), "tags.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40
	for _, s := range sessions {
		if err := storage.SaveSession(s.ToRow()); err != nil {
			t.Fatalf("save: %v", err)
		}
		h.flatItems = append(h.flatItems, sessionRow(s))
	}
	h.sessions = sessions
	h.rebuildSessionMap()
	return h
}

var (
	workerTags = session.Tags{Role: session.RoleWorker, Specialty: "gh", Task: "auth"}
	orchTags   = session.Tags{Role: session.RoleOrchestrator, Task: "auth"}
)

func TestRetaggedElsewhereDiffsAgainstTheSnapshot(t *testing.T) {
	a := session.NewSession("alpha", "/tmp/tg")
	b := session.NewSession("beta", "/tmp/tg")
	mem := snapshotTags([]*session.Session{a, b})
	rows := []*session.SessionRow{
		{ID: a.ID, Role: "worker", Specialty: "gh", Task: "auth"}, // changed elsewhere
		{ID: b.ID},                        // unchanged
		{ID: "unknown-1", Role: "worker"}, // not known here: adoption's job
	}
	got := retaggedElsewhere(rows, mem)
	if len(got) != 1 || got[0].id != a.ID || got[0].to != workerTags || got[0].from != (session.Tags{}) {
		t.Fatalf("changes = %+v, want one: alpha untagged → worker", got)
	}
}

func TestExternalRetagIsAdopted(t *testing.T) {
	a := session.NewSession("alpha", "/tmp/tg")
	h := tagsHome(t, a)
	h.Update(externalRetagMsg{changes: []externalRetag{{id: a.ID, from: session.Tags{}, to: workerTags}}})
	if a.GetTags() != workerTags {
		t.Errorf("tags = %+v, want %+v", a.GetTags(), workerTags)
	}
}

// The race the snapshot order exists for: the worker snapshots memory, the user
// edits in the TUI (row and memory both updated), then the worker loads the
// row. The diff reports the edit itself as a change from the old tags; the
// handler must drop it rather than re-apply — and above all must never apply
// an OLD row over the edit.
func TestExternalRetagDropsAChangeMemoryHasMovedPast(t *testing.T) {
	a := session.NewSession("alpha", "/tmp/tg")
	h := tagsHome(t, a)

	mem := snapshotTags(h.sessions)                  // worker: memory snapshot (untagged)
	if err := h.applyTags(a, orchTags); err != nil { // user edits in the TUI
		t.Fatal(err)
	}
	rows, err := h.storage.LoadSessions() // worker: loads the edited row
	if err != nil {
		t.Fatal(err)
	}
	changes := retaggedElsewhere(rows, mem)
	if len(changes) != 1 {
		t.Fatalf("expected the edit to show up as one change, got %+v", changes)
	}
	h.Update(externalRetagMsg{changes: changes})
	if a.GetTags() != orchTags {
		t.Errorf("tags = %+v, want the user's edit %+v", a.GetTags(), orchTags)
	}

	// And a change computed from memory that has since moved is dropped too.
	h.Update(externalRetagMsg{changes: []externalRetag{{id: a.ID, from: session.Tags{}, to: workerTags}}})
	if a.GetTags() != orchTags {
		t.Errorf("a stale change overwrote the edit: %+v", a.GetTags())
	}
}

func TestApplyTagsPersists(t *testing.T) {
	a := session.NewSession("alpha", "/tmp/tg")
	h := tagsHome(t, a)
	if err := h.applyTags(a, workerTags); err != nil {
		t.Fatal(err)
	}
	rows, _ := h.storage.LoadSessions()
	if got := rowTags(rows[0]); got != workerTags {
		t.Errorf("stored = %+v, want %+v", got, workerTags)
	}
}

// The dialog names one row; a rebuild moving the cursor while it is open must
// not tag a different session.
func TestTagDialogFollowsTheTargetNotTheCursor(t *testing.T) {
	a := session.NewSession("alpha", "/tmp/tg")
	b := session.NewSession("beta", "/tmp/tg")
	h := tagsHome(t, a, b)
	h.cursor = 0
	h.handleKey(key("T"))
	if !h.tagDialog.IsVisible() {
		t.Fatal("T did not open the tag dialog")
	}
	h.cursor = 1 // an async rebuild lands the cursor on beta
	h.Update(tagsSetMsg{tags: workerTags})
	if a.GetTags() != workerTags {
		t.Errorf("alpha = %+v, want %+v", a.GetTags(), workerTags)
	}
	if !b.GetTags().IsZero() {
		t.Errorf("beta was tagged %+v; the dialog never named it", b.GetTags())
	}
}

func TestSessionMatchesFilterOnTags(t *testing.T) {
	s := session.NewSession("Fix login", "/tmp/tg")
	s.SetTags(session.Tags{Role: session.RoleWorker, Specialty: "GH", Task: "auth-rework"})
	for _, q := range []string{"login", "auth", "rework", "gh"} {
		if !sessionMatchesFilter(s, q) {
			t.Errorf("filter %q should match", q)
		}
	}
	if sessionMatchesFilter(s, "billing") {
		t.Error("filter billing matched")
	}
}

// An untagged row must render exactly as before tags existed; a tagged one
// carries its glyph and specialty and still fits its width.
func TestSessionRowRendersTags(t *testing.T) {
	s := session.NewSession("Implement the new authentication middleware", "/tmp/tg")
	untagged := renderSessionItem(SidebarItem{Session: s}, 40, false, -1)
	if strings.Contains(untagged, orchestratorGlyph) || strings.Contains(untagged, conciergeGlyph) || strings.Contains(untagged, " · ") {
		t.Errorf("untagged row carries tag marks: %q", untagged)
	}

	s.SetTags(session.Tags{Role: session.RoleOrchestrator, Specialty: "infra"})
	for _, sel := range []bool{false, true} {
		row := renderSessionItem(SidebarItem{Session: s}, 40, sel, 3)
		plain := ansi.Strip(row)
		if !strings.Contains(plain, orchestratorGlyph+" ") || !strings.Contains(plain, " · infra") {
			t.Errorf("selected=%v: row missing glyph or specialty: %q", sel, plain)
		}
		if w := ansi.StringWidth(plain); w > 40+2 {
			t.Errorf("selected=%v: row is %d wide at width 40: %q", sel, w, plain)
		}
	}

	s.SetTags(session.Tags{Role: session.RoleWorker})
	if row := ansi.Strip(renderSessionItem(SidebarItem{Session: s}, 40, false, -1)); row != ansi.Strip(untagged) {
		t.Errorf("a worker with no specialty should render like an untagged row:\n got %q\nwant %q", row, ansi.Strip(untagged))
	}
}

func TestRoleGlyphsAreOneCell(t *testing.T) {
	for _, g := range []string{orchestratorGlyph, conciergeGlyph} {
		if w := ansi.StringWidth(g); w != 1 {
			t.Errorf("%q is %d cells wide, want 1", g, w)
		}
	}
}

func TestTeamLabel(t *testing.T) {
	for _, tc := range []struct {
		in   session.Tags
		want string
	}{
		{session.Tags{}, ""},
		{workerTags, "worker·gh @auth"},
		{session.Tags{Task: "auth"}, "@auth"},
	} {
		if got := teamLabel(tc.in); got != tc.want {
			t.Errorf("teamLabel(%+v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func special(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func typeInto(d *TagDialog, s string) {
	for _, r := range s {
		d.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func openDialog(cur session.Tags, tasks ...string) *TagDialog {
	d := NewTagDialog()
	d.SetSize(120, 40)
	d.Show("Tag alpha", cur, tasks)
	return d
}

func saved(t *testing.T, d *TagDialog) (session.Tags, bool) {
	t.Helper()
	_, cmd := d.Update(special(tea.KeyEnter))
	if cmd == nil {
		return session.Tags{}, false
	}
	m, ok := cmd().(tagsSetMsg)
	if !ok {
		t.Fatalf("enter emitted %T", cmd())
	}
	return m.tags, true
}

func TestTagDialogEditsAndSaves(t *testing.T) {
	d := openDialog(session.Tags{})
	d.Update(special(tea.KeyRight)) // role: orchestrator
	d.Update(special(tea.KeyRight)) // concierge
	d.Update(special(tea.KeyRight)) // worker
	d.Update(special(tea.KeyTab))   // specialty
	typeInto(d, "gh")
	d.Update(special(tea.KeyTab)) // task
	typeInto(d, "Auth")
	got, ok := saved(t, d)
	if !ok {
		t.Fatal("enter did not save")
	}
	if got != workerTags {
		t.Errorf("saved %+v, want %+v", got, workerTags)
	}
	if d.IsVisible() {
		t.Error("dialog still open after save")
	}
}

func TestTagDialogSeedsCurrentTags(t *testing.T) {
	d := openDialog(workerTags)
	if got, _ := saved(t, d); got != workerTags {
		t.Errorf("unchanged save = %+v, want %+v", got, workerTags)
	}
}

// Refuse, don't fall back: an invalid task blocks enter and says why.
func TestTagDialogRefusesAnInvalidTask(t *testing.T) {
	d := openDialog(session.Tags{})
	d.Update(special(tea.KeyTab))
	d.Update(special(tea.KeyTab))
	typeInto(d, "has space")
	if _, ok := saved(t, d); ok {
		t.Fatal("enter saved an invalid task")
	}
	if !d.IsVisible() {
		t.Fatal("dialog closed on an invalid task")
	}
	if !strings.Contains(ansi.Strip(d.View()), "task:") {
		t.Errorf("footer doesn't name the task field:\n%s", ansi.Strip(d.View()))
	}
}

// Enter on a suggestion fills the Task field; it does not save.
func TestTagDialogSuggestionFillsTask(t *testing.T) {
	d := openDialog(session.Tags{}, "auth-rework", "billing")
	d.Update(special(tea.KeyTab))
	d.Update(special(tea.KeyTab))
	typeInto(d, "bil")
	d.Update(special(tea.KeyDown)) // onto "billing"
	if _, cmd := d.Update(special(tea.KeyEnter)); cmd != nil {
		t.Fatal("enter on a suggestion saved")
	}
	if d.task.Value() != "billing" || d.focus != tagFocusTask {
		t.Fatalf("task = %q focus = %d, want billing on the Task field", d.task.Value(), d.focus)
	}
	if got, ok := saved(t, d); !ok || got.Task != "billing" {
		t.Errorf("save = %+v, %v", got, ok)
	}
}

// Fixed size: the box never changes height as you type or move.
func TestTagDialogHeightIsStable(t *testing.T) {
	d := openDialog(session.Tags{}, "auth-rework", "billing", "checkout", "docs")
	want := strings.Count(d.View(), "\n")
	check := func(step string) {
		t.Helper()
		if got := strings.Count(d.View(), "\n"); got != want {
			t.Errorf("after %s: %d lines, want %d", step, got+1, want+1)
		}
	}
	d.Update(special(tea.KeyTab))
	typeInto(d, "far-too-long-a-specialty")
	check("an over-long specialty (error footer)")
	d.Update(special(tea.KeyTab))
	check("focusing task (suggestions shown)")
	typeInto(d, "zzz")
	check("a task matching nothing")
	d.Update(special(tea.KeyBackspace))
	d.Update(special(tea.KeyBackspace))
	d.Update(special(tea.KeyBackspace))
	d.Update(special(tea.KeyDown))
	check("a suggestion highlighted")
}

// Text the user types into a field must be typed, not read as a command key:
// `h`/`l` cycle the role only on the Role row.
func TestTagDialogLettersTypeIntoFields(t *testing.T) {
	d := openDialog(session.Tags{})
	d.Update(special(tea.KeyTab))
	typeInto(d, "hlq")
	if d.spec.Value() != "hlq" {
		t.Errorf("specialty = %q, want hlq", d.spec.Value())
	}
	if d.role() != session.RoleNone {
		t.Errorf("typing in a field cycled the role to %q", d.role())
	}
	if !d.IsVisible() {
		t.Error("q closed the dialog while typing")
	}
}

// The footer's path gives way to the chips after it, and is cut at the front so
// the checkout's own name survives.
func TestPreviewFooterFitsTheTeamLabel(t *testing.T) {
	s := session.NewSession("alpha", "/very/long/path/to/some/deeply/nested/repos/api-server")
	s.SetTags(session.Tags{Role: session.RoleOrchestrator, Specialty: "infra", Task: "auth-rework"})
	const width = 60
	got := ansi.Strip(BuildPreviewFooter(s, "", width))
	if w := ansi.StringWidth(got); w > width {
		t.Errorf("footer is %d wide, budget %d: %q", w, width, got)
	}
	if !strings.HasSuffix(got, "orchestrator·infra @auth-rework") {
		t.Errorf("team label clipped: %q", got)
	}
	if !strings.HasPrefix(got, "…") || !strings.Contains(got, "api-server") {
		t.Errorf("path should be cut at the front, keeping its tail: %q", got)
	}
}
