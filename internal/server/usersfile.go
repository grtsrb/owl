package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"owl/internal/store"
)

// Users from a file (milestone 12): `owl users plan|apply -f users.yaml`. The server is the state —
// users applied from a file are marked managed — so there's no state file. The plan is computed here,
// and apply recomputes it and refuses when it differs from the one the person confirmed.

const maxFileUsers = 2000

type fileUser struct {
	Email string     `json:"email"`
	Role  store.Role `json:"role"`
}

type usersFileBody struct {
	Users  []fileUser `json:"users"`
	Prune  bool       `json:"prune"`
	Source string     `json:"source"` // file name, for the audit log ("users.yaml")
	Expect string     `json:"expect"` // apply: the plan id that was shown
}

// UserPlanChange is one user's change. Actions, in order: "invite", "role", "enable", "reinvite" (a new
// invite for someone who never signed in and whose link expired), "disable", "manage" (start managing a
// user that already matches), "unmanage" (no longer in the file).
type UserPlanChange struct {
	Email    string     `json:"email"`
	Actions  []string   `json:"actions"`
	Role     store.Role `json:"role"`
	FromRole store.Role `json:"fromRole,omitempty"`

	change store.UserChange
}

type UsersPlan struct {
	ID      string           `json:"id"`
	Changes []UserPlanChange `json:"changes"`
	// NotInFile: users who can sign in (or have a pending invite) but aren't in the file — drift; with
	// prune they're disabled (and listed under changes instead).
	NotInFile []string `json:"notInFile"`
}

var reSource = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// validateUsersFile checks the desired users; problems are listed all at once.
func validateUsersFile(b *usersFileBody) []string {
	var probs []string
	if len(b.Users) > maxFileUsers {
		return []string{fmt.Sprintf("at most %d users", maxFileUsers)}
	}
	seen := map[string]bool{}
	for i, u := range b.Users {
		e, ok := ValidEmail(u.Email)
		switch {
		case !ok:
			probs = append(probs, fmt.Sprintf("users[%d]: %q isn't a plain email address", i, u.Email))
		case seen[e]:
			probs = append(probs, fmt.Sprintf("users[%d]: %s is listed twice", i, e))
		}
		seen[e] = true
		b.Users[i].Email = e
		if !u.Role.Valid() {
			probs = append(probs, fmt.Sprintf("users[%d] (%s): role must be admin or viewer", i, u.Email))
		}
	}
	if b.Source == "" {
		b.Source = "users file"
	} else if !reSource.MatchString(b.Source) {
		probs = append(probs, "source: a file name like users.yaml")
	}
	return probs
}

// planUsers compares the current users with the file. Pure; the order is the file's, then the others
// by email.
func planUsers(current []store.User, desired []fileUser, prune bool) UsersPlan {
	plan := UsersPlan{Changes: []UserPlanChange{}, NotInFile: []string{}}
	byEmail := map[string]store.User{}
	for _, u := range current {
		byEmail[u.Email] = u
	}
	inFile := map[string]bool{}
	for _, d := range desired {
		inFile[d.Email] = true
		u, ok := byEmail[d.Email]
		if !ok {
			plan.Changes = append(plan.Changes, UserPlanChange{Email: d.Email, Actions: []string{"invite"}, Role: d.Role,
				change: store.UserChange{Invite: true, Email: d.Email, Role: d.Role}})
			continue
		}
		var acts []string
		c := UserPlanChange{Email: u.Email, Role: d.Role}
		if u.Role != d.Role {
			acts = append(acts, "role")
			c.FromRole = u.Role
		}
		if u.DisabledAt != nil {
			acts = append(acts, "enable")
		}
		if u.ActivatedAt == nil && (u.DisabledAt != nil || !u.InvitePending) {
			acts = append(acts, "reinvite") // never signed in and no working invite left
		}
		if !u.Managed {
			acts = append(acts, "manage")
		}
		if len(acts) == 0 {
			continue
		}
		c.Actions = acts
		c.change = store.UserChange{Email: u.Email, UserID: u.ID, Role: d.Role, Managed: true,
			WasRole: u.Role, WasDisabled: u.DisabledAt != nil, WasManaged: u.Managed}
		plan.Changes = append(plan.Changes, c)
	}
	others := slices.Clone(current)
	slices.SortFunc(others, func(a, b store.User) int { return strings.Compare(a.Email, b.Email) })
	for _, u := range others {
		if inFile[u.Email] {
			continue
		}
		disabled := u.DisabledAt != nil
		c := UserPlanChange{Email: u.Email, Role: u.Role,
			change: store.UserChange{Email: u.Email, UserID: u.ID, Role: u.Role, Disabled: disabled,
				WasRole: u.Role, WasDisabled: disabled, WasManaged: u.Managed}}
		switch {
		case !disabled && prune:
			c.Actions = []string{"disable"}
			c.change.Disabled = true
		case !disabled:
			plan.NotInFile = append(plan.NotInFile, u.Email)
			if !u.Managed {
				continue
			}
			c.Actions = []string{"unmanage"}
		case u.Managed:
			c.Actions = []string{"unmanage"}
		default:
			continue
		}
		plan.Changes = append(plan.Changes, c)
	}
	raw, _ := json.Marshal(struct {
		C []UserPlanChange
		N []string
	}{plan.Changes, plan.NotInFile})
	sum := sha256.Sum256(raw)
	plan.ID = hex.EncodeToString(sum[:8])
	return plan
}

// readUsersFile decodes and validates the body (users files can be bigger than other bodies).
func (s *Server) readUsersFile(w http.ResponseWriter, r *http.Request) (*usersFileBody, bool) {
	var b usersFileBody
	if !decodeJSONMax(w, r, &b, 1<<20) {
		return nil, false
	}
	if probs := validateUsersFile(&b); len(probs) > 0 {
		httpError(w, http.StatusBadRequest, strings.Join(probs, "; "))
		return nil, false
	}
	return &b, true
}

// selfCheck refuses a plan that would demote or disable the person applying it (their own token or
// session): they'd lock themselves out halfway through managing users.
func selfCheck(plan UsersPlan, self *store.User) string {
	for _, c := range plan.Changes {
		if c.change.UserID != self.ID {
			continue
		}
		if c.change.Disabled && !c.change.WasDisabled {
			return fmt.Sprintf("the plan would disable you (%s): add yourself to the file, or have another admin apply it", self.Email)
		}
		if c.change.Role != store.RoleAdmin {
			return fmt.Sprintf("the plan would make you (%s) a %s: change your role in the file back, or have another admin apply it", self.Email, c.change.Role)
		}
	}
	return ""
}

func (s *Server) usersPlan(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readUsersFile(w, r)
	if !ok {
		return
	}
	us, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, planUsers(us, b.Users, b.Prune))
}

type usersApplyJSON struct {
	Plan  UsersPlan  `json:"plan"`
	Links []linkJSON `json:"links"` // one per invited user
}

func (s *Server) usersApply(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readUsersFile(w, r)
	if !ok {
		return
	}
	us, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	plan := planUsers(us, b.Users, b.Prune)
	if b.Expect != "" && b.Expect != plan.ID {
		httpError(w, http.StatusConflict, "users changed since the plan was shown: run the plan again")
		return
	}
	self := &sessionFrom(r).User
	if msg := selfCheck(plan, self); msg != "" {
		httpError(w, http.StatusConflict, msg)
		return
	}
	var changes []store.UserChange
	for _, c := range plan.Changes {
		changes = append(changes, c.change)
	}
	after := map[int64]plannedUser{}
	for _, c := range changes {
		if !c.Invite {
			after[c.UserID] = plannedUser{Role: c.Role, Disabled: c.Disabled}
		}
	}
	if err := googleAdminGuard(r.Context(), s.Store, s.Auth.Google != nil, after); errors.Is(err, store.ErrLastAdmin) {
		httpError(w, http.StatusConflict, "Password sign-in is off and the plan leaves no active admin who signs in with Google: keep one in the file as admin (or turn password sign-in back on).")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	out := usersApplyJSON{Plan: plan, Links: []linkJSON{}}
	if len(changes) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	invited, err := s.Store.ApplyUserChanges(r.Context(), changes)
	switch {
	case errors.Is(err, store.ErrChanged):
		httpError(w, http.StatusConflict, "users changed while applying ("+err.Error()+"): nothing was changed — run the plan again")
		return
	case err != nil:
		s.userError(w, err)
		return
	}
	for _, c := range plan.Changes {
		args := []any{"user", c.Email, "via", b.Source}
		for _, a := range c.Actions {
			switch a {
			case "invite":
				s.audit(r, "user invited", append(args, "role", c.Role)...)
			case "role":
				s.audit(r, "user role changed", append(args, "from", c.FromRole, "to", c.Role)...)
			case "enable":
				s.audit(r, "user enabled", args...)
			case "reinvite":
				s.audit(r, "invite sent again", args...)
			case "disable":
				s.audit(r, "user disabled", args...)
			case "manage":
				s.audit(r, "user now managed by file", args...)
			case "unmanage":
				s.audit(r, "user no longer managed by file", args...)
			}
		}
	}
	// Invite links after the commit: a failed email (or link) doesn't undo the users; the admin can
	// send the invite again from the UI.
	for _, c := range plan.Changes {
		if slices.Contains(c.Actions, "reinvite") {
			if u, err := s.Store.GetUser(r.Context(), c.change.UserID); err == nil {
				invited = append(invited, *u)
			}
		}
	}
	for i := range invited {
		l, err := s.sendLink(r, &invited[i], store.TokenInvite)
		if err != nil {
			s.Log.Error("invite link failed", "user", invited[i].Email, "err", err, "req", requestID(r))
			continue
		}
		out.Links = append(out.Links, l)
	}
	writeJSON(w, http.StatusOK, out)
}
