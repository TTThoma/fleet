package session

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// Team tags describe a session's place in a team of agents working one task:
// an orchestrator that breaks the task down and hands out work, workers that do
// it, and a concierge that is the user's single point of contact.
//
// A session with no tags (role "", task "", specialty "") behaves exactly as
// sessions did before tags existed. Every behaviour a role changes is decided
// by a method on Role, so the policy lives in this file and nowhere else.

// Role is a session's job in its team. The set is fixed because roles drive
// behaviour (attention, the sidebar glyph); what a worker specialises in is the
// free-text Specialty, not a new role.
type Role string

const (
	RoleNone         Role = ""
	RoleOrchestrator Role = "orchestrator"
	RoleConcierge    Role = "concierge"
	RoleWorker       Role = "worker"
)

// Roles is the set in display order, for pickers and help text.
var Roles = []Role{RoleOrchestrator, RoleConcierge, RoleWorker}

// TagNone is the value that clears a tag from the CLI (`--role none`). An
// empty flag value would be indistinguishable from a forgotten one in a shell
// script that expands an unset variable.
const TagNone = "none"

// ParseRole validates a role name. Unlike agent.Parse there is no fallback: a
// typo must not silently tag a session as something else. "" and "none" both
// mean no role.
func ParseRole(s string) (Role, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" || v == TagNone {
		return RoleNone, nil
	}
	for _, r := range Roles {
		if v == string(r) {
			return r, nil
		}
	}
	return RoleNone, fmt.Errorf("unknown role %q (want %s, or %s to clear)", s, RoleNameList(), TagNone)
}

// RoleNameList is the valid role names joined for help and error text.
func RoleNameList() string {
	names := make([]string, len(Roles))
	for i, r := range Roles {
		names[i] = string(r)
	}
	return strings.Join(names, "|")
}

// QuietWhenFinished reports whether a finished turn in a session with this
// role rests as idle instead of finished — read by the user, as far as the
// attention surfaces are concerned.
//
// True for workers only: their finished turns belong to the orchestrator, not
// to the user, so they must not enter the Space rotation or the status pills.
// A worker blocked on a prompt still reads waiting, because no other agent can
// answer a permission dialog for the user.
func (r Role) QuietWhenFinished() bool { return r == RoleWorker }

// taskPattern shapes a task name like a branch name: it is a grouping key and a
// CLI filter value, so it must be easy to type and never need quoting.
var taskPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)

// ParseTask validates and normalises a task name (lowercased, trimmed). "" and
// "none" both mean no task.
func ParseTask(s string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" || v == TagNone {
		return "", nil
	}
	if !taskPattern.MatchString(v) {
		return "", fmt.Errorf("invalid task %q: use 1-40 of a-z, 0-9, '.', '_' or '-', starting with a letter or digit", s)
	}
	return v, nil
}

// MaxSpecialtyWidth caps a specialty in terminal cells: it is drawn on the
// sidebar row and comes out of the title's width budget.
const MaxSpecialtyWidth = 12

// ParseSpecialty validates a specialty tag (trimmed). "" and "none" both mean
// no specialty.
func ParseSpecialty(s string) (string, error) {
	v := strings.TrimSpace(s)
	if v == "" || strings.EqualFold(v, TagNone) {
		return "", nil
	}
	for _, r := range v {
		if !unicode.IsPrint(r) {
			return "", fmt.Errorf("invalid specialty %q: printable characters only", s)
		}
	}
	if w := ansi.StringWidth(v); w > MaxSpecialtyWidth {
		return "", fmt.Errorf("specialty %q is %d columns wide; the limit is %d", s, w, MaxSpecialtyWidth)
	}
	return v, nil
}

// Tags is a session's team tags as one value.
type Tags struct {
	Role      Role
	Specialty string
	Task      string
}

// IsZero reports an untagged session.
func (t Tags) IsZero() bool { return t == Tags{} }

// GetTags returns the session's tags (thread-safe).
func (s *Session) GetTags() Tags {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Tags{Role: s.Role, Specialty: s.Specialty, Task: s.Task}
}

// SetTags replaces the session's tags (thread-safe). Callers validate first and
// persist through StateDB.UpdateTags.
func (s *Session) SetTags(t Tags) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Role, s.Specialty, s.Task = t.Role, t.Specialty, t.Task
}

// settleQuietFinished rests a finished turn as idle for a role that keeps its
// finished turns quiet. Deferred from UpdateStatus, the one place every agent's
// status pass goes through, so no branch that picks "finished" can miss it —
// there are several per agent, and a rule applied at each would drift.
//
// Acknowledged is set too, so the next pass reads the turn as already seen
// exactly as if the user had looked at it, and the "preserve idle" branches
// keep it idle rather than bouncing it back to finished in between.
func (s *Session) settleQuietFinished() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status == StatusFinished && s.Role.QuietWhenFinished() {
		s.Status = StatusIdle
		s.Acknowledged = true
	}
}

// StoredRole reads a stored role, treating an unknown value (one a newer or
// older build wrote) as no role rather than failing the load.
func StoredRole(v string) Role {
	r, err := ParseRole(v)
	if err != nil {
		return RoleNone
	}
	return r
}
