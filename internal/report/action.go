package report

import (
	"fmt"
	"strings"
	"time"

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
	ActionPlan    ActionKind = "plan"    // on the newest patch of a line upstream still supports: plan the move
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

// ActionFor turns a score into the recommended action for one instance. For an image the cluster's
// distribution ships itself (BundledWith) a patch or upgrade means upgrading the distribution.
func ActionFor(in inventory.Instance, sc Score, target *policy.Target, info *upstream.Info) Action {
	a := coverFixes(scoreAction(in, sc, target, info), in, sc, target, info)
	switch {
	case in.BundledWith != "" && (a.Kind == ActionPatch || a.Kind == ActionUpgrade):
		a.Text = bundledText(bundledName(in), a.To)
	case in.BundledWith != "" && a.Kind == ActionPlan:
		d := bundledName(in)
		a.Text = "Comes with " + d + " — plan the " + d + " upgrade that ships " + a.To + " or newer"
	}
	return a
}

// bundledName: how what ships the image is written in sentences ("RKE2", "Cilium").
func bundledName(in inventory.Instance) string {
	switch {
	case in.BundledName != "":
		return in.BundledName
	case in.BundledWith == "rke2":
		return "RKE2"
	}
	return in.BundledWith
}

// bundledText: "Comes with RKE2 — upgrade RKE2 to a release that ships 1.11.3 or newer".
func bundledText(d, to string) string {
	if to == "" {
		return "Comes with " + d + " — upgrade " + d + " to a newer release"
	}
	return "Comes with " + d + " — upgrade " + d + " to a release that ships " + to + " or newer"
}

// coverFixes makes sure a patch/upgrade step (or "Review advisories") actually fixes the instance's
// advisories: when one is fixed only above the step (a later line than the policy target, e.g. Velero
// 1.17.1 with a fix only in 1.18.1), the step becomes the newest patch of the line that has the fix —
// security wins over the policy target, as it does for the level. EOL lines keep "Move to a supported
// release line"; retired, unpinned and unknown aren't steps.
func coverFixes(a Action, in inventory.Instance, sc Score, target *policy.Target, info *upstream.Info) Action {
	if len(sc.Advisories) == 0 || sc.EOL != nil || info == nil ||
		a.Kind != ActionPatch && a.Kind != ActionUpgrade && a.Kind != ActionCheck {
		return a
	}
	v, ok := catalog.ParseVersion(in.Version, "")
	if !ok {
		return a
	}
	var step *catalog.Version
	if s, ok := catalog.ParseVersion(a.To, ""); ok {
		step = &s
	}
	var need *catalog.Version // highest fix the step doesn't reach
	var missed []string
	for _, m := range sc.Advisories {
		fix, ok := fixAbove(m, v)
		if !ok || step != nil && fix.Compare(*step) <= 0 {
			continue
		}
		missed = append(missed, advisoryLabel(m.Advisory)+" ("+m.Severity+") fixed in "+fix.String())
		if need == nil || fix.Compare(*need) > 0 {
			f := fix
			need = &f
		}
	}
	if need == nil {
		return a
	}
	to := need.String()
	lines := policy.UsableLines(info.Lines)
	if pos, exact := policy.Position(*need, lines); exact {
		if lv, ok := catalog.ParseVersion(lines[pos].Latest, ""); ok && lv.Compare(*need) >= 0 {
			to = lines[pos].Latest // the newest patch of the line with the fix
		}
	}
	why := missed
	if target != nil && to != target.Version {
		why = append(why, "beyond the policy target "+target.Version+" ("+target.Policy+"), which doesn't include the fix")
	}
	return Action{ActionUpgrade, "Upgrade to " + to, strings.Join(why, "; "), to}
}

// fixAbove: the fix that matters for v (RelevantFix, else the lowest listed fix above v).
func fixAbove(m upstream.Match, v catalog.Version) (catalog.Version, bool) {
	if f, ok := catalog.ParseVersion(m.RelevantFix, ""); ok && f.Compare(v) > 0 {
		return f, true
	}
	var best *catalog.Version
	for _, s := range m.FixedIn {
		if f, ok := catalog.ParseVersion(s, ""); ok && f.Compare(v) > 0 && (best == nil || f.Compare(*best) < 0) {
			b := f
			best = &b
		}
	}
	if best == nil {
		return catalog.Version{}, false
	}
	return *best, true
}

func scoreAction(in inventory.Instance, sc Score, target *policy.Target, info *upstream.Info) Action {
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
			e += " since " + sc.EOL.Since.Format(time.DateOnly)
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
	if sc.Supported != "" && len(sc.Advisories) == 0 {
		// Far behind on a line upstream still supports: take its newest patch now, plan the move.
		why = append(why, sc.Supported, "plan the move to "+target.Version+" ("+target.Policy+")")
		if lv, ok := catalog.ParseVersion(sc.LatestInLine, ""); okV && ok && lv.Compare(v) > 0 {
			return Action{ActionPatch, "Patch to " + sc.LatestInLine, strings.Join(why, "; "), sc.LatestInLine}
		}
		return Action{ActionPlan, "Plan the move to " + target.Version, strings.Join(why, "; "), target.Version}
	}
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
					strings.Join(append(why, sc.Line+" reaches end-of-life on "+sc.EOLSoon.Format(time.DateOnly)), "; "), ""}
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
		// Planning steps (instances on the newest patch of a supported line) aren't counted as steps: the
		// summary is what to do now.
		if target == nil {
			break
		}
		// "Update to <target>" only when some instance's step is the target itself; instances ahead of
		// the target that only need a patch in their own line must not read as "go back to the target".
		toTarget, steps := false, map[string]Action{} // distinct steps by text
		bundled, first := "", true                    // the distribution every step goes through, if they all do
		for _, in := range insts {
			if in.Action.Kind == ActionPatch || in.Action.Kind == ActionUpgrade {
				steps[in.Action.Text] = in.Action
				toTarget = toTarget || in.Action.To == target.Version
				if first {
					bundled, first = bundledName(in.Instance), false
				} else if in.BundledWith == "" || bundledName(in.Instance) != bundled {
					bundled = ""
				}
			}
		}
		// bundled: every step is an upgrade of that distribution, so say that instead of a version step.
		if toTarget {
			text := "Update to " + target.Version
			if bundled != "" {
				text = bundledText(bundled, target.Version)
			}
			return Action{ActionUpdate, text, worst.Action.Why, target.Version}
		}
		if len(steps) == 1 {
			for _, a := range steps {
				return Action{a.Kind, a.Text, worst.Action.Why, a.To}
			}
		}
		if bundled != "" {
			return Action{ActionUpgrade, bundledText(bundled, ""), worst.Action.Why, ""}
		}
		return Action{ActionPatch, "Patch each to the newest of its line", worst.Action.Why, ""}
	case needs[ActionPin]:
		return Action{ActionPin, "Pin an exact version", worst.Action.Why, ""}
	case needs[ActionCheck]:
		return Action{ActionCheck, worst.Action.Text, worst.Action.Why, ""}
	case needs[ActionPlan]:
		for _, in := range insts { // only planning left (nothing to do now): one target
			if in.Action.Kind == ActionPlan {
				return Action{ActionPlan, in.Action.Text, worst.Action.Why, in.Action.To}
			}
		}
	}
	return worst.Action
}
