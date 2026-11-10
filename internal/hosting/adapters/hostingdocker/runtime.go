// Package hostingdocker is the Docker runtime of hosted agents: it builds
// environment images and runs slots (slots.go), through the Docker Engine API at
// DOCKER_HOST (in production a docker-socket proxy that allows only what
// Seaport needs). The Docker SDK stays in this package.
package hostingdocker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
)

type Runtime struct {
	api *client.Client
	cfg Config
}

var _ hosting.Runtime = (*Runtime)(nil)

// Config says how slot containers are wired.
type Config struct {
	// Network is the Docker network slot containers join (default
	// "bridge").
	Network string
	// ExtraHosts are added to their /etc/hosts ("host.docker.internal:
	// host-gateway" lets them reach a port on the host).
	ExtraHosts []string
	// Egress: the port runs the egress proxy, so the network should be
	// internal (no route out but through it); New warns when it isn't.
	Egress bool
}

// New connects to the engine named by the environment (DOCKER_HOST,
// DOCKER_TLS_VERIFY, DOCKER_CERT_PATH, DOCKER_API_VERSION) and checks it
// answers.
func New(ctx context.Context, cfg Config) (*Runtime, error) {
	if cfg.Network == "" {
		cfg.Network = "bridge"
	}
	api, err := client.New(client.FromEnv)
	if err != nil {
		return nil, errx.Wrap(err, "docker client", errx.TypeInternal)
	}
	ping, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := api.Ping(ping, client.PingOptions{}); err != nil {
		_ = api.Close()
		return nil, errx.Wrap(err, "SEAPORT_RUNTIME=docker but the Docker engine (DOCKER_HOST) does not answer", errx.TypeInternal)
	}
	if cfg.Egress {
		n, err := api.NetworkInspect(ping, cfg.Network, client.NetworkInspectOptions{})
		switch {
		case err != nil:
			slog.Warn("hosting: can't inspect the runtime network", "network", cfg.Network, "err", err)
		case !n.Network.Internal:
			slog.Warn("hosting: the runtime network is not internal: slots can reach the internet around the egress proxy",
				"network", cfg.Network, "hint", "docker network create --internal <name>; SEAPORT_RUNTIME_NETWORK=<name>")
		}
	}
	return &Runtime{api: api, cfg: cfg}, nil
}

func (r *Runtime) Close() error { return r.api.Close() }

func (r *Runtime) Build(ctx context.Context, req hosting.BuildRequest, log io.Writer) (string, error) {
	tag := req.Tag()
	if !req.Force {
		if img, err := r.api.ImageInspect(ctx, tag); err == nil {
			fmt.Fprintf(log, "image %s is already built\n", tag)
			return img.ID, nil
		} else if !cerrdefs.IsNotFound(err) {
			return "", errx.Wrap(err, "inspect image", errx.TypeExternal)
		}
	}
	if err := r.base(ctx, req, log); err != nil {
		return "", err
	}
	if len(req.Spec.Packages) == 0 && req.Spec.Setup == "" && req.Spec.Home == "" {
		fmt.Fprintf(log, "no packages or setup: the environment runs on %s\n", req.Base)
	}
	ctxTar, err := buildContext(req)
	if err != nil {
		return "", err
	}
	res, err := r.api.ImageBuild(ctx, ctxTar, client.ImageBuildOptions{
		Tags: []string{tag}, Dockerfile: "Dockerfile", Remove: true, ForceRemove: true, NoCache: req.Force,
		// the base is local (pulled above); the classic builder takes the
		// in-memory context as is
		Version: build.BuilderV1,
		Labels: map[string]string{
			hosting.LabelOrganization: req.Organization.String(),
			hosting.LabelEnvironment:  req.Environment.String(),
			hosting.LabelVersion:      fmt.Sprint(req.Version),
		},
	})
	if err != nil {
		return "", errx.Wrap(err, "start the image build", errx.TypeExternal)
	}
	defer res.Body.Close()
	id, err := stream(res.Body, log)
	if err != nil {
		return "", err
	}
	if id == "" { // older engines report no aux id: inspect the tag
		img, err := r.api.ImageInspect(ctx, tag)
		if err != nil {
			return "", errx.Wrap(err, "inspect the built image", errx.TypeExternal)
		}
		id = img.ID
	}
	fmt.Fprintf(log, "built %s (%s)\n", tag, id)
	return id, nil
}

// base makes sure the base image is here (pulling it when missing or
// forced) and is Seaport's base for the spec's harness.
func (r *Runtime) base(ctx context.Context, req hosting.BuildRequest, log io.Writer) error {
	img, err := r.api.ImageInspect(ctx, req.Base)
	if req.Force || cerrdefs.IsNotFound(err) {
		fmt.Fprintf(log, "pulling %s\n", req.Base)
		if perr := r.pull(ctx, req.Base, log); perr != nil {
			if err == nil { // forced pull failed: the local image still serves
				fmt.Fprintf(log, "pull failed (%v); using the local %s\n", perr, req.Base)
			} else {
				return perr
			}
		}
		img, err = r.api.ImageInspect(ctx, req.Base)
	}
	if err != nil {
		return errx.Wrap(err, "inspect the base image", errx.TypeExternal)
	}
	var labels map[string]string
	if img.Config != nil {
		labels = img.Config.Labels
	}
	if labels[hosting.LabelBase] == "" {
		return errx.Business("image " + req.Base + " is not built FROM a Seaport base image (ghcr.io/abraxas-365/seaport-" +
			req.Spec.Harness + ")")
	}
	if h := labels[hosting.LabelHarness]; h != req.Spec.Harness {
		return errx.Business("image " + req.Base + " is a Seaport base for harness " + h + ", not " + req.Spec.Harness)
	}
	return nil
}

func (r *Runtime) pull(ctx context.Context, ref string, log io.Writer) error {
	res, err := r.api.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return errx.Wrap(err, "pull "+ref, errx.TypeExternal)
	}
	defer res.Close()
	last := ""
	for m, err := range res.JSONMessages(ctx) {
		if err != nil {
			return errx.Wrap(err, "pull "+ref, errx.TypeExternal)
		}
		if m.Error != nil {
			return errx.External("pull " + ref + ": " + m.Error.Message)
		}
		// progress lines repeat per layer: log each status once
		if line := strings.TrimSpace(m.ID + " " + m.Status); m.Progress == nil && line != last {
			fmt.Fprintln(log, line)
			last = line
		}
	}
	return nil
}

// buildContext is the tar with the derived Dockerfile, the setup script
// and the home's files (under hosting.HomeContext/<harness home>/).
func buildContext(req hosting.BuildRequest) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	files := map[string]string{"Dockerfile": hosting.Dockerfile(req.Spec, req.Base)}
	if req.Spec.Setup != "" {
		files[hosting.SetupFile] = req.Spec.Setup
	}
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), ModTime: time.Unix(0, 0)}); err != nil {
			return nil, errx.Wrap(err, "build context", errx.TypeInternal)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			return nil, errx.Wrap(err, "build context", errx.TypeInternal)
		}
	}
	if req.Spec.Home != "" {
		if err := homeContext(tw, req); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, errx.Wrap(err, "build context", errx.TypeInternal)
	}
	return &buf, nil
}

// homeContext copies the home's normalized tar (hosting.ReadHome: regular
// files, relative paths) under hosting.HomeContext/<harness home>/.
func homeContext(tw *tar.Writer, req hosting.BuildRequest) error {
	root := hosting.HomeRoot(req.Spec.Harness)
	if root == "" || len(req.Home) == 0 {
		return errx.Internal("the build has no home")
	}
	tr := tar.NewReader(bytes.NewReader(req.Home))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errx.Wrap(err, "build context: home", errx.TypeInternal)
		}
		if h.Typeflag != tar.TypeReg || hosting.HomePath(req.Spec.Harness, h.Name) != nil {
			return errx.Internal("build context: the home holds " + h.Name)
		}
		h.Name = hosting.HomeContext + "/" + root + "/" + h.Name
		if err := tw.WriteHeader(h); err != nil {
			return errx.Wrap(err, "build context: home", errx.TypeInternal)
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return errx.Wrap(err, "build context: home", errx.TypeInternal)
		}
	}
}

// stream copies a build's JSON messages to log and answers the image id
// (aux); a message with an error fails the build with its text.
func stream(body io.Reader, log io.Writer) (string, error) {
	dec := json.NewDecoder(body)
	id := ""
	for {
		var m jsonstream.Message
		if err := dec.Decode(&m); err == io.EOF {
			return id, nil
		} else if err != nil {
			return "", errx.Wrap(err, "read the build output", errx.TypeExternal)
		}
		if m.Error != nil {
			fmt.Fprintln(log, m.Error.Message)
			return "", errx.External(failure(m.Error.Message))
		}
		if m.Stream != "" {
			_, _ = io.WriteString(log, m.Stream)
		} else if m.Status != "" && m.Progress == nil {
			fmt.Fprintln(log, strings.TrimSpace(m.ID+" "+m.Status))
		}
		if m.Aux != nil {
			var res build.Result
			if json.Unmarshal(*m.Aux, &res) == nil && res.ID != "" {
				id = res.ID
			}
		}
	}
}

// failure is the detail of a failed build: a step's exit code (the
// command itself is in the log), else the engine's last line.
func failure(msg string) string {
	if _, code, ok := strings.Cut(msg, "returned a non-zero code: "); ok {
		step := "the packages step"
		if strings.Contains(msg, hosting.SetupFile) {
			step = "the setup script"
		}
		return step + " exited with code " + strings.TrimSpace(code) + " (see the log)"
	}
	return "the build failed: " + lastLine(msg)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
