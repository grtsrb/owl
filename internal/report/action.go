package report

import (
	"fmt"
	"strings"

	"service-inventory/internal/catalog"
	"service-inventory/internal/inventory"
	"service-inventory/internal/policy"
	"service-inventory/internal/upstream"
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
)

// Action is the recommended next step as one sentence: Text says what ("Upgrade to 1.14.7"), Why the
// most important reasons ("fixes 9 high advisories; 1.13 is EOL since 2026-06-28").
type Action struct {
	Kind ActionKind `json:"kind"`
	Text string     `json:"text"`
	Why  string     `json:"why"`
}

// ActionFor turns a score into the recommended action for one instance.
func ActionFor(in inventory.Instance, sc Score, target *policy.Target, info *upstream.Info) Action {
	switch {
	case in.Status == string(catalog.StatusUnpinned):
		return Action{ActionPin, "Pin an exact version", "tag " + tagOf(in.Image) + " doesn't say which version runs"}
	case info != nil && info.Retired:
		why := "retired upstream"
		if s := advisorySummary(sc.Advisories); s != "" {
			why += "; " + s + " open"
		}
		return Action{ActionReplace, "Replace", why}
	case sc.Level == LevelUnknown:
		return Action{ActionCheck, "Check manually", firstReason(sc, LevelUnknown)}
	case sc.Level == LevelOK:
		return Action{ActionNone, "Up to date", "at target, latest patch"}
	}

	var why []string
	if s := advisorySummary(sc.Advisories); s != "" {
		why = append(why, "fixes "+s)
	}
	for _, r := range sc.Reasons {
		if strings.HasPrefix(r.Text, "release line ") && strings.Contains(r.Text, "end-of-life") {
			why = append(why, strings.Replace(strings.TrimPrefix(r.Text, "release line "), "is end-of-life", "is EOL", 1))
			break
		}
	}
	if sc.LinesBehind >= 1 && len(why) < 2 {
		why = append(why, plural(sc.LinesBehind, "release line")+" behind")
	}

	if target == nil {
		return Action{ActionCheck, "Check manually", "no version target for the policy"}
	}
	v, okV := catalog.ParseVersion(in.Version, "")
	tv, okT := catalog.ParseVersion(target.Version, "")
	if sc.LinesBehind == 0 {
		to := target.Version
		if okV && okT && v.Compare(tv) >= 0 && sc.LatestInLine != "" {
			to = sc.LatestInLine // already at/above target: the patch is the newest of its own line
		}
		if len(why) == 0 {
			why = append(why, "patch release")
		}
		return Action{ActionPatch, "Patch to " + to, strings.Join(why, "; ")}
	}
	if len(why) == 0 {
		why = append(why, "behind target ("+target.Policy+")")
	}
	return Action{ActionUpgrade, "Upgrade to " + target.Version, strings.Join(why, "; ")}
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

func plural(n int, word string) string {
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
