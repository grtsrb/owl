package report

import (
	"fmt"
	"strings"

	"owl/internal/catalog"
	"owl/internal/inventory"
	"owl/internal/policy"
	"owl/internal/upstream"
)

// ActionKind is what to do about an instance. It is derived from the Score (never the other way
// round): the level says how urgent, the action says what.
type ActionKind string

const (
	ActionNone    ActionKind = "none"    // at target and latest patch
	ActionPatch   ActionKind = "patch"   // stay on the line, take its newest patch
	ActionUpgrade ActionKind = "upgrade" // move to the target line
	ActionReplace ActionKind = "replace" // upstream retired
	ActionPin     ActionKind = "pin"     // unpinned tag: pin a version first
	ActionCheck   ActionKind = "check"   // no data to decide on
	ActionUpdate  ActionKind = "update"  // component level: bring every instance to the target
)

// Action is the recommended next step as one sentence: Text says what ("Upgrade to 1.14.7"), Why the
// most important reasons ("fixes 9 high advisories; 1.13 is EOL since 2026-06-28").
type Action struct {
	Kind ActionKind `json:"kind"`
	Text string     `json:"text"`
	Why  string     `json:"why"`
	// To is the version a patch or upgrade step goes to ("" for every other step).
	To string `json:"-"`
}

// ActionFor turns a score into the recommended action for one instance.
func ActionFor(in inventory.Instance, sc Score, target *policy.Target, info *upstream.Info) Action {
	switch {
	case in.Status == string(catalog.StatusUnpinned):
		return Action{ActionPin, "Pin an exact version", "tag " + tagOf(in.Image) + " doesn't say which version runs", ""}
	case info != nil && info.Retired:
		why := "retired upstream"
		if s := advisorySummary(sc.Advisories); s != "" {
			why += "; " + s + " open"
		}
		return Action{ActionReplace, "Replace", why, ""}
	case sc.Level == LevelUnknown:
		return Action{ActionCheck, "Check manually", firstReason(sc, LevelUnknown), ""}
	case sc.Level == LevelOK:
		return Action{ActionNone, "Up to date", "at target, latest patch", ""}
	}

	var why []string
	if s := advisorySummary(sc.Advisories); s != "" {
		why = append(why, "fixes "+s)
	}
	if sc.EOL != nil {
		e := sc.EOL.Line + " is EOL"
		if sc.EOL.Since != nil {
			e += " since " + sc.EOL.Since.Format("2006-01-02")
		}
		why = append(why, e)
	}
	if sc.LinesBehind >= 1 && len(why) < 2 {
		why = append(why, Plural(sc.LinesBehind, "release line")+" behind")
	}

	if target == nil {
		return Action{ActionCheck, "Check manually", "no version target for the policy", ""}
	}
	v, okV := catalog.ParseVersion(in.Version, "")
	tv, okT := catalog.ParseVersion(target.Version, "")
	if sc.LinesBehind == 0 {
		to := target.Version
		if okV && okT && v.Compare(tv) >= 0 {
			to = sc.LatestInLine // already at/above target: the patch is the newest of its own line
		}
		if lv, ok := catalog.ParseVersion(to, ""); to == "" || !okV || !ok || lv.Compare(v) <= 0 {
			// Nothing newer in this line, yet the level isn't OK: EOL line (e.g. a pin on it) or an
			// advisory fixed only in a later line. Patching can't help — say what can.
			if sc.EOL != nil {
				return Action{ActionUpgrade, "Move to a supported release line", strings.Join(append(why, "policy "+target.Policy+" targets it"), "; "), ""}
			}
			if sc.EOLSoon != nil && len(sc.Advisories) == 0 {
				// At the newest patch of a line that ends soon: the work is planning the next line.
				return Action{ActionUpgrade, "Plan the move off " + sc.Line,
					strings.Join(append(why, sc.Line+" reaches end-of-life on "+sc.EOLSoon.Format("2006-01-02")), "; "), ""}
			}
			return Action{ActionCheck, "Review advisories", strings.Join(append(why, "no newer patch in "+sc.Line), "; "), ""}
		}
		if len(why) == 0 {
			why = append(why, "patch release")
		}
		return Action{ActionPatch, "Patch to " + to, strings.Join(why, "; "), to}
	}
	if len(why) == 0 {
		why = append(why, "behind target ("+target.Policy+")")
	}
	return Action{ActionUpgrade, "Upgrade to " + target.Version, strings.Join(why, "; "), target.Version}
}

// advisorySummary: "1 critical, 2 high advisories" for the advisories that count (certain, with a fix).
func advisorySummary(ms []upstream.Match) string {
	crit, high := 0, 0
	for _, m := range ms {
		switch m.Severity {
		case "critical":
			crit++
		case "high":
			high++
		}
	}
	var parts []string
	if crit > 0 {
		parts = append(parts, fmt.Sprintf("%d critical", crit))
	}
	if high > 0 {
		parts = append(parts, fmt.Sprintf("%d high", high))
	}
	if len(parts) == 0 {
		return ""
	}
	noun := " advisories"
	if crit+high == 1 {
		noun = " advisory"
	}
	return strings.Join(parts, ", ") + noun
}

func firstReason(sc Score, l Level) string {
	for _, r := range sc.Reasons {
		if r.Level == l {
			return r.Text
		}
	}
	return "no upstream release data"
}

// Plural: "1 release line", "2 release lines".
func Plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func tagOf(image string) string {
	ref := catalog.ParseRef(image)
	switch {
	case ref.Tag != "":
		return ref.Tag
	case ref.Digest != "":
		return "(digest only)"
	}
	return image
}

// ComponentAction is the headline for a component: one goal for all instances, so it doesn't change
// with which clusters are looked at. Why comes from the worst instance (most reasons on a tie).
func ComponentAction(insts []ScoredInstance, target *policy.Target) Action {
	var worst *ScoredInstance
	needs := map[ActionKind]bool{}
	for i := range insts {
		in := &insts[i]
		needs[in.Action.Kind] = true
		if worst == nil || in.Score.Level.Rank() > worst.Score.Level.Rank() ||
			in.Score.Level.Rank() == worst.Score.Level.Rank() && len(in.Score.Reasons) > len(worst.Score.Reasons) {
			worst = in
		}
	}
	switch {
	case worst == nil:
		return Action{ActionCheck, "Check manually", "not running anywhere", ""}
	case needs[ActionReplace]:
		return Action{ActionReplace, "Replace", worst.Action.Why, ""}
	case needs[ActionPatch] || needs[ActionUpgrade]:
		if target == nil {
			break
		}
		// "Update to <target>" only when some instance's step is the target itself; instances ahead of
		// the target that only need a patch in their own line must not read as "go back to the target".
		toTarget, steps := false, map[string]Action{} // distinct steps by text
		for _, in := range insts {
			if in.Action.Kind == ActionPatch || in.Action.Kind == ActionUpgrade {
				steps[in.Action.Text] = in.Action
				toTarget = toTarget || in.Action.To == target.Version
			}
		}
		if toTarget {
			return Action{ActionUpdate, "Update to " + target.Version, worst.Action.Why, target.Version}
		}
		if len(steps) == 1 {
			for _, a := range steps {
				return Action{a.Kind, a.Text, worst.Action.Why, a.To}
			}
		}
		return Action{ActionPatch, "Patch each to the newest of its line", worst.Action.Why, ""}
	case needs[ActionPin]:
		return Action{ActionPin, "Pin an exact version", worst.Action.Why, ""}
	case needs[ActionCheck]:
		return Action{ActionCheck, worst.Action.Text, worst.Action.Why, ""}
	}
	return worst.Action
}
