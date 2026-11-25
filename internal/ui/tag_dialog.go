package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// tagsSetMsg carries the tags the dialog saved back to app.go, already
// validated — the handler never re-parses.
type tagsSetMsg struct{ tags session.Tags }

// tagRoleCycle is the Role row's order: none first, so ←/→ from an untagged
// session reaches every role and back.
var tagRoleCycle = append([]session.Role{session.RoleNone}, session.Roles...)

// tagSuggestionRows is how many task suggestions the dialog shows. The rows
// are always reserved, filled or blank, so the box never changes height while
// you type.
const tagSuggestionRows = 3

// Focus rows. Suggestions follow the task input at tagFocusTask+1+i.
const (
	tagFocusRole = iota
	tagFocusSpecialty
	tagFocusTask
	tagFieldCount
)

// TagDialog sets the role, specialty and task of the session row it was opened
// over. A row-anchored dropdown like SnoozeDialog: it acts on one visible row
// and stays attached to it.
//
// One highlight walks the whole box — the Role cycler, the two text fields and
// the task suggestions under the last — and the caret lives with it. Enter on a
// field saves; Enter on a suggestion puts it in the Task field instead, since
// picking a name is not the same act as saving.
//
// It owns text inputs, so it gets the raw key, never the keyboard-layout
// remap (see TestNormalizedDialogsHoldNoTextInput): a Hebrew user typing a
// specialty must get Hebrew.
type TagDialog struct {
	visible bool
	width   int
	height  int

	title   string
	roleIdx int
	spec    textinput.Model
	task    textinput.Model
	// tasks are the task names already in use, offered under the Task field.
	tasks []string

	focus int

	anchorX     int
	rowY        int
	bottomLimit int

	clickRows overlayRowMap
}

func NewTagDialog() *TagDialog {
	spec := NewTextInput()
	spec.Placeholder = "e.g. gh, infra"
	spec.CharLimit = 24 // the width cap is enforced on save, in cells
	spec.SetWidth(20)
	task := NewTextInput()
	task.Placeholder = "e.g. auth-rework"
	task.CharLimit = 40
	task.SetWidth(20)
	return &TagDialog{spec: spec, task: task}
}

// Show opens the dialog for one session, seeded with its current tags.
// tasks is the suggestion list (tasksInUse); the dialog never reads sessions
// itself, so it is a pure function of what it was handed.
func (d *TagDialog) Show(title string, cur session.Tags, tasks []string) {
	d.visible = true
	d.title = title
	d.roleIdx = 0
	for i, r := range tagRoleCycle {
		if r == cur.Role {
			d.roleIdx = i
		}
	}
	d.spec.SetValue(cur.Specialty)
	d.spec.CursorEnd()
	d.task.SetValue(cur.Task)
	d.task.CursorEnd()
	d.tasks = tasks
	d.setFocus(tagFocusRole)
}

// ShowFocusingTask opens the dialog with the Task field highlighted — for a
// caller whose next step is naming a task.
func (d *TagDialog) ShowFocusingTask(title string, cur session.Tags, tasks []string) {
	d.Show(title, cur, tasks)
	d.setFocus(tagFocusTask)
}

func (d *TagDialog) Hide() {
	d.visible = false
	d.spec.Blur()
	d.task.Blur()
}

func (d *TagDialog) IsVisible() bool { return d.visible }

// Box geometry. Fixed width so nothing reflows as you type; the widest footer
// hint fits inner at the comfortable size, and every line is truncated to it
// below that.
const (
	tagContentW = 46
	tagChrome   = 6  // DialogStyle: border + padding each side (see SnoozeDialog)
	tagLabelW   = 11 // " Specialty " column
)

func (d *TagDialog) boxWidth() int {
	return min(tagContentW, max(d.width-6, tagChrome+1))
}

func (d *TagDialog) inner() int { return max(d.boxWidth()-tagChrome, 1) }

// SetSize also fits the text inputs to the box: at a narrow terminal a fixed
// 20-wide input plus its label is wider than the box, and the row wraps.
func (d *TagDialog) SetSize(w, h int) {
	d.width, d.height = w, h
	// A row is " " + label + " " + the input, which draws a 2-cell prompt and
	// one cell for the caret past its width.
	iw := max(d.inner()-1-tagLabelW-1-2-1, 4)
	d.spec.SetWidth(min(iw, 20))
	d.task.SetWidth(min(iw, 20))
}

// SetAnchor positions the dropdown against its sidebar row, same contract as
// ContextMenuDialog.SetAnchor.
func (d *TagDialog) SetAnchor(x, rowY, bottomLimit int) {
	d.anchorX, d.rowY, d.bottomLimit = x, rowY, bottomLimit
}

func (d *TagDialog) role() session.Role { return tagRoleCycle[d.roleIdx] }

// suggestions are the in-use tasks matching what is typed in the Task field,
// excluding an exact match (a row echoing the field answers nothing), capped
// at tagSuggestionRows.
func (d *TagDialog) suggestions() []string {
	q := strings.ToLower(strings.TrimSpace(d.task.Value()))
	var out []string
	for _, t := range d.tasks {
		if t == q || !strings.Contains(t, q) {
			continue
		}
		out = append(out, t)
		if len(out) == tagSuggestionRows {
			break
		}
	}
	return out
}

func (d *TagDialog) onSuggestion() bool { return d.focus > tagFocusTask }

// setFocus moves the highlight, clamped to the rows that exist, and keeps each
// text input's own focus (and caret) in step with it.
func (d *TagDialog) setFocus(i int) {
	last := tagFocusTask + len(d.suggestions())
	if i < 0 {
		i = 0
	}
	if i > last {
		i = last
	}
	d.focus = i
	if i == tagFocusSpecialty {
		d.spec.Focus()
	} else {
		d.spec.Blur()
	}
	if i == tagFocusTask {
		d.task.Focus()
	} else {
		d.task.Blur()
	}
}

// parsed validates the fields. err names the field that is wrong; it renders in
// the footer and blocks Enter — never a silent fallback.
func (d *TagDialog) parsed() (session.Tags, string) {
	spec, err := session.ParseSpecialty(d.spec.Value())
	if err != nil {
		return session.Tags{}, "specialty: " + err.Error()
	}
	task, err := session.ParseTask(d.task.Value())
	if err != nil {
		return session.Tags{}, "task: use a-z 0-9 . _ - (≤40)"
	}
	return session.Tags{Role: d.role(), Specialty: spec, Task: task}, ""
}

func (d *TagDialog) cycleRole(delta int) {
	n := len(tagRoleCycle)
	d.roleIdx = ((d.roleIdx+delta)%n + n) % n
}

func (d *TagDialog) Update(msg tea.Msg) (*TagDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}
	key := keyMsg.String()
	switch key {
	case "esc", "ctrl+c":
		d.Hide()
		return d, nil
	case "up":
		d.setFocus(d.focus - 1)
		return d, nil
	case "down":
		d.setFocus(d.focus + 1)
		return d, nil
	case "tab", "shift+tab":
		// Fields only, cycling: suggestions are reached with ↓.
		delta := 1
		if key == "shift+tab" {
			delta = -1
		}
		f := d.focus
		if d.onSuggestion() {
			f = tagFocusTask
		}
		d.setFocus(((f+delta)%tagFieldCount + tagFieldCount) % tagFieldCount)
		return d, nil
	case "enter":
		if d.onSuggestion() {
			// The highlight is a task name: put it in the field and go back to it.
			s := d.suggestions()[d.focus-tagFocusTask-1]
			d.task.SetValue(s)
			d.task.CursorEnd()
			d.setFocus(tagFocusTask)
			return d, nil
		}
		tags, bad := d.parsed()
		if bad != "" {
			return d, nil // the footer says why; stay open so it's fixable
		}
		d.Hide()
		return d, func() tea.Msg { return tagsSetMsg{tags: tags} }
	}

	if d.focus == tagFocusRole {
		switch key {
		case "left", "h":
			d.cycleRole(-1)
		case "right", "l", "space":
			d.cycleRole(1)
		}
		return d, nil
	}
	// Typing on a suggestion returns to the Task field and keeps the keystroke.
	if d.onSuggestion() && isTypingKey(key) {
		d.setFocus(tagFocusTask)
	}
	var cmd tea.Cmd
	switch d.focus {
	case tagFocusSpecialty:
		d.spec, cmd = d.spec.Update(msg)
	case tagFocusTask:
		d.task, cmd = d.task.Update(msg)
		d.setFocus(d.focus) // the suggestion list may have shrunk
	}
	return d, cmd
}

// View returns the bare box (empty when hidden); app.go composites it at
// Position, like the other dropdowns.
func (d *TagDialog) View() string {
	if !d.visible {
		return ""
	}
	boxW, inner := d.boxWidth(), d.inner()
	const labelW = tagLabelW
	row := func(i int, label, value string) string {
		l := " " + padRight(label, labelW-1) // pad raw, then style
		if d.focus == i {
			return " " + selTitle().Render(l) + " " + value
		}
		return " " + DimStyle.Render(l) + " " + value
	}

	var b strings.Builder
	b.WriteString(TitleStyle.Render(ansi.Truncate(d.title, inner, "…")))
	b.WriteString("\n\n")

	roleName := string(d.role())
	if roleName == "" {
		roleName = "none"
	}
	roleVal := "‹ " + roleName + " ›"
	if d.focus != tagFocusRole {
		roleVal = DimStyle.Render(roleVal)
	}
	b.WriteString(row(tagFocusRole, "Role", roleVal) + "\n")
	b.WriteString(row(tagFocusSpecialty, "Specialty", d.spec.View()) + "\n")
	b.WriteString(row(tagFocusTask, "Task", d.task.View()) + "\n")

	// DialogStyle puts the title on the box's third line; fields start two
	// below it, suggestions right after the fields.
	d.clickRows.reset(2)
	d.clickRows.set(2+tagFocusRole, tagFocusRole)
	d.clickRows.set(2+tagFocusSpecialty, tagFocusSpecialty)
	d.clickRows.set(2+tagFocusTask, tagFocusTask)

	sugg := d.suggestions()
	showSugg := d.focus >= tagFocusTask
	for i := 0; i < tagSuggestionRows; i++ {
		line := ""
		if showSugg && i < len(sugg) {
			text := ansi.Truncate(sugg[i], inner-labelW-3, "…")
			if d.focus == tagFocusTask+1+i {
				line = strings.Repeat(" ", labelW+1) + SelectionMarker(true).Render("▸") + " " + selTitle().Render(text)
			} else {
				line = strings.Repeat(" ", labelW+3) + DimStyle.Render(text)
			}
			d.clickRows.set(2+tagFieldCount+i, tagFocusTask+1+i)
		}
		b.WriteString(line + "\n")
	}

	// Footer: what Enter does now, or why it won't. Exactly one line in every
	// state, so the box height never moves.
	b.WriteString("\n")
	// Every variant is truncated to inner: on a narrow terminal a hint that
	// wraps grows the box a row the moment the highlight moves.
	foot := func(st lipgloss.Style, text string) {
		b.WriteString(st.Render(ansi.Truncate("  "+text, inner, "…")))
	}
	_, bad := d.parsed()
	switch {
	case bad != "":
		foot(ErrorStyle, bad)
	case d.onSuggestion():
		foot(DimStyle, "enter use task • ↑↓ move • esc cancel")
	case d.focus == tagFocusRole:
		foot(DimStyle, "←→ role • ↑↓ field • enter save")
	default:
		foot(DimStyle, "enter save • tab field • esc cancel")
	}
	return DialogStyle.Width(boxW).Render(b.String())
}

// padRight pads raw text to w cells (no styling inside).
func padRight(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// Position places the dropdown below its row, flipping above near the footer —
// same rules as SnoozeDialog.Position.
func (d *TagDialog) Position(boxW, boxH int) (int, int) {
	x := d.anchorX
	if x+boxW > d.width {
		x = d.width - boxW
	}
	if x < 0 {
		x = 0
	}
	y := d.rowY + 1
	if y+boxH > d.bottomLimit {
		y = d.rowY - boxH
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// ClickRowAt implements clickableOverlay. A click moves the highlight (and the
// caret, on a field) and fires nothing: a field is for typing, and saving on a
// click would store whatever the other fields happened to hold. A suggestion
// fills the Task field, as Enter on it does.
func (d *TagDialog) ClickRowAt(_, dy int) rune {
	i, ok := d.clickRows.at(dy)
	if !ok {
		return 0
	}
	d.setFocus(i)
	if d.onSuggestion() {
		return tea.KeyEnter
	}
	return 0
}
