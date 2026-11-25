package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Image vulnerability scan results (milestone 15). Local mirrors of internal/server's JSON.

type sevCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

type vulnImage struct {
	Key          string    `json:"key"`
	Digest       string    `json:"digest"`
	Ref          string    `json:"ref"`
	Repositories []string  `json:"repositories"`
	InHouse      bool      `json:"inHouse"`
	Components   []string  `json:"components"`
	State        string    `json:"state"`
	ScannedAt    *string   `json:"scannedAt"`
	Error        string    `json:"error"`
	Fixable      sevCounts `json:"fixable"`
	Total        sevCounts `json:"total"`
	Accepted     int       `json:"accepted"`
	Places       []struct {
		Cluster   string `json:"cluster"`
		Namespace string `json:"namespace"`
		Workload  string `json:"workload"`
	} `json:"places"`
}

type vulnRow struct {
	ID        string `json:"id"`
	Package   string `json:"package"`
	Installed string `json:"installed"`
	Fixed     string `json:"fixed"`
	Severity  string `json:"severity"`
	Class     string `json:"class"`
	Target    string `json:"target"`
	Accepted  *struct {
		Reason string `json:"reason"`
	} `json:"accepted"`
}

func runVulns(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("vulns", "vulns [--in-house] [--image <digest or repository>] [--fail-on critical|high [--allow-unscanned]] [-o json]", true)
	inHouse := f.Bool("in-house", false, "only in-house images")
	image := f.String("image", "", "one image's findings: its digest (sha256:…) or a repository (registry/team/app)")
	failOn := f.String("fail-on", "", "exit 1 if a shown image has a fixable finding at this level or above: critical | high (for CI);\nan image without a scan result (queued, failed, not scannable, scanning off) fails too")
	allowUnscanned := f.Bool("allow-unscanned", false, "with --fail-on: let images without a scan result pass")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return a.usageErr("unexpected arguments: %s", strings.Join(pos, " "))
	}
	if *failOn != "" && *failOn != "critical" && *failOn != "high" {
		return a.usageErr("--fail-on: critical or high")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var list struct {
		Enabled bool        `json:"enabled"`
		Images  []vulnImage `json:"images"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/vulns", nil, &list); err != nil {
		return err
	}
	if !list.Enabled {
		fmt.Fprintln(a.Stderr, "owl: image scanning is off on this server")
	}
	shown := []vulnImage{}
	for _, img := range list.Images {
		if *inHouse && !img.InHouse || *image != "" && !matchesImage(img, *image) {
			continue
		}
		shown = append(shown, img)
	}
	gate := func(img vulnImage) string { // why this image fails --fail-on ("" = passes)
		switch {
		case *failOn == "":
			return ""
		case img.ScannedAt == nil && !*allowUnscanned:
			return "no scan result (" + img.State + ")"
		case img.Fixable.Critical > 0 || *failOn == "high" && img.Fixable.High > 0:
			return "fixable " + *failOn + " findings"
		}
		return ""
	}
	if *image != "" && len(shown) == 1 && shown[0].Digest != "" {
		return a.vulnDetail(ctx, c, f, shown[0], gate(shown[0]))
	}
	var failures []string
	for _, img := range shown {
		if why := gate(img); why != "" {
			failures = append(failures, img.Ref+": "+why)
		}
	}
	switch {
	case f.json():
		if err := a.printJSON(shown); err != nil {
			return err
		}
	case len(shown) == 0 && *image != "":
		return fmt.Errorf("no running image matches %q", *image)
	case len(shown) == 0:
		fmt.Fprintln(a.Stdout, "No images running.")
	default:
		tw := a.table()
		fmt.Fprintln(tw, "CRITICAL\tHIGH\tIN-HOUSE\tSTATE\tIMAGE\tWHERE")
		for _, img := range shown {
			crit, high := "-", "-"
			if img.ScannedAt != nil {
				crit, high = strconv.Itoa(img.Fixable.Critical), strconv.Itoa(img.Fixable.High)
			}
			house := ""
			if img.InHouse {
				house = "yes"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", crit, high, house, img.State, img.Ref, where(img))
		}
		tw.Flush()
		if *image != "" && len(shown) > 1 {
			fmt.Fprintln(a.Stderr, "owl: several images match; pass a digest for one image's findings")
		}
	}
	if len(failures) > 0 {
		for _, f := range failures {
			fmt.Fprintf(a.Stderr, "owl: %s (--fail-on %s)\n", f, *failOn)
		}
		return exitError{1}
	}
	return nil
}

func matchesImage(img vulnImage, q string) bool {
	if strings.HasPrefix(q, "sha256:") {
		return img.Digest == q
	}
	for _, r := range img.Repositories {
		if r == q {
			return true
		}
	}
	repo, _, _ := strings.Cut(img.Ref, "@")
	return repo == q
}

func where(img vulnImage) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range img.Places {
		s := p.Cluster + ":" + p.Namespace + "/" + p.Workload
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) > 2 {
		return strings.Join(out[:2], ", ") + fmt.Sprintf(" +%d", len(out)-2)
	}
	return strings.Join(out, ", ")
}

// vulnDetail prints one image's findings; fail is why it fails --fail-on ("" = it passes).
func (a *App) vulnDetail(ctx context.Context, c *client, f *flags, img vulnImage, fail string) error {
	var d struct {
		Vulns     []vulnRow `json:"vulns"`
		Confirmed []struct {
			ID        string `json:"id"`
			Component string `json:"component"`
		} `json:"confirmed"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/vulns/images/"+url.PathEscape(img.Digest), nil, &d); err != nil {
		return err
	}
	if f.json() {
		if err := a.printJSON(map[string]any{"image": img, "vulns": d.Vulns, "confirmed": d.Confirmed}); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(a.Stdout, "%s (%s)\n", img.Ref, img.State)
		if img.Error != "" {
			fmt.Fprintf(a.Stdout, "  last attempt: %s\n", img.Error)
		}
		if len(d.Vulns) == 0 {
			fmt.Fprintln(a.Stdout, "  No findings.")
		} else {
			tw := a.table()
			fmt.Fprintln(tw, "SEVERITY\tID\tPACKAGE\tINSTALLED\tFIXED\tNOTE")
			for _, v := range d.Vulns {
				note := v.Target
				if v.Accepted != nil {
					note = "accepted: " + v.Accepted.Reason
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", strings.ToUpper(v.Severity), v.ID, v.Package, v.Installed, dash(v.Fixed), note)
			}
			tw.Flush()
		}
		for _, cf := range d.Confirmed {
			fmt.Fprintf(a.Stdout, "  %s: also a component advisory (owl component %s)\n", cf.ID, cf.Component)
		}
	}
	if fail != "" {
		fmt.Fprintf(a.Stderr, "owl: %s: %s\n", img.Ref, fail)
		return exitError{1}
	}
	return nil
}
