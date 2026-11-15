package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Abraxas-365/seaport/internal/errx"
)

// datadogInfo is the admin API's answer for a Datadog dock.
type datadogInfo struct {
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
	Payload   string `json:"payload"`
	HasSecret bool   `json:"has_secret"`
	Secret    string `json:"secret"`
	Mirror    *struct {
		Dock        string `json:"dock_id"`
		Destination string `json:"destination"`
	} `json:"mirror"`
}

// dockDatadog shows a Datadog dock's endpoint and payload, sets or clears
// its mirror, or makes a new secret.
func dockDatadog(ctx context.Context, args []string) error {
	const usage = "usage: seaport dock datadog <dock-id> [-mirror <dock-id>:<destination> | -no-mirror] [-rotate]"
	if len(args) == 0 {
		return errx.Validation(usage)
	}
	id := args[0]
	fs := flag.NewFlagSet("dock datadog", flag.ContinueOnError)
	mirror := fs.String("mirror", "", "destination where each new alert thread is opened, as <dock-id>:<destination name>")
	noMirror := fs.Bool("no-mirror", false, "open alert threads nowhere")
	rotate := fs.Bool("rotate", false, "make a new secret (the old one stops working) and print it once")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *mirror != "" && *noMirror {
		return errx.Validation("-mirror and -no-mirror exclude each other")
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	path := "/api/v1/docks/" + url.PathEscape(id) + "/datadog"
	var info datadogInfo
	switch {
	case *mirror != "" || *noMirror:
		req := map[string]any{"mirror": nil}
		if *mirror != "" {
			dockID, dest, ok := strings.Cut(*mirror, ":")
			if !ok || dockID == "" || dest == "" {
				return errx.Validation("-mirror must be <dock-id>:<destination name>")
			}
			req["mirror"] = map[string]string{"dock_id": dockID, "destination": dest}
		}
		if err := a.do(ctx, http.MethodPut, path, req, &info); err != nil {
			return err
		}
		fmt.Println("mirror saved")
	case *rotate:
		if err := a.do(ctx, http.MethodPost, path+"/secret", nil, &info); err != nil {
			return err
		}
	default:
		if err := a.do(ctx, http.MethodGet, path, nil, &info); err != nil {
			return err
		}
	}
	fmt.Printf("url:      %s\n", info.URL)
	if !info.Reachable {
		fmt.Println("          (not reachable from the internet: set SEAPORT_PUBLIC_URL to an https address)")
	}
	if info.Secret != "" {
		fmt.Printf("secret:   %s\n          (shown once; send it as the custom header X-Seaport-Token of Datadog's webhook)\n", info.Secret)
	}
	if info.Mirror != nil {
		fmt.Printf("mirror:   %s:%s\n", info.Mirror.Dock, info.Mirror.Destination)
	} else {
		fmt.Println("mirror:   (none: alerts wake the agent, nothing is posted anywhere)")
	}
	fmt.Printf("payload:  (paste in Datadog's webhook integration)\n%s\n", info.Payload)
	return nil
}
