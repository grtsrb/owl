package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

// App is one CLI invocation. Fields are swappable for tests.
type App struct {
	Version string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Getenv  func(string) string
	// Terminal reports whether stdin is an interactive terminal; ReadSecret reads a line from it
	// without echo.
	Terminal   func() bool
	ReadSecret func() (string, error)
}

// NewApp is the App for the real process.
func NewApp(version string) *App {
	return &App{Version: version, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv,
		Terminal: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		ReadSecret: func() (string, error) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			return string(b), err
		}}
}

type command struct {
	run     func(a *App, ctx context.Context, args []string) error
	summary string
}

// Commands are the client commands, by name. cmd/owl falls back to its own (server, agent, db, …).
var commands map[string]command

func init() {
	commands = map[string]command{
		"login":        {runLogin, "save the server URL and a personal API token (Account → API tokens)"},
		"logout":       {runLogout, "revoke the saved token and forget it (--keep-token: only forget it)"},
		"whoami":       {runWhoami, "show the server, your user and the token's scope"},
		"status":       {runStatus, "components per priority level and every cluster's last report"},
		"outdated":     {runOutdated, "what needs attention, per cluster and workload (--fail-on for CI)"},
		"component":    {runComponent, "one component: release lines, EOL, advisories, where it runs"},
		"clusters":     {runClusters, "registered clusters"},
		"changes":      {runChanges, "recent image changes in the clusters"},
		"unrecognized": {runUnrecognized, "images Owl doesn't recognize yet"},
		"cluster":      {runClusterCmd, "add | rotate-token | delete a cluster (admin token with write scope)"},
		"backup":       {runBackup, "backup now: take a backup on the server (admin, write scope)"},
		"users":        {runUsers, "list | export | plan | apply | add | set-role | disable | enable"},
	}
}

// Has reports whether name is a client command.
func Has(name string) bool { _, ok := commands[name]; return ok }

// Usage lists the client commands (for the binary's usage text).
func Usage() string {
	order := []string{"login", "logout", "whoami", "status", "outdated", "component", "clusters", "changes",
		"unrecognized", "cluster", "users", "backup"}
	var b strings.Builder
	for _, n := range order {
		fmt.Fprintf(&b, "  %-13s%s\n", n, commands[n].summary)
	}
	return b.String()
}

// exitError ends the program with a code but no "error:" line of its own (the command already
// printed what happened), e.g. `outdated --fail-on` or `users plan --detailed-exitcode`.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// Run runs a client command and returns the process exit code.
func (a *App) Run(name string, args []string) int {
	c, ok := commands[name]
	if !ok {
		fmt.Fprintf(a.Stderr, "unknown command %q\n", name)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := c.run(a, ctx, args)
	var ee exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.code
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		return 1 // 2 is reserved for "drift" (users plan --detailed-exitcode), like terraform
	}
	fmt.Fprintln(a.Stderr, "error:", err)
	return 1
}

var errUsage = errors.New("usage")

// usageErr prints a usage problem and returns errUsage (exit 1).
func (a *App) usageErr(format string, args ...any) error {
	fmt.Fprintf(a.Stderr, format+"\n", args...)
	return errUsage
}

// flags is a flag set that accepts flags anywhere (`owl component coredns -o json`).
type flags struct {
	*flag.FlagSet
	output *string
}

func (a *App) newFlags(name, usage string, withOutput bool) *flags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(a.Stderr, "usage: owl %s\n", usage)
		fs.PrintDefaults()
	}
	f := &flags{FlagSet: fs}
	if withOutput {
		f.output = fs.String("o", "text", "output: text or json")
	}
	return f
}

// parse parses flags and positionals in any order and returns the positionals.
func (f *flags) parse(args []string) ([]string, error) {
	var pos, rest []string
	for i, a := range args { // everything after "--" is positional
		if a == "--" {
			args, rest = args[:i], args[i+1:]
			break
		}
	}
	for {
		if err := f.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, errUsage
		}
		args = f.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	pos = append(pos, rest...)
	if f.output != nil && *f.output != "text" && *f.output != "json" {
		fmt.Fprintf(f.Output(), "-o: text or json, not %q\n", *f.output)
		return nil, errUsage
	}
	return pos, nil
}

func (f *flags) json() bool { return f.output != nil && *f.output == "json" }

// client builds the API client from OWL_SERVER/OWL_TOKEN, else the config file.
func (a *App) client() (*client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	server, token := cfg.Server, cfg.Token
	envServer, envToken := a.Getenv("OWL_SERVER"), a.Getenv("OWL_TOKEN")
	if envServer != "" && envToken == "" && token != "" && strings.TrimRight(envServer, "/") != strings.TrimRight(cfg.Server, "/") {
		// The saved token belongs to the saved server: never send it somewhere else.
		return nil, fmt.Errorf("OWL_SERVER is %s but the saved token is for %s: set OWL_TOKEN too, or unset OWL_SERVER", envServer, cfg.Server)
	}
	if envServer != "" {
		server = envServer
	}
	if envToken != "" {
		token = envToken
	}
	if server == "" || token == "" {
		return nil, errors.New("not signed in: run `owl login --server https://owl.example.com` (or set OWL_SERVER and OWL_TOKEN)")
	}
	return newClient(server, token, a.Version)
}

func (a *App) printJSON(v any) error {
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *App) table() *tabwriter.Writer { return tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0) }

// confirm asks a yes/no question on the terminal; without one it refuses (use --yes).
func (a *App) confirm(question string) (bool, error) {
	if !a.Terminal() {
		return false, errors.New("not a terminal: pass --yes to confirm")
	}
	fmt.Fprintf(a.Stderr, "%s [y/N] ", question)
	line, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	l := strings.ToLower(strings.TrimSpace(line))
	return l == "y" || l == "yes", nil
}

// --- login / logout / whoami ---

type meJSON struct {
	Email string `json:"email"`
	Role  string `json:"role"`
	Token *struct {
		Name      string     `json:"name"`
		Scope     string     `json:"scope"`
		ExpiresAt *time.Time `json:"expiresAt"`
	} `json:"token"`
}

func (m *meJSON) describe() string {
	if m.Token == nil {
		return m.Email
	}
	scope := "read-only"
	if m.Token.Scope == "write" {
		scope = "read & write"
	}
	exp := "never expires"
	if m.Token.ExpiresAt != nil {
		exp = "expires " + m.Token.ExpiresAt.Local().Format("2006-01-02")
	}
	return fmt.Sprintf("%s (%s) — token %q, %s, %s", m.Email, m.Role, m.Token.Name, scope, exp)
}

func runLogin(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("login", "login [--server https://owl.example.com]   (the token is read from the terminal, or stdin when piped)", false)
	server := f.String("server", "", "Owl server URL (default: OWL_SERVER, then the saved one)")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return a.usageErr("owl login takes no arguments: the token is asked for, never passed on the command line (it would show in ps and shell history)")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if *server == "" {
		*server = a.Getenv("OWL_SERVER")
	}
	if *server == "" {
		*server = cfg.Server
	}
	if *server == "" {
		return a.usageErr("owl login --server https://owl.example.com")
	}
	if _, err := checkServerURL(*server); err != nil {
		return err
	}
	var token string
	if a.Terminal() {
		fmt.Fprintf(a.Stderr, "Create a token in Owl under Account → API tokens, then paste it here.\nToken: ")
		token, err = a.ReadSecret()
		fmt.Fprintln(a.Stderr)
	} else {
		var line string
		line, err = bufio.NewReader(a.Stdin).ReadString('\n')
		if errors.Is(err, io.EOF) && line != "" {
			err = nil
		}
		token = line
	}
	if err != nil {
		return fmt.Errorf("read token: %w", err)
	}
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "owl_pat_") {
		return errors.New("that isn't an Owl API token (they start with owl_pat_): create one under Account → API tokens")
	}
	c, err := newClient(*server, token, a.Version)
	if err != nil {
		return err
	}
	var me meJSON
	if err := c.do(ctx, http.MethodGet, "/api/v1/auth/me", nil, &me); err != nil {
		return fmt.Errorf("token check failed: %w", err)
	}
	if err := saveConfig(config{Server: c.base.String(), Token: token}); err != nil {
		return err
	}
	p, _ := configPath()
	fmt.Fprintf(a.Stdout, "Signed in to %s as %s\nSaved to %s\n", c.base, me.describe(), p)
	if a.Getenv("OWL_TOKEN") != "" {
		fmt.Fprintln(a.Stderr, "note: OWL_TOKEN is set in your environment and takes precedence over the saved token")
	}
	return nil
}

func runLogout(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("logout", "logout [--keep-token]", false)
	keep := f.Bool("keep-token", false, "only forget the token on this machine; it keeps working on the server")
	if _, err := f.parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.Token == "" {
		if a.Getenv("OWL_TOKEN") != "" {
			return errors.New("no saved token: OWL_TOKEN comes from the environment — revoke it in Owl under Account → API tokens")
		}
		fmt.Fprintln(a.Stdout, "Not signed in.")
		return nil
	}
	if !*keep {
		c, err := newClient(cfg.Server, cfg.Token, a.Version)
		if err != nil {
			return err
		}
		err = c.do(ctx, http.MethodDelete, "/api/v1/tokens/current", nil, nil)
		var ae *apiError
		switch {
		case err == nil:
			fmt.Fprintln(a.Stdout, "Token revoked on", c.base.Host)
		case errors.As(err, &ae) && ae.Status == http.StatusUnauthorized:
			fmt.Fprintln(a.Stdout, "The token no longer worked on the server (expired or revoked).")
		default:
			return fmt.Errorf("couldn't revoke the token, so it's still saved (retry, or `owl logout --keep-token` to only forget it): %w", err)
		}
	}
	cfg.Token = ""
	if err := saveConfig(config{Server: cfg.Server}); err != nil {
		return err
	}
	if *keep {
		fmt.Fprintln(a.Stdout, "Token forgotten on this machine; it still works on the server until it expires or is revoked.")
	}
	return nil
}

func runWhoami(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("whoami", "whoami [-o json]", true)
	if _, err := f.parse(args); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/api/v1/auth/me", nil, &raw); err != nil {
		return err
	}
	if f.json() {
		_, err := fmt.Fprintln(a.Stdout, string(raw))
		return err
	}
	var me meJSON
	if err := json.Unmarshal(raw, &me); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "%s\n%s\n", c.base, me.describe())
	return nil
}
