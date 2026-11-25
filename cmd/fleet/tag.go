package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
)

// Team tags from the shell: `--role`, `--specialty` and `--task` on `fleet add`
// and `fleet worktree`, and `fleet tag` to change them on existing sessions.
// The policy (which roles exist, what a role changes) lives in
// internal/session/role.go; this file only parses and writes.

const tagUsage = "Usage: fleet tag [--role R] [--specialty S] [--task T] <session|self>..."

// tagFlagVals are the raw tag flag values plus which were given. A flag left
// out means "leave it alone" for `fleet tag` and "untagged" at creation;
// telling the two apart needs fs.Visit, since both leave the string empty.
type tagFlagVals struct {
	role, specialty, task          string
	roleSet, specialtySet, taskSet bool
}

// bindTagFlags registers the three tag flags on fs.
func bindTagFlags(fs *flag.FlagSet, v *tagFlagVals) {
	fs.StringVar(&v.role, "role", "", "team role: "+session.RoleNameList()+" ("+session.TagNone+" clears)")
	fs.StringVar(&v.specialty, "specialty", "", fmt.Sprintf("what this session specialises in, shown on its row, e.g. gh or infra (≤%d columns; %s clears)", session.MaxSpecialtyWidth, session.TagNone))
	fs.StringVar(&v.task, "task", "", "task this session works on, grouping its team, e.g. auth-rework ("+session.TagNone+" clears)")
}

// noteVisited records which tag flags appeared on the command line. Call after
// every fs.Parse.
func (v *tagFlagVals) noteVisited(fs *flag.FlagSet) {
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "role":
			v.roleSet = true
		case "specialty":
			v.specialtySet = true
		case "task":
			v.taskSet = true
		}
	})
}

func (v tagFlagVals) any() bool { return v.roleSet || v.specialtySet || v.taskSet }

// apply validates the given flags and overlays them on base, leaving fields
// whose flag was not given untouched.
func (v tagFlagVals) apply(base session.Tags) (session.Tags, error) {
	out := base
	if v.roleSet {
		r, err := session.ParseRole(v.role)
		if err != nil {
			return base, err
		}
		out.Role = r
	}
	if v.specialtySet {
		sp, err := session.ParseSpecialty(v.specialty)
		if err != nil {
			return base, err
		}
		out.Specialty = sp
	}
	if v.taskSet {
		t, err := session.ParseTask(v.task)
		if err != nil {
			return base, err
		}
		out.Task = t
	}
	return out, nil
}

// noSessionTagError refuses tag flags alongside --no-session, the rule every
// session-shaping flag follows: nothing is launched, so nothing is tagged.
func (v tagFlagVals) noSessionTagError() error {
	if v.any() {
		return fmt.Errorf("--role, --specialty and --task have no effect with --no-session (no session is started)")
	}
	return nil
}

// tagOpts is a parsed `fleet tag` invocation.
type tagOpts struct {
	flags     tagFlagVals
	selectors []string
}

func tagFlagSet(v *tagFlagVals) *flag.FlagSet {
	fs := flag.NewFlagSet("fleet tag", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	bindTagFlags(fs, v)
	return fs
}

func printTagUsage(w io.Writer) {
	fmt.Fprintln(w, tagUsage)
	fmt.Fprintln(w, "\nSet the team tags of one or more sessions. A flag left out leaves that tag")
	fmt.Fprintln(w, "as it is; the value 'none' clears it. 'self' is the session this command runs")
	fmt.Fprintln(w, "in. Flags come before the sessions.")
	fmt.Fprintln(w)
	var discard tagFlagVals
	fs := tagFlagSet(&discard)
	fs.SetOutput(w)
	fs.PrintDefaults()
}

// errNoTagFlags is a `fleet tag` that names no tag to change.
var errNoTagFlags = errors.New("nothing to change: pass at least one of --role, --specialty, --task")

// parseTagArgs parses `fleet tag`. Pure, so the rules are testable. Values are
// validated here, before any session is resolved, so a typo fails without
// touching anything.
func parseTagArgs(args []string) (tagOpts, error) {
	var o tagOpts
	fs := tagFlagSet(&o.flags)
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	o.flags.noteVisited(fs)
	o.selectors = fs.Args()
	if !o.flags.any() {
		return o, errNoTagFlags
	}
	if len(o.selectors) == 0 {
		return o, fmt.Errorf("no session given")
	}
	for _, sel := range o.selectors {
		// flag stops at the first positional, so a flag after a session would
		// otherwise be looked up as a session name and fail confusingly.
		if strings.HasPrefix(sel, "-") {
			return o, fmt.Errorf("flags must come before the sessions (got %q after one)", sel)
		}
	}
	if _, err := o.flags.apply(session.Tags{}); err != nil {
		return o, err
	}
	return o, nil
}

// selfSelector is the selector meaning "the session this command runs in".
const selfSelector = "self"

// resolveSelfID returns the fleet session id of the pane this process runs
// in, from the FLEET_INSTANCE_ID every fleet session's environment carries.
func resolveSelfID(getenv func(string) string) (string, error) {
	id := strings.TrimSpace(getenv("FLEET_INSTANCE_ID"))
	if id == "" {
		return "", fmt.Errorf("'self' only works inside a fleet session (FLEET_INSTANCE_ID is not set)")
	}
	return id, nil
}

func runTag(args []string) {
	opts, err := parseTagArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printTagUsage(os.Stdout)
			return
		}
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errNoTagFlags) || strings.HasPrefix(err.Error(), "flag ") || err.Error() == "no session given" {
			printTagUsage(os.Stderr)
		}
		os.Exit(1)
	}

	// Without this the database's Info lines fall back to stderr.
	debuglog.Init()
	defer debuglog.Close()

	storage, err := session.Open(session.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer storage.Close()

	rows, err := storage.LoadSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load sessions: %v\n", err)
		os.Exit(1)
	}
	slots, _ := storage.LoadSlotBindings()

	// Resolve every selector before writing anything: one bad name must not
	// leave half a team tagged.
	targets, err := resolveTagTargets(rows, slots, opts.selectors, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	for _, r := range targets {
		cur := session.Tags{Role: session.StoredRole(r.Role), Specialty: r.Specialty, Task: r.Task}
		next, _ := opts.flags.apply(cur) // validated in parseTagArgs
		if err := storage.UpdateTags(r.ID, string(next.Role), next.Specialty, next.Task); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to tag '%s' (%s): %v\n", r.Title, r.ID, err)
			os.Exit(1)
		}
		fmt.Printf("Tagged '%s' (%s): %s\n", r.Title, r.ID, describeTags(next))
	}
}

// resolveTagTargets resolves each selector to one row, deduplicated, in the
// order given. Exact tiers only, as for `fleet remove`: a near miss would tag
// the wrong member of a team.
func resolveTagTargets(rows []*session.SessionRow, slots map[int]string, selectors []string, getenv func(string) string) ([]*session.SessionRow, error) {
	var out []*session.SessionRow
	seen := make(map[string]bool)
	for _, sel := range selectors {
		var match *session.SessionRow
		if sel == selfSelector {
			id, err := resolveSelfID(getenv)
			if err != nil {
				return nil, err
			}
			for _, r := range rows {
				if r.ID == id {
					match = r
					break
				}
			}
			if match == nil {
				return nil, fmt.Errorf("'self' is session %s, which fleet has no record of", id)
			}
		} else {
			m, err := resolveSessionExact(rows, slots, sel, branchOfHead)
			if err != nil {
				return nil, err
			}
			match = m
		}
		if !seen[match.ID] {
			seen[match.ID] = true
			out = append(out, match)
		}
	}
	return out, nil
}

// describeTags renders tags for a confirmation line: "worker · gh · task auth".
func describeTags(t session.Tags) string {
	if t.IsZero() {
		return "untagged"
	}
	var parts []string
	if t.Role != session.RoleNone {
		parts = append(parts, string(t.Role))
	}
	if t.Specialty != "" {
		parts = append(parts, t.Specialty)
	}
	if t.Task != "" {
		parts = append(parts, "task "+t.Task)
	}
	return strings.Join(parts, " · ")
}
