package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// byTask switches the sidebar to the by-task view for one test.
func byTask(t *testing.T) {
	t.Helper()
	prev := SidebarGroupBy
	SidebarGroupBy = "task"
	t.Cleanup(func() { SidebarGroupBy = prev })
}

func tagged(title, path string, role session.Role, task string) *session.Session {
	s := session.NewSession(title, path)
	s.SetTags(session.Tags{Role: role, Task: task})
	return s
}

// render draws the sidebar as plain text lines.
func renderPlain(h *Home) []string {
	out := RenderSidebar(h.flatItems, h.sessions, nil, nil, -1, 0, 60, 60, true)
	return strings.Split(ansi.Strip(out), "\n")
}

func rowIndex(h *Home, match func(SidebarItem) bool) int {
	for i, it := range h.flatItems {
		if match(it) {
			return i
		}
	}
	return -1
}

func taskHeader(task string) func(SidebarItem) bool {
	return func(it SidebarItem) bool { return it.IsTaskHeader && it.TaskKey == task }
}

// With nothing tagged the by-task view is the repo view, byte for byte.
func TestTaskViewWithoutTagsIsTheRepoView(t *testing.T) {
	h := tagsHome(t, session.NewSession("one", "/tmp/tv-a"), session.NewSession("two", "/tmp/tv-b"))
	h.rebuildFlatItems()
	repo := RenderSidebar(h.flatItems, h.sessions, nil, nil, 1, 0, 60, 30, true)

	byTask(t)
	h.rebuildFlatItems()
	if got := RenderSidebar(h.flatItems, h.sessions, nil, nil, 1, 0, 60, 30, true); got != repo {
		t.Errorf("by-task view with no tags differs from the repo view:\n%s\n---\n%s", got, repo)
	}
}

func TestTaskViewGroupsByTaskInRoleOrder(t *testing.T) {
	byTask(t)
	w1 := tagged("worker one", "/tmp/tg-a", session.RoleWorker, "auth")
	orch := tagged("the orchestrator", "/tmp/tg-b", session.RoleOrchestrator, "auth")
	bare := tagged("no role", "/tmp/tg-a", session.RoleNone, "auth")
	conc := tagged("concierge", "/tmp/tg-a", session.RoleConcierge, "auth")
	other := tagged("billing worker", "/tmp/tg-a", session.RoleWorker, "billing")
	loose := session.NewSession("untagged", "/tmp/tg-c")
	h := tagsHome(t, w1, orch, bare, conc, other, loose)
	h.rebuildFlatItems()

	var order []string
	for _, it := range h.flatItems {
		switch {
		case it.IsTaskHeader:
			order = append(order, "@"+it.TaskKey)
		case it.IsRepoHeader:
			order = append(order, "#hdr")
		case it.Session != nil:
			order = append(order, it.Session.Title)
		}
	}
	want := []string{"@auth", "the orchestrator", "concierge", "worker one", "no role", "@billing", "billing worker", "#hdr", "#hdr", "untagged"}
	if strings.Join(order, "|") != strings.Join(want, "|") {
		t.Errorf("rows:\n got %v\nwant %v", order, want)
	}
	if i := rowIndex(h, taskHeader("auth")); h.flatItems[i].SessionCount != 4 {
		t.Errorf("@auth counts %d, want 4", h.flatItems[i].SessionCount)
	}
	// A member keeps its real checkout, which every session action reads.
	if i := rowIndex(h, func(it SidebarItem) bool { return it.Session == orch }); h.flatItems[i].RepoPath != "/tmp/tg-b" {
		t.Errorf("member RepoPath = %q", h.flatItems[i].RepoPath)
	}
}

// A checkout whose every session sits under a task is not drawn as "(empty)";
// a pinned checkout with no sessions at all still is.
func TestTaskViewHidesCheckoutsEmptiedByTasks(t *testing.T) {
	byTask(t)
	h := tagsHome(t, tagged("w", "/tmp/th-a", session.RoleWorker, "auth"))
	h.pinnedRepos = map[string]bool{"/tmp/th-a": true, "/tmp/th-empty": true}
	h.rebuildFlatItems()
	for _, it := range h.flatItems {
		if it.IsCheckoutHeader && it.RepoPath == "/tmp/th-a" {
			t.Error("checkout holding only task members drawn in the repo tree")
		}
	}
	if rowIndex(h, func(it SidebarItem) bool { return it.IsCheckoutHeader && it.RepoPath == "/tmp/th-empty" }) < 0 {
		t.Error("pinned empty checkout vanished")
	}
}

func TestTaskViewFilterAndCollapse(t *testing.T) {
	byTask(t)
	a := tagged("alpha", "/tmp/tf", session.RoleWorker, "auth")
	b := tagged("beta", "/tmp/tf", session.RoleWorker, "billing")
	h := tagsHome(t, a, b)

	h.filterText = "alpha"
	h.rebuildFlatItems()
	if rowIndex(h, taskHeader("billing")) >= 0 || rowIndex(h, taskHeader("auth")) < 0 {
		t.Error("filter should keep only the task with a matching member")
	}

	h.filterText = ""
	h.rebuildFlatItems()
	h.cursor = rowIndex(h, taskHeader("auth"))
	h.toggleRepoGroup()
	if rowIndex(h, func(it SidebarItem) bool { return it.Session == a }) >= 0 {
		t.Error("collapsed task still shows its member")
	}
	if !h.flatItems[h.cursor].IsTaskHeader || h.flatItems[h.cursor].TaskKey != "auth" {
		t.Error("cursor left the task header it collapsed")
	}
	if IsExpanded(h.repoExpanded, TaskExpandKey("auth")) {
		t.Error("collapse not recorded under the task key")
	}
}

func TestTaskSnoozeMutesInBothViews(t *testing.T) {
	a := tagged("alpha", "/tmp/ts", session.RoleWorker, "auth")
	now := time.Now()
	groups := map[string]time.Time{TaskExpandKey("auth"): now.Add(time.Hour)}
	if got := snoozeState(a, "local:ts", "/tmp/ts", groups, now); !got.Muted || got.ByTask != "auth" || got.OwnTimer {
		t.Errorf("snoozeState = %+v, want muted by task auth", got)
	}

	h := tagsHome(t, a)
	h.groupSnooze = groups
	h.rebuildFlatItems()
	if !h.snoozeMuted[a.ID] {
		t.Error("repo view: task snooze did not mute the member")
	}
	sel := rowIndex(h, func(it SidebarItem) bool { return it.Session == a })
	if row := ansi.Strip(renderSessionItem(h.flatItems[sel], 80, true, -1)); !strings.Contains(row, "@auth snoozed") {
		t.Errorf("repo view's selected row doesn't name the task holding the snooze: %q", row)
	}

	byTask(t)
	h.rebuildFlatItems()
	i := rowIndex(h, taskHeader("auth"))
	if h.flatItems[i].GroupSnooze.IsZero() {
		t.Error("task header doesn't carry its snooze")
	}
	h.cursor = i
	if sc, ok := h.snoozeScopeAtCursor(); !ok || sc.groupKey != TaskExpandKey("auth") || sc.logLabel() != "" {
		t.Errorf("task scope = %+v (log label %q), want the task key and no logged name", sc, sc.logLabel())
	}
}

// `g` keeps the cursor on its session, and a task header that vanishes lands
// on that task's first member.
func TestGroupByToggleKeepsTheCursor(t *testing.T) {
	a := tagged("alpha", "/tmp/tc", session.RoleWorker, "auth")
	b := session.NewSession("beta", "/tmp/tc")
	h := tagsHome(t, a, b)
	prev := SidebarGroupBy
	t.Cleanup(func() { SidebarGroupBy = prev })
	h.rebuildFlatItems()
	h.cursor = rowIndex(h, func(it SidebarItem) bool { return it.Session == b })

	h.toggleSidebarGroupBy()
	if SidebarGroupBy != "task" || h.cfg.GetSidebarGroupBy() != "task" {
		t.Fatal("toggle didn't switch to the task view")
	}
	if it := h.flatItems[h.cursor]; it.Session != b {
		t.Errorf("cursor moved off beta onto %+v", it)
	}

	h.cursor = rowIndex(h, taskHeader("auth"))
	h.toggleSidebarGroupBy()
	if it := h.flatItems[h.cursor]; it.Session != a {
		t.Errorf("from a vanished task header the cursor should land on its member, got %+v", it)
	}
	if h.cfg.SidebarGroupBy != "" {
		t.Errorf("back to repo should leave the default unwritten, got %q", h.cfg.SidebarGroupBy)
	}
}

func TestTaskHeadersAreTopLevelForJumps(t *testing.T) {
	byTask(t)
	h := tagsHome(t,
		tagged("a", "/tmp/tj", session.RoleWorker, "auth"),
		tagged("b", "/tmp/tj", session.RoleWorker, "billing"),
		session.NewSession("c", "/tmp/tj"))
	h.rebuildFlatItems()
	start := rowIndex(h, taskHeader("auth"))
	if got := NextOriginItem(h.flatItems, start, 1); !h.flatItems[got].IsTaskHeader || h.flatItems[got].TaskKey != "billing" {
		t.Errorf("ctrl+shift+↓ from @auth landed on %+v", h.flatItems[got])
	}
	if got := NextHeaderItem(h.flatItems, start, 1); !h.flatItems[got].IsRepoHeader {
		t.Errorf("shift+↓ landed on a non-header %+v", h.flatItems[got])
	}
	h.cursor = start
	if h.cursorBarContext() != BarContextTask {
		t.Error("task header isn't its own bar context")
	}
	if tgt := h.targetForCursor(); tgt.taskKey != "auth" || tgt.find(h.flatItems) != start {
		t.Errorf("target %+v doesn't round-trip", tgt)
	}
}

// The `.` menu on a task header offers only dispatchable ids.
func TestTaskContextMenuIDsAreDispatchable(t *testing.T) {
	byTask(t)
	h := tagsHome(t, tagged("a", "/tmp/tm", session.RoleWorker, "auth"))
	h.rebuildFlatItems()
	h.cursor = rowIndex(h, taskHeader("auth"))
	title, items := h.buildContextMenuItems()
	if title != "task: @auth" || len(items) == 0 {
		t.Fatalf("menu = %q %v", title, items)
	}
	known := dispatchCommandIDs(t)
	for _, it := range items {
		if !known[it.ID] {
			t.Errorf("menu id %q has no dispatchCommand case", it.ID)
		}
	}
}

func TestDeleteTaskCoversHiddenMembersAndSparesLeavers(t *testing.T) {
	byTask(t)
	a := tagged("alpha", "/tmp/td-a", session.RoleOrchestrator, "auth")
	b := tagged("beta", "/tmp/td-b", session.RoleWorker, "auth")
	c := tagged("gamma", "/tmp/td-a", session.RoleWorker, "billing")
	h := tagsHome(t, a, b, c)
	h.filterText = "alpha" // beta is filtered out, still in the task
	h.rebuildFlatItems()
	h.cursor = rowIndex(h, taskHeader("auth"))

	h.deleteAtCursor()
	if !h.confirmDialog.visible || !h.confirmDialog.requireConfirm {
		t.Fatal("task delete must open a checkbox-gated confirm")
	}
	if !strings.Contains(strings.Join(h.confirmDialog.details, "\n"), "2 session(s) in 2 checkout(s)") {
		t.Errorf("details = %v", h.confirmDialog.details)
	}
	msg, ok := h.confirmDialog.onYes().(taskDeleteMsg)
	if !ok || len(msg.ids) != 2 {
		t.Fatalf("confirm sends %+v", msg)
	}

	// beta leaves the task while the dialog is open.
	b.SetTags(session.Tags{Role: session.RoleWorker, Task: "other"})
	h.deferDeleteTask(msg)
	if _, gone := h.sessionByID[a.ID]; gone {
		t.Error("alpha should be deleted")
	}
	if _, kept := h.sessionByID[b.ID]; !kept {
		t.Error("beta left the task and must be spared")
	}
	if _, kept := h.sessionByID[c.ID]; !kept {
		t.Error("another task's session was deleted")
	}
}

func TestWorktreeOnATaskHeader(t *testing.T) {
	byTask(t)
	one := tagsHome(t, tagged("a", "/tmp/tw-a", session.RoleWorker, "solo"))
	one.rebuildFlatItems()
	one.cursor = rowIndex(one, taskHeader("solo"))
	if got := one.worktreeInheritTags(); got != (session.Tags{Role: session.RoleWorker, Task: "solo"}) {
		t.Errorf("inherit on a task header = %+v", got)
	}
	if one.openWorktreeAtCursor() == nil || one.contextMenu.IsVisible() {
		t.Error("a one-origin task should open the worktree dialog directly")
	}

	h := tagsHome(t,
		tagged("a", "/tmp/tw-b", session.RoleWorker, "multi"),
		tagged("b", "/tmp/tw-c", session.RoleWorker, "multi"))
	h.rebuildFlatItems()
	hdr := rowIndex(h, taskHeader("multi"))
	h.cursor = hdr
	if cmd := h.openWorktreeAtCursor(); cmd != nil || !h.contextMenu.IsVisible() {
		t.Fatal("a two-origin task should ask which repo first")
	}
	if len(h.contextMenu.items) != 2 || h.contextMenu.items[0].Key != "1" {
		t.Errorf("picker rows = %+v", h.contextMenu.items)
	}

	// Picking sends the picker's own message, never a dispatch id.
	h.cursor = 0
	_, cmd := h.contextMenu.Update(tea.KeyPressMsg{Code: '2', Text: "2"})
	picked, ok := cmd().(taskRepoPickedMsg)
	if !ok || picked.repo != h.taskOrigins("multi")[1].base {
		t.Fatalf("pick sent %#v", picked)
	}
	if _, c := h.handleTaskRepoPicked(picked); c == nil || h.cursor != hdr {
		t.Error("the pick should re-find the task header and open the dialog")
	}

	// The `.` menu is a menu again afterwards.
	h.contextMenu.Show("x", []ContextMenuItem{{ID: "toggle_group", Label: "x", Enabled: true}})
	_, cmd = h.contextMenu.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := cmd().(contextMenuMsg); !ok {
		t.Error("Show after ShowPicker kept the picker's handler")
	}
}

func TestTaskRowNamesItsCheckout(t *testing.T) {
	s := tagged("Implement the new authentication middleware", "/tmp/fleet-x", session.RoleWorker, "auth")
	item := SidebarItem{Session: s, TaskKey: "auth", OriginKey: "local:fleet", RepoPath: "/tmp/fleet-x"}
	if row := ansi.Strip(renderSessionItem(item, 60, false, -1)); !strings.Contains(row, "fleet-x") {
		t.Errorf("task row without git info should name its folder: %q", row)
	}
	// Too narrow: the suffix gives way to the title.
	if row := ansi.Strip(renderSessionItem(item, 30, false, -1)); strings.Contains(row, "fleet-x") {
		t.Errorf("suffix should drop at width 30: %q", row)
	}
	for _, sel := range []bool{false, true} {
		if w := ansi.StringWidth(ansi.Strip(renderSessionItem(item, 60, sel, 3))); w > 62 {
			t.Errorf("selected=%v: row %d wide at width 60", sel, w)
		}
	}
	// Repo-view rows never carry it.
	item.TaskKey = ""
	if row := ansi.Strip(renderSessionItem(item, 60, false, -1)); strings.Contains(row, "fleet-x") {
		t.Errorf("repo-view row carries a context suffix: %q", row)
	}
}

func TestTaskHeaderRendersTheTask(t *testing.T) {
	byTask(t)
	h := tagsHome(t, tagged("a", "/tmp/tr", session.RoleWorker, "auth"))
	h.rebuildFlatItems()
	lines := renderPlain(h)
	if !strings.Contains(lines[0], "@auth 1") {
		t.Errorf("first row = %q, want the @auth header with its count", lines[0])
	}
}
