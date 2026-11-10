package hostingdocker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// Slot containers and volumes: seaport-slot-<key> (+ -state, -ws).
const slotPrefix = "seaport-slot-"

func containerName(ref hosting.SlotRef) string { return slotPrefix + ref.Key() }
func stateVolume(ref hosting.SlotRef) string   { return slotPrefix + ref.Key() + "-state" }
func workVolume(ref hosting.SlotRef) string    { return slotPrefix + ref.Key() + "-ws" }

// slotLabels label a slot's container and volumes: the supervisor finds
// them by port and slot.
func slotLabels(port string, ref hosting.SlotRef) map[string]string {
	return map[string]string{
		hosting.LabelPort:         port,
		hosting.LabelOrganization: ref.Organization.String(),
		hosting.LabelAgent:        ref.Agent.String(),
		hosting.LabelSlot:         strconv.Itoa(ref.Index),
	}
}

func (r *Runtime) Start(ctx context.Context, s hosting.StartSlot) error {
	name := containerName(s.Slot)
	// A leftover container (a crash, a failed start) goes first; its
	// volumes stay.
	if err := r.remove(ctx, name, ""); err != nil {
		return err
	}
	labels := slotLabels(s.Port, s.Slot)
	mounts := []mount.Mount{
		// The home is a fresh anonymous volume seeded from the image at
		// each start (the harness writes there); what must survive a
		// restart lives in the state volume, linked by agent-run.
		{Type: mount.TypeVolume, Target: "/home/agent"},
		{Type: mount.TypeVolume, Source: stateVolume(s.Slot), Target: hosting.StateDir,
			VolumeOptions: &mount.VolumeOptions{Labels: labels}},
		// /tmp is writable but small and not executable-proof: tools need it.
		{Type: mount.TypeTmpfs, Target: "/tmp", TmpfsOptions: &mount.TmpfsOptions{SizeBytes: 512 << 20, Mode: 0o1777}},
	}
	ws := mount.Mount{Type: mount.TypeVolume, Target: hosting.WorkspaceDir}
	if s.Persistent {
		ws.Source, ws.VolumeOptions = workVolume(s.Slot), &mount.VolumeOptions{Labels: labels}
	}
	mounts = append(mounts, ws)
	env := make([]string, 0, len(s.Env))
	for k, v := range s.Env {
		env = append(env, k+"="+v)
	}
	containerLabels := map[string]string{hosting.LabelRun: s.Run}
	for k, v := range labels {
		containerLabels[k] = v
	}
	pids := int64(1024)
	stop := int(hosting.StopTimeout.Seconds())
	init := true
	host := &container.HostConfig{
		Mounts:         mounts,
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		Init:           &init,
		NetworkMode:    container.NetworkMode(r.cfg.Network),
		ExtraHosts:     r.cfg.ExtraHosts,
		Resources: container.Resources{
			NanoCPUs:  int64(s.Resources.CPUs * 1e9),
			Memory:    int64(s.Resources.MemoryMB) << 20,
			PidsLimit: &pids,
		},
		// A crashed agent comes back by itself (a few times); a slot
		// stopped on purpose is removed, so it doesn't.
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 5},
	}
	cfg := &container.Config{
		Image:       s.Image,
		User:        "1000:1000",
		Env:         env,
		Labels:      containerLabels,
		Hostname:    hostname(s.Name),
		WorkingDir:  hosting.WorkspaceDir,
		StopTimeout: &stop,
		// the harness reads its stdin: keep it open (rness exits on EOF)
		OpenStdin: true,
	}
	if _, err := r.api.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: cfg, HostConfig: host}); err != nil {
		return errx.Wrap(err, "create the slot's container", errx.TypeExternal)
	}
	if _, err := r.api.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		_ = r.remove(context.WithoutCancel(ctx), name, "")
		return errx.Wrap(err, "start the slot's container", errx.TypeExternal)
	}
	return nil
}

// hostname is the slot's name as a hostname (what its prompt shows).
func hostname(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	h := strings.Trim(b.String(), "-")
	if len(h) > 63 {
		h = h[:63]
	}
	if h == "" {
		return "slot"
	}
	return h
}

func (r *Runtime) Stop(ctx context.Context, ref hosting.SlotRef, run string) error {
	return r.remove(ctx, containerName(ref), run)
}

// remove stops (StopTimeout) and removes a container, if there is one
// (of run, when set).
func (r *Runtime) remove(ctx context.Context, name, run string) error {
	got, err := r.api.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return errx.Wrap(err, "inspect the slot's container", errx.TypeExternal)
	}
	if run != "" && (got.Container.Config == nil || got.Container.Config.Labels[hosting.LabelRun] != run) {
		return nil // another run's container: not ours to stop
	}
	timeout := int(hosting.StopTimeout.Seconds())
	if _, err := r.api.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: &timeout}); err != nil &&
		!cerrdefs.IsNotFound(err) && !cerrdefs.IsNotModified(err) {
		return errx.Wrap(err, "stop the slot's container", errx.TypeExternal)
	}
	// RemoveVolumes takes the anonymous ones (home, ephemeral workspace);
	// named volumes stay.
	if _, err := r.api.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil &&
		!cerrdefs.IsNotFound(err) && !cerrdefs.IsConflict(err) {
		return errx.Wrap(err, "remove the slot's container", errx.TypeExternal)
	}
	return nil
}

func (r *Runtime) Remove(ctx context.Context, ref hosting.SlotRef) error {
	if err := r.remove(ctx, containerName(ref), ""); err != nil {
		return err
	}
	for _, v := range []string{stateVolume(ref), workVolume(ref)} {
		if _, err := r.api.VolumeRemove(ctx, v, client.VolumeRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			return errx.Wrap(err, "remove the slot's volume", errx.TypeExternal)
		}
	}
	return nil
}

func (r *Runtime) Slots(ctx context.Context, port string) ([]hosting.RuntimeSlot, error) {
	filter := client.Filters{}.Add("label", hosting.LabelPort+"="+port)
	containers, err := r.api.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		return nil, errx.Wrap(err, "list slot containers", errx.TypeExternal)
	}
	byKey := map[string]*hosting.RuntimeSlot{}
	var order []string
	add := func(labels map[string]string) *hosting.RuntimeSlot {
		ref, ok := slotRef(labels)
		if !ok {
			return nil
		}
		k := ref.Key()
		if s := byKey[k]; s != nil {
			return s
		}
		byKey[k] = &hosting.RuntimeSlot{Slot: ref}
		order = append(order, k)
		return byKey[k]
	}
	for _, c := range containers.Items {
		s := add(c.Labels)
		if s == nil {
			continue
		}
		s.Container, s.Run = true, c.Labels[hosting.LabelRun]
		s.Running = c.State == container.StateRunning || c.State == container.StateRestarting
		if !s.Running {
			s.Detail = "the agent's container " + string(c.State) + ": " + c.Status
		}
	}
	volumes, err := r.api.VolumeList(ctx, client.VolumeListOptions{Filters: filter})
	if err != nil {
		return nil, errx.Wrap(err, "list slot volumes", errx.TypeExternal)
	}
	for _, v := range volumes.Items {
		add(v.Labels)
	}
	out := make([]hosting.RuntimeSlot, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out, nil
}

func slotRef(labels map[string]string) (hosting.SlotRef, bool) {
	org, err1 := identity.ParseOrganizationID(labels[hosting.LabelOrganization])
	agent, err2 := identity.ParseAgentID(labels[hosting.LabelAgent])
	idx, err3 := strconv.Atoi(labels[hosting.LabelSlot])
	if err1 != nil || err2 != nil || err3 != nil || idx < 1 {
		return hosting.SlotRef{}, false
	}
	return hosting.SlotRef{Organization: org, Agent: agent, Index: idx}, true
}

// String names the runtime in logs.
func (r *Runtime) String() string { return fmt.Sprintf("docker (network %s)", r.cfg.Network) }

func (r *Runtime) Exec(ctx context.Context, ref hosting.SlotRef, run string, cmd []string, stdin io.Reader, out io.Writer) (int, error) {
	name := containerName(ref)
	got, err := r.api.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return 0, errx.NotFound("the slot has no container (it is stopped): start it first")
	}
	if err != nil {
		return 0, errx.Wrap(err, "inspect the slot's container", errx.TypeExternal)
	}
	c := got.Container
	if c.State == nil || !c.State.Running || (run != "" && (c.Config == nil || c.Config.Labels[hosting.LabelRun] != run)) {
		return 0, errx.Business("the slot's container isn't running (it is starting or stopping): try again")
	}
	created, err := r.api.ExecCreate(ctx, name, client.ExecCreateOptions{User: "1000:1000", Cmd: cmd,
		AttachStdin: stdin != nil, AttachStdout: true, AttachStderr: true}) // in the image's WORKDIR
	if err != nil {
		return 0, errx.Wrap(err, "run in the slot's container", errx.TypeExternal)
	}
	att, err := r.api.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return 0, errx.Wrap(err, "run in the slot's container", errx.TypeExternal)
	}
	defer att.Close()
	go func() { // ctx ending ends the attach (the command goes with the container's stdin closing)
		<-ctx.Done()
		att.Close()
	}()
	if stdin != nil {
		go func() {
			_, _ = io.Copy(att.Conn, stdin)
			_ = att.CloseWrite()
		}()
	}
	// No TTY: stdout and stderr come multiplexed.
	if _, err := stdcopy.StdCopy(out, out, att.Reader); err != nil && ctx.Err() == nil {
		return 0, errx.Wrap(err, "read the command's output", errx.TypeExternal)
	}
	if ctx.Err() != nil {
		return 0, errx.Wrap(ctx.Err(), "run in the slot's container", errx.TypeExternal)
	}
	ins, err := r.api.ExecInspect(context.WithoutCancel(ctx), created.ID, client.ExecInspectOptions{})
	if err != nil {
		return 0, errx.Wrap(err, "read the command's exit", errx.TypeExternal)
	}
	return ins.ExitCode, nil
}

// maxLogBytes bounds what Logs answers (its tail): lines can be long.
const maxLogBytes = 1 << 20

func (r *Runtime) Logs(ctx context.Context, ref hosting.SlotRef, lines int) (string, error) {
	rc, err := r.api.ContainerLogs(ctx, containerName(ref), client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Timestamps: true, Tail: strconv.Itoa(lines)})
	if cerrdefs.IsNotFound(err) {
		return "", errx.NotFound("the slot has no container (it is stopped): start it to see its log")
	}
	if err != nil {
		return "", errx.Wrap(err, "read the slot's log", errx.TypeExternal)
	}
	defer rc.Close()
	// No TTY: stdout and stderr come multiplexed, frame by frame in order.
	var out tail
	if _, err := stdcopy.StdCopy(&out, &out, rc); err != nil {
		return "", errx.Wrap(err, "read the slot's log", errx.TypeExternal)
	}
	return out.String(), nil
}

// tail keeps the last maxLogBytes written to it, from a line start.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if over := len(t.b) - 2*maxLogBytes; over > 0 { // trim now and then, not per write
		t.b = append(t.b[:0], t.b[len(t.b)-maxLogBytes:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	b := t.b
	if len(b) > maxLogBytes {
		b = b[len(b)-maxLogBytes:]
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return string(b)
}
