package environment

import (
	"encoding/json"
	"reflect"
	"strings"
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

	wantValid := []Domain{
		{Domain: "app.example.com", Port: 3000, Path: "/", Scheme: "http"},
		{Domain: "api.example.com", Port: 8080, Path: "/", Scheme: "http"},
	}
	if !reflect.DeepEqual(valid, wantValid) {
		t.Fatalf("valid = %v, want %v", valid, wantValid)
	}
	if len(dropped) != 3 {
		t.Fatalf("dropped %d, want 3: %v", len(dropped), dropped)
	}
	for _, d := range dropped {
		if d.Reason == "" {
			t.Errorf("no reason for dropping %v", d.Domain)
		}
	}
}

// An older remote sends only domain and port: HTTPS on the whole host, as before
func TestDomainDefaultsKeepTheOldBehaviour(t *testing.T) {
	var d Domain
	if err := json.Unmarshal([]byte(`{"domain":"app.example.com","port":3000}`), &d); err != nil {
		t.Fatal(err)
	}
	n, err := d.Normalized()
	if err != nil {
		t.Fatal(err)
	}
	want := Domain{Domain: "app.example.com", Port: 3000, Path: "/", Scheme: "http"}
	if n != want {
		t.Fatalf("got %+v, want %+v", n, want)
	}
	if n.HTTPOnly || n.StripPath || n.DestPath != "" {
		t.Fatalf("defaults must be HTTPS, no strip, no prefix: %+v", n)
	}
}

func TestDomainNewFieldsFromJSON(t *testing.T) {
	var d Domain
	raw := `{"domain":"example.com","port":8443,"path":"/api","strip_path":true,"http_only":true,"scheme":"HTTPS","dest_path":"/v1/"}`
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	n, err := d.Normalized()
	if err != nil {
		t.Fatal(err)
	}
	want := Domain{Domain: "example.com", Port: 8443, Path: "/api", StripPath: true, HTTPOnly: true, Scheme: "https", DestPath: "/v1"}
	if n != want {
		t.Fatalf("got %+v, want %+v", n, want)
	}
}

func TestDomainPathValidation(t *testing.T) {
	for _, tc := range []struct {
		path string
		ok   bool
	}{
		{"", true},
		{"/", true},
		{"/api", true},
		{"/api/", true},
		{"/api/v1", true},
		{"/a-b_c.d~e", true},
		{"/%7Euser", true},
		{"/@scope:x", true},
		{"api", false},
		{"/api v1", false},
		{"/api`)", false},
		{"/api') || Host(`x.com", false},
		{`/a"b`, false},
		{"/a'b", false},
		{`/a\b`, false},
		{"/a?b=1", false},
		{"/a#b", false},
		{"/{id}", false},
		{"//api", false},
		{"/a//b", false},
		{"/a/../b", false},
		{"/./a", false},
		{"/..", false},
		{"/" + strings.Repeat("a", MaxDomainPathLength), false},
		{"/" + strings.Repeat("a", MaxDomainPathLength-1), true},
	} {
		_, err := Domain{Domain: "app.example.com", Port: 80, Path: tc.path}.Normalized()
		if (err == nil) != tc.ok {
			t.Errorf("path %q: err = %v, want ok %v", tc.path, err, tc.ok)
		}
		_, err = Domain{Domain: "app.example.com", Port: 80, DestPath: tc.path}.Normalized()
		if (err == nil) != tc.ok {
			t.Errorf("dest_path %q: err = %v, want ok %v", tc.path, err, tc.ok)
		}
	}
}

func TestDomainNormalization(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Domain
		want Domain
	}{
		{
			"empty path is the whole host",
			Domain{Domain: "a.example.com", Port: 80},
			Domain{Domain: "a.example.com", Port: 80, Path: "/", Scheme: "http"},
		},
		{
			"nothing to strip from /",
			Domain{Domain: "a.example.com", Port: 80, Path: "/", StripPath: true},
			Domain{Domain: "a.example.com", Port: 80, Path: "/", Scheme: "http"},
		},
		{
			"dest_path / is nothing",
			Domain{Domain: "a.example.com", Port: 80, DestPath: "/"},
			Domain{Domain: "a.example.com", Port: 80, Path: "/", Scheme: "http"},
		},
		{
			"scheme is lowercased",
			Domain{Domain: "a.example.com", Port: 80, Scheme: "Http"},
			Domain{Domain: "a.example.com", Port: 80, Path: "/", Scheme: "http"},
		},
		{
			"path keeps its trailing slash",
			Domain{Domain: "a.example.com", Port: 80, Path: "/docs/", StripPath: true},
			Domain{Domain: "a.example.com", Port: 80, Path: "/docs/", StripPath: true, Scheme: "http"},
		},
	} {
		got, err := tc.in.Normalized()
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestDomainSchemeValidation(t *testing.T) {
	for scheme, ok := range map[string]bool{"": true, "http": true, "https": true, "HTTPS": true, "h2c": false, "tcp": false, "ftp": false} {
		_, err := Domain{Domain: "app.example.com", Port: 80, Scheme: scheme}.Normalized()
		if (err == nil) != ok {
			t.Errorf("scheme %q: err = %v, want ok %v", scheme, err, ok)
		}
	}
}

func TestValidDomainsSameHostDifferentPaths(t *testing.T) {
	valid, dropped := ValidDomains([]Domain{
		{Domain: "example.com", Port: 3000},
		{Domain: "example.com", Port: 4000, Path: "/api"},
		{Domain: "example.com", Port: 5000, Path: "/api", HTTPOnly: true},
		{Domain: "example.com", Port: 6000, Path: "/"},
		{Domain: "example.com", Port: 7000, Path: "/bad path"},
		{Domain: "example.com", Port: 7000, Scheme: "ws"},
	})
	if len(valid) != 2 || valid[0].Port != 3000 || valid[1].Port != 4000 {
		t.Fatalf("valid = %+v", valid)
	}
	if len(dropped) != 4 {
		t.Fatalf("dropped = %+v", dropped)
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
