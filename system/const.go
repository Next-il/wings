package system

var Version = "develop"

// Features lists what this NextHost build adds beyond upstream Wings, so NextHost checks for a
// feature instead of comparing version numbers. It is sent to the remote on every request
// (FeaturesHeader, comma separated) and reported as "features" by GET /api/system; a build
// without it has none of them.
//
//   - container-ports: allocations.container_ports in a server's configuration
//   - shell: the staff shell socket at /api/servers/:server/shell
//   - domains: a server's domains, routed by the node's proxy (GET /api/system's proxy.features
//     says which domain options it takes)
//   - app-storage: app.volumes and app.files in a server's configuration
var Features = []string{"container-ports", "shell", "domains", "app-storage"}

// FeaturesHeader is the request header Features is sent in, comma separated.
const FeaturesHeader = "X-NextHost-Features"
