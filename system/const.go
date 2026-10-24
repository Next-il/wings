package system

var Version = "develop"

// Features lists what this build understands beyond upstream Wings' remote API. It is sent
// to the remote on every request (FeaturesHeader), so NextHost only uses a feature with a
// node that runs a build that has it - an older Wings would silently ignore it instead.
//
//   - container-ports: allocations.container_ports in a server's configuration
var Features = []string{"container-ports"}

// FeaturesHeader is the request header Features is sent in, comma separated.
const FeaturesHeader = "X-NextHost-Features"
