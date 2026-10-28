package environment

import (
	"reflect"
	"testing"
)

func TestValidDomain(t *testing.T) {
	cases := map[string]bool{
		"app.example.com":         true,
		"a.b.example.co.il":       true,
		"xn--6dbbec0c.co.il":      true,
		"example.com":             true,
		"":                        false,
		"localhost":               false,
		"1.2.3.4":                 false,
		"*.example.com":           false,
		"App.Example.com":         false,
		"-bad.com":                false,
		"bad-.com":                false,
		"a`) || Host(`x.com":      false,
		"exa mple.com":            false,
		"example.com:443":         false,
		"example.123":             false,
		string(make([]byte, 254)): false,
	}
	for domain, want := range cases {
		if got := ValidDomain(domain); got != want {
			t.Errorf("ValidDomain(%q) = %v, want %v", domain, got, want)
		}
	}
}

func TestValidDomainsDropsWhatCannotBeRouted(t *testing.T) {
	valid, dropped := ValidDomains([]Domain{
		{Domain: "app.example.com", Port: 3000},
		{Domain: "app.example.com", Port: 4000},
		{Domain: "bad`.com", Port: 80},
		{Domain: "api.example.com", Port: 0},
		{Domain: "api.example.com", Port: 8080},
	})

	wantValid := []Domain{{Domain: "app.example.com", Port: 3000}, {Domain: "api.example.com", Port: 8080}}
	if !reflect.DeepEqual(valid, wantValid) {
		t.Fatalf("valid = %v, want %v", valid, wantValid)
	}
	if len(dropped) != 3 {
		t.Fatalf("dropped %d, want 3: %v", len(dropped), dropped)
	}
}

func TestStripProxyLabels(t *testing.T) {
	got := StripProxyLabels(map[string]string{
		"traefik.enable":              "true",
		"Traefik.http.routers.x.rule": "Host(`victim.com`)",
		"com.example.keep":            "yes",
	})
	if !reflect.DeepEqual(got, map[string]string{"com.example.keep": "yes"}) {
		t.Fatalf("got %v", got)
	}
}

func TestProxyLabels(t *testing.T) {
	if ProxyLabels("8f2c1a9b-0000", "pelican_nw", nil) != nil {
		t.Fatal("no domains must give no labels")
	}

	got := ProxyLabels("8f2c1a9b-0000-4000-8000-000000000000", "pelican_nw", []Domain{
		{Domain: "app.example.com", Port: 3000},
		{Domain: "api.example.com", Port: 8080},
	})
	want := map[string]string{
		"traefik.enable":         "true",
		"traefik.docker.network": "pelican_nw",

		"traefik.http.routers.nh-8f2c1a9b-0.rule":                      "Host(`app.example.com`)",
		"traefik.http.routers.nh-8f2c1a9b-0.entrypoints":               "websecure",
		"traefik.http.routers.nh-8f2c1a9b-0.tls.certresolver":          "le",
		"traefik.http.routers.nh-8f2c1a9b-0.service":                   "nh-8f2c1a9b-0",
		"traefik.http.services.nh-8f2c1a9b-0.loadbalancer.server.port": "3000",

		"traefik.http.routers.nh-8f2c1a9b-1.rule":                      "Host(`api.example.com`)",
		"traefik.http.routers.nh-8f2c1a9b-1.entrypoints":               "websecure",
		"traefik.http.routers.nh-8f2c1a9b-1.tls.certresolver":          "le",
		"traefik.http.routers.nh-8f2c1a9b-1.service":                   "nh-8f2c1a9b-1",
		"traefik.http.services.nh-8f2c1a9b-1.loadbalancer.server.port": "8080",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}
