package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"text/tabwriter"

	"github.com/Abraxas-365/seaport/internal/errx"
)

const dockWhatsAppUsage = "usage: seaport dock whatsapp <dock-id> [react on|off] [emoji <emoji>] [read on|off]"

// whatsappSettings is the admin API's answer for a WhatsApp dock.
type whatsappSettings struct {
	React    bool   `json:"react"`
	Emoji    string `json:"emoji"`
	MarkRead bool   `json:"mark_read"`
}

// dockWhatsApp shows a WhatsApp dock's settings, or changes the ones named
// (what the dock does to a message an agent has) and shows the result.
func dockWhatsApp(ctx context.Context, args []string) error {
	if len(args) == 0 || len(args)%2 == 0 {
		return errx.Validation(dockWhatsAppUsage)
	}
	patch := map[string]any{}
	for i := 1; i < len(args); i += 2 {
		name, v := args[i], args[i+1]
		switch {
		case name == "emoji":
			patch["emoji"] = v
		case (name == "react" || name == "read") && (v == "on" || v == "off"):
			key := name
			if name == "read" {
				key = "mark_read"
			}
			patch[key] = v == "on"
		default:
			return errx.Validation(dockWhatsAppUsage)
		}
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	var s whatsappSettings
	path := "/api/v1/docks/" + url.PathEscape(args[0]) + "/whatsapp"
	if len(patch) == 0 {
		err = a.do(ctx, http.MethodGet, path, nil, &s)
	} else {
		err = a.do(ctx, http.MethodPut, path, patch, &s)
	}
	if err != nil {
		return err
	}
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	fmt.Fprintf(stdout, "dock %s, when an agent gets a message:\n", args[0])
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "  react\t%s\treact with %s\n", onOff(s.React), s.Emoji)
	fmt.Fprintf(w, "  read\t%s\tmark it read (blue ticks)\n", onOff(s.MarkRead))
	return w.Flush()
}
