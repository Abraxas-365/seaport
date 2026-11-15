package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/Abraxas-365/seaport/internal/errx"
)

// webhookInfo is the admin API's answer for a webhook dock.
type webhookInfo struct {
	URL       string         `json:"url"`
	Reachable bool           `json:"reachable"`
	Template  map[string]any `json:"template"`
	HasSecret bool           `json:"has_secret"`
	Secret    string         `json:"secret"`
	Preview   map[string]any `json:"preview"`
}

// dockWebhook shows a webhook dock's endpoint, sets its template or makes
// a new secret.
func dockWebhook(ctx context.Context, args []string) error {
	const usage = "usage: seaport dock webhook <dock-id> [-template file.json [-sample body.json]] [-rotate]"
	if len(args) == 0 {
		return errx.Validation(usage)
	}
	id := args[0]
	fs := flag.NewFlagSet("dock webhook", flag.ContinueOnError)
	templateFile := fs.String("template", "", "JSON file mapping the body to the message (chat_id, chat_title, external_id, sender_id, sender_name, text, tags, agent)")
	sampleFile := fs.String("sample", "", "JSON body to preview the template with (needs -template)")
	rotate := fs.Bool("rotate", false, "make a new secret (the old one stops working) and print it once")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *sampleFile != "" && *templateFile == "" {
		return errx.Validation("-sample needs -template")
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	path := "/api/v1/docks/" + url.PathEscape(id) + "/webhook"
	var info webhookInfo
	switch {
	case *templateFile != "":
		var req struct {
			Template json.RawMessage `json:"template"`
			Sample   json.RawMessage `json:"sample,omitempty"`
		}
		if req.Template, err = os.ReadFile(*templateFile); err != nil {
			return errx.Validation("read the template: " + err.Error())
		}
		if !json.Valid(req.Template) {
			return errx.Validation(*templateFile + " is not valid JSON")
		}
		if *sampleFile != "" {
			if req.Sample, err = os.ReadFile(*sampleFile); err != nil {
				return errx.Validation("read the sample: " + err.Error())
			}
			if !json.Valid(req.Sample) {
				return errx.Validation(*sampleFile + " is not valid JSON")
			}
		}
		if err := a.do(ctx, http.MethodPut, path, req, &info); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "template saved")
	case *rotate:
		if err := a.do(ctx, http.MethodPost, path+"/secret", nil, &info); err != nil {
			return err
		}
	default:
		if err := a.do(ctx, http.MethodGet, path, nil, &info); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "url:      %s\n", info.URL)
	if !info.Reachable {
		fmt.Fprintln(stdout, "          (not reachable from the internet: set SEAPORT_PUBLIC_URL to an https address)")
	}
	if info.Secret != "" {
		fmt.Fprintf(stdout, "secret:   %s\n          (shown once; send it as Authorization: Bearer, X-Seaport-Token, or sign the body with X-Seaport-Signature: sha256=<hmac>)\n", info.Secret)
	}
	if len(info.Template) > 0 {
		raw, _ := json.MarshalIndent(info.Template, "", "  ")
		fmt.Fprintf(stdout, "template: %s\n", raw)
	} else {
		fmt.Fprintln(stdout, "template: (default: one conversation, the body as JSON)")
	}
	if info.Preview != nil {
		raw, _ := json.MarshalIndent(info.Preview, "", "  ")
		fmt.Fprintf(stdout, "preview:  %s\n", raw)
	}
	return nil
}
