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

// Claude upgrade summaries (internal/summary). Upgrade risk is Claude's estimate, never a priority level.
export type SummaryMode = "off" | "estimate" | "manual" | "auto";
export interface SummaryItem { text: string; version: string }
export interface NoteSource { version: string; url: string }
export interface SummaryView {
  headline: string;
  breakingChanges: SummaryItem[]; migrations: SummaryItem[]; deprecations: SummaryItem[];
  securityFixes: SummaryItem[]; notableFeatures: SummaryItem[];
  upgradePath: { direct: "direct_supported" | "via_intermediate" | "not_stated"; via: string[]; reason: string };
  risk: { level: "low" | "medium" | "high"; reason: string };
  sources: NoteSource[]; model: string; promptVersion: number; createdAt: string;
}
export type JumpState = "ready" | "off" | "collecting" | "no_notes" | "error" | "estimate" | "no_key" | "paused"
  | "too_large" | "failed" | "budget" | "available" | "queued";
export interface JumpView {
  component: string; from: string; to: string; name: string; state: JumpState;
  tokens?: number; exact?: boolean; maxCostMicros?: number; sources?: NoteSource[]; error?: string; summary?: SummaryView;
}
export interface SummaryOverview { mode: SummaryMode; enabled: boolean; paused: boolean; jumps: JumpView[] }
export interface SummarySettings { mode: SummaryMode; dailyCapMicros: number; monthlyCapMicros: number; maxInputTokens: number; maxOutputTokens: number }
export interface SummaryRequestLog {
  id: number; at: string; component: string; from: string; to: string; model: string;
  inputTokens: number; outputTokens: number; costMicros: number; result: "ok" | "failed" | "resumed"; error?: string;
}
export interface SummaryStatus {
  settings: SummarySettings; enabled: boolean; model: string; fallback?: string; price?: { Input: number; Output: number };
  paused?: string; spentTodayMicros: number; spentMonthMicros: number; spentTotalMicros: number; summaries: number;
  requests: SummaryRequestLog[]; maxPerSummaryMicros: number;
}

/** Micro-dollars as money: $0.0123 below a cent, else cents. */
export function usd(micros: number | undefined): string {
  const m = micros ?? 0;
  return m > 0 && m < 10_000 ? `$${(m / 1e6).toFixed(4)}` : `$${(m / 1e6).toFixed(2)}`;
}

export interface Policies { global: string; overrides: Record<string, string>; presets: string[] }

export class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

/** User-facing text for a failed write. */
export function writeErrorText(e: unknown): string {
  if (e instanceof ApiError && e.status === 401) return "Your session ended — sign in again.";
  if (e instanceof ApiError && e.status === 403) return e.message;
  return e instanceof Error ? e.message : String(e);
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  // Same-origin fetch sends the session cookie (HttpOnly: scripts can't read it).
  const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  return parse<T>(res, path);
}

async function parse<T>(res: Response, path: string): Promise<T> {
  const text = await res.text();
  let data: unknown = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* non-JSON error page */ }
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith("/api/v1/auth/")) session.set(null); // signed out elsewhere or expired
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
  exportCluster: (name: string) => request<unknown>("GET", `/api/v1/clusters/${encodeURIComponent(name)}/export`),

  overrides: () => request<CatalogOverride[]>("GET", "/api/v1/catalog/overrides"),
  setOverride: (o: { repository: string; kind: CatalogOverride["kind"]; component?: string }) =>
    request<CatalogOverride[]>("PUT", "/api/v1/catalog/overrides", o),
  deleteOverride: (repository: string) =>
    request<CatalogOverride[]>("DELETE", `/api/v1/catalog/overrides?repository=${encodeURIComponent(repository)}`),

  settings: () => request<ServerSettings>("GET", "/api/v1/settings"),
  saveSettings: (s: ServerSettings) => request<ServerSettings>("PUT", "/api/v1/settings", s),
  agentManifestURL: "/api/v1/agent/manifest",

  ops: () => request<Ops>("GET", "/api/v1/ops"), // admin-only read
  saveOps: (o: OpsSettings) => request<Ops>("PUT", "/api/v1/ops", o),
  backups: () => request<{ backups: BackupInfo[]; remoteError?: string }>("GET", "/api/v1/backups"),
  backupNow: () => request<BackupInfo>("POST", "/api/v1/backups"),
  restoreBackup: (name: string) => request<{ safetyBackup: string }>("POST", `/api/v1/backups/${encodeURIComponent(name)}/restore`),
  restoreUpload: (file: File) => rawPost<{ safetyBackup: string }>("/api/v1/restore", file,
    file.name.endsWith(".gz") ? "application/gzip" : "application/octet-stream"),
  importCluster: (exportJSON: string) => rawPost<{ cluster: Cluster; token: string }>("/api/v1/clusters/import", exportJSON, "application/json"),
  summaries: () => request<SummaryOverview>("GET", "/api/v1/summaries"),
  summaryStatus: () => request<SummaryStatus>("GET", "/api/v1/summaries/status"), // admin-only read
  saveSummarySettings: (s: SummarySettings) => request<SummaryStatus>("PUT", "/api/v1/summaries/settings", s),
  generateSummary: (j: { component: string; from: string; to: string }, force = false) =>
    request<SummaryOverview>("POST", "/api/v1/summaries/generate", { component: j.component, from: j.from, to: j.to, force }),
  resumeSummaries: () => request<SummaryStatus>("POST", "/api/v1/summaries/resume"),
  audit: (limit = 1000) => request<AuditEntry[]>("GET", `/api/v1/audit?limit=${limit}`),

  // Sign-in and users (milestone 9).
  authConfig: () => request<AuthConfig>("GET", "/api/v1/auth/config"),
  me: () => request<Me>("GET", "/api/v1/auth/me"),
  login: (email: string, password: string) => request<Me>("POST", "/api/v1/auth/login", { email, password }),
  logout: () => request("POST", "/api/v1/auth/logout", {}),
  tokenInfo: (token: string) => request<TokenInfo>("POST", "/api/v1/auth/token", { token }),
  accept: (token: string, password: string) => request<Me>("POST", "/api/v1/auth/accept", { token, password }),
  forgot: (email: string) => request<{ message: string }>("POST", "/api/v1/auth/forgot", { email }),
  changePassword: (current: string, next: string) => request("POST", "/api/v1/auth/password", { current, new: next }),
  users: () => request<User[]>("GET", "/api/v1/users"),
  inviteUser: (email: string, role: Role) => request<LinkResult>("POST", "/api/v1/users", { email, role }),
  updateUser: (id: number, role: Role, disabled: boolean) => request<User>("PATCH", `/api/v1/users/${id}`, { role, disabled }),
  deleteUser: (id: number) => request("DELETE", `/api/v1/users/${id}`),
  reinviteUser: (id: number) => request<LinkResult>("POST", `/api/v1/users/${id}/invite`),
  resetUser: (id: number) => request<LinkResult>("POST", `/api/v1/users/${id}/reset`),
  authSettings: () => request<AuthSettings>("GET", "/api/v1/auth/settings"),
  saveAuthSettings: (passwordLogin: boolean) => request<AuthSettings>("PUT", "/api/v1/auth/settings", { passwordLogin }),
  testMail: () => request<{ to: string }>("POST", "/api/v1/mail/test"),
  /** Fetches an admin-only file and saves it. */
  downloadBackup: async (name: string) => {
    const res = await fetch(`/api/v1/backups/${encodeURIComponent(name)}`);
    if (!res.ok) throw new ApiError(res.status, (await res.text()) || res.statusText);
    const url = URL.createObjectURL(await res.blob());
    const a = document.createElement("a");
    a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  },
};

async function rawPost<T>(path: string, body: BodyInit, contentType: string): Promise<T> {
  const res = await fetch(path, { method: "POST", body, headers: { "Content-Type": contentType, Accept: "application/json" } });
  return parse<T>(res, path);
}

export type Role = "admin" | "viewer";
export interface Me { email: string; role: Role; hasPassword: boolean; hasGoogle: boolean }
export interface AuthConfig { password: boolean; google: boolean; forgot: boolean }
export interface TokenInfo { email: string; kind: "invite" | "reset"; password: boolean; google: boolean }
export interface User {
  id: number; email: string; role: Role; createdAt: string; activatedAt?: string; lastLoginAt?: string;
  disabledAt?: string; hasPassword: boolean; hasGoogle: boolean;
}
export interface LinkResult { user: User; link: string; emailed: boolean; emailError?: string }
export interface AuthSettings {
  passwordLogin: boolean;
  google: { configured: boolean; domain?: string; redirectURL?: string; linkedAdmins: number };
  mail: { configured: boolean; host?: string; port?: number; from?: string; tls?: string };
  publicURL?: string;
}

/** A link from the server: absolute when Owl knows its public URL, else a path on this origin. */
export function absoluteLink(link: string): string {
  return link.startsWith("/") ? window.location.origin + link : link;
}

// The signed-in user: undefined while loading, null when signed out. Any 401 from the API signs the
// UI out (the session expired, or the user was disabled).
let current: Me | null | undefined;
const sessionListeners = new Set<() => void>();
export const session = {
  get: () => current,
  set: (m: Me | null) => { current = m; sessionListeners.forEach((f) => f()); },
  subscribe: (f: () => void) => { sessionListeners.add(f); return () => { sessionListeners.delete(f); }; },
  load: async () => {
    try { session.set(await api.me()); } catch (e) { if (e instanceof ApiError && e.status === 401) session.set(null); else throw e; }
  },
};

export function useMe(): Me | null | undefined {
  return useSyncExternalStore(session.subscribe, session.get);
}

/** Whether the signed-in user may change things (admin role). */
export function useIsAdmin(): boolean {
  return useMe()?.role === "admin";
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
