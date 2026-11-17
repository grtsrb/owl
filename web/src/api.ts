import { useSyncExternalStore } from "react";

// Typed access to the server API. Shapes mirror the Go JSON types in internal/report, internal/inventory,
// internal/upstream and internal/store.

export type Level = "critical" | "high" | "medium" | "low" | "unknown" | "ok";
export const LEVELS: Level[] = ["critical", "high", "medium", "low", "unknown", "ok"];

export interface Reason { level: Level; text: string }

export interface RunningImage { container: string; image: string; imageID?: string; pods: number; ready: number }

export interface AffectedRange { range: string; patched: string }
export interface Advisory {
  id: string; aliases?: string[]; severity: string; summary: string; url: string; source: string;
  ranges?: AffectedRange[]; fixedIn?: string[];
}
/** relevantFix: the fixed version that matters for the running version (computed by the server). */
export interface Match extends Advisory { certain: boolean; noFix?: boolean; relevantFix?: string }

export interface Score {
  level: Level; reasons: Reason[]; notes?: string[]; line?: string; linesBehind: number;
  latestInLine?: string; advisories?: Match[];
  eol?: { line: string; since?: string }; review?: number;
}

export interface Instance {
  cluster: string; stage: string; tenant: string; namespace?: string; kind?: string; workload?: string;
  container?: string; image: string; version?: string; status: string; reason?: string;
  repository?: string; override?: "map" | "inhouse" | "ignore";
  desiredReplicas: number; running?: RunningImage[];
}
export type ActionKind = "none" | "patch" | "upgrade" | "replace" | "pin" | "check" | "update";
export interface Action { kind: ActionKind; text: string; why: string }
export interface ScoredInstance extends Instance { score: Score; action: Action }

export interface Target { policy: string; line: string; version: string; note?: string }
export interface Drift { kind: "pending_promotion" | "anomaly" | "only_in"; text: string; tenant: string; clusters: string[] }

export interface ComponentReport {
  id: string; name: string; level: Level; target?: Target; notes?: string;
  upstream: {
    latestStable?: string; retired: boolean; archived: boolean; lastReleaseAt?: string;
    advisorySource: string; stale?: boolean; fetchedAt?: string;
  };
  instances: ScoredInstance[]; drift: Drift[];
  /** The component's goal across all instances — independent of which clusters are shown. */
  action: Action;
}

export interface Report {
  generatedAt: string; summary: Partial<Record<Level, number>>; policies: Record<string, string>;
  components: ComponentReport[]; unpinned: number; inHouse: number; unrecognized: number;
  stages: string[];
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
  unpinned: Instance[]; inHouse: Instance[]; ignored: Instance[];
  unrecognized: { repository: string; instances: Instance[] }[];
}

export interface StageInfo { name: string; position: number; clusters: number }
export interface TenantInfo { name: string; clusters: number }
export interface CatalogOverride { repository: string; kind: "map" | "inhouse" | "ignore"; component?: string; updatedAt: string }
export interface ServerSettings { serverURL: string; agentImage: string }
export interface CatalogComponent { id: string; name: string; source?: string }

export interface OpsSettings {
  backupTime: string; backupLocalDays: number; backupLocalCopies: number;
  logLevel: "debug" | "info" | "warn" | "error"; logRetentionDays: number; auditRetentionDays: number;
}
export interface BackupStatus {
  target: string; dir: string; lastOK: string; lastAttempt: string; lastError?: string; pendingUploads: number;
  nextRun: string; warnings: string[];
}
export interface Ops { settings: OpsSettings; backupsEnabled: boolean; logFile: boolean; backup?: BackupStatus }
export interface BackupInfo { name: string; size: number; at: string; local: boolean; remote: boolean; uploaded: boolean }
export interface AuditEntry { id: number; at: string; action: string; details: string; remote: string; actor?: string }

export interface Policies { global: string; overrides: Record<string, string>; presets: string[] }

export class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

/** User-facing text for a failed write. */
export function writeErrorText(e: unknown): string {
  if (e instanceof ApiError && e.status === 401) return "Admin token missing or wrong — set it in Settings.";
  if (e instanceof ApiError && e.status === 403) return e.message;
  return e instanceof Error ? e.message : String(e);
}

async function request<T>(method: string, path: string, body?: unknown, admin = method !== "GET"): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (admin) {
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
  catalog: () => request<{ components: CatalogComponent[] }>("GET", "/api/v1/catalog"),

  stages: () => request<StageInfo[]>("GET", "/api/v1/stages"),
  addStage: (name: string) => request<StageInfo[]>("POST", "/api/v1/stages", { name }),
  reorderStages: (order: string[]) => request<StageInfo[]>("PUT", "/api/v1/stages", { order }),
  deleteStage: (name: string) => request<StageInfo[]>("DELETE", `/api/v1/stages/${encodeURIComponent(name)}`),

  tenants: () => request<TenantInfo[]>("GET", "/api/v1/tenants"),
  addTenant: (name: string) => request<TenantInfo[]>("POST", "/api/v1/tenants", { name }),
  renameTenant: (from: string, name: string) => request<TenantInfo[]>("PATCH", `/api/v1/tenants/${encodeURIComponent(from)}`, { name }),
  deleteTenant: (name: string) => request<TenantInfo[]>("DELETE", `/api/v1/tenants/${encodeURIComponent(name)}`),

  addCluster: (c: { name: string; stage: string; tenant: string }) =>
    request<{ cluster: Cluster; token: string }>("POST", "/api/v1/clusters", c),
  editCluster: (name: string, c: { stage: string; tenant: string }) =>
    request<Cluster>("PATCH", `/api/v1/clusters/${encodeURIComponent(name)}`, c),
  rotateToken: (name: string) => request<{ cluster: Cluster; token: string }>("POST", `/api/v1/clusters/${encodeURIComponent(name)}/rotate-token`),
  deleteCluster: (name: string) => request("DELETE", `/api/v1/clusters/${encodeURIComponent(name)}`),
  exportCluster: (name: string) => request<unknown>("GET", `/api/v1/clusters/${encodeURIComponent(name)}/export`, undefined, true),

  overrides: () => request<CatalogOverride[]>("GET", "/api/v1/catalog/overrides"),
  setOverride: (o: { repository: string; kind: CatalogOverride["kind"]; component?: string }) =>
    request<CatalogOverride[]>("PUT", "/api/v1/catalog/overrides", o),
  deleteOverride: (repository: string) =>
    request<CatalogOverride[]>("DELETE", `/api/v1/catalog/overrides?repository=${encodeURIComponent(repository)}`),

  settings: () => request<ServerSettings>("GET", "/api/v1/settings"),
  saveSettings: (s: ServerSettings) => request<ServerSettings>("PUT", "/api/v1/settings", s),
  agentManifestURL: "/api/v1/agent/manifest",

  ops: () => request<Ops>("GET", "/api/v1/ops", undefined, true), // admin-only read
  saveOps: (o: OpsSettings) => request<Ops>("PUT", "/api/v1/ops", o),
  backups: () => request<{ backups: BackupInfo[]; remoteError?: string }>("GET", "/api/v1/backups", undefined, true),
  backupNow: () => request<BackupInfo>("POST", "/api/v1/backups"),
  restoreBackup: (name: string) => request<{ safetyBackup: string }>("POST", `/api/v1/backups/${encodeURIComponent(name)}/restore`),
  restoreUpload: (file: File) => rawPost<{ safetyBackup: string }>("/api/v1/restore", file,
    file.name.endsWith(".gz") ? "application/gzip" : "application/octet-stream"),
  importCluster: (exportJSON: string) => rawPost<{ cluster: Cluster; token: string }>("/api/v1/clusters/import", exportJSON, "application/json"),
  audit: (limit = 1000) => request<AuditEntry[]>("GET", `/api/v1/audit?limit=${limit}`, undefined, true),
  /** Fetches an admin-only file (the token goes in a header, never in a URL) and saves it. */
  downloadBackup: async (name: string) => {
    const res = await fetch(`/api/v1/backups/${encodeURIComponent(name)}`, { headers: { Authorization: `Bearer ${adminToken.get()}` } });
    if (!res.ok) throw new ApiError(res.status, (await res.text()) || res.statusText);
    const url = URL.createObjectURL(await res.blob());
    const a = document.createElement("a");
    a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  },
};

async function rawPost<T>(path: string, body: BodyInit, contentType: string): Promise<T> {
  const res = await fetch(path, { method: "POST", body, headers: { "Content-Type": contentType, Accept: "application/json", Authorization: `Bearer ${adminToken.get()}` } });
  const text = await res.text();
  let data: unknown = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* not JSON */ }
  if (!res.ok) throw new ApiError(res.status, (data as { error?: string } | null)?.error ?? (text || res.statusText));
  return data as T;
}

// The admin token lives only in this tab's sessionStorage (cleared when the tab closes) — never in
// localStorage or a cookie. Interim until real sign-in.
const KEY = "owl-admin-token";
const tokenListeners = new Set<() => void>();
export const adminToken = {
  get: (): string => { try { return sessionStorage.getItem(KEY) ?? ""; } catch { return ""; } },
  set: (t: string) => {
    try { t ? sessionStorage.setItem(KEY, t) : sessionStorage.removeItem(KEY); } catch { /* storage blocked */ }
    tokenListeners.forEach((f) => f()); // every component using useAdminToken re-renders
  },
  subscribe: (f: () => void) => { tokenListeners.add(f); return () => { tokenListeners.delete(f); }; },
};

/** The admin token, re-rendering the caller when it's set or forgotten anywhere in the app. */
export function useAdminToken(): string {
  return useSyncExternalStore(adminToken.subscribe, adminToken.get);
}

/** Cleans up what people paste: the whole `OWL_ADMIN_TOKEN=…` line from .env, quotes, "Bearer ". */
export function normalizeToken(raw: string): string {
  return raw.trim().replace(/^(export\s+)?OWL_ADMIN_TOKEN\s*=\s*/, "").replace(/^Bearer\s+/i, "").replace(/^(["'])(.*)\1$/, "$2").trim();
}

/** Checks a token against the server (an admin-only read) without storing it. */
export async function verifyAdminToken(token: string): Promise<void> {
  const res = await fetch("/api/v1/ops", { headers: { Accept: "application/json", Authorization: `Bearer ${token}` } });
  if (res.ok) return;
  let msg = res.statusText;
  try { msg = ((await res.json()) as { error?: string }).error ?? msg; } catch { /* not JSON */ }
  throw new ApiError(res.status, res.status === 401 ? "The server rejected this token — check it matches OWL_ADMIN_TOKEN on the server." : msg);
}

/** Save text as a file (blob URL: no server round trip, so a token never travels in a URL). */
export function download(filename: string, text: string, type = "application/yaml") {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** The token Secret for a cluster's agent. Values are JSON strings, which are valid YAML scalars. */
export function agentSecretYAML(serverURL: string, token: string): string {
  const lines = [
    "# Agent token for Owl. Apply, then delete this file:",
    "#   kubectl apply -f <this file>",
    "apiVersion: v1",
    "kind: Secret",
    "metadata:",
    "  name: owl-agent",
    "  namespace: owl",
    "type: Opaque",
    "stringData:",
    `  OWL_SERVER_URL: ${JSON.stringify(serverURL || "https://SET-SERVER-URL-IN-SETTINGS")}`,
    `  OWL_TOKEN: ${JSON.stringify(token)}`,
  ];
  if (serverURL.startsWith("http://")) lines.push(`  OWL_ALLOW_INSECURE_HTTP: "true" # plain HTTP: local development only`);
  return lines.join("\n") + "\n";
}
