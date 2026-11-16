// Everything running in one cluster, combined from the report (scored components) and the inventory
// (in-house, unrecognized, ignored images).
import type { Inventory, Level, Report } from "./api";

export interface Flag { text: string; tone: string; title: string }

/** Every flag with its plain-language meaning (tooltip and the legend under the table). */
export const FLAG = {
  unpinned: { text: "unpinned", tone: "warn", title: "The tag (e.g. :latest or :16) doesn't say which version runs, so it can't be checked. Pin an exact version." },
  scaled0: { text: "scaled to 0", tone: "", title: "0 replicas: nothing runs right now, but the image is still deployed and would start if scaled up, so it's checked like any other." },
  rollout: { text: "rollout in progress", tone: "warn", title: "Pods run more than one image version: an update is half-way (or stuck)." },
  mapped: { text: "mapped", tone: "info", title: "You mapped this image to a component (Settings → Catalog overrides)." },
  inhouse: { text: "in-house", tone: "info", title: "From an internal registry: our own software. Listed for inventory, not checked against upstream." },
  inhouseMarked: { text: "in-house (marked)", tone: "info", title: "You marked this image as our own software. Listed, not checked against upstream." },
  unrecognized: { text: "unrecognized", tone: "warn", title: "Not in the catalog, so nothing is checked. Map it to a component, mark it in-house or ignore it (below)." },
  ignored: { text: "ignored", tone: "", title: "You chose to ignore this image. Undo in Settings → Catalog overrides." },
} satisfies Record<string, Flag>;

export interface WorkloadRow {
  key: string;
  namespace: string; workload: string; kind: string; container: string; image: string;
  componentId?: string; componentName?: string; version?: string;
  level?: Level;
  /** Why this level: the instance's action ("Upgrade to 1.14.7") and reasons, worst first. */
  action?: string; why?: string; reasons?: string[];
  group: "third" | "inhouse" | "unrecognized" | "ignored";
  flags: Flag[];
}

export function workloadsFor(cluster: string, report: Report, inv: Inventory): WorkloadRow[] {
  const rows: WorkloadRow[] = [];
  const base = (i: { namespace?: string; workload?: string; kind?: string; container?: string; image: string }) => ({
    namespace: i.namespace ?? "", workload: i.workload ?? "", kind: i.kind ?? "", container: i.container ?? "", image: i.image,
  });
  for (const c of report.components) {
    for (const i of c.instances) {
      if (i.cluster !== cluster || !i.namespace) continue; // cluster-version components live in the header
      const flags: Flag[] = [];
      if (i.status === "unpinned") flags.push({ ...FLAG.unpinned, title: `${FLAG.unpinned.title} (${i.reason})` });
      if (i.override === "map") flags.push({ ...FLAG.mapped, title: `You mapped ${i.repository} to ${c.name}.` });
      if (i.desiredReplicas === 0 && i.kind !== "CronJob" && i.kind !== "Job") flags.push(FLAG.scaled0);
      if ((i.running?.length ?? 0) > 1) flags.push(FLAG.rollout);
      rows.push({ ...base(i), key: `${c.id}/${i.namespace}/${i.kind}/${i.workload}/${i.container}`, componentId: c.id, componentName: c.name,
        version: i.version, level: i.score.level, action: i.action.text, why: i.action.why,
        reasons: i.score.reasons.map((r) => r.text), group: "third", flags });
    }
  }
  for (const i of inv.inHouse.filter((x) => x.cluster === cluster)) {
    rows.push({ ...base(i), key: `ih/${i.namespace}/${i.kind}/${i.workload}/${i.container}`, version: i.version, group: "inhouse",
      flags: [i.override === "inhouse" ? FLAG.inhouseMarked : FLAG.inhouse] });
  }
  for (const u of inv.unrecognized) {
    for (const i of u.instances.filter((x) => x.cluster === cluster)) {
      rows.push({ ...base(i), key: `un/${u.repository}/${i.namespace}/${i.kind}/${i.workload}/${i.container}`, group: "unrecognized",
        flags: [FLAG.unrecognized] });
    }
  }
  for (const i of inv.ignored.filter((x) => x.cluster === cluster)) {
    rows.push({ ...base(i), key: `ig/${i.namespace}/${i.kind}/${i.workload}/${i.container}`, group: "ignored", flags: [FLAG.ignored] });
  }
  return rows.sort((a, b) => a.namespace.localeCompare(b.namespace) || a.workload.localeCompare(b.workload) || a.container.localeCompare(b.container));
}

/** Distinct workloads (a workload with three containers counts once). */
export function workloadCount(rows: WorkloadRow[]): number {
  return new Set(rows.map((r) => `${r.namespace}/${r.kind}/${r.workload}`)).size;
}

export function unrecognizedIn(cluster: string, inv: Inventory) {
  return inv.unrecognized.filter((u) => u.instances.some((i) => i.cluster === cluster));
}
