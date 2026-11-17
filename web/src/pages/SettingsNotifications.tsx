import { useState } from "react";
import { api, useIsAdmin, type NotifySettings, type NotifyStatus, type Weekday } from "../api";
import { Icon } from "../icons";
import { ago, Card, ErrorBox, Loading, Msg, useApi } from "../ui";
import { useWrite } from "./Settings";

const KINDS: [keyof NotifySettings, string, string][] = [
  ["digest", "Weekly digest", "Components per level, top 5 actions, drift, what was upgraded or rolled back, what's no longer Critical, clusters not reporting."],
  ["critical", "Critical alert", "When a component on a cluster newly becomes Critical. Once — no repeats while it stays Critical."],
  ["unrecognized", "Unrecognized image", "The first time any cluster runs an image Owl doesn't know, so it can be mapped, marked in-house or ignored."],
  ["newCluster", "New cluster connected", "On a cluster's first report: a summary of its situation (replaces the two alerts above for that report)."],
];

const DAYS: Weekday[] = ["monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"];

// The browser's zone list; Chromium leaves out "UTC", which the server accepts, so it's added.
const ZONES: string[] = (() => {
  let z: string[] = [];
  try { z = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone") ?? []; } catch { /* old browser */ }
  return z.length === 0 || z.includes("UTC") ? z : ["UTC", ...z];
})();

export default function NotificationsTab() {
  if (!useIsAdmin()) {
    return <Card title="Notifications"><div className="callout info"><Icon.info /><span>Only admins can change notifications. You can get the weekly digest by email under Account.</span></div></Card>;
  }
  return <Inner />;
}

function Inner() {
  const ov = useApi(api.notifications);
  if (ov.error) return <ErrorBox error={ov.error} retry={ov.reload} />;
  if (!ov.data) return <Loading />;
  const { settings, status } = ov.data;
  return (
    <>
      <StatusCard s={status} onSent={ov.reload} />
      <SettingsForm key={JSON.stringify(settings)} s={settings} status={status} onSaved={ov.reload} />
      <Card title="What gets sent">
        <p className="subtle" style={{ margin: 0 }}>
          Messages name clusters, tenants, stages and components, with versions, levels and recommended actions — everyone in the
          Mattermost channel sees them. Nothing is ever sent to Claude for a notification: the digest only shows summaries that
          already exist. Owl checks for alerts after every agent report and every upstream refresh (new advisories, a passed EOL date).
        </p>
      </Card>
    </>
  );
}

function StatusCard({ s, onSent }: { s: NotifyStatus; onSent: () => void }) {
  const w = useWrite();
  return (
    <Card title="Mattermost">
      {!s.webhookConfigured && (
        <div className="callout warn"><Icon.warn /><span>
          OWL_MATTERMOST_WEBHOOK_URL is not set on the server, so nothing is posted. Create an incoming webhook in Mattermost
          (Integrations → Incoming Webhooks), put its URL in the server's environment and restart. Owl keeps track in the
          meantime, so switching a notification on later doesn't post what's already known.
        </span></div>
      )}
      {s.lastError && <div className="callout critical"><Icon.warn /><span><b>Last delivery failed</b> {ago(s.lastErrorAt)}: {s.lastError}</span></div>}
      <div className="kv-grid">
        <div><span>Webhook</span><span>{s.webhookConfigured ? "configured" : "not configured"}</span></div>
        <div><span>Last message</span><span>{s.lastSentAt ? <>{ago(s.lastSentAt)} <span className="subtle">({s.lastSentKind})</span></> : "never"}</span></div>
        <div><span>Next digest</span><span>{s.nextDigest ? new Date(s.nextDigest).toLocaleString() : "off"}</span></div>
        <div><span>Digest by email</span><span>{s.mailConfigured ? `${s.emailRecipients} ${s.emailRecipients === 1 ? "user" : "users"} opted in` : "email isn't set up"}</span></div>
      </div>
      {!s.linksConfigured && <p className="subtle" style={{ margin: 0 }}>OWL_PUBLIC_URL isn't set: messages are sent without links into Owl.</p>}
      <div className="row">
        <button className="btn" disabled={!s.webhookConfigured || w.busy} onClick={async () => { await w.run(api.testNotification, "Test message sent."); onSent(); }}>Send test message</button>
        <button className="btn" disabled={!s.webhookConfigured || w.busy} onClick={async () => { await w.run(api.digestNow, "Digest sent."); onSent(); }}>Send digest now</button>
        <Msg msg={w.msg} />
      </div>
      <p className="subtle" style={{ margin: 0 }}>“Send digest now” posts this week's digest to Mattermost only. It doesn't replace the scheduled one.</p>
    </Card>
  );
}

function SettingsForm({ s, status, onSaved }: { s: NotifySettings; status: NotifyStatus; onSaved: () => void }) {
  const [v, setV] = useState<NotifySettings>(s);
  const w = useWrite();
  const dirty = JSON.stringify(v) !== JSON.stringify(s);
  const zones = ZONES.includes(v.digestTimeZone) ? ZONES : [v.digestTimeZone, ...ZONES];
  return (
    <Card title="Notifications">
      <form className="stack" onSubmit={async (e) => { e.preventDefault(); if (await w.run(() => api.saveNotifications(v), "Saved.")) onSaved(); }}>
        <fieldset className="stack" style={{ border: 0, padding: 0, margin: 0 }}>
          <legend className="subtle" style={{ marginBottom: 4 }}>Send</legend>
          {KINDS.map(([k, label, desc]) => (
            <label key={k} className="row" style={{ alignItems: "flex-start", gap: 8 }}>
              <input type="checkbox" checked={v[k] as boolean} onChange={(e) => setV({ ...v, [k]: e.target.checked })} />
              <span><b>{label}</b> <span className="subtle">{desc}</span></span>
            </label>
          ))}
        </fieldset>
        <div className="kv-grid">
          <label className="field">Digest day
            <select className="select" value={v.digestDay} onChange={(e) => setV({ ...v, digestDay: e.target.value as Weekday })}>
              {DAYS.map((d) => <option key={d} value={d}>{d[0].toUpperCase() + d.slice(1)}</option>)}
            </select>
          </label>
          <label className="field">Time
            <input className="input" type="time" value={v.digestTime} onChange={(e) => setV({ ...v, digestTime: e.target.value })} />
          </label>
          <label className="field">Time zone
            {zones.length > 1 ? (
              <select className="select" value={v.digestTimeZone} onChange={(e) => setV({ ...v, digestTimeZone: e.target.value })}>
                {zones.map((z) => <option key={z} value={z}>{z}</option>)}
              </select>
            ) : <input className="input" value={v.digestTimeZone} onChange={(e) => setV({ ...v, digestTimeZone: e.target.value })} />}
          </label>
        </div>
        <p className="subtle" style={{ margin: 0 }}>
          A digest missed while the server was down is sent once when it's back, if it's less than 24 hours late. Switching an
          alert on starts from the current state — it doesn't post what's already Critical or already known.
          {!status.webhookConfigured && " Nothing is posted until the webhook is configured."}
        </p>
        <div className="row"><button className="btn primary" disabled={!dirty || w.busy}>Save</button><Msg msg={w.msg} /></div>
      </form>
    </Card>
  );
}
