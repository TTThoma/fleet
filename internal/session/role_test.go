package session

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
)

func TestParseRole(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    Role
		wantErr bool
	}{
		{"", RoleNone, false},
		{"none", RoleNone, false},
		{"NONE", RoleNone, false},
		{"worker", RoleWorker, false},
		{" Orchestrator ", RoleOrchestrator, false},
		{"concierge", RoleConcierge, false},
		// No fallback: a typo must not silently become some role.
		{"wroker", RoleNone, true},
		{"lead", RoleNone, true},
	} {
		got, err := ParseRole(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseRole(%q) = %q, %v; want %q, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestStoredRoleToleratesUnknownValues(t *testing.T) {
	if got := StoredRole("from-a-newer-build"); got != RoleNone {
		t.Errorf("StoredRole(unknown) = %q, want none", got)
	}
	if got := StoredRole("worker"); got != RoleWorker {
		t.Errorf("StoredRole(worker) = %q", got)
	}
}

func TestParseTask(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"none", "", false},
		{"Auth-Rework", "auth-rework", false},
		{"v2.1_migration", "v2.1_migration", false},
		{"-leading-dash", "", true},
		{"has space", "", true},
		{"semi;colon", "", true},
		{strings.Repeat("a", 40), strings.Repeat("a", 40), false},
		{strings.Repeat("a", 41), "", true},
	} {
		got, err := ParseTask(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseTask(%q) = %q, %v; want %q, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestParseSpecialty(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"None", "", false},
		{" gh ", "gh", false},
		{"repo-x infra", "repo-x infra", false},
		{"twelve-chars", "twelve-chars", false},
		{"thirteen-char", "", true},
		{"tab\there", "", true},
		{"new\nline", "", true},
	} {
		got, err := ParseSpecialty(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseSpecialty(%q) = %q, %v; want %q, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

// A worker's finished turn rests as idle; its waiting prompt still reads
// waiting. This is the whole attention policy for workers, driven through the
// real status pipeline rather than the helper alone.
func TestScenarioWorkerFinishedRestsIdle(t *testing.T) {
	runScenario(t, Scenario{
		Name: "worker: running → waiting → running → finished reads idle",
		Role: RoleWorker,
		Events: []ScenarioEvent{
			{At: 0, Hook: "running", Pane: "⠋ Working on your request...\nctrl+c to interrupt\n"},
			{At: 3 * time.Second, Hook: "waiting", Pane: "output\n❯ 1. Yes\n  2. No\nEsc to cancel\n"},
			{At: 5 * time.Second, Hook: "running", Pane: "⠋ Applying changes...\nctrl+c to interrupt\n"},
			{At: 8 * time.Second, Hook: "finished", Pane: "Done!\n\n❯ \n"},
			{At: 12 * time.Second, Hook: "running", Pane: "⠋ Next task...\nctrl+c to interrupt\n"},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusRunning},
			{At: 3 * time.Second, Expected: StatusWaiting},
			{At: 5 * time.Second, Expected: StatusRunning},
			{At: 8 * time.Second, Expected: StatusIdle},
			{At: 10 * time.Second, Expected: StatusIdle}, // stays idle, no bounce back to finished
			{At: 12 * time.Second, Expected: StatusRunning},
		},
	})
}

// The pane-led settle (a waiting hook whose prompt left the screen) is a
// different branch that concludes finished; the rule must hold there too.
func TestScenarioWorkerEscapedPromptRestsIdle(t *testing.T) {
	runScenario(t, Scenario{
		Name: "worker: waiting → user escapes → idle",
		Role: RoleWorker,
		Events: []ScenarioEvent{
			{At: 0, Hook: "waiting", Pane: "output\n❯ 1. Yes\n  2. No\nEsc to cancel\n"},
			{At: 3 * time.Second, Pane: "❯ \n"},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusWaiting},
			{At: 3 * time.Second, Expected: StatusIdle},
		},
	})
}

// Orchestrator and concierge keep today's behaviour: their finished turns are
// the user's unread items.
func TestScenarioNonWorkerRolesStillFinish(t *testing.T) {
	for _, r := range []Role{RoleNone, RoleOrchestrator, RoleConcierge} {
		runScenario(t, Scenario{
			Name: "role " + string(r) + ": finished stays finished",
			Role: r,
			Events: []ScenarioEvent{
				{At: 0, Hook: "running", Pane: "⠋ Working...\nctrl+c to interrupt\n"},
				{At: 3 * time.Second, Hook: "finished", Pane: "Done!\n\n❯ \n"},
			},
			Checks: []ScenarioCheck{
				{At: 3 * time.Second, Expected: StatusFinished},
			},
		})
	}
}

// The rule is agent-independent: it sits on UpdateStatus, not inside the
// Claude branch.
func TestScenarioCodexWorkerFinishedRestsIdle(t *testing.T) {
	runScenario(t, Scenario{
		Name:  "codex worker: finished hook reads idle",
		Agent: agent.Codex,
		Role:  RoleWorker,
		Events: []ScenarioEvent{
			{At: 0, Hook: "running"},
			{At: 3 * time.Second, Hook: "finished"},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusRunning},
			{At: 3 * time.Second, Expected: StatusIdle},
		},
	})
}

// The tag columns ride the hand-maintained positional SQL in SaveSession and
// LoadSessions; a misaligned list scans one column into its neighbour.
func TestSessionTagsRoundTrip(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().Truncate(time.Second)
	row := &SessionRow{
		ID: "tag11111-1700000000", Title: "Tagged", ProjectPath: "/tmp/p",
		Status: "idle", CreatedAt: now, LastAccessed: now, PromptCount: 3,
		SnoozedUntil: now.Add(time.Hour),
		Role:         "worker", Specialty: "gh", Task: "auth-rework",
	}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got := loadOne(t, db)
	if got.Role != "worker" || got.Specialty != "gh" || got.Task != "auth-rework" {
		t.Errorf("tags = %q/%q/%q, want worker/gh/auth-rework", got.Role, got.Specialty, got.Task)
	}
	if !got.SnoozedUntil.Equal(row.SnoozedUntil) || got.PromptCount != 3 {
		t.Errorf("neighbouring columns misaligned: snooze=%v prompts=%d", got.SnoozedUntil, got.PromptCount)
	}

	s := FromRow(got)
	if tg := s.GetTags(); tg != (Tags{Role: RoleWorker, Specialty: "gh", Task: "auth-rework"}) {
		t.Errorf("FromRow tags = %+v", tg)
	}
	if back := s.ToRow(); back.Role != "worker" || back.Specialty != "gh" || back.Task != "auth-rework" {
		t.Errorf("ToRow tags = %q/%q/%q", back.Role, back.Specialty, back.Task)
	}

	if err := db.UpdateTags(row.ID, "orchestrator", "", "billing"); err != nil {
		t.Fatalf("UpdateTags: %v", err)
	}
	got = loadOne(t, db)
	if got.Role != "orchestrator" || got.Specialty != "" || got.Task != "billing" {
		t.Errorf("after UpdateTags = %q/%q/%q", got.Role, got.Specialty, got.Task)
	}
	if got.Title != "Tagged" || got.PromptCount != 3 {
		t.Errorf("UpdateTags touched other columns: title=%q prompts=%d", got.Title, got.PromptCount)
	}
}

func TestUpdateTagsUnknownSessionErrors(t *testing.T) {
	db := openTestDB(t)
	if err := db.UpdateTags("nope-1", "worker", "", ""); err == nil {
		t.Fatal("UpdateTags on a missing session returned nil")
	}
}

// A state.db written before tags existed gains the columns on open, and its
// rows load untagged.
func TestTagColumnsMigrateOntoOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Now().Truncate(time.Second)
	if err := db.SaveSession(&SessionRow{ID: "old11111-1700000000", Title: "Old", ProjectPath: "/tmp/o", Status: "idle", CreatedAt: now, LastAccessed: now}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	db.Close()

	raw, err := sql.Open("sqlite", stateDBDSN(path))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	for _, col := range []string{"role", "specialty", "task"} {
		if _, err := raw.Exec("ALTER TABLE sessions DROP COLUMN " + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	raw.Close()

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	got := loadOne(t, db)
	if got.Role != "" || got.Specialty != "" || got.Task != "" {
		t.Errorf("migrated row tags = %q/%q/%q, want untagged", got.Role, got.Specialty, got.Task)
	}
	if err := db.UpdateTags(got.ID, "worker", "", ""); err != nil {
		t.Errorf("UpdateTags after migration: %v", err)
	}
}

func loadOne(t *testing.T, db *StateDB) *SessionRow {
	t.Helper()
	rows, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d sessions, want 1", len(rows))
	}
	return rows[0]
}
