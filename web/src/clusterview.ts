// Everything running in one cluster, combined from the report (scored components) and the inventory
// (in-house, unrecognized, ignored images).
import type { Inventory, Level, Report } from "./api";

export interface Flag { text: string; tone: string; title?: string }

export interface WorkloadRow {
  key: string;
  namespace: string; workload: string; kind: string; container: string; image: string;
  componentId?: string; componentName?: string; version?: string;
  level?: Level;
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
      if (i.status === "unpinned") flags.push({ text: "unpinned", tone: "warn", title: i.reason });
      if (i.override === "map") flags.push({ text: "mapped", tone: "info", title: `You mapped ${i.repository} to ${c.name}` });
      if (i.desiredReplicas === 0 && i.kind !== "CronJob" && i.kind !== "Job") flags.push({ text: "scaled to 0", tone: "" });
      if ((i.running?.length ?? 0) > 1) flags.push({ text: "rollout in progress", tone: "warn" });
      rows.push({ ...base(i), key: `${c.id}/${i.namespace}/${i.workload}/${i.container}`, componentId: c.id, componentName: c.name,
        version: i.version, level: i.score.level, group: "third", flags });
    }
  }
  for (const i of inv.inHouse.filter((x) => x.cluster === cluster)) {
    rows.push({ ...base(i), key: `ih/${i.namespace}/${i.workload}/${i.container}`, version: i.version, group: "inhouse",
      flags: [{ text: i.override === "inhouse" ? "in-house (marked)" : "in-house", tone: "info" }] });
  }
  for (const u of inv.unrecognized) {
    for (const i of u.instances.filter((x) => x.cluster === cluster)) {
      rows.push({ ...base(i), key: `un/${i.namespace}/${i.workload}/${i.container}`, group: "unrecognized",
        flags: [{ text: "unrecognized", tone: "warn", title: "Not in the catalog: nothing is checked" }] });
    }
  }
  for (const i of inv.ignored.filter((x) => x.cluster === cluster)) {
    rows.push({ ...base(i), key: `ig/${i.namespace}/${i.workload}/${i.container}`, group: "ignored", flags: [{ text: "ignored", tone: "" }] });
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
