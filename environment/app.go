package environment

import (
	"path"
	"regexp"
	"strings"
)

// App is a NextHost addition to a server's configuration: the server runs an application image - one
// NextHost built from the customer's Dockerfile on the node, or pulled from a registry - the way
// Docker runs it anywhere else, rather than a game image made for Wings.
//
//	"app": {"enabled": true, "data_path": "/data"}
//
// or, with its storage named (AppVolume, AppFile):
//
//	"app": {"enabled": true,
//	        "volumes": [{"name": "data", "target": "/data"}],
//	        "files": [{"name": "config", "target": "/app/config.json", "content": "<base64>", "mode": 420}]}
//
// What changes for such a server, and nothing else:
//   - The process runs as the image's own user (its USER), not Wings' user.
//   - The root filesystem is writable. The container is recreated on every start, so what the app
//     writes there is gone after a restart - like any Docker container. What it keeps goes in DataPath.
//   - The server's folder is mounted at DataPath instead of /home/container - or, when Volumes is
//     given, only its folders named there, each where it says (App.Storage).
//   - Docker's own capability set, without the ones that matter for breaking out or spoofing
//     (AppCapDrop) - an image's entrypoint that switches user or chowns its files still works.
//   - Wings does not chown the folder to its own user before each start: the files belong to
//     whichever user the image runs as.
//
// The rest stays as for any server: no-new-privileges, the resource limits, the network, the port
// mappings, the domains.
type App struct {
	Enabled bool `json:"enabled"`
	// DataPath is where the server's folder is mounted inside the container, when Volumes is nil
	DataPath string `json:"data_path"`

	// Volumes, when present (even empty), replaces the server's folder at DataPath: the folder
	// itself is not mounted, each volume is a folder in it mounted read-write where it says. Nil
	// (absent from the JSON) keeps the whole folder at DataPath. No omitempty, so an empty list
	// stays one.
	Volumes []AppVolume `json:"volumes"`

	// Files are written by Wings into the server's folder before each start and mounted read-only
	// where they say.
	Files []AppFile `json:"files,omitempty"`
}

// DefaultAppDataPath is the folder's mount point when NextHost names none.
const DefaultAppDataPath = "/data"

// AppCapDrop is what an app container drops from Docker's default capabilities.
var AppCapDrop = []string{"setpcap", "mknod", "audit_write", "net_raw", "setfcap", "sys_chroot", "sys_ptrace"}

var appDataPath = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// Folders an image needs as they are - the server's folder is never mounted over or under one of
// them, and no volume or file is mounted over one (AppStorage).
var appReservedPaths = []string{
	"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32", "/proc", "/run",
	"/sbin", "/sys", "/tmp", "/usr", "/var/run",
}

// IsEnabled reports whether the server runs as an app.
func (a *App) IsEnabled() bool {
	return a != nil && a.Enabled
}

// MountPath is where the server's folder goes inside the container: DataPath when it is a plain
// absolute path outside the system folders, DefaultAppDataPath otherwise.
func (a *App) MountPath() string {
	if a == nil || !appDataPath.MatchString(a.DataPath) {
		return DefaultAppDataPath
	}

	cleaned := path.Clean(a.DataPath)
	if cleaned == "/" || strings.Contains(a.DataPath, "..") {
		return DefaultAppDataPath
	}
	for _, reserved := range appReservedPaths {
		if cleaned == reserved || strings.HasPrefix(cleaned, reserved+"/") {
			return DefaultAppDataPath
		}
	}
	return cleaned
}
