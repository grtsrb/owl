//go:build !owlclient

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"owl/internal/auth"
	"owl/internal/backup"
	"owl/internal/catalog"
	"owl/internal/docs"
	"owl/internal/logfile"
	"owl/internal/notify"
	"owl/internal/registry"
	"owl/internal/server"
	"owl/internal/store"
	"owl/internal/store/sqlite"
	"owl/internal/summary"
	"owl/internal/upstream"
	"owl/web"
)

func defaultDB() string {
	if v := os.Getenv("OWL_DB"); v != "" {
		return v
	}
	return "owl.db"
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	listen := fs.String("listen", envOr("OWL_LISTEN", "127.0.0.1:8080"), "listen address (env OWL_LISTEN)")
	dbPath := fs.String("db", defaultDB(), "SQLite database path (env OWL_DB)")
	noUpstream := fs.Bool("no-upstream", false, "disable upstream checks (GitHub, endoflife.date, OSV)")
	allowedHosts := fs.String("allowed-hosts", os.Getenv("OWL_ALLOWED_HOSTS"),
		"comma-separated extra Host names the UI/read API answers to (localhost always allowed; env OWL_ALLOWED_HOSTS)")
	internal := fs.String("internal-registries", os.Getenv("OWL_INTERNAL_REGISTRIES"),
		"comma-separated registries of in-house images, e.g. registry.gitlab.example.com (env OWL_INTERNAL_REGISTRIES)")
	logDir := fs.String("log-dir", os.Getenv("OWL_LOG_DIR"), "directory for rotated log files (default: <db dir>/logs; \"-\" = stderr only; env OWL_LOG_DIR)")
	backupDir := fs.String("backup-dir", os.Getenv("OWL_BACKUP_DIR"), "local backup directory (default: <db dir>/backups; env OWL_BACKUP_DIR)")
	_ = fs.Parse(args)

	// Logs: JSON to stderr and to a rotating file. The level starts from OWL_LOG_LEVEL and then
	// follows the UI setting (ApplyOpsSettings).
	level := new(slog.LevelVar)
	if v := os.Getenv("OWL_LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			return fmt.Errorf("OWL_LOG_LEVEL: %w", err)
		}
	}
	var logOut io.Writer = os.Stderr
	var logFile *logfile.Writer
	if *logDir != "-" {
		dir := *logDir
		if dir == "" {
			dir = filepath.Join(filepath.Dir(*dbPath), "logs")
		}
		lf, err := logfile.Open(dir, "server", 5, 50<<20)
		if err != nil {
			return fmt.Errorf("log file: %w", err)
		}
		defer lf.Close()
		logFile, logOut = lf, io.MultiWriter(os.Stderr, lf)
	}
	log := slog.New(slog.NewJSONHandler(logOut, &slog.HandlerOptions{Level: level}))
	cat, err := catalog.Builtin()
	if err != nil {
		return err
	}
	for _, r := range strings.Split(*internal, ",") {
		if r = strings.TrimSpace(r); r != "" {
			cat.InternalRegistries = append(cat.InternalRegistries, r)
		}
	}
	db, err := sqlite.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	authCfg, err := authConfigFromEnv()
	if err != nil {
		return err
	}
	app := &server.Server{Store: db, Catalog: cat, Log: log, AllowedHosts: strings.Split(*allowedHosts, ","),
		Auth: authCfg, UI: web.Dist(), Docs: docs.Dist(), LogLevel: level, Version: version, CLIDir: envOr("OWL_CLI_DIR", "/cli")}
	if logFile != nil {
		app.LogFile = logFile
	}
	bdir := *backupDir
	if bdir == "" {
		bdir = filepath.Join(filepath.Dir(*dbPath), "backups")
	}
	app.Backups = &backup.Manager{Store: db, Dir: bdir, Log: log}
	if os.Getenv("OWL_BACKUP_S3_BUCKET") != "" {
		remote, err := backup.NewS3(backup.S3Config{
			Endpoint: os.Getenv("OWL_BACKUP_S3_ENDPOINT"), Region: os.Getenv("OWL_BACKUP_S3_REGION"),
			Bucket: os.Getenv("OWL_BACKUP_S3_BUCKET"), Prefix: os.Getenv("OWL_BACKUP_S3_PREFIX"),
			AccessKey: os.Getenv("OWL_BACKUP_S3_ACCESS_KEY"), SecretKey: os.Getenv("OWL_BACKUP_S3_SECRET_KEY"),
			Insecure: os.Getenv("OWL_BACKUP_S3_INSECURE") == "true",
		})
		if err != nil {
			return fmt.Errorf("S3 backups: %w", err)
		}
		app.Backups.Remote = remote
		log.Info("backups go to S3", "target", remote.String(), "localDir", bdir)
	} else {
		log.Warn("OWL_BACKUP_S3_BUCKET not set: backups are local only (same disk as the database)", "dir", bdir)
	}
	app.ApplyOpsSettings(context.Background())
	if err := app.ReloadCatalog(context.Background()); err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	logAuthSetup(log, db, authCfg)
	if *noUpstream {
		log.Warn("upstream checks disabled")
	} else {
		if os.Getenv("GITHUB_TOKEN") == "" {
			log.Warn("GITHUB_TOKEN not set: GitHub allows 60 requests/hour; upstream data will be incomplete")
		}
		app.Upstream = newUpstreamClient(db)
		// Label lookups for "New component" suggestions: outbound like upstream, so off with --no-upstream.
		app.Registry = &registry.Client{UserAgent: "owl/" + version}
		if err := setupSummaries(app, db, cat, log); err != nil {
			return err
		}
	}
	if err := setupNotify(app, db, cat, authCfg, log); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.RunUpstreamRefresher(ctx, 6*time.Hour)
	go app.Backups.RunScheduler(ctx)
	go app.RunMaintenance(ctx)
	go app.Notify.Run(ctx)
	if app.Summaries != nil {
		go app.Summaries.RunScheduler(ctx)
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("server listening", "addr", *listen, "db", *dbPath, "version", version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// runCluster manages registrations directly on the database file. It's an operator command run on
// the server host (or `docker exec` into the server container); Settings → Clusters does the same.
func runCluster(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: owl db cluster <add|list|rotate-token|delete> --name <cluster> [flags]")
	}
	fs := flag.NewFlagSet("cluster "+args[0], flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "SQLite database path (env OWL_DB)")
	name := fs.String("name", "", "cluster name, e.g. it-hetzner")
	stage := fs.String("stage", "", "a configured stage (default: staging | prelive | live)")
	tenant := fs.String("tenant", "", "tenant using the cluster, e.g. IT or DS")
	_ = fs.Parse(args[1:])

	db, err := openExistingDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	switch args[0] {
	case "add":
		switch {
		case !store.ValidClusterName(*name):
			return errors.New("--name: " + store.ClusterNameRule)
		case !store.ValidTenantName(*tenant):
			return errors.New("--tenant: " + store.TenantNameRule)
		case *stage == "":
			return errors.New("--stage: one of the configured stages (default staging|prelive|live)")
		}
		token, hash, prefix := auth.NewToken()
		c, err := db.CreateClusterWithToken(ctx, *name, store.Stage(*stage), *tenant, hash, prefix)
		if err != nil {
			return fmt.Errorf("create cluster %q: %w", *name, err)
		}
		return printToken(c, token)
	case "rotate-token":
		c, err := db.GetCluster(ctx, *name)
		if err != nil {
			return fmt.Errorf("cluster %q: %w", *name, err)
		}
		token, hash, prefix := auth.NewToken()
		if err := db.RotateToken(ctx, c.ID, hash, prefix); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "previous tokens revoked")
		return printToken(c, token)
	case "list":
		cs, err := db.ListClusters(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSTAGE\tTENANT\tKUBERNETES\tAGENT\tLAST SEEN")
		for _, c := range cs {
			seen := "never"
			if c.LastSeenAt != nil {
				seen = time.Since(*c.LastSeenAt).Round(time.Second).String() + " ago"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", c.Name, c.Stage, c.Tenant, c.KubernetesVersion, c.AgentVersion, seen)
		}
		return tw.Flush()
	case "delete":
		if err := db.DeleteCluster(ctx, *name); err != nil {
			return fmt.Errorf("delete %q: %w", *name, err)
		}
		fmt.Println("deleted", *name, "(including its tokens, inventory and history)")
		return nil
	}
	return fmt.Errorf("unknown cluster command %q", args[0])
}

// openExistingDB opens the server's database for `owl db …`, refusing to create one: run by mistake on a
// laptop, it would otherwise create an empty owl.db and report "no clusters".
func openExistingDB(path string) (*sqlite.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("no Owl database at %s: `owl db …` runs on the server host (or `docker exec` into the server container), with --db or OWL_DB pointing at the database; from your machine, use the client commands (`owl clusters`, `owl users`)", path)
	}
	return sqlite.Open(path)
}

func printToken(c *store.Cluster, token string) error {
	fmt.Fprintf(os.Stderr, "cluster %s (%s, %s) — agent token (shown only once, store it in the cluster's Secret):\n", c.Name, c.Stage, c.Tenant)
	fmt.Println(token)
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func newUpstreamClient(db store.Store) *upstream.Client {
	return &upstream.Client{F: &upstream.Fetcher{
		HTTP:        &http.Client{Timeout: 30 * time.Second},
		Cache:       db,
		GitHubToken: os.Getenv("GITHUB_TOKEN"),
		UserAgent:   "owl/" + version,
		TTL:         6 * time.Hour,
	}}
}

// runUpstream is a debugging aid: fetch (or read cached) upstream info and print it.
func runUpstream(args []string) error {
	fs := flag.NewFlagSet("upstream", flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "SQLite database path (used as cache; env OWL_DB)")
	comp := fs.String("component", "", "component id (default: all)")
	version := fs.String("version", "", "also list advisories affecting this version (needs --component)")
	_ = fs.Parse(args)

	db, err := sqlite.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	builtin, err := catalog.Builtin()
	if err != nil {
		return err
	}
	cat := builtin
	if rows, err := db.ListCustomComponents(context.Background()); err == nil { // components added in the UI
		var comps []catalog.Component
		for _, r := range rows {
			var c catalog.Component
			if json.Unmarshal(r.Data, &c) == nil {
				c.ID = r.ID
				if catalog.ValidateCustom(&c) == nil {
					comps = append(comps, c)
				}
			}
		}
		cat, _ = catalog.Merge(builtin, comps)
	}
	if os.Getenv("GITHUB_TOKEN") == "" {
		fmt.Fprintln(os.Stderr, "warning: GITHUB_TOKEN not set; GitHub allows only 60 requests/hour")
	}
	cl := newUpstreamClient(db)
	ctx := context.Background()
	for i := range cat.Components {
		c := &cat.Components[i]
		if *comp != "" && c.ID != *comp {
			continue
		}
		info := cl.Refresh(ctx, c)
		lines := []string{}
		for j, l := range info.Lines {
			if j == 4 {
				break
			}
			tag := l.Name + "→" + l.Latest
			if l.LTS {
				tag += " LTS"
			}
			if l.EOL {
				tag += " EOL"
			} else if l.EOLDate != nil {
				tag += " (eol " + l.EOLDate.Format("2006-01-02") + ")"
			}
			lines = append(lines, tag)
		}
		fmt.Printf("%-28s latest=%-10s archived=%-5v advisories=%-11s lines=[%s] %s\n", c.ID, info.LatestStable,
			info.Archived, info.AdvisorySource, strings.Join(lines, ", "), strings.Join(info.Errors, "; "))
		if *version != "" {
			for _, m := range upstream.AdvisoriesFor(info, *version) {
				fmt.Printf("    %-22s %-9s certain=%-5v fixed=%v %s\n", m.ID, m.Severity, m.Certain, m.FixedIn, m.Summary)
			}
		}
	}
	return nil
}

// setupSummaries configures Claude upgrade summaries. The API key is read from the environment only
// (never a flag, the UI or the database) and never logged. Without it, summaries can be set to
// "estimate only" but nothing is ever sent. The mode itself (default off) is a UI setting.
func setupSummaries(app *server.Server, db store.Store, cat *catalog.Catalog, log *slog.Logger) error {
	model := os.Getenv("OWL_CLAUDE_MODEL")
	if model == "" {
		model = summary.DefaultModel
	}
	if !summary.KnownModel(model) {
		return fmt.Errorf("OWL_CLAUDE_MODEL %q: Owl has no price for this model, so it can't enforce the spending caps (known: %s)",
			model, strings.Join(summary.KnownModels(), ", "))
	}
	m := &summary.Manager{Store: db, Catalog: cat, CatalogFn: app.CurrentCatalog, Notes: app.Upstream, Infos: app.UpstreamInfo,
		Report: app.BuildReport, Model: model, Log: log}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		m.Claude = summary.NewClient(key)
		log.Info("Claude summaries available (mode is set in Settings → Summaries; default off)", "model", model)
	} else {
		log.Info("ANTHROPIC_API_KEY not set: Claude summaries can only run in estimate mode")
	}
	app.Summaries = m
	return nil
}

// setupNotify configures notifications. The webhook URL is a secret (its path is the key): read from
// the environment only, never logged, shown or stored. Without it, notifications still track state so
// switching them on later starts from the present.
func setupNotify(app *server.Server, db store.Store, cat *catalog.Catalog, authCfg server.AuthConfig, log *slog.Logger) error {
	n := &notify.Notifier{Store: db, Catalog: cat, CatalogFn: app.CurrentCatalog, Report: app.BuildReport, Ready: app.UpstreamLoaded,
		Incomplete: app.UpstreamIncomplete, Fetched: func(id string) bool { return app.UpstreamInfo(id) != nil },
		Mail: authCfg.Mail, PublicURL: authCfg.PublicURL, Log: log}
	if raw := os.Getenv("OWL_MATTERMOST_WEBHOOK_URL"); raw != "" {
		if err := notify.ValidateWebhookURL(raw); err != nil {
			return fmt.Errorf("OWL_MATTERMOST_WEBHOOK_URL: %w", err)
		}
		n.Webhook = &notify.Webhook{URL: raw, Username: "Owl", HTTP: &http.Client{Timeout: 15 * time.Second}}
		log.Info("Mattermost notifications available (switch them on in Settings → Notifications)")
	} else {
		log.Info("OWL_MATTERMOST_WEBHOOK_URL not set: no Mattermost notifications")
	}
	if authCfg.PublicURL == nil {
		log.Info("OWL_PUBLIC_URL not set: notifications are sent without links")
	}
	app.Notify = n
	return nil
}
