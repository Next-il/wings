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
type Domain struct {
	Domain string `json:"domain"`
	Port   int    `json:"port"`
}

// ProxyLabelPrefix is the label namespace Traefik reads. Only Wings writes it - a server's own
// labels cannot set or override anything under it.
const ProxyLabelPrefix = "traefik."

var domainLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

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

// ValidDomains splits domains into the ones that can be routed and the ones that cannot: an invalid
// name, a port outside 1-65535, or a name already listed.
func ValidDomains(in []Domain) (valid []Domain, dropped []Domain) {
	seen := make(map[string]bool, len(in))
	for _, d := range in {
		if !ValidDomain(d.Domain) || d.Port < 1 || d.Port > 65535 || seen[d.Domain] {
			dropped = append(dropped, d)
			continue
		}
		seen[d.Domain] = true
		valid = append(valid, d)
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

// ProxyRouterName is the stable Traefik router and service name of a server's i-th domain.
func ProxyRouterName(serverID string, i int) string {
	short := serverID
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("nh-%s-%d", short, i)
}

// ProxyLabels are the labels that make the proxy route each domain to its port in the container,
// over network, with a Let's Encrypt certificate from the "le" resolver. Domains must be valid
// (ValidDomains). Nil when there are none.
func ProxyLabels(serverID string, network string, domains []Domain) map[string]string {
	if len(domains) == 0 {
		return nil
	}

	labels := map[string]string{
		"traefik.enable":         "true",
		"traefik.docker.network": network,
	}
	for i, d := range domains {
		name := ProxyRouterName(serverID, i)
		router := "traefik.http.routers." + name
		labels[router+".rule"] = fmt.Sprintf("Host(`%s`)", d.Domain)
		labels[router+".entrypoints"] = "websecure"
		labels[router+".tls.certresolver"] = "le"
		labels[router+".service"] = name
		labels["traefik.http.services."+name+".loadbalancer.server.port"] = strconv.Itoa(d.Port)
	}
	return labels
}
