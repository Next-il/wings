package environment

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Domain is a NextHost addition to a server's configuration: one of the customer's domains, which
// the node's proxy (Traefik, config.ProxyConfiguration) routes to Port inside the server's container.
// NextHost only sends a domain once its DNS points at the node.
//
//	"domains": [{"domain": "app.example.com", "port": 3000}]
//
// Every other field is optional, and its zero value is what a remote that sends only domain and port
// gets: HTTPS with a Let's Encrypt certificate on the whole host, plain HTTP redirected to it, and
// plain HTTP to the container with the path as it came.
//
//	{"domain": "example.com", "port": 8443, "path": "/api", "strip_path": true,
//	 "http_only": false, "scheme": "https", "dest_path": "/v1"}
type Domain struct {
	Domain string `json:"domain"`
	Port   int    `json:"port"`

	// Path is the URL path prefix this entry matches, "/" (the whole host) when empty.
	Path string `json:"path,omitempty"`
	// StripPath removes Path from the request before it goes to the container.
	StripPath bool `json:"strip_path,omitempty"`
	// HTTPOnly serves the entry on plain HTTP only: no certificate, no redirect to HTTPS.
	HTTPOnly bool `json:"http_only,omitempty"`
	// Scheme is how the proxy talks to the container: "http" (the default) or "https" for an app
	// that serves TLS itself. Its certificate is not checked, so a self-signed one works.
	Scheme string `json:"scheme,omitempty"`
	// DestPath is put in front of the path before the request goes to the container ("" or "/" for
	// nothing). It comes after StripPath.
	DestPath string `json:"dest_path,omitempty"`
}

// The schemes the proxy can talk to a container in.
const (
	DomainSchemeHTTP  = "http"
	DomainSchemeHTTPS = "https"
)

// MaxDomainPathLength is the longest path or dest_path a domain entry may have.
const MaxDomainPathLength = 200

// ProxyLabelPrefix is the label namespace Traefik reads. Only Wings writes it - a server's own
// labels cannot set or override anything under it.
const ProxyLabelPrefix = "traefik."

var domainLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// The characters a URL path may have here. No space, backtick, quote, backslash, brace or ?/#: a
// path goes inside a Traefik rule.
var domainPath = regexp.MustCompile(`^/[A-Za-z0-9._~!$&()*+,;=:@%/-]*$`)

// ValidDomain is a plain lowercase hostname: dot-separated labels of letters, digits and hyphens, at
// least two of them, 253 characters at most, the last label not all digits. It goes inside a Traefik
// rule, so this is also what keeps a backtick or a space out of one.
func ValidDomain(domain string) bool {
	if len(domain) == 0 || len(domain) > 253 {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if !domainLabel.MatchString(label) {
			return false
		}
	}
	_, err := strconv.Atoi(labels[len(labels)-1])
	return err != nil
}

// validDomainPath is a URL path: it starts with "/", has none of the characters domainPath keeps
// out, no empty, "." or ".." segment (a trailing "/" is fine) and is at most MaxDomainPathLength long.
func validDomainPath(p string) bool {
	if len(p) > MaxDomainPathLength || !domainPath.MatchString(p) {
		return false
	}
	segments := strings.Split(p[1:], "/")
	for i, segment := range segments {
		if segment == "." || segment == ".." {
			return false
		}
		if segment == "" && i != len(segments)-1 {
			return false
		}
	}
	return true
}

// Normalized is the entry with its defaults filled in - Path "/", Scheme "http", DestPath without a
// trailing "/" ("" for nothing) - or the reason it cannot be routed.
func (d Domain) Normalized() (Domain, error) {
	if !ValidDomain(d.Domain) {
		return d, fmt.Errorf("not a plain lowercase hostname")
	}
	if d.Port < 1 || d.Port > 65535 {
		return d, fmt.Errorf("port %d is outside 1-65535", d.Port)
	}

	if d.Path == "" {
		d.Path = "/"
	}
	if !validDomainPath(d.Path) {
		return d, fmt.Errorf("path %q is not a URL path", d.Path)
	}
	if d.Path == "/" {
		// Nothing to strip from the whole host
		d.StripPath = false
	}

	switch d.Scheme = strings.ToLower(d.Scheme); d.Scheme {
	case "":
		d.Scheme = DomainSchemeHTTP
	case DomainSchemeHTTP, DomainSchemeHTTPS:
	default:
		return d, fmt.Errorf("scheme %q is neither http nor https", d.Scheme)
	}

	if d.DestPath != "" && !validDomainPath(d.DestPath) {
		return d, fmt.Errorf("dest_path %q is not a URL path", d.DestPath)
	}
	d.DestPath = strings.TrimRight(d.DestPath, "/")

	return d, nil
}

// DroppedDomain is a domain entry that cannot be routed, and why.
type DroppedDomain struct {
	Domain Domain
	Reason string
}

// ValidDomains splits domains into the ones that can be routed, normalized (Domain.Normalized), and
// the ones that cannot: an invalid entry, or a domain and path already listed.
func ValidDomains(in []Domain) (valid []Domain, dropped []DroppedDomain) {
	seen := make(map[string]bool, len(in))
	for _, d := range in {
		n, err := d.Normalized()
		if err != nil {
			dropped = append(dropped, DroppedDomain{Domain: d, Reason: err.Error()})
			continue
		}
		key := n.Domain + n.Path
		if seen[key] {
			dropped = append(dropped, DroppedDomain{Domain: d, Reason: "the domain and path are already listed"})
			continue
		}
		seen[key] = true
		valid = append(valid, n)
	}
	return valid, dropped
}

// StripProxyLabels returns labels without the proxy's namespace.
func StripProxyLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for key, value := range labels {
		if strings.HasPrefix(strings.ToLower(key), ProxyLabelPrefix) {
			continue
		}
		out[key] = value
	}
	return out
}
