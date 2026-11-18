package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Users from a file (milestone 12): plan / apply / export, plus one-off commands for teams without a
// file. The server computes the plan and marks applied users as managed (no state file).

const usersUse = `users list [-o json]
       owl users export                        current users in the file format (start your users.yaml from it)
       owl users plan  -f users.yaml [--prune] [--detailed-exitcode] [-o json]
       owl users apply -f users.yaml [--prune] [--yes]
       owl users add <email> --role viewer|admin
       owl users set-role <email> --role viewer|admin
       owl users disable <email>
       owl users enable <email>`

type userJSON struct {
	ID          int64      `json:"id"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	ActivatedAt *time.Time `json:"activatedAt"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
	DisabledAt  *time.Time `json:"disabledAt"`
	Managed     bool       `json:"managed"`
}

func (u userJSON) status() string {
	switch {
	case u.DisabledAt != nil:
		return "disabled"
	case u.ActivatedAt == nil:
		return "invited"
	}
	return "active"
}

type fileUserJSON struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type usersFile struct {
	Users []fileUserJSON `json:"users"`
}

type planChange struct {
	Email    string   `json:"email"`
	Actions  []string `json:"actions"`
	Role     string   `json:"role"`
	FromRole string   `json:"fromRole"`
}

type planJSON struct {
	ID        string       `json:"id"`
	Changes   []planChange `json:"changes"`
	NotInFile []string     `json:"notInFile"`
}

type linkJSON struct {
	User       userJSON `json:"user"`
	Link       string   `json:"link"`
	Emailed    bool     `json:"emailed"`
	EmailError string   `json:"emailError"`
}

func runUsers(a *App, ctx context.Context, args []string) error {
	if len(args) == 0 {
		return a.usageErr("usage: owl %s", usersUse)
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "list":
		return usersList(a, ctx, args)
	case "export":
		return usersExport(a, ctx, args)
	case "plan", "apply":
		return usersPlanApply(a, ctx, sub, args)
	case "add", "set-role", "disable", "enable":
		return usersOne(a, ctx, sub, args)
	}
	return a.usageErr("usage: owl %s", usersUse)
}

func (a *App) listUsers(ctx context.Context, c *client) ([]userJSON, error) {
	var us []userJSON
	return us, c.do(ctx, http.MethodGet, "/api/v1/users", nil, &us)
}

func usersList(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("users list", "users list [-o json]", true)
	if _, err := f.parse(args); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var us []userJSON
	if done, err := a.getRaw(ctx, c, f, "/api/v1/users", &us); done || err != nil {
		return err
	}
	tw := a.table()
	fmt.Fprintln(tw, "EMAIL\tROLE\tSTATUS\tMANAGED\tLAST SIGN-IN")
	for _, u := range us {
		m := "-"
		if u.Managed {
			m = "file"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", u.Email, u.Role, u.status(), m, ago(u.LastLoginAt))
	}
	return tw.Flush()
}

func usersExport(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("users export", "users export > users.yaml", false)
	if _, err := f.parse(args); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	us, err := a.listUsers(ctx, c)
	if err != nil {
		return err
	}
	file := usersFile{Users: []fileUserJSON{}}
	for _, u := range us {
		if u.DisabledAt == nil { // disabled users have no access: leaving them out keeps them disabled
			file.Users = append(file.Users, fileUserJSON{Email: u.Email, Role: u.Role})
		}
	}
	raw, err := yaml.Marshal(file)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "# Who can sign in to Owl (%s) and with which role: admin or viewer.\n", c.base.Host)
	fmt.Fprintln(a.Stdout, "# Preview changes with `owl users plan -f users.yaml`, apply with `owl users apply -f users.yaml`.")
	_, err = a.Stdout.Write(raw)
	return err
}

// readUsersFile reads and checks a users file ("-" = stdin). Problems are listed all at once.
func (a *App) readUsersFile(path string) (usersFile, error) {
	var f usersFile
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(io.LimitReader(a.Stdin, 1<<20))
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return f, err
	}
	if err := yaml.UnmarshalStrict(raw, &f); err != nil {
		return f, fmt.Errorf("%s: %w (expected `users:` with entries of `email:` and `role:`)", path, err)
	}
	var probs []string
	seen := map[string]int{}
	for i, u := range f.Users {
		e := strings.ToLower(strings.TrimSpace(u.Email))
		switch {
		case e == "" || !strings.Contains(e, "@"):
			probs = append(probs, fmt.Sprintf("entry %d: email %q", i+1, u.Email))
		case seen[e] > 0:
			probs = append(probs, fmt.Sprintf("entry %d: %s is already entry %d", i+1, e, seen[e]))
		}
		seen[e] = i + 1
		if u.Role != "admin" && u.Role != "viewer" {
			probs = append(probs, fmt.Sprintf("entry %d (%s): role must be admin or viewer, not %q", i+1, u.Email, u.Role))
		}
	}
	if len(probs) > 0 {
		return f, fmt.Errorf("%s:\n  %s", path, strings.Join(probs, "\n  "))
	}
	return f, nil
}

func describeChange(ch planChange) string {
	var parts []string
	for _, act := range ch.Actions {
		switch act {
		case "invite":
			parts = append(parts, "invite as "+ch.Role)
		case "role":
			parts = append(parts, fmt.Sprintf("role %s → %s", ch.FromRole, ch.Role))
		case "enable":
			parts = append(parts, "enable")
		case "disable":
			parts = append(parts, "disable (not in the file)")
		case "manage":
			parts = append(parts, "start managing (already matches)")
		case "unmanage":
			parts = append(parts, "stop managing (not in the file)")
		default:
			parts = append(parts, act)
		}
	}
	return strings.Join(parts, ", ")
}

func (a *App) printPlan(p planJSON, prune bool) {
	sym := func(ch planChange) string {
		switch ch.Actions[0] {
		case "invite":
			return "+"
		case "disable":
			return "-"
		}
		return "~"
	}
	if len(p.Changes) == 0 {
		fmt.Fprintln(a.Stdout, "No changes: Owl's users match the file.")
	} else {
		fmt.Fprintln(a.Stdout, "Changes:")
		tw := a.table()
		for _, ch := range p.Changes {
			fmt.Fprintf(tw, "  %s %s\t%s\n", sym(ch), ch.Email, describeChange(ch))
		}
		tw.Flush()
	}
	if len(p.NotInFile) > 0 {
		fmt.Fprintf(a.Stdout, "\nIn Owl but not in the file (can still sign in):\n  %s\n", strings.Join(p.NotInFile, "\n  "))
		if !prune {
			fmt.Fprintln(a.Stdout, "  Add them to the file, or apply with --prune to disable them.")
		}
	}
}

func usersPlanApply(a *App, ctx context.Context, sub string, args []string) error {
	f := a.newFlags("users "+sub, usersUse, sub == "plan")
	file := f.String("f", "", "the users file (- for stdin)")
	prune := f.Bool("prune", false, "disable users who aren't in the file (never deletes)")
	detailed := f.Bool("detailed-exitcode", false, "plan: exit 0 = no changes, 2 = changes (drift), 1 = error")
	yes := f.Bool("yes", false, "apply: don't ask for confirmation")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if *file == "" || len(pos) > 0 {
		return a.usageErr("usage: owl users %s -f users.yaml [--prune]", sub)
	}
	uf, err := a.readUsersFile(*file)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	source := "stdin"
	if *file != "-" {
		source = sourceLabel(filepath.Base(*file))
	}
	body := map[string]any{"users": uf.Users, "prune": *prune, "source": source}
	var p planJSON
	if err := c.do(ctx, http.MethodPost, "/api/v1/users/plan", body, &p); err != nil {
		return err
	}
	drift := len(p.Changes) > 0 || len(p.NotInFile) > 0
	if sub == "plan" {
		if f.json() {
			if err := a.printJSON(p); err != nil {
				return err
			}
		} else {
			a.printPlan(p, *prune)
		}
		if *detailed && drift {
			return exitError{2}
		}
		return nil
	}

	a.printPlan(p, *prune)
	if len(p.Changes) == 0 {
		return nil
	}
	if !*yes {
		ok, err := a.confirm(fmt.Sprintf("\nApply %d change(s) to %s?", len(p.Changes), c.base.Host))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("nothing applied")
		}
	}
	body["expect"] = p.ID
	var out struct {
		Links []linkJSON `json:"links"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/users/apply", body, &out); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Applied %d change(s).\n", len(p.Changes))
	a.printLinks(out.Links)
	return nil
}

// printLinks shows invite links that weren't emailed (they're shown once).
func (a *App) printLinks(ls []linkJSON) {
	for _, l := range ls {
		if l.Emailed {
			fmt.Fprintf(a.Stdout, "Invite emailed to %s.\n", l.User.Email)
			continue
		}
		link := l.Link
		if strings.HasPrefix(link, "/") {
			if c, err := a.client(); err == nil {
				link = c.base.String() + link
			}
		}
		why := "email isn't set up on the server"
		if l.EmailError != "" {
			why = "the email failed"
		}
		fmt.Fprintf(a.Stdout, "Invite for %s (%s — send it yourself; works once, 72 h, not shown again):\n  %s\n", l.User.Email, why, link)
	}
}

func usersOne(a *App, ctx context.Context, sub string, args []string) error {
	f := a.newFlags("users "+sub, usersUse, false)
	role := f.String("role", "", "admin or viewer")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return a.usageErr("usage: owl users %s <email>%s", sub, map[bool]string{true: " --role viewer|admin"}[sub == "add" || sub == "set-role"])
	}
	email := strings.ToLower(strings.TrimSpace(pos[0]))
	needRole := sub == "add" || sub == "set-role"
	if needRole && *role != "admin" && *role != "viewer" {
		return a.usageErr("--role: admin or viewer")
	}
	if !needRole && *role != "" {
		return a.usageErr("--role only goes with add and set-role")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	if sub == "add" {
		var l linkJSON
		if err := c.do(ctx, http.MethodPost, "/api/v1/users", map[string]string{"email": email, "role": *role}, &l); err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "Invited %s as %s.\n", l.User.Email, l.User.Role)
		a.printLinks([]linkJSON{l})
		return nil
	}
	us, err := a.listUsers(ctx, c)
	if err != nil {
		return err
	}
	var u *userJSON
	for i := range us {
		if us[i].Email == email {
			u = &us[i]
		}
	}
	if u == nil {
		return fmt.Errorf("no user %s (`owl users list`)", email)
	}
	newRole, disabled := u.Role, u.DisabledAt != nil
	switch sub {
	case "set-role":
		newRole = *role
	case "disable":
		disabled = true
	case "enable":
		disabled = false
	}
	if newRole == u.Role && disabled == (u.DisabledAt != nil) {
		fmt.Fprintf(a.Stdout, "%s is already %s (%s): nothing to do.\n", email, u.Role, u.status())
		return nil
	}
	var after userJSON
	if err := c.do(ctx, http.MethodPatch, "/api/v1/users/"+strconv.FormatInt(u.ID, 10), map[string]any{"role": newRole, "disabled": disabled}, &after); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "%s: %s, %s\n", after.Email, after.Role, after.status())
	if u.Managed {
		fmt.Fprintf(a.Stderr, "note: %s is managed by a users file: this shows as drift until the file is updated\n", email)
	}
	return nil
}

// sourceLabel makes the file name a valid audit label (letters, digits, . _ -; ≤ 64; starting with a
// letter or digit): it's only a label, so an unusual name must not stop plan/apply.
func sourceLabel(name string) string {
	b := []rune{}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b = append(b, r)
		} else {
			b = append(b, '_')
		}
	}
	out := strings.TrimLeft(string(b), "._-")
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		return "users file"
	}
	return out
}
