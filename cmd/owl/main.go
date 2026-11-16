// Command owl is the owl binary: `owl agent` runs in each cluster, `owl server` is the
// central service, `owl cluster` manages cluster registrations and agent tokens.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"owl/internal/agent"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "agent":
		err = runAgent(os.Args[2:])
	case "server":
		err = runServer(os.Args[2:])
	case "cluster":
		err = runCluster(os.Args[2:])
	case "upstream":
		err = runUpstream(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: owl <command> [flags]

commands:
  agent     collect workload inventory from a cluster and push it to the server
  server    run the central inventory server
  cluster   manage registered clusters and agent tokens (add, list, rotate-token, delete)
  upstream  show upstream release lines, EOL, archived status and advisories for components
  version   print version`)
}

func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	kubeconfig := fs.String("kubeconfig", "", "path to kubeconfig (default: in-cluster, then $KUBECONFIG / ~/.kube/config)")
	kubeContext := fs.String("context", "", "kubeconfig context to use (out-of-cluster only)")
	once := fs.Bool("once", false, "collect and push once, then exit")
	printOnly := fs.Bool("print", false, "collect once and print the snapshot as JSON instead of pushing")
	interval := fs.Duration("interval", envDuration("OWL_INTERVAL", 15*time.Minute), "collection interval")
	server := fs.String("server", os.Getenv("OWL_SERVER_URL"), "server base URL (env OWL_SERVER_URL)")
	_ = fs.Parse(args)

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := kubeConfig(*kubeconfig, *kubeContext)
	if err != nil {
		return err
	}
	cfg.UserAgent = "service-owl-agent/" + version
	cfg.QPS, cfg.Burst = 5, 10 // be gentle with the API server
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	col := &agent.Collector{Client: client, Version: version}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *printOnly {
		snap, err := col.Collect(ctx)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snap)
	}

	a := &agent.Agent{
		Collector: col,
		Log:       log,
		HTTP:      agent.NewHTTPClient(),
		Config: agent.Config{
			ServerURL: *server,
			Token:     os.Getenv("OWL_TOKEN"), // env only: never accept the token as a flag (shows in ps)
			Interval:  *interval,
			Timeout:   5 * time.Minute,

			AllowInsecureHTTP: os.Getenv("OWL_ALLOW_INSECURE_HTTP") == "true",
		},
	}
	if *once {
		return a.RunOnce(ctx)
	}
	return a.Run(ctx)
}

func kubeConfig(path, kubeContext string) (*rest.Config, error) {
	if path == "" && kubeContext == "" {
		if cfg, err := rest.InClusterConfig(); err == nil {
			return cfg, nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
