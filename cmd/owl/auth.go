//go:build !owlclient && !owlagent

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"owl/internal/auth"
	"owl/internal/mail"
	"owl/internal/server"
	"owl/internal/store"
)

// publicURLFromEnv parses OWL_PUBLIC_URL: https://host[:port] (http only for localhost), no path.
func publicURLFromEnv() (*url.URL, error) {
	raw := strings.TrimSpace(os.Getenv("OWL_PUBLIC_URL"))
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("OWL_PUBLIC_URL %q: use the address users open, like https://owl.example.com (no path)", raw)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	local := host == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, fmt.Errorf("OWL_PUBLIC_URL %q must be https:// (sign-in cookies are Secure; http only for localhost)", raw)
	}
	u.Path = ""
	return u, nil
}

// trustedProxiesFromEnv parses OWL_TRUSTED_PROXIES: comma-separated IPs or CIDRs.
func trustedProxiesFromEnv() ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(os.Getenv("OWL_TRUSTED_PROXIES"), ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(f); err == nil {
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		} else {
			return nil, fmt.Errorf("OWL_TRUSTED_PROXIES: %q is not an IP or CIDR", f)
		}
	}
	return out, nil
}

func authConfigFromEnv() (server.AuthConfig, error) {
	var c server.AuthConfig
	var err error
	if c.PublicURL, err = publicURLFromEnv(); err != nil {
		return c, err
	}
	if c.TrustedProxies, err = trustedProxiesFromEnv(); err != nil {
		return c, err
	}
	if c.Google, err = server.NewGoogleConfig(os.Getenv("OWL_GOOGLE_CLIENT_ID"), os.Getenv("OWL_GOOGLE_CLIENT_SECRET"),
		os.Getenv("OWL_GOOGLE_DOMAIN"), c.PublicURL); err != nil {
		return c, err
	}
	mc, err := mail.FromEnv()
	if err != nil {
		return c, fmt.Errorf("email: %w", err)
	}
	if mc != nil {
		if c.PublicURL == nil {
			return c, errors.New("email needs OWL_PUBLIC_URL: the links in invite and reset emails point there")
		}
		c.Mail, c.MailInfo = &mail.SMTP{Config: *mc}, mc.Status()
	}
	return c, nil
}

func logAuthSetup(log *slog.Logger, st store.Store, c server.AuthConfig) {
	if os.Getenv("OWL_ADMIN_TOKEN") != "" {
		log.Warn("OWL_ADMIN_TOKEN is no longer used: admins sign in (invite users in Settings → Users); remove it")
	}
	pub := ""
	if c.PublicURL != nil {
		pub = c.PublicURL.String()
	}
	log.Info("sign-in", "publicURL", pub, "google", c.Google != nil, "email", c.Mail != nil, "trustedProxies", len(c.TrustedProxies))
	if m, err := st.GetSettings(context.Background()); err == nil && m["authPasswordLogin"] == "off" && c.Google == nil {
		log.Warn("password sign-in is turned off but Google sign-in isn't configured: passwords are back on until it is (set OWL_GOOGLE_CLIENT_ID/SECRET)")
	}
	if us, err := st.ListUsers(context.Background()); err == nil && len(us) == 0 {
		log.Warn("no users yet: create the first admin with `owl db user invite --admin <email>` and open the link it prints")
	}
}

// runUser manages users directly on the database file (like `owl cluster`): the first admin's invite
// and the emergency reset link work without anyone signed in. Run it on the server host, or
// `docker exec` into the server container.
func runUser(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: owl db user <invite|list|reset-link|delete> [flags]")
	}
	fs := flag.NewFlagSet("user "+args[0], flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "SQLite database path (env OWL_DB)")
	email := fs.String("email", "", "the user's email address")
	admin := fs.Bool("admin", false, "invite as admin (default: viewer)")
	_ = fs.Parse(args[1:])
	if fs.NArg() > 0 && *email == "" {
		// `owl db user invite you@example.com --admin`: the flag package stops at the first non-flag,
		// so parse what follows the address too.
		*email = fs.Arg(0)
		_ = fs.Parse(fs.Args()[1:])
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	base, err := publicURLFromEnv()
	if err != nil {
		return err
	}
	db, err := openExistingDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	link := func(u *store.User, kind store.AuthTokenKind) error {
		secret, hash := auth.NewSecret()
		ttl := server.InviteTTL
		if kind == store.TokenReset {
			ttl = server.ResetTTL
		}
		if err := db.CreateAuthToken(ctx, hash, u.ID, kind, time.Now().Add(ttl)); err != nil {
			return err
		}
		l := server.LinkPath(base, kind, secret)
		fmt.Fprintf(os.Stderr, "%s link for %s (%s), valid %s, works once:\n", kind, u.Email, u.Role, ttl)
		if base == nil {
			fmt.Fprintln(os.Stderr, "(OWL_PUBLIC_URL isn't set: put Owl's address in front, e.g. http://127.0.0.1:8080"+l+")")
		}
		fmt.Println(l)
		return nil
	}

	switch args[0] {
	case "invite":
		e, ok := server.ValidEmail(*email)
		if !ok {
			return errors.New("--email: a plain email address, like name@example.com")
		}
		role := store.RoleViewer
		if *admin {
			role = store.RoleAdmin
		}
		u, err := db.CreateUser(ctx, e, role)
		if errors.Is(err, store.ErrExists) {
			return fmt.Errorf("%s already exists: use `owl db user reset-link` (or Settings → Users)", e)
		} else if err != nil {
			return err
		}
		return link(u, store.TokenInvite)
	case "reset-link":
		u, err := db.UserByEmail(ctx, *email)
		if err != nil {
			return fmt.Errorf("user %q: %w", *email, err)
		}
		if u.DisabledAt != nil {
			return fmt.Errorf("%s is disabled: an admin must enable the user first", u.Email)
		}
		if u.ActivatedAt == nil {
			return link(u, store.TokenInvite) // never accepted: a fresh invite
		}
		// Works even with password sign-in turned off: it's the way back in when Google sign-in breaks.
		return link(u, store.TokenReset)
	case "delete":
		u, err := db.UserByEmail(ctx, *email)
		if err != nil {
			return fmt.Errorf("user %q: %w", *email, err)
		}
		google := os.Getenv("OWL_GOOGLE_CLIENT_ID") != "" && os.Getenv("OWL_GOOGLE_CLIENT_SECRET") != ""
		if err := server.GoogleAdminGuard(ctx, db, google, u.ID); err != nil {
			return fmt.Errorf("delete %s: password sign-in is off and this is the last admin who signs in with Google: %w", u.Email, err)
		}
		if err := db.DeleteUser(ctx, u.ID); err != nil {
			return fmt.Errorf("delete %s: %w", u.Email, err)
		}
		fmt.Fprintln(os.Stderr, "deleted", u.Email, "(signed out everywhere; the audit log keeps what they did)")
		return nil
	case "list":
		us, err := db.ListUsers(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "EMAIL\tROLE\tSTATUS\tSIGN-IN\tLAST SIGN-IN")
		for _, u := range us {
			status := "active"
			if u.DisabledAt != nil {
				status = "disabled"
			} else if u.ActivatedAt == nil {
				status = "invited"
			}
			var methods []string
			if u.HasPassword {
				methods = append(methods, "password")
			}
			if u.HasGoogle {
				methods = append(methods, "google")
			}
			last := "never"
			if u.LastLoginAt != nil {
				last = u.LastLoginAt.Local().Format("2006-01-02 15:04")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", u.Email, u.Role, status, strings.Join(methods, "+"), last)
		}
		return tw.Flush()
	}
	return fmt.Errorf("unknown user command %q", args[0])
}

// runHealthcheck: the image is distroless (no curl), so Docker's HEALTHCHECK runs this.
func runHealthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	addr := fs.String("addr", envOr("OWL_LISTEN", "127.0.0.1:8080"), "server listen address (env OWL_LISTEN)")
	_ = fs.Parse(args)
	host, port, err := net.SplitHostPort(*addr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: HTTP %d", res.StatusCode)
	}
	return nil
}
