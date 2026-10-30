package environment

import (
	"encoding/base64"
	"fmt"
	iofs "io/fs"
	"path"
	"regexp"
	"strings"
)

// AppVolume is a folder of the server's folder an app keeps its data in: <server folder>/<Name>,
// created when missing and open to the image's user, mounted read-write at Target.
type AppVolume struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

// AppFile is a file Wings writes before every start to <server folder>/.files/<Name> and mounts
// read-only at Target (a file path): a config file the app reads, say.
type AppFile struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	// Content is the file, base64 encoded (standard alphabet, padded).
	Content string `json:"content"`
	// Mode is the file's permission bits as a number (420 is 0644, the default when 0).
	Mode int `json:"mode,omitempty"`
}

// Limits on what an app may ask for.
const (
	MaxAppVolumes  = 10
	MaxAppFiles    = 20
	MaxAppFileSize = 256 << 10 // decoded

	// DefaultAppFileMode is a file's mode when it names none.
	DefaultAppFileMode iofs.FileMode = 0o644

	// AppFilesDir is the folder of the server's folder the files are written to.
	AppFilesDir = ".files"
)

var appStorageName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// Nothing is ever mounted under these: the kernel's own filesystems.
var appKernelPaths = []string{"/proc", "/sys", "/dev"}

// validAppTarget says why target cannot be a volume's or a file's mount point, nil when it can: a
// clean absolute path, not "/" or one of the system folders (appReservedPaths) itself, nothing
// under /proc, /sys or /dev. Under a system folder is fine (/etc/nginx/conf.d/default.conf).
func validAppTarget(target string) error {
	if len(target) > 1024 || !appDataPath.MatchString(target) {
		return fmt.Errorf("target %q is not a plain absolute path", target)
	}
	if path.Clean(target) != target {
		return fmt.Errorf("target %q is not a clean path", target)
	}
	for _, reserved := range appReservedPaths {
		if target == reserved {
			return fmt.Errorf("target %q is a system folder", target)
		}
	}
	for _, kernel := range appKernelPaths {
		if strings.HasPrefix(target, kernel+"/") {
			return fmt.Errorf("target %q is under %s", target, kernel)
		}
	}
	return nil
}

// AppFileContent is a file that passed validation, decoded.
type AppFileContent struct {
	Name   string
	Target string
	Data   []byte
	Mode   iofs.FileMode
}

// AppStorageProblem is a volume or file that is not mounted, and why.
type AppStorageProblem struct {
	Kind   string // "volume" or "file"
	Name   string
	Target string
	Reason string
}

// AppStorage is what of an app's storage is mounted (App.Storage).
type AppStorage struct {
	// FolderTarget is where the whole server folder is mounted, "" when it is not (Volumes given).
	FolderTarget string
	Volumes      []AppVolume
	Files        []AppFileContent
}

// Storage is the app's storage that can be mounted, and what cannot and why: a name that is not
// lowercase letters, digits, "-" and "_" (40 at most, starting with a letter or digit) or is
// already used by one of its kind, a target validAppTarget refuses or already used by a volume, a
// file, the server's folder or one of taken (the container's other mounts), a file whose content
// is not base64 or is over MaxAppFileSize, a mode outside 0-0777, more than MaxAppVolumes volumes
// or MaxAppFiles files. None of it fails a start.
func (a *App) Storage(taken ...string) (AppStorage, []AppStorageProblem) {
	var storage AppStorage
	var problems []AppStorageProblem
	if !a.IsEnabled() {
		return storage, nil
	}

	targets := make(map[string]bool, len(taken)+len(a.Volumes)+len(a.Files)+1)
	for _, t := range taken {
		targets[t] = true
	}
	if a.Volumes == nil {
		storage.FolderTarget = a.MountPath()
		targets[storage.FolderTarget] = true
	}

	names := make(map[string]bool, len(a.Volumes))
	for _, v := range a.Volumes {
		reason := ""
		switch {
		case !appStorageName.MatchString(v.Name):
			reason = fmt.Sprintf("name %q is not 1-40 lowercase letters, digits, - and _", v.Name)
		case names[v.Name]:
			reason = fmt.Sprintf("name %q is already a volume", v.Name)
		case validAppTarget(v.Target) != nil:
			reason = validAppTarget(v.Target).Error()
		case targets[v.Target]:
			reason = fmt.Sprintf("target %q is already mounted", v.Target)
		case len(storage.Volumes) >= MaxAppVolumes:
			reason = fmt.Sprintf("more than %d volumes", MaxAppVolumes)
		}
		if reason != "" {
			problems = append(problems, AppStorageProblem{Kind: "volume", Name: v.Name, Target: v.Target, Reason: reason})
			continue
		}
		names[v.Name] = true
		targets[v.Target] = true
		storage.Volumes = append(storage.Volumes, v)
	}

	names = make(map[string]bool, len(a.Files))
	for _, f := range a.Files {
		var data []byte
		reason := ""
		switch {
		case !appStorageName.MatchString(f.Name):
			reason = fmt.Sprintf("name %q is not 1-40 lowercase letters, digits, - and _", f.Name)
		case names[f.Name]:
			reason = fmt.Sprintf("name %q is already a file", f.Name)
		case validAppTarget(f.Target) != nil:
			reason = validAppTarget(f.Target).Error()
		case targets[f.Target]:
			reason = fmt.Sprintf("target %q is already mounted", f.Target)
		case f.Mode < 0 || f.Mode > 0o777:
			reason = fmt.Sprintf("mode %o is not permission bits", f.Mode)
		case base64.StdEncoding.DecodedLen(len(f.Content)) > MaxAppFileSize+3:
			reason = fmt.Sprintf("content is over %d bytes", MaxAppFileSize)
		case len(storage.Files) >= MaxAppFiles:
			reason = fmt.Sprintf("more than %d files", MaxAppFiles)
		default:
			var err error
			if data, err = base64.StdEncoding.DecodeString(f.Content); err != nil {
				reason = "content is not base64"
			} else if len(data) > MaxAppFileSize {
				reason = fmt.Sprintf("content is over %d bytes", MaxAppFileSize)
			}
		}
		if reason != "" {
			problems = append(problems, AppStorageProblem{Kind: "file", Name: f.Name, Target: f.Target, Reason: reason})
			continue
		}

		mode := DefaultAppFileMode
		if f.Mode != 0 {
			mode = iofs.FileMode(f.Mode)
		}
		names[f.Name] = true
		targets[f.Target] = true
		storage.Files = append(storage.Files, AppFileContent{Name: f.Name, Target: f.Target, Data: data, Mode: mode})
	}

	return storage, problems
}

// AppFilePath is where a file is written, relative to the server's folder.
func AppFilePath(name string) string {
	return AppFilesDir + "/" + name
}

// Mounts is where the storage goes in the container, root being the server's folder: the folder
// itself at FolderTarget when it is mounted whole, then each volume, then each file read-only.
// Volumes and files are paths inside root (Mount.Subpath), so Docker resolves them inside it.
func (s AppStorage) Mounts(root string) []Mount {
	out := make([]Mount, 0, 1+len(s.Volumes)+len(s.Files))
	if s.FolderTarget != "" {
		out = append(out, Mount{Default: true, Target: s.FolderTarget, Source: root})
	}
	for _, v := range s.Volumes {
		out = append(out, Mount{Target: v.Target, Source: root, Subpath: v.Name})
	}
	for _, f := range s.Files {
		out = append(out, Mount{Target: f.Target, Source: root, Subpath: AppFilePath(f.Name), ReadOnly: true})
	}
	return out
}

// AppVolumeName is the Docker volume an app's volumes and files are mounted through: the server's
// folder, bound (local driver, o=bind), with each mount a subpath of it. Docker resolves a subpath
// inside the volume and refuses one that leads out through a symlink, which a bind mount of
// <folder>/<name> would follow on the host - and the customer can put a symlink there over SFTP.
func AppVolumeName(serverID string) string {
	return "nexthost-app-" + serverID
}
