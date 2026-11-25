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
  eol?: { line: string; since?: string }; review?: number; eolSoon?: string;
  /** 2+ lines behind on a line upstream still supports ("8.19 supported until 2027-07-15"): why it's Medium. */
  supported?: string;
  /** An older version the pods still run that decided the level; templateLevel is the template version's own level then. */
  stillRunning?: string; templateLevel?: Level;
}

export interface Instance {
  cluster: string; stage: string; tenant: string; namespace?: string; kind?: string; workload?: string;
  container?: string; image: string; version?: string; status: string; reason?: string;
  repository?: string; override?: "map" | "inhouse" | "ignore";
  /** What ships and upgrades this image itself: the cluster's distribution or a component (cilium). */
  bundledWith?: string; bundledName?: string;
  desiredReplicas: number; running?: RunningImage[];
  /** Other versions the pods still run (left from a rollout), oldest first; notRolledOut: no pod runs the template's image. */
  runningVersions?: string[]; notRolledOut?: boolean;
}
export type ActionKind = "none" | "patch" | "upgrade" | "replace" | "pin" | "check" | "update" | "plan" | "rollout";
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
  /** Scanned images this component runs; trivyConfirmed: advisory ids Trivy found in them too. */
  imageScans: VulnImage[]; trivyConfirmed: string[];
}

// Image vulnerability scans (milestone 15, internal/server/vulns.go + internal/scan). A separate signal:
// never a priority level.
export type Severity = "critical" | "high" | "medium" | "low" | "unknown";
export const SEVERITIES: Severity[] = ["critical", "high", "medium", "low", "unknown"];
export type SevCounts = Record<Severity, number>;
export type VulnState = "ok" | "failed" | "scanning" | "queued" | "not scanned yet" | "not scannable";
export interface VulnPlace { cluster: string; stage: string; tenant: string; namespace: string; kind: string; workload: string; container: string }
export interface VulnImage {
  key: string; digest?: string; ref: string; repositories: string[]; inHouse: boolean; components: string[];
  places: VulnPlace[]; state: VulnState; queue?: "scanning" | "queued"; scannedAt?: string; error?: string; os?: string;
  /** Not accepted findings; fixable = with a fixed version. */
  fixable: SevCounts; total: SevCounts; accepted: number; confirmed: number;
}
export interface Acceptance { repository: string; vulnId: string; reason: string; expiresAt?: string; createdBy: string; createdAt: string }
export interface Vuln {
  id: string; package: string; installed: string; fixed?: string; severity: Severity; class: "os" | "lang";
  pkgType?: string; target?: string; title?: string; url?: string;
  accepted?: Acceptance; expiredAcceptance?: boolean;
}
export interface VulnDetail { image: VulnImage; vulns: Vuln[]; confirmed: { id: string; component: string }[] }
export interface ScanJob { digest: string; ref: string; repository: string; platform?: string; reason: "new" | "daily" | "manual"; queuedAt: string }
export interface ScannerStatus {
  current: (ScanJob & { startedAt: string; timeoutSeconds: number }) | null;
  queue: ScanJob[]; queueLength: number;
  recent: (ScanJob & { startedAt: string; tookMs: number; outcome: "ok" | "failed" | "cancelled" | "timeout" | "removed from queue"; error?: string })[];
  db: { version: string; updatedAt: string; downloadedAt: string; error?: string; updating: boolean };
  lastDaily: string | null;
  authFailures: { prefix: string; ref: string; at: string }[];
  credentialPrefixes: string[];
  credentialsExpiring: { prefix: string; expiresAt: string; expired: boolean }[];
}

// Registry credentials Trivy pulls with (milestone 15.1, internal/server/registries.go). Write-only: a
// password is sent once and never comes back.
export type LoginOutcome = "ok" | "rejected" | "dns" | "connect" | "tls" | "unexpected";
export interface LoginResult { outcome: LoginOutcome; message: string; at: string }
export interface RegistryCredential {
  prefix: string; username: string; source: "ui" | "file"; expiresAt?: string; updatedAt?: string; updatedBy?: string;
  problem?: string; authFailedAt?: string; lastTest?: LoginResult;
}
export interface RegistriesView { credentials: RegistryCredential[]; canEdit: boolean; scanning: boolean; scansOff: string }

export interface ComponentView {
  id: string; name: string; github?: string; eol?: string; notes?: string; instances: Instance[];
  /** cluster -> distinct versions there */
  versionsByCluster: Record<string, string[]>;
}
export interface Inventory {
  components: ComponentView[];
  unpinned: Instance[]; inHouse: Instance[]; ignored: Instance[];
  unrecognized: { repository: string; instances: Instance[] }[];
}

export interface StageInfo { name: string; position: number; clusters: number }
export interface TenantInfo { name: string; clusters: number }
/** recognizedAs: the component the catalog itself now recognizes the repository as (a map override then no longer applies). */
export interface CatalogOverride { repository: string; kind: "map" | "inhouse" | "ignore"; component?: string; updatedAt: string; recognizedAs?: string }
export interface ServerSettings { serverURL: string; agentImage: string }
export interface CatalogComponent {
  id: string; name: string; source?: string; images?: string[]; github?: string; eol?: string;
  versionParts?: number; tagPrefix?: string; releaseTagPrefix?: string; versionPattern?: string; custom?: boolean;
  osv?: { ecosystem: string; name: string }; notes?: string; bundledWith?: Record<string, string>;
}
/** A component as entered in the New component form (milestone 11). */
export interface CustomInput {
  id: string; name: string; images: string[]; github: string; eol?: string;
  versionParts?: number; tagPrefix?: string; releaseTagPrefix?: string; versionPattern?: string;
}
export interface CustomState { id: string; active: boolean; replacedBy?: string; problem?: string }
export interface CustomView { component: CatalogComponent; state: CustomState; createdAt: string; updatedAt: string }
export interface CustomImpact { policy: boolean; overrides: string[] }
export interface PreviewTag { image: string; clusters: string[]; status: string; version?: string; reason?: string }
export interface Preview {
  upstream?: UpstreamInfo; githubLatest?: string; eolLatest?: string; tags: PreviewTag[]; warnings: string[]; conflict?: string;
}
export interface Suggestion {
  image: string; name: string; id: string; github: { repo: string; from: "label" | "same-name" }[];
  eol: { slug: string; label: string; aliases?: string[] }[]; labelSource?: string; labelError?: string;
}

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
export interface SummarySettings { mode: SummaryMode; dailyCapMicros: number; monthlyCapMicros: number; maxInputTokens: number; maxOutputTokens: number; model?: string }
/** A model admins can choose for summaries, priced with the saved limits ("" model = the server default). */
export interface SummaryModel { id: string; label: string; price: { Input: number; Output: number }; fallback?: string; maxPerSummaryMicros: number }
export interface SummaryRequestLog {
  id: number; at: string; component: string; from: string; to: string; model: string;
  inputTokens: number; outputTokens: number; costMicros: number; result: "ok" | "failed" | "resumed"; error?: string;
}
export interface SummaryStatus {
  settings: SummarySettings; enabled: boolean; model: string; fallback?: string; price?: { Input: number; Output: number };
  paused?: string; spentTodayMicros: number; spentMonthMicros: number; spentTotalMicros: number; summaries: number;
  requests: SummaryRequestLog[]; maxPerSummaryMicros: number; defaultModel: string; models: SummaryModel[];
}

/** Micro-dollars as money: $0.0123 below a cent, else cents. */
export function usd(micros: number | undefined): string {
  const m = micros ?? 0;
  return m > 0 && m < 10_000 ? `$${(m / 1e6).toFixed(4)}` : `$${(m / 1e6).toFixed(2)}`;
}

export interface Policies { global: string; overrides: Record<string, string>; presets: string[] }

/** Built-in docs (rendered at build time; GET /api/v1/docs). Section level 1 = top of a page. */
export interface DocPage { id: string; title: string; description: string }
export interface DocSection { page: string; anchor: string; heading: string; level: number; text: string }
export interface DocsIndex { version: string; pages: DocPage[]; sections: DocSection[] }
export interface DocPageView extends DocPage { html: string }

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
  docs: () => request<DocsIndex>("GET", "/api/v1/docs"),
  docPage: (id: string) => request<DocPageView>("GET", `/api/v1/docs/${encodeURIComponent(id)}`),
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
  customComponents: () => request<CustomView[]>("GET", "/api/v1/catalog/custom"),
  createComponent: (c: CustomInput) => request<CustomView[]>("POST", "/api/v1/catalog/custom", { component: c }),
  updateComponent: (c: CustomInput) => request<CustomView[]>("PUT", `/api/v1/catalog/custom/${encodeURIComponent(c.id)}`, { component: c }),
  componentImpact: (id: string) => request<CustomImpact>("GET", `/api/v1/catalog/custom/${encodeURIComponent(id)}/impact`),
  deleteComponent: (id: string) => request<{ removed: CustomImpact; components: CustomView[] }>("DELETE", `/api/v1/catalog/custom/${encodeURIComponent(id)}`),
  previewComponent: (c: CustomInput) => request<Preview>("POST", "/api/v1/catalog/preview", { component: c }),
  suggestComponent: (image: string) => request<Suggestion>("GET", `/api/v1/catalog/suggest?image=${encodeURIComponent(image)}`),
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

  vulns: () => request<{ enabled: boolean; images: VulnImage[] }>("GET", "/api/v1/vulns"),
  vulnImage: (digest: string) => request<VulnDetail>("GET", `/api/v1/vulns/images/${encodeURIComponent(digest)}`),
  scanner: () => request<{ enabled: boolean; reason?: string; status?: ScannerStatus }>("GET", "/api/v1/vulns/scanner"),
  rescan: (digest: string) => request<{ queued: boolean }>("POST", `/api/v1/vulns/images/${encodeURIComponent(digest)}/rescan`),
  cancelScan: (digest: string) => request("POST", "/api/v1/vulns/scanner/cancel", { digest }),
  acceptances: () => request<Acceptance[]>("GET", "/api/v1/vulns/acceptances"),
  registries: () => request<RegistriesView>("GET", "/api/v1/registries"),
  addRegistry: (b: { prefix: string; username: string; password: string; expiresAt: string }) => request("POST", "/api/v1/registries", b),
  updateRegistry: (b: { prefix: string; username?: string; password?: string; expiresAt: string }) => request("PUT", "/api/v1/registries", b),
  deleteRegistry: (prefix: string) => request("DELETE", "/api/v1/registries", { prefix }),
  internalRegistries: () => request<{ server: string[]; ui: string[] }>("GET", "/api/v1/registries/internal"),
  setInternalRegistries: (registries: string[]) => request<{ ui: string[] }>("PUT", "/api/v1/registries/internal", { registries }),
  testRegistry: (prefix: string) => request<LoginResult>("POST", "/api/v1/registries/test", { prefix }),
  acceptVuln: (a: { repository: string; vulnId: string; reason: string; expiresAt: string }) => request("PUT", "/api/v1/vulns/acceptances", a),
  unacceptVuln: (repository: string, vulnId: string) => request("DELETE", "/api/v1/vulns/acceptances", { repository, vulnId }),

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
  apiTokens: () => request<ApiToken[]>("GET", "/api/v1/tokens"),
  allApiTokens: () => request<ApiToken[]>("GET", "/api/v1/tokens/all"),
  createApiToken: (name: string, scope: TokenScope, expiresDays: number) =>
    request<CreatedApiToken>("POST", "/api/v1/tokens", { name, scope, expiresDays }),
  revokeApiToken: (id: number) => request("DELETE", `/api/v1/tokens/${id}`),
  cliDownloads: () => request<CliDownloads>("GET", "/api/v1/cli"),
  authSettings: () => request<AuthSettings>("GET", "/api/v1/auth/settings"),
  saveAuthSettings: (passwordLogin: boolean) => request<AuthSettings>("PUT", "/api/v1/auth/settings", { passwordLogin }),
  testMail: () => request<{ to: string }>("POST", "/api/v1/mail/test"),
  setDigestEmail: (on: boolean) => request<Me>("PUT", "/api/v1/auth/digest-email", { on }),

  // Notifications (milestone 10).
  notifications: () => request<NotifyOverview>("GET", "/api/v1/notifications"),
  saveNotifications: (s: NotifySettings) => request<NotifyOverview>("PUT", "/api/v1/notifications", s),
  testNotification: () => request<{ sent: boolean }>("POST", "/api/v1/notifications/test", {}),
  digestNow: () => request<{ sent: boolean }>("POST", "/api/v1/notifications/digest", {}),
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
export interface Me {
  email: string; role: Role; hasPassword: boolean; hasGoogle: boolean; digestEmail: boolean; emailAvailable?: boolean;
  /** Only when the request used a personal API token; scope is the effective one. */
  token?: { name: string; scope: TokenScope; expiresAt?: string };
}
export interface AuthConfig { password: boolean; google: boolean; forgot: boolean }
export interface TokenInfo { email: string; kind: "invite" | "reset"; password: boolean; google: boolean }
export interface User {
  id: number; email: string; role: Role; createdAt: string; activatedAt?: string; lastLoginAt?: string;
  disabledAt?: string; hasPassword: boolean; hasGoogle: boolean; digestEmail: boolean;
  /** Listed in a users file (`owl users apply`). */
  managed: boolean;
  /** An invite link that still works exists. */
  invitePending: boolean;
}
export type TokenScope = "read" | "write";
/** A personal API token (milestone 12). The secret is only in CreatedApiToken, once. */
export interface ApiToken {
  id: number; userId: number; userEmail: string; name: string; prefix: string; scope: TokenScope;
  createdAt: string; expiresAt?: string; lastUsedAt?: string;
}
export interface CliDownloads { version: string; files: { os: string; arch: string; sha256: string; size: number }[] }
export interface CreatedApiToken { token: ApiToken; secret: string }
export interface LinkResult { user: User; link: string; emailed: boolean; emailError?: string }
export interface AuthSettings {
  passwordLogin: boolean;
  google: { configured: boolean; domain?: string; redirectURL?: string; linkedAdmins: number };
  mail: { configured: boolean; host?: string; port?: number; from?: string; tls?: string };
  publicURL?: string;
}

export type Weekday = "monday" | "tuesday" | "wednesday" | "thursday" | "friday" | "saturday" | "sunday";
export interface NotifySettings {
  digest: boolean; critical: boolean; unrecognized: boolean; newCluster: boolean;
  digestDay: Weekday; digestTime: string; digestTimeZone: string;
}
export interface NotifyStatus {
  webhookConfigured: boolean; mailConfigured: boolean; linksConfigured: boolean;
  lastSentAt?: string; lastSentKind?: string; lastErrorAt?: string; lastError?: string;
  lastDigest?: string; nextDigest?: string; emailRecipients: number; tracking: boolean;
}
export interface NotifyOverview { settings: NotifySettings; status: NotifyStatus }

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
