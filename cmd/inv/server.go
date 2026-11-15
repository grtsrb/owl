package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"service-inventory/internal/auth"
	"service-inventory/internal/catalog"
	"service-inventory/internal/server"
	"service-inventory/internal/store"
	"service-inventory/internal/store/sqlite"
	"service-inventory/internal/upstream"
)

func defaultDB() string {
	if v := os.Getenv("INV_DB"); v != "" {
		return v
	}
	return "inventory.db"
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	listen := fs.String("listen", envOr("INV_LISTEN", "127.0.0.1:8080"), "listen address (env INV_LISTEN)")
	dbPath := fs.String("db", defaultDB(), "SQLite database path (env INV_DB)")
	noUpstream := fs.Bool("no-upstream", false, "disable upstream checks (GitHub, endoflife.date, OSV)")
	allowedHosts := fs.String("allowed-hosts", os.Getenv("INV_ALLOWED_HOSTS"),
		"comma-separated extra Host names the UI/read API answers to (localhost always allowed; env INV_ALLOWED_HOSTS)")
	internal := fs.String("internal-registries", os.Getenv("INV_INTERNAL_REGISTRIES"),
		"comma-separated registries of in-house images, e.g. registry.gitlab.example.com (env INV_INTERNAL_REGISTRIES)")
	_ = fs.Parse(args)

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
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

	app := &server.Server{Store: db, Catalog: cat, Log: log, AllowedHosts: strings.Split(*allowedHosts, ","),
		AdminToken: os.Getenv("INV_ADMIN_TOKEN")}
	if app.AdminToken == "" {
		log.Warn("INV_ADMIN_TOKEN not set: write endpoints (policies) are disabled")
	} else if len(app.AdminToken) < 24 {
		return errors.New("INV_ADMIN_TOKEN must be at least 24 characters")
	}
	if *noUpstream {
		log.Warn("upstream checks disabled")
	} else {
		if os.Getenv("GITHUB_TOKEN") == "" {
			log.Warn("GITHUB_TOKEN not set: GitHub allows 60 requests/hour; upstream data will be incomplete")
		}
		app.Upstream = newUpstreamClient(db)
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
// the server host (or `docker exec` into the server container); a UI/API with admin auth comes later.
func runCluster(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: inv cluster <add|list|rotate-token|delete> [flags]")
	}
	fs := flag.NewFlagSet("cluster "+args[0], flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "SQLite database path (env INV_DB)")
	name := fs.String("name", "", "cluster name, e.g. it-hetzner")
	stage := fs.String("stage", "", "staging | prelive | live")
	tenant := fs.String("tenant", "", "tenant using the cluster, e.g. IT or DS")
	_ = fs.Parse(args[1:])

	db, err := sqlite.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	switch args[0] {
	case "add":
		if *name == "" || *tenant == "" || !store.Stage(*stage).Valid() {
			return errors.New("add requires --name, --tenant and --stage (staging|prelive|live)")
		}
		c, err := db.CreateCluster(ctx, *name, store.Stage(*stage), *tenant)
		if err != nil {
			return fmt.Errorf("create cluster %q: %w", *name, err)
		}
		return issueToken(ctx, db, c)
	case "rotate-token":
		c, err := db.GetCluster(ctx, *name)
		if err != nil {
			return fmt.Errorf("cluster %q: %w", *name, err)
		}
		if err := db.RevokeTokens(ctx, c.ID); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "previous tokens revoked")
		return issueToken(ctx, db, c)
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

func issueToken(ctx context.Context, db store.Store, c *store.Cluster) error {
	token, hash, prefix := auth.NewToken()
	if err := db.AddToken(ctx, c.ID, hash, prefix); err != nil {
		return err
	}
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
		UserAgent:   "service-inventory/" + version,
		TTL:         6 * time.Hour,
	}}
}

// runUpstream is a debugging aid: fetch (or read cached) upstream info and print it.
func runUpstream(args []string) error {
	fs := flag.NewFlagSet("upstream", flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "SQLite database path (used as cache; env INV_DB)")
	comp := fs.String("component", "", "component id (default: all)")
	version := fs.String("version", "", "also list advisories affecting this version (needs --component)")
	_ = fs.Parse(args)

	db, err := sqlite.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	cat, err := catalog.Builtin()
	if err != nil {
		return err
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
			ms, err := cl.AdvisoriesFor(ctx, c, info, *version)
			if err != nil {
				return err
			}
			for _, m := range ms {
				fmt.Printf("    %-22s %-9s certain=%-5v fixed=%v %s\n", m.ID, m.Severity, m.Certain, m.FixedIn, m.Summary)
			}
		}
	}
	return nil
}
