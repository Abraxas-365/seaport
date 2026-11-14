package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Abraxas-365/seaport/internal/errx"
)

// providers are the well-known mail servers: IMAP and SMTP over implicit
// TLS. Their accounts need an app password (not the account's own).
var providers = map[string][2]string{
	"gmail":   {"imap.gmail.com", "smtp.gmail.com"},
	"outlook": {"outlook.office365.com", "smtp.office365.com"},
	"icloud":  {"imap.mail.me.com", "smtp.mail.me.com"},
}

// endpoint reads "host" or "host:port"; the port decides the TLS mode when
// it is the conventional STARTTLS one (143, 587) unless -tls says.
func endpoint(spec string, tlsPort, startPort int) (map[string]any, error) {
	host, port := spec, tlsPort
	if h, p, ok := strings.Cut(spec, ":"); ok {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, errx.Validation("bad port in " + spec)
		}
		host, port = h, n
	}
	mode := "tls"
	if port == startPort {
		mode = "starttls"
	}
	return map[string]any{"host": host, "port": port, "tls": mode}, nil
}

// dockEmail connects an email dock: both servers, the account and its
// password (never on the command line).
func dockEmail(ctx context.Context, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errx.Validation("usage: seaport dock email <dock-id> -address A (-provider gmail|outlook|icloud | -imap H[:P] -smtp H[:P]) [-username U] [-folder F] [-display-name N]")
	}
	id := args[0]
	fs := flag.NewFlagSet("dock email", flag.ContinueOnError)
	address := fs.String("address", "", "the mailbox's email address")
	provider := fs.String("provider", "", "gmail, outlook or icloud: fills in the servers")
	imap := fs.String("imap", "", "IMAP server host[:port] (default port 993, TLS; 143 is STARTTLS)")
	smtp := fs.String("smtp", "", "SMTP server host[:port] (default port 465, TLS; 587 is STARTTLS)")
	username := fs.String("username", "", "login name (default: the address)")
	folder := fs.String("folder", "", "folder to read (default INBOX)")
	display := fs.String("display-name", "", "name shown on replies")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *address == "" {
		return errx.Validation("-address is required")
	}
	if p, ok := providers[strings.ToLower(*provider)]; ok {
		if *imap == "" {
			*imap = p[0]
		}
		if *smtp == "" {
			*smtp = p[1]
		}
	} else if *provider != "" {
		return errx.Validation("unknown provider " + *provider + " (gmail, outlook, icloud; or name the servers)")
	}
	if *imap == "" || *smtp == "" {
		return errx.Validation("name the servers with -provider, or -imap and -smtp")
	}
	im, err := endpoint(*imap, 993, 143)
	if err != nil {
		return err
	}
	sm, err := endpoint(*smtp, 465, 587)
	if err != nil {
		return err
	}
	password, err := readSecret("the mailbox password (an app password for Gmail, Outlook, iCloud)")
	if err != nil {
		return err
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	body := map[string]any{"address": *address, "display_name": *display, "imap": im, "smtp": sm,
		"username": *username, "folder": *folder, "password": password}
	var out struct {
		Account *struct {
			Address string `json:"address"`
			Folder  string `json:"folder"`
		} `json:"account"`
	}
	if err := a.do(ctx, http.MethodPut, "/api/v1/docks/"+url.PathEscape(id)+"/email", body, &out); err != nil {
		return err
	}
	if out.Account != nil {
		fmt.Printf("dock %s reads %s (%s) and answers by SMTP\n", id, out.Account.Address, out.Account.Folder)
	}
	return nil
}
