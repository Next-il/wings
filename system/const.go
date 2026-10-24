package system

var Version = "develop"

// Features lists what this NextHost build adds beyond upstream Wings, so NextHost checks for a
// feature instead of comparing version numbers. It is sent to the remote on every request
// (FeaturesHeader, comma separated) and reported as "features" by GET /api/system; a build
// without it has none of them.
//
//   - container-ports: allocations.container_ports in a server's configuration
//   - shell: the staff shell socket at /api/servers/:server/shell
var Features = []string{"container-ports", "shell"}

// FeaturesHeader is the request header Features is sent in, comma separated.
const FeaturesHeader = "X-NextHost-Features"
