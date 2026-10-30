package proxy

import (
	"slices"
	"strings"
	"testing"

	"github.com/creasty/defaults"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"gopkg.in/yaml.v2"

	"github.com/pelican/wings/config"
)

func defaultConfig(t *testing.T) config.ProxyConfiguration {
	t.Helper()
	var cfg config.ProxyConfiguration
	if err := defaults.Set(&cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// On when config.yml has no proxy section (every node that upgrades), off only when it says so
func TestEnabledFromConfigFile(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want bool
	}{
		{"debug: false\n", true},
		{"proxy:\n  acme_email: ops@example.com\n", true},
		{"proxy:\n  enabled: false\n", false},
		{"proxy:\n  enabled: true\n", true},
	} {
		c, err := config.NewAtPath("/tmp/config.yml")
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal([]byte(tc.yaml), c); err != nil {
			t.Fatal(err)
		}
		if c.Proxy.Enabled != tc.want {
			t.Errorf("%q: enabled = %v, want %v", tc.yaml, c.Proxy.Enabled, tc.want)
		}
		if c.Proxy.Image != "traefik:v3.7.15" {
			t.Errorf("%q: the image default is lost: %q", tc.yaml, c.Proxy.Image)
		}
	}
}

func TestDefaults(t *testing.T) {
	cfg := defaultConfig(t)
	if !cfg.Enabled || cfg.Image != "traefik:v3.7.15" || cfg.ContainerName != "nexthost-proxy" ||
		cfg.HTTPPort != 80 || cfg.HTTPSPort != 443 || cfg.DataDir != "/var/lib/pelican/proxy" || cfg.AcmeEmail != "" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestBuildSpec(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.Enabled = true
	cfg.AcmeEmail = "contact@next-il.co.il"

	spec := BuildSpec(cfg, "pelican_nw")

	if spec.Name != "nexthost-proxy" || spec.Config.Image != "traefik:v3.7.15" {
		t.Fatalf("name/image: %s %s", spec.Name, spec.Config.Image)
	}
	for _, arg := range []string{
		"--providers.docker=true",
		"--providers.docker.exposedbydefault=false",
		"--providers.docker.network=pelican_nw",
		"--providers.file.directory=/etc/traefik/dynamic",
		"--providers.file.watch=true",
		"--entrypoints.web.address=:80",
		"--entrypoints.websecure.address=:443",
		"--certificatesresolvers.le.acme.httpchallenge=true",
		"--certificatesresolvers.le.acme.httpchallenge.entrypoint=web",
		"--certificatesresolvers.le.acme.email=contact@next-il.co.il",
	} {
		if !slices.Contains(spec.Config.Cmd, arg) {
			t.Errorf("missing arg %s", arg)
		}
	}
	for _, arg := range spec.Config.Cmd {
		if arg == "--api" || arg == "--api.insecure=true" || arg == "--api.dashboard=true" {
			t.Errorf("the dashboard/API must not be exposed: %s", arg)
		}
		// HTTP to HTTPS is a route now (BaseRoutes), so a domain can ask for plain HTTP
		if strings.HasPrefix(arg, "--entrypoints.web.http.redirections") {
			t.Errorf("no entrypoint redirect: %s", arg)
		}
	}

	if got := spec.Host.PortBindings["80/tcp"][0].HostPort; got != "80" {
		t.Errorf("http port %s", got)
	}
	if got := spec.Host.PortBindings["443/tcp"][0].HostPort; got != "443" {
		t.Errorf("https port %s", got)
	}
	if spec.Host.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Errorf("restart policy %s", spec.Host.RestartPolicy.Name)
	}
	if string(spec.Host.NetworkMode) != "pelican_nw" || spec.Network.EndpointsConfig["pelican_nw"] == nil {
		t.Errorf("network %s", spec.Host.NetworkMode)
	}

	socket, data := spec.Host.Mounts[0], spec.Host.Mounts[1]
	if socket.Type != mount.TypeBind || socket.Source != "/var/run/docker.sock" || !socket.ReadOnly {
		t.Errorf("docker socket mount %+v", socket)
	}
	if data.Source != "/var/lib/pelican/proxy" || data.Target != "/data" || data.ReadOnly {
		t.Errorf("data mount %+v", data)
	}
	if len(spec.Host.Mounts) != 3 {
		t.Fatalf("mounts %+v", spec.Host.Mounts)
	}
	routes := spec.Host.Mounts[2]
	if routes.Type != mount.TypeBind || routes.Source != "/var/lib/pelican/proxy/dynamic" || routes.Target != "/etc/traefik/dynamic" || !routes.ReadOnly {
		t.Errorf("routes mount %+v", routes)
	}
	if spec.Config.Labels[SpecLabel] != spec.Hash || spec.Hash == "" {
		t.Errorf("spec label %v", spec.Config.Labels)
	}
}

func TestSpecHashFollowsTheSettings(t *testing.T) {
	cfg := defaultConfig(t)
	base := BuildSpec(cfg, "pelican_nw").Hash

	if BuildSpec(cfg, "pelican_nw").Hash != base {
		t.Fatal("the same settings must give the same hash")
	}

	changed := cfg
	changed.AcmeEmail = "x@example.com"
	if BuildSpec(changed, "pelican_nw").Hash == base {
		t.Error("a new email must recreate the container")
	}

	changed = cfg
	changed.Image = "traefik:v3.7.16"
	if BuildSpec(changed, "pelican_nw").Hash == base {
		t.Error("a new image must recreate the container")
	}

	changed = cfg
	changed.DataDir = "/srv/proxy"
	if BuildSpec(changed, "pelican_nw").Hash == base {
		t.Error("a new data folder (and so routes folder) must recreate the container")
	}

	// Turning it on or off is not a change to the container itself
	changed = cfg
	changed.Enabled = !cfg.Enabled
	if BuildSpec(changed, "pelican_nw").Hash != base {
		t.Error("Enabled must not be part of the hash")
	}
}
