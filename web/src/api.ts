// Typed access to the server API. Shapes mirror the Go JSON types in internal/report, internal/inventory,
// internal/upstream and internal/store.

export type Level = "critical" | "high" | "medium" | "low" | "unknown" | "ok";
export const LEVELS: Level[] = ["critical", "high", "medium", "low", "unknown", "ok"];

export interface Reason { level: Level; text: string }

export interface RunningImage { container: string; image: string; imageID?: string; pods: number; ready: number }

export interface Advisory {
  id: string; aliases?: string[]; severity: string; summary: string; url: string; source: string;
  fixedIn?: string[];
}
export interface Match extends Advisory { certain: boolean; noFix?: boolean }

export interface Score {
  level: Level; reasons: Reason[]; notes?: string[]; line?: string; linesBehind: number;
  latestInLine?: string; advisories?: Match[];
}

export interface Instance {
  cluster: string; stage: string; tenant: string; namespace?: string; kind?: string; workload?: string;
  container?: string; image: string; version?: string; status: string; reason?: string;
  desiredReplicas: number; running?: RunningImage[];
}
export interface ScoredInstance extends Instance { score: Score }

export interface Target { policy: string; line: string; version: string; note?: string }
export interface Drift { kind: "pending_promotion" | "anomaly"; text: string }

export interface ComponentReport {
  id: string; name: string; level: Level; target?: Target; notes?: string;
  upstream: {
    latestStable?: string; retired: boolean; archived: boolean; lastReleaseAt?: string;
    advisorySource: string; stale?: boolean; fetchedAt?: string;
  };
  instances: ScoredInstance[]; drift: Drift[];
}

export interface Report {
  generatedAt: string; summary: Partial<Record<Level, number>>; policies: Record<string, string>;
  components: ComponentReport[]; unpinned: number; inHouse: number; unrecognized: number;
}

export interface Cluster {
  name: string; stage: string; tenant: string; kubernetesVersion: string; agentVersion: string;
  lastSeenAt: string | null;
}

export interface Change {
  id: number; cluster: string; at: string; type: string; namespace?: string; kind?: string;
  workload?: string; container?: string; oldValue?: string; newValue?: string;
}

export interface ReleaseLine {
  name: string; latest: string; latestDate?: string; lts: boolean; eolDate?: string; eol: boolean;
  maintained?: boolean;
}
export interface UpstreamInfo {
  componentId: string; lines: ReleaseLine[]; latestStable?: string; linesSource?: string;
  archived: boolean; retired: boolean; lastReleaseAt?: string; advisorySource: string;
  fetchedAt: string; stale?: boolean; errors?: string[];
}
export interface ComponentDetail {
  component: { id: string; name: string; github?: string; eol?: string; notes?: string };
  upstream: UpstreamInfo | null;
  advisoriesByVersion: { version: string; advisories: Match[]; error?: string }[];
}

export interface Inventory {
  unpinned: Instance[]; inHouse: Instance[];
  unrecognized: { repository: string; instances: Instance[] }[];
}

export interface Policies { global: string; overrides: Record<string, string>; presets: string[] }

export class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET") {
    const token = adminToken.get();
    if (token) headers.Authorization = `Bearer ${token}`;
  }
  const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await res.text();
  let data: unknown = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* non-JSON error page */ }
  if (!res.ok) {
    const msg = (data as { error?: string } | null)?.error ?? (text || res.statusText);
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

export const api = {
  report: () => request<Report>("GET", "/api/v1/report"),
  component: (id: string) => request<ComponentDetail>("GET", `/api/v1/components/${encodeURIComponent(id)}`),
  clusters: () => request<Cluster[]>("GET", "/api/v1/clusters"),
  changes: (cluster = "", limit = 300) =>
    request<Change[]>("GET", `/api/v1/changes?limit=${limit}${cluster ? `&cluster=${encodeURIComponent(cluster)}` : ""}`),
  inventory: () => request<Inventory>("GET", "/api/v1/inventory"),
  policies: () => request<Policies>("GET", "/api/v1/policies"),
  setPolicy: (scope: string, policy: string) => request("PUT", `/api/v1/policies/${encodeURIComponent(scope)}`, { policy }),
  clearPolicy: (scope: string) => request("DELETE", `/api/v1/policies/${encodeURIComponent(scope)}`),
};

// The admin token lives only in this tab's sessionStorage (cleared when the tab closes) — never in
// localStorage or a cookie. Interim until real sign-in.
const KEY = "inv-admin-token";
export const adminToken = {
  get: (): string => { try { return sessionStorage.getItem(KEY) ?? ""; } catch { return ""; } },
  set: (t: string) => { try { t ? sessionStorage.setItem(KEY, t) : sessionStorage.removeItem(KEY); } catch { /* storage blocked */ } },
};

export const stageOrder: Record<string, number> = { staging: 1, prelive: 2, live: 3 };
