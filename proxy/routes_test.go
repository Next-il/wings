package proxy

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/pelican/wings/environment"
)

const testServerID = "8f2c1a9b-0000-4000-8000-000000000000"

func normalized(t *testing.T, domains ...environment.Domain) []environment.Domain {
	t.Helper()
	valid, dropped := environment.ValidDomains(domains)
	if len(dropped) > 0 {
		t.Fatalf("test domains dropped: %+v", dropped)
	}
	return valid
}

func names[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The defaults, as an older remote sends them: what the labels did, plus HTTP redirected on the host
func TestServerRoutesDefaults(t *testing.T) {
	cfg := buildServerRoutes(testServerID, normalized(t, environment.Domain{Domain: "app.example.com", Port: 3000}))
	name := "nh-" + testServerID + "-0"

	want := &dynamicConfig{HTTP: httpConfig{
		Routers: map[string]*router{
			name: {
				EntryPoints: []string{"websecure"},
				Rule:        "Host(`app.example.com`)",
				Service:     name,
				TLS:         &routerTLS{CertResolver: "le"},
			},
			name + "-http": {
				EntryPoints: []string{"web"},
				Rule:        "Host(`app.example.com`)",
				Middlewares: []string{name + "-https"},
				Service:     name,
			},
		},
		Middlewares: map[string]*middleware{
			name + "-https": {RedirectScheme: &redirectScheme{Scheme: "https", Permanent: true}},
		},
		Services: map[string]*service{
			name: {LoadBalancer: loadBalancer{Servers: []lbServer{{URL: "http://" + testServerID + ":3000"}}}},
		},
	}}
	if !reflect.DeepEqual(cfg, want) {
		got, _ := yaml.Marshal(cfg)
		exp, _ := yaml.Marshal(want)
		t.Fatalf("got\n%s\nwant\n%s", got, exp)
	}
}

func TestServerRoutesNone(t *testing.T) {
	if buildServerRoutes(testServerID, nil) != nil {
		t.Fatal("no domains must give no routes")
	}
	data, err := ServerRoutes(testServerID, nil)
	if err != nil || data != nil {
		t.Fatalf("no domains must give no file: %q %v", data, err)
	}
}

func TestServerRoutesOptions(t *testing.T) {
	name := "nh-" + testServerID + "-0"

	for _, tc := range []struct {
		desc            string
		domain          environment.Domain
		rule            string
		secureRouter    bool
		chain           []string
		wantMiddlewares []string
		url             string
		transport       bool
	}{
		{
			desc:   "path only",
			domain: environment.Domain{Domain: "example.com", Port: 3000, Path: "/api"},
			rule:   "Host(`example.com`) && PathPrefix(`/api`)", secureRouter: true,
			wantMiddlewares: []string{name + "-https"},
			url:             "http://" + testServerID + ":3000",
		},
		{
			desc:   "path stripped",
			domain: environment.Domain{Domain: "example.com", Port: 3000, Path: "/api", StripPath: true},
			rule:   "Host(`example.com`) && PathPrefix(`/api`)", secureRouter: true,
			chain:           []string{name + "-strip"},
			wantMiddlewares: []string{name + "-https", name + "-strip"},
			url:             "http://" + testServerID + ":3000",
		},
		{
			desc:   "dest path",
			domain: environment.Domain{Domain: "example.com", Port: 3000, DestPath: "/v1"},
			rule:   "Host(`example.com`)", secureRouter: true,
			chain:           []string{name + "-prefix"},
			wantMiddlewares: []string{name + "-https", name + "-prefix"},
			url:             "http://" + testServerID + ":3000",
		},
		{
			desc:   "strip then prefix",
			domain: environment.Domain{Domain: "example.com", Port: 3000, Path: "/old", StripPath: true, DestPath: "/new/"},
			rule:   "Host(`example.com`) && PathPrefix(`/old`)", secureRouter: true,
			chain:           []string{name + "-strip", name + "-prefix"},
			wantMiddlewares: []string{name + "-https", name + "-prefix", name + "-strip"},
			url:             "http://" + testServerID + ":3000",
		},
		{
			desc:   "https to the container",
			domain: environment.Domain{Domain: "example.com", Port: 8443, Scheme: "https"},
			rule:   "Host(`example.com`)", secureRouter: true,
			wantMiddlewares: []string{name + "-https"},
			url:             "https://" + testServerID + ":8443",
			transport:       true,
		},
		{
			desc:   "http only",
			domain: environment.Domain{Domain: "example.com", Port: 80, HTTPOnly: true},
			rule:   "Host(`example.com`)",
			url:    "http://" + testServerID + ":80",
		},
		{
			desc:   "http only with every option",
			domain: environment.Domain{Domain: "example.com", Port: 443, HTTPOnly: true, Path: "/x", StripPath: true, DestPath: "/y", Scheme: "https"},
			rule:   "Host(`example.com`) && PathPrefix(`/x`)",
			chain:  []string{name + "-strip", name + "-prefix"}, wantMiddlewares: []string{name + "-prefix", name + "-strip"},
			url:       "https://" + testServerID + ":443",
			transport: true,
		},
		{
			desc:   "https with every option",
			domain: environment.Domain{Domain: "example.com", Port: 443, Path: "/x/", StripPath: true, DestPath: "/y", Scheme: "https"},
			rule:   "Host(`example.com`) && PathPrefix(`/x/`)", secureRouter: true,
			chain:           []string{name + "-strip", name + "-prefix"},
			wantMiddlewares: []string{name + "-https", name + "-prefix", name + "-strip"},
			url:             "https://" + testServerID + ":443",
			transport:       true,
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			cfg := buildServerRoutes(testServerID, normalized(t, tc.domain))

			main := cfg.HTTP.Routers[name]
			if main == nil {
				t.Fatalf("no router %s: %v", name, names(cfg.HTTP.Routers))
			}
			if main.Rule != tc.rule {
				t.Errorf("rule %q, want %q", main.Rule, tc.rule)
			}
			if main.Service != name || !slices.Equal(main.Middlewares, tc.chain) {
				t.Errorf("router %+v, want service %s and chain %v", main, name, tc.chain)
			}

			if tc.secureRouter {
				if !slices.Equal(main.EntryPoints, []string{"websecure"}) || main.TLS == nil || main.TLS.CertResolver != "le" {
					t.Errorf("an HTTPS router on websecure with le: %+v", main)
				}
				redirect := cfg.HTTP.Routers[name+"-http"]
				if redirect == nil || !slices.Equal(redirect.EntryPoints, []string{"web"}) || redirect.Rule != tc.rule ||
					!slices.Equal(redirect.Middlewares, []string{name + "-https"}) {
					t.Errorf("plain HTTP must redirect on the same rule: %+v", redirect)
				}
				if m := cfg.HTTP.Middlewares[name+"-https"]; m == nil || m.RedirectScheme == nil || m.RedirectScheme.Scheme != "https" || !m.RedirectScheme.Permanent {
					t.Errorf("redirect middleware %+v", m)
				}
				if len(cfg.HTTP.Routers) != 2 {
					t.Errorf("routers %v", names(cfg.HTTP.Routers))
				}
			} else {
				if !slices.Equal(main.EntryPoints, []string{"web"}) || main.TLS != nil {
					t.Errorf("an HTTP only router is on web without TLS: %+v", main)
				}
				if len(cfg.HTTP.Routers) != 1 {
					t.Errorf("an HTTP only entry has one router: %v", names(cfg.HTTP.Routers))
				}
			}

			if got := names(cfg.HTTP.Middlewares); !slices.Equal(got, tc.wantMiddlewares) && !(len(got) == 0 && len(tc.wantMiddlewares) == 0) {
				t.Errorf("middlewares %v, want %v", got, tc.wantMiddlewares)
			}
			if m := cfg.HTTP.Middlewares[name+"-strip"]; m != nil && !slices.Equal(m.StripPrefix.Prefixes, []string{tc.domain.Path}) {
				t.Errorf("strip %v", m.StripPrefix.Prefixes)
			}
			if m := cfg.HTTP.Middlewares[name+"-prefix"]; m != nil && m.AddPrefix.Prefix != strings.TrimRight(tc.domain.DestPath, "/") {
				t.Errorf("prefix %q", m.AddPrefix.Prefix)
			}

			svc := cfg.HTTP.Services[name]
			if svc == nil || len(svc.LoadBalancer.Servers) != 1 || svc.LoadBalancer.Servers[0].URL != tc.url {
				t.Fatalf("service %+v, want %s", svc, tc.url)
			}
			if tc.transport {
				st := cfg.HTTP.ServersTransports[name]
				if st == nil || !st.InsecureSkipVerify || svc.LoadBalancer.ServersTransport != name {
					t.Errorf("https needs a transport that skips verification: %+v %+v", st, svc)
				}
			} else if cfg.HTTP.ServersTransports != nil || svc.LoadBalancer.ServersTransport != "" {
				t.Errorf("no transport for http: %+v", cfg.HTTP.ServersTransports)
			}
		})
	}
}

func TestServerRoutesManyDomains(t *testing.T) {
	cfg := buildServerRoutes(testServerID, normalized(t,
		environment.Domain{Domain: "app.example.com", Port: 3000},
		environment.Domain{Domain: "app.example.com", Port: 4000, Path: "/api", StripPath: true},
		environment.Domain{Domain: "plain.example.com", Port: 80, HTTPOnly: true},
	))
	n := func(i int) string { return RouteName(testServerID, i) }

	wantRouters := []string{n(0), n(0) + "-http", n(1), n(1) + "-http", n(2)}
	sort.Strings(wantRouters)
	if got := names(cfg.HTTP.Routers); !slices.Equal(got, wantRouters) {
		t.Fatalf("routers %v, want %v", got, wantRouters)
	}
	if got := names(cfg.HTTP.Services); !slices.Equal(got, []string{n(0), n(1), n(2)}) {
		t.Fatalf("services %v", got)
	}
	if cfg.HTTP.Services[n(1)].LoadBalancer.Servers[0].URL != "http://"+testServerID+":4000" {
		t.Errorf("each entry goes to its own port")
	}
	if cfg.HTTP.Routers[n(1)].Rule != "Host(`app.example.com`) && PathPrefix(`/api`)" {
		t.Errorf("rule %s", cfg.HTTP.Routers[n(1)].Rule)
	}
	if !slices.Equal(cfg.HTTP.Routers[n(2)].EntryPoints, []string{"web"}) {
		t.Errorf("the http only entry is on web")
	}
}

// What Traefik reads: YAML with its own keys
func TestServerRoutesFile(t *testing.T) {
	data, err := ServerRoutes(testServerID, normalized(t,
		environment.Domain{Domain: "app.example.com", Port: 8443, Path: "/api", StripPath: true, DestPath: "/v1", Scheme: "https"},
	))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"http:", "routers:", "entryPoints:", "certResolver: le", "middlewares:", "stripPrefix:", "addPrefix:",
		"redirectScheme:", "loadBalancer:", "serversTransport: nh-" + testServerID + "-0", "serversTransports:",
		"insecureSkipVerify: true", "url: https://" + testServerID + ":8443", "PathPrefix(`/api`)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}

	var back map[string]any
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("the file must be valid YAML: %v", err)
	}
	routers := back["http"].(map[string]any)["routers"].(map[string]any)
	main := routers["nh-"+testServerID+"-0"].(map[string]any)
	if main["rule"] != "Host(`app.example.com`) && PathPrefix(`/api`)" {
		t.Errorf("rule after a round trip: %v", main["rule"])
	}

	again, _ := ServerRoutes(testServerID, normalized(t,
		environment.Domain{Domain: "app.example.com", Port: 8443, Path: "/api", StripPath: true, DestPath: "/v1", Scheme: "https"},
	))
	if string(again) != text {
		t.Error("the same domains must give the same file")
	}
}

func TestServerRoutesRefusesABadServerID(t *testing.T) {
	for _, id := range []string{"", "../etc", "a/b", "x y", "a.yml"} {
		if _, err := ServerRoutes(id, normalized(t, environment.Domain{Domain: "app.example.com", Port: 3000})); err == nil {
			t.Errorf("%q must be refused", id)
		}
	}
}

func TestBaseRoutes(t *testing.T) {
	cfg := buildBaseRoutes()
	r := cfg.HTTP.Routers["nh-http-to-https"]
	if r == nil || !slices.Equal(r.EntryPoints, []string{"web"}) || r.Priority != 1 || r.Rule != "HostRegexp(`^.+$`)" ||
		r.Service != "noop@internal" || !slices.Equal(r.Middlewares, []string{"nh-http-to-https"}) || r.TLS != nil {
		t.Fatalf("catch-all router %+v", r)
	}
	m := cfg.HTTP.Middlewares["nh-http-to-https"]
	if m == nil || m.RedirectScheme == nil || m.RedirectScheme.Scheme != "https" || !m.RedirectScheme.Permanent {
		t.Fatalf("redirect %+v", m)
	}

	data, err := BaseRoutes()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"priority: 1", "noop@internal", "HostRegexp(`^.+$`)", "permanent: true"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in\n%s", want, data)
		}
	}
}

// A server's routers on web always beat the catch-all: Traefik ranks by priority, and a router
// without one has its rule's length
func TestServerRoutersOutrankTheCatchAll(t *testing.T) {
	cfg := buildServerRoutes(testServerID, normalized(t,
		environment.Domain{Domain: "a.io", Port: 80, HTTPOnly: true},
		environment.Domain{Domain: "b.io", Port: 80},
	))
	for name, r := range cfg.HTTP.Routers {
		if r.Priority != 0 || len(r.Rule) <= 1 {
			t.Errorf("%s: priority %d, rule %q", name, r.Priority, r.Rule)
		}
	}
}

func TestWriteServerRoutes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dynamic")
	file := filepath.Join(dir, testServerID+".yml")

	domains := normalized(t, environment.Domain{Domain: "app.example.com", Port: 3000})
	if err := writeServerRoutes(dir, testServerID, domains); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ServerRoutes(testServerID, domains)
	if string(first) != string(want) {
		t.Fatalf("file\n%s\nwant\n%s", first, want)
	}

	// The same content is not rewritten: Traefik is not reloaded for nothing
	before, _ := os.Stat(file)
	if err := writeServerRoutes(dir, testServerID, domains); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(file)
	if !os.SameFile(before, after) {
		t.Error("an unchanged file must not be replaced")
	}

	// A change replaces it, and leaves no temporary file behind
	changed := normalized(t, environment.Domain{Domain: "app.example.com", Port: 4000})
	if err := writeServerRoutes(dir, testServerID, changed); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), ":4000") {
		t.Errorf("the change is not written:\n%s", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("only the server's file is left: %v", entries)
	}

	// No domains: no file
	if err := writeServerRoutes(dir, testServerID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the file must be gone: %v", err)
	}
	// And removing what is not there is fine
	if err := writeServerRoutes(dir, testServerID, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPruneServerRoutes(t *testing.T) {
	dir := t.TempDir()
	keep := "11111111-0000-4000-8000-000000000000"
	gone := "22222222-0000-4000-8000-000000000000"
	domains := normalized(t, environment.Domain{Domain: "app.example.com", Port: 3000})
	for _, id := range []string{keep, gone} {
		if err := writeServerRoutes(dir, id, domains); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeBaseRoutes(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".x.yml.123.tmp", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := pruneServerRoutes(dir, []string{keep})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(removed)
	if !slices.Equal(removed, []string{".x.yml.123.tmp", gone + ".yml"}) {
		t.Errorf("removed %v", removed)
	}
	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	sort.Strings(left)
	wantLeft := []string{baseRoutesFile, keep + ".yml", "notes.txt"}
	sort.Strings(wantLeft)
	if !slices.Equal(left, wantLeft) {
		t.Errorf("left %v", left)
	}

	if removed, err := pruneServerRoutes(filepath.Join(dir, "missing"), nil); err != nil || removed != nil {
		t.Errorf("a missing folder has nothing to prune: %v %v", removed, err)
	}
}
