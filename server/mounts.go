package server

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/apex/log"

	"github.com/pelican/wings/config"
	"github.com/pelican/wings/environment"
)

// To avoid confusion when working with mounts, assume that a server.Mount has not been properly
// cleaned up and had the paths set. An environment.Mount should only be returned with valid paths
// that have been checked.
type Mount environment.Mount

// Returns the default container mounts for the server instance. This includes the data directory
// for the server. Previously this would also mount in host timezone files, however we've moved from
// that approach to just setting `TZ=Timezone` environment values in containers which should work
// in most scenarios.
func (s *Server) Mounts() []environment.Mount {
	// NextHost: an app keeps its folder where it asks for it - or only its volumes, each where it
	// asks - and its files, and its image's own /etc/passwd
	app := s.Config().App
	if app.IsEnabled() {
		others := s.systemMounts(app)
		storage, _ := s.appStorage(app, others)
		return append(storage.Mounts(s.Filesystem().Path()), others...)
	}

	m := []environment.Mount{
		{
			Default:  true,
			Target:   "/home/container",
			Source:   s.Filesystem().Path(),
			ReadOnly: false,
		},
	}
	return append(m, s.systemMounts(app)...)
}

// appStorage is what of an app's storage is mounted (environment.App.Storage), next to others.
func (s *Server) appStorage(app *environment.App, others []environment.Mount) (environment.AppStorage, []environment.AppStorageProblem) {
	taken := make([]string, len(others))
	for i, m := range others {
		taken[i] = m.Target
	}
	return app.Storage(taken...)
}

// systemMounts are a server's mounts besides its own folder: /etc/passwd and /etc/group (not for an
// app), the machine-id, and the custom mounts.
func (s *Server) systemMounts(app *environment.App) []environment.Mount {
	var m []environment.Mount

	if config.Get().System.User.Passwd.Enable && !app.IsEnabled() {
		passwdMount := environment.Mount{
			Target:   "/etc/passwd",
			Source:   filepath.Join(config.Get().System.User.Passwd.Directory, "passwd"),
			ReadOnly: true,
		}

		m = append(m, passwdMount)

		groupMount := environment.Mount{
			Target:   "/etc/group",
			Source:   filepath.Join(config.Get().System.User.Passwd.Directory, "group"),
			ReadOnly: true,
		}

		m = append(m, groupMount)
	}

	if config.Get().System.MachineID.Enable {
		machineIDMount := environment.Mount{
			Target:   "/etc/machine-id",
			Source:   filepath.Join(config.Get().System.MachineID.Directory, s.ID()),
			ReadOnly: true,
		}

		m = append(m, machineIDMount)
	}
	// Also include any of this server's custom mounts when returning them.
	return append(m, s.customMounts()...)
}

// Returns the custom mounts for a given server after verifying that they are within a list of
// allowed mount points for the node.
func (s *Server) customMounts() []environment.Mount {
	var mounts []environment.Mount

	for _, m := range s.Config().Mounts {
		source := filepath.Clean(m.Source)
		target := filepath.Clean(m.Target)

		logger := s.Log().WithFields(log.Fields{
			"source_path": source,
			"target_path": target,
			"read_only":   m.ReadOnly,
		})

		// Check if the source path exists
		if _, err := os.Stat(source); os.IsNotExist(err) {
			logger.WithField("missing_source_path", source).Warn("skipping custom server mount, source path does not exist")
			continue
		}

		// Check if the target path includes /home/container
		if strings.HasPrefix(target, "/home/container") && config.Get().BlockBaseDirMount {
			logger.WithField("invalid_target_path", target).Warn("Skipping custom server mount; target path includes /home/container")
			continue
		}

		mounted := false
		for _, allowed := range config.Get().AllowedMounts {
			// Check if the source path is included in the allowed mounts list.
			// filepath.Clean will strip all trailing slashes (unless the path is a root directory).
			if !strings.HasPrefix(source, filepath.Clean(allowed)) {
				continue
			}

			mounted = true
			mounts = append(mounts, environment.Mount{
				Source:   source,
				Target:   target,
				ReadOnly: m.ReadOnly,
			})

			break
		}

		if !mounted {
			logger.Warn("skipping custom server mount, not in list of allowed mount points")
		}
	}

	return mounts
}
