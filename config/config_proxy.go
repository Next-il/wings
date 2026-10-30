package config

// ProxyConfiguration is NextHost's domain proxy: Traefik in a container Wings keeps running, which
// routes the customers' domains of a server (its "domains" setting) to that server's container over
// the Wings Docker network, with Let's Encrypt certificates. On unless config.yml turns it off.
type ProxyConfiguration struct {
	// Enabled makes Wings create and keep the proxy container running, and write each running
	// server's routes for it. Turning it off removes the routes as servers sync or change state and
	// leaves an existing proxy container alone. When ports 80 or 443 are taken by something else on
	// the node, the proxy does not start and Wings runs on without it.
	Enabled bool `default:"true" json:"enabled" yaml:"enabled"`

	// Image is the Traefik image, pinned to an exact v3 release.
	Image string `default:"traefik:v3.7.15" json:"image" yaml:"image"`

	// ContainerName is the proxy container's name. The node agent stops and starts it by this name
	// around certificate renewals, so keep the default.
	ContainerName string `default:"nexthost-proxy" json:"container_name" yaml:"container_name"`

	// HTTPPort and HTTPSPort are bound on every interface of the node.
	HTTPPort  int `default:"80" json:"http_port" yaml:"http_port"`
	HTTPSPort int `default:"443" json:"https_port" yaml:"https_port"`

	// AcmeEmail is the Let's Encrypt account email for the proxy's certificates.
	AcmeEmail string `json:"acme_email" yaml:"acme_email"`

	// DataDir holds acme.json, the proxy's certificates, and dynamic/, the routes Wings writes for
	// Traefik (one <server uuid>.yml per running server with domains, and _nexthost.yml).
	DataDir string `default:"/var/lib/pelican/proxy" json:"data_dir" yaml:"data_dir"`
}
