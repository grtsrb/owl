// Where each "?" help link opens the docs: the section that explains the whole feature. Kept short on
// purpose (page headers + the level legend only). Checked against the built docs by
// internal/docs/render (TestHelpLinks): a renamed heading fails the build's tests, not the user.
export const DOC_HELP = {
  overview: "user-guide#overview",
  changes: "concepts#change-history",
  clusters: "concepts#clusters-stages-and-tenants",
  catalog: "catalog",
  notifications: "operations#notifications",
  backups: "operations#backups",
  users: "security#users-and-sign-in",
  levels: "concepts#priority-levels",
} as const;
