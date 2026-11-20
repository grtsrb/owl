import { LevelBadge } from "../ui";
import { DocHelp } from "../pages/Docs";
import { DOC_HELP } from "../dochelp";

/** The priority rules in plain words (DESIGN.md "Priority"). Levels are rule-based; this only explains them. */
export default function LevelHelp() {
  return (
    <details className="level-help">
      <summary>What do the levels mean?<DocHelp to={DOC_HELP.levels} label="priority levels" /></summary>
      <dl>
        <div><dt><LevelBadge level="critical" /></dt><dd>A high or critical security advisory that has a fix, its release line is past end-of-life, or the project is retired upstream.</dd></div>
        <div><dt><LevelBadge level="high" /></dt><dd>End-of-life within 90 days, or 2 or more release lines behind the target your version policy sets.</dd></div>
        <div><dt><LevelBadge level="medium" /></dt><dd>1 release line behind the target, an unpinned tag, or a moderate / unknown-severity advisory.</dd></div>
        <div><dt><LevelBadge level="low" /></dt><dd>Only a patch release is available (or a low-severity advisory).</dd></div>
        <div><dt><LevelBadge level="ok" /></dt><dd>At or above the target, on the newest patch of its line.</dd></div>
        <div><dt><LevelBadge level="unknown" /></dt><dd>No upstream release data, so it can't be judged (never shown as OK).</dd></div>
      </dl>
      <p className="subtle">A component's level is its worst instance. Notes like “advisories need review” or “no advisory data” are shown but never change the level. The target comes from the version policy (Settings → Version policy, or per component).</p>
    </details>
  );
}
