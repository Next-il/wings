package proxy

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"emperror.dev/errors"
	"gopkg.in/yaml.v3"

	"github.com/pelican/wings/config"
	"github.com/pelican/wings/environment"
)

// The routes come from Traefik's file provider: Wings writes one file per server into DynamicDir,
// which the proxy container watches, so a domain change applies within seconds, with no restart of
// the server or the proxy. A server has a file while it is starting or running and has domains;
// otherwise it has none.

// RouteFeatures is what GET /api/system reports in proxy.features when the proxy routes from files.
var RouteFeatures = []string{"paths", "http-only", "scheme", "live"}

// containerDynamicDir is where DynamicDir is mounted, read-only, inside the proxy container.
const containerDynamicDir = "/etc/traefik/dynamic"

// baseRoutesFile holds the routes that are not any one server's: plain HTTP redirected to HTTPS.
const baseRoutesFile = "_nexthost.yml"

// A file is a server's when its name is the server's uuid plus this.
const serverRoutesExt = ".yml"

// Only these go in a file name or a Traefik name. Server uuids always match.
var serverIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// DynamicDir is the folder of Traefik dynamic configuration files Wings writes.
func DynamicDir(cfg config.ProxyConfiguration) string {
	return filepath.Join(cfg.DataDir, "dynamic")
}

// Traefik's dynamic configuration, only as much of it as Wings writes. Keys are Traefik's own.
type dynamicConfig struct {
	HTTP httpConfig `yaml:"http"`
}

type httpConfig struct {
	Routers           map[string]*router           `yaml:"routers,omitempty"`
	Middlewares       map[string]*middleware       `yaml:"middlewares,omitempty"`
	Services          map[string]*service          `yaml:"services,omitempty"`
	ServersTransports map[string]*serversTransport `yaml:"serversTransports,omitempty"`
}

type router struct {
	EntryPoints []string   `yaml:"entryPoints"`
	Rule        string     `yaml:"rule"`
	Priority    int        `yaml:"priority,omitempty"`
	Middlewares []string   `yaml:"middlewares,omitempty"`
	Service     string     `yaml:"service"`
	TLS         *routerTLS `yaml:"tls,omitempty"`
}

type routerTLS struct {
	CertResolver string `yaml:"certResolver"`
}

type middleware struct {
	StripPrefix    *stripPrefix    `yaml:"stripPrefix,omitempty"`
	AddPrefix      *addPrefix      `yaml:"addPrefix,omitempty"`
	RedirectScheme *redirectScheme `yaml:"redirectScheme,omitempty"`
}

type stripPrefix struct {
	Prefixes []string `yaml:"prefixes"`
}

type addPrefix struct {
	Prefix string `yaml:"prefix"`
}

type redirectScheme struct {
	Scheme    string `yaml:"scheme"`
	Permanent bool   `yaml:"permanent"`
}

type service struct {
	LoadBalancer loadBalancer `yaml:"loadBalancer"`
}

type loadBalancer struct {
	Servers          []lbServer `yaml:"servers"`
	ServersTransport string     `yaml:"serversTransport,omitempty"`
}

type lbServer struct {
	URL string `yaml:"url"`
}

type serversTransport struct {
	InsecureSkipVerify bool `yaml:"insecureSkipVerify"`
}

// RouteName is the stable Traefik name of a server's i-th domain: its HTTPS router (or its only
// router when the entry is HTTP only), its service and its servers transport. The entry's other
// routers and middlewares are this plus a suffix. The whole uuid, so no two servers' names meet in
// the one configuration Traefik makes of all the files.
func RouteName(serverID string, i int) string {
	return fmt.Sprintf("nh-%s-%d", serverID, i)
}

// buildServerRoutes is the dynamic configuration that routes domains (normalized, see
// environment.ValidDomains) to the server's container, nil when there are none. The container is
// reached by its name, the server's uuid, over the Wings network the proxy is on.
func buildServerRoutes(serverID string, domains []environment.Domain) *dynamicConfig {
	if len(domains) == 0 {
		return nil
	}

	cfg := &dynamicConfig{HTTP: httpConfig{
		Routers:  map[string]*router{},
		Services: map[string]*service{},
	}}
	addMiddleware := func(name string, m *middleware) {
		if cfg.HTTP.Middlewares == nil {
			cfg.HTTP.Middlewares = map[string]*middleware{}
		}
		cfg.HTTP.Middlewares[name] = m
	}

	for i, d := range domains {
		name := RouteName(serverID, i)

		rule := fmt.Sprintf("Host(`%s`)", d.Domain)
		if d.Path != "/" {
			rule += fmt.Sprintf(" && PathPrefix(`%s`)", d.Path)
		}

		// What a request goes through on its way to the container: the path stripped, then prefixed
		var chain []string
		if d.StripPath {
			addMiddleware(name+"-strip", &middleware{StripPrefix: &stripPrefix{Prefixes: []string{d.Path}}})
			chain = append(chain, name+"-strip")
		}
		if d.DestPath != "" {
			addMiddleware(name+"-prefix", &middleware{AddPrefix: &addPrefix{Prefix: d.DestPath}})
			chain = append(chain, name+"-prefix")
		}

		scheme := d.Scheme
		if scheme == "" {
			scheme = environment.DomainSchemeHTTP
		}
		svc := &service{LoadBalancer: loadBalancer{
			Servers: []lbServer{{URL: fmt.Sprintf("%s://%s:%d", scheme, serverID, d.Port)}},
		}}
		if scheme == environment.DomainSchemeHTTPS {
			// The app serves TLS itself, most likely with a certificate made for no name at all
			if cfg.HTTP.ServersTransports == nil {
				cfg.HTTP.ServersTransports = map[string]*serversTransport{}
			}
			cfg.HTTP.ServersTransports[name] = &serversTransport{InsecureSkipVerify: true}
			svc.LoadBalancer.ServersTransport = name
		}
		cfg.HTTP.Services[name] = svc

		if d.HTTPOnly {
			cfg.HTTP.Routers[name] = &router{
				EntryPoints: []string{"web"},
				Rule:        rule,
				Middlewares: chain,
				Service:     name,
			}
			continue
		}

		cfg.HTTP.Routers[name] = &router{
			EntryPoints: []string{"websecure"},
			Rule:        rule,
			Middlewares: chain,
			Service:     name,
			TLS:         &routerTLS{CertResolver: "le"},
		}
		// Plain HTTP for the same host and path only redirects. The service is never reached
		addMiddleware(name+"-https", &middleware{RedirectScheme: &redirectScheme{Scheme: "https", Permanent: true}})
		cfg.HTTP.Routers[name+"-http"] = &router{
			EntryPoints: []string{"web"},
			Rule:        rule,
			Middlewares: []string{name + "-https"},
			Service:     name,
		}
	}
	return cfg
}

// buildBaseRoutes redirects plain HTTP to HTTPS for every host no other router on "web" takes: the
// domains of containers still routed by labels (made before routes came from files), and any host
// at all, as the proxy did before. The lowest priority there is, so a server's own routers on "web"
// win. Let's Encrypt's HTTP challenge is Traefik's own router, above everything.
func buildBaseRoutes() *dynamicConfig {
	return &dynamicConfig{HTTP: httpConfig{
		Routers: map[string]*router{
			"nh-http-to-https": {
				EntryPoints: []string{"web"},
				Rule:        "HostRegexp(`^.+$`)",
				Priority:    1,
				Middlewares: []string{"nh-http-to-https"},
				Service:     "noop@internal",
			},
		},
		Middlewares: map[string]*middleware{
			"nh-http-to-https": {RedirectScheme: &redirectScheme{Scheme: "https", Permanent: true}},
		},
	}}
}

func render(cfg *dynamicConfig, header string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(header)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return nil, errors.WithStack(err)
	}
	if err := enc.Close(); err != nil {
		return nil, errors.WithStack(err)
	}
	return buf.Bytes(), nil
}

// ServerRoutes is the file that routes domains (normalized, see environment.ValidDomains) to the
// server's container, nil when there are none.
func ServerRoutes(serverID string, domains []environment.Domain) ([]byte, error) {
	if !serverIDPattern.MatchString(serverID) {
		return nil, errors.Errorf("proxy: %q is not a server uuid", serverID)
	}
	cfg := buildServerRoutes(serverID, domains)
	if cfg == nil {
		return nil, nil
	}
	return render(cfg, "# Written by Wings for server "+serverID+", rewritten whenever its domains or state change.\n")
}

// BaseRoutes is the file with the routes that are not any one server's.
func BaseRoutes() ([]byte, error) {
	return render(buildBaseRoutes(), "# Written by Wings: plain HTTP to HTTPS for every host no server routes on HTTP.\n")
}

// writeAtomic puts data at dir/name through a temporary file and a rename, so the watching proxy
// never reads half a file. A file that already has data is left alone. The temporary file's name
// does not end in .yml, so Traefik skips it.
func writeAtomic(dir, name string, data []byte) error {
	target := filepath.Join(dir, name)
	if current, err := os.ReadFile(target); err == nil && bytes.Equal(current, data) {
		return nil
	}

	tmp, err := os.CreateTemp(dir, "."+name+".*.tmp")
	if err != nil {
		return errors.WithStack(err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errors.WithStack(err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return errors.WithStack(err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errors.WithStack(err)
	}
	if err := tmp.Close(); err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(os.Rename(tmp.Name(), target))
}

func removeFile(dir, name string) error {
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
		return errors.WithStack(err)
	}
	return nil
}

// writeServerRoutes writes the server's file in dir, or removes it when there are no domains.
func writeServerRoutes(dir, serverID string, domains []environment.Domain) error {
	data, err := ServerRoutes(serverID, domains)
	if err != nil {
		return err
	}
	if data == nil {
		return removeFile(dir, serverID+serverRoutesExt)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.WithStack(err)
	}
	return writeAtomic(dir, serverID+serverRoutesExt, data)
}

// WriteServerRoutes makes the proxy route domains (normalized, see environment.ValidDomains) to the
// server's container, or stop routing it when there are none or the proxy is off.
func WriteServerRoutes(serverID string, domains []environment.Domain) error {
	cfg := config.Get().Proxy
	if !cfg.Enabled {
		domains = nil
	}
	return writeServerRoutes(DynamicDir(cfg), serverID, domains)
}

// RemoveServerRoutes stops the proxy routing anything to the server.
func RemoveServerRoutes(serverID string) error {
	if !serverIDPattern.MatchString(serverID) {
		return errors.Errorf("proxy: %q is not a server uuid", serverID)
	}
	return removeFile(DynamicDir(config.Get().Proxy), serverID+serverRoutesExt)
}

// pruneServerRoutes removes the files in dir of servers not in keep, and temporary files left by a
// write that never finished. The base file stays.
func pruneServerRoutes(dir string, keep []string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.WithStack(err)
	}

	wanted := make(map[string]bool, len(keep))
	for _, id := range keep {
		wanted[id+serverRoutesExt] = true
	}

	var removed []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == baseRoutesFile || wanted[name] {
			continue
		}
		if !strings.HasSuffix(name, serverRoutesExt) && !strings.HasSuffix(name, ".tmp") {
			continue
		}
		if err := removeFile(dir, name); err != nil {
			return removed, err
		}
		removed = append(removed, name)
	}
	return removed, nil
}

// PruneServerRoutes removes the files of servers that are not on this node (keep is every server
// that is), so the proxy stops routing what no longer exists here.
func PruneServerRoutes(keep []string) ([]string, error) {
	return pruneServerRoutes(DynamicDir(config.Get().Proxy), keep)
}

// writeBaseRoutes writes the base file into dir.
func writeBaseRoutes(dir string) error {
	data, err := BaseRoutes()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.WithStack(err)
	}
	return writeAtomic(dir, baseRoutesFile, data)
}
