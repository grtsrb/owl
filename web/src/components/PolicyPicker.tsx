// Choosing a version policy: a list of the named policies (with what each means) and, per component
// only, a pin to a release line or version. The server refuses a pin as the global policy — one version
// can't be the target of every component.

/** Named policies, in the order offered. */
export const POLICY_CHOICES: { value: string; label: string; hint: string }[] = [
  { value: "lts-or-n-1", label: "lts-or-n-1 (default)", hint: "The newest LTS line if the product has one, otherwise the previous release line." },
  { value: "lts", label: "lts", hint: "Same as lts-or-n-1: the newest LTS line, or the previous release line without one." },
  { value: "latest", label: "latest", hint: "The newest release line." },
  { value: "n-1", label: "n-1", hint: "One release line behind the newest." },
  { value: "n-2", label: "n-2", hint: "Two release lines behind the newest." },
];

const PIN = "pin";

/** Splits a stored policy into the list choice and the pinned version ("pin:16" → ["pin", "16"]). */
export function splitPolicy(p: string): [string, string] {
  if (p.startsWith("pin:")) return [PIN, p.slice(4)];
  return [p, ""];
}

/** The policy string for a choice ("" = none chosen / inherit). */
export function joinPolicy(choice: string, pin: string): string {
  return choice === PIN ? (pin.trim() ? `pin:${pin.trim()}` : "") : choice;
}

export function policyHint(choice: string, inherited?: string): string {
  if (choice === "") return `Uses the global policy (${inherited}).`;
  if (choice === PIN) return "Always target this release line or exact version, e.g. 16 or 1.36 or 1.36.4.";
  return POLICY_CHOICES.find((c) => c.value === choice)?.hint ?? "";
}

/** The select (and pin field). inherited set = per-component: offers "Use global" and pinning. */
export function PolicyPicker({ id, label, choice, pin, onChoice, onPin, inherited, disabled }: {
  id: string; label: string; choice: string; pin: string; onChoice: (c: string) => void; onPin: (p: string) => void;
  inherited?: string; disabled?: boolean;
}) {
  const perComponent = inherited !== undefined;
  // Anything else (e.g. a global pin stored before it was refused) is shown as itself, so nothing changes by accident.
  const known = POLICY_CHOICES.some((c) => c.value === choice) || perComponent && (choice === "" || choice === PIN);
  return (
    <>
      <select id={id} aria-label={label} className="select mono" value={choice} disabled={disabled} onChange={(e) => onChoice(e.target.value)}>
        {perComponent && <option value="">Use global ({inherited})</option>}
        {POLICY_CHOICES.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
        {!known && <option value={choice}>{choice} (not offered any more)</option>}
        {perComponent && <option value={PIN}>pin a line or version…</option>}
      </select>
      {choice === PIN && (
        <input id={`${id}-pin`} className="input mono" style={{ width: "9em" }} placeholder="e.g. 16" aria-label="Pinned line or version"
          value={pin} disabled={disabled} onChange={(e) => onPin(e.target.value)} />
      )}
    </>
  );
}
