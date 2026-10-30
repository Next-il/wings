// Package proxy runs NextHost's domain proxy: Traefik in a container next to the servers, on the
// Wings Docker network, routing each customer domain to its server's container (see
// environment.Domain and config.ProxyConfiguration). Wings creates it and keeps it running, and
// writes the routes as files Traefik watches (routes.go). Traefik also still reads the labels of
// containers created before routes came from files, until those are recreated.
package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strconv"

	"emperror.dev/errors"
	"github.com/apex/log"
	"github.com/docker/docker/api/types/container"
	dockerImage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"github.com/pelican/wings/config"
	"github.com/pelican/wings/environment"
)

// SpecLabel holds the hash of the settings the container was created with: a change recreates it.
const SpecLabel = "nexthost.proxy.spec"

// Spec is everything the proxy container is created from.
type Spec struct {
	Name    string
	Config  *container.Config
	Host    *container.HostConfig
	Network *network.NetworkingConfig
	Hash    string
}

// Args is Traefik's static configuration: routes come from the files Wings writes (watched, so a
// change applies at once) and from the labels of containers that opt in, over the Wings network; one
// ACME resolver named "le", its HTTP challenge on port 80. Plain HTTP is not redirected here, on the
// entrypoint, but by routes: a catch-all at the lowest priority (BaseRoutes), so a domain that asks
// for plain HTTP can have it. No dashboard or API.
func Args(cfg config.ProxyConfiguration, networkName string) []string {
	args := []string{
		"--providers.docker=true",
		"--providers.docker.exposedbydefault=false",
		"--providers.docker.network=" + networkName,
		"--providers.file.directory=" + containerDynamicDir,
		"--providers.file.watch=true",
		"--entrypoints.web.address=:80",
		"--entrypoints.websecure.address=:443",
		"--certificatesresolvers.le.acme.storage=/data/acme.json",
		"--certificatesresolvers.le.acme.httpchallenge=true",
		"--certificatesresolvers.le.acme.httpchallenge.entrypoint=web",
		"--log.level=INFO",
	}
	if cfg.AcmeEmail != "" {
		args = append(args, "--certificatesresolvers.le.acme.email="+cfg.AcmeEmail)
	}
	return args
}

// logConfig is the proxy's log: one file of 5 MB. The local driver compresses rotated files unless
// told not to, and Docker refuses to start a container that asks for compression with one file.
var logConfig = container.LogConfig{Type: "local", Config: map[string]string{"max-size": "5m", "max-file": "1", "compress": "false"}}

// BuildSpec is the container the configuration asks for. Pure, so it can be tested without Docker.
func BuildSpec(cfg config.ProxyConfiguration, networkName string) Spec {
	args := Args(cfg, networkName)
	mounts := []mount.Mount{
		{Type: mount.TypeBind, Source: "/var/run/docker.sock", Target: "/var/run/docker.sock", ReadOnly: true},
		{Type: mount.TypeBind, Source: cfg.DataDir, Target: "/data"},
		{Type: mount.TypeBind, Source: DynamicDir(cfg), Target: containerDynamicDir, ReadOnly: true},
	}

	// Everything the container is made from: a node that upgrades Wings gets a new container when
	// what this build makes differs from what an older one made
	sum := sha256.New()
	_ = json.NewEncoder(sum).Encode(struct {
		Image     string
		Args      []string
		HTTPPort  int
		HTTPSPort int
		DataDir   string
		Network   string
		Mounts    []mount.Mount
		Log       container.LogConfig
	}{cfg.Image, args, cfg.HTTPPort, cfg.HTTPSPort, cfg.DataDir, networkName, mounts, logConfig})
	hash := hex.EncodeToString(sum.Sum(nil))[:16]

	return Spec{
		Name: cfg.ContainerName,
		Config: &container.Config{
			Image:        cfg.Image,
			Cmd:          args,
			ExposedPorts: nat.PortSet{"80/tcp": {}, "443/tcp": {}},
			Labels: map[string]string{
				"Service":       "NextHost",
				"ContainerType": "proxy",
				SpecLabel:       hash,
			},
		},
		Host: &container.HostConfig{
			// No host IP: every interface, IPv4 and IPv6 - a domain may point at any of the node's addresses
			PortBindings: nat.PortMap{
				"80/tcp":  {{HostPort: strconv.Itoa(cfg.HTTPPort)}},
				"443/tcp": {{HostPort: strconv.Itoa(cfg.HTTPSPort)}},
			},
			Mounts:        mounts,
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			LogConfig:     logConfig,
			NetworkMode:   container.NetworkMode(networkName),
		},
		Network: &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{networkName: {}},
		},
		Hash: hash,
	}
}

// Status is what GET /api/system reports about the proxy.
type Status struct {
	Enabled bool   `json:"enabled"`
	Running bool   `json:"running"`
	Image   string `json:"image"`
	// Features is RouteFeatures when the proxy container is the one this build makes, so it routes
	// from Wings' files; empty otherwise.
	Features []string `json:"features"`
}

// State reads the proxy's status. Running is false, and Features empty, when Docker cannot be asked.
func State(ctx context.Context) Status {
	cfg := config.Get().Proxy
	status := Status{Enabled: cfg.Enabled, Image: cfg.Image, Features: []string{}}

	cli, err := environment.Docker()
	if err != nil {
		return status
	}
	if c, err := cli.ContainerInspect(ctx, cfg.ContainerName); err == nil {
		if c.State != nil {
			status.Running = c.State.Running
		}
		spec := BuildSpec(cfg, config.Get().Docker.Network.Name)
		if cfg.Enabled && c.Config != nil && c.Config.Labels[SpecLabel] == spec.Hash {
			status.Features = RouteFeatures
		}
	}
	return status
}

// Ensure makes the proxy container match the configuration: created when missing, recreated when its
// settings changed, started when stopped. With the proxy off it never creates one, and leaves one that
// is already there alone.
func Ensure(ctx context.Context) error {
	cfg := config.Get().Proxy
	logger := log.WithField("subsystem", "proxy").WithField("container", cfg.ContainerName)

	cli, err := environment.Docker()
	if err != nil {
		return err
	}

	existing, err := cli.ContainerInspect(ctx, cfg.ContainerName)
	found := err == nil
	if err != nil && !client.IsErrNotFound(err) {
		return errors.WithMessage(err, "proxy: could not inspect the container")
	}

	if !cfg.Enabled {
		if found {
			logger.Info("the proxy is off in config.yml - leaving the existing container alone")
		}
		return nil
	}

	// acme.json is created by Traefik with mode 600; the folder holding it is Wings' (root) only. The
	// routes' folder in it has to be there before the container that mounts it, and the base routes
	// in it before Traefik reads them
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return errors.WithMessage(err, "proxy: could not create the data directory")
	}
	if err := writeBaseRoutes(DynamicDir(cfg)); err != nil {
		return errors.WithMessage(err, "proxy: could not write the base routes")
	}

	spec := BuildSpec(cfg, config.Get().Docker.Network.Name)

	if found && existing.Config != nil && existing.Config.Labels[SpecLabel] == spec.Hash {
		if existing.State != nil && existing.State.Running {
			return nil
		}
		logger.Info("starting the proxy container")
		return cli.ContainerStart(ctx, existing.ID, container.StartOptions{})
	}

	if found {
		logger.Info("the proxy settings changed - recreating the container")
		if err := cli.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true}); err != nil {
			return errors.WithMessage(err, "proxy: could not remove the old container")
		}
	}

	if _, err := cli.ImageInspect(ctx, cfg.Image); err != nil {
		logger.WithField("image", cfg.Image).Info("pulling the proxy image")
		out, err := cli.ImagePull(ctx, cfg.Image, dockerImage.PullOptions{})
		if err != nil {
			return errors.WithMessage(err, "proxy: could not pull the image")
		}
		_, _ = io.Copy(io.Discard, out)
		_ = out.Close()
	}

	created, err := cli.ContainerCreate(ctx, spec.Config, spec.Host, spec.Network, nil, spec.Name)
	if err != nil {
		return errors.WithMessage(err, "proxy: could not create the container")
	}
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return errors.WithMessage(err, "proxy: could not start the container")
	}

	logger.WithField("image", cfg.Image).Info("the proxy is running")
	return nil
}
