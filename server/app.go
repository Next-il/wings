package server

import (
	"bytes"

	"emperror.dev/errors"
	"github.com/apex/log"
	"golang.org/x/sys/unix"

	"github.com/pelican/wings/environment"
	"github.com/pelican/wings/internal/ufs"
	"github.com/pelican/wings/server/filesystem"
)

// prepareAppStorage is NextHost's: it makes an app's storage (environment.App.Storage) ready before
// its container is made - each volume's folder created in the server's folder when missing and open
// to the image's user (0777, as the folder itself is), each file written to .files and given its
// mode, files no longer asked for removed from there. What cannot be mounted is logged and left out.
//
// Everything goes through the server's filesystem, which never follows a symlink out of the folder.
// The customer can put symlinks there (over SFTP), which is also why Docker mounts these paths
// through a volume of the folder rather than as bind mounts (environment.AppVolumeName).
func (s *Server) prepareAppStorage() error {
	app := s.Config().App
	if !app.IsEnabled() {
		return nil
	}

	storage, problems := s.appStorage(app, s.systemMounts(app))
	for _, p := range problems {
		s.Log().WithFields(log.Fields{
			p.Kind:   p.Name,
			"target": p.Target,
			"reason": p.Reason,
		}).Warn("not mounting an invalid app " + p.Kind)
		s.PublishConsoleOutputFromDaemon("Not mounting app " + p.Kind + " " + p.Name + ": " + p.Reason)
	}

	for _, v := range storage.Volumes {
		if err := openAppFolder(s.Filesystem().UnixFS(), v.Name); err != nil {
			return errors.WithMessagef(err, "failed to prepare the app volume %q", v.Name)
		}
	}
	return writeAppFiles(s.Filesystem(), storage.Files)
}

// openAppFolder makes name, in the folder fs is, a folder anyone can write to: created when missing,
// and refused when it is a symlink or a file.
func openAppFolder(fs *ufs.UnixFS, name string) error {
	if err := fs.Mkdir(name, 0o777); err != nil && !errors.Is(err, ufs.ErrExist) {
		return err
	}
	return chmodNoFollow(fs, name, ufs.O_DIRECTORY, 0o777)
}

// chmodNoFollow sets the mode of name through a descriptor opened without following a symlink, so it
// is the entry itself that changes, never what a symlink there points at.
func chmodNoFollow(fs *ufs.UnixFS, name string, flag int, mode ufs.FileMode) error {
	f, err := fs.OpenFile(name, ufs.O_RDONLY|ufs.O_NOFOLLOW|flag, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return errors.WithStack(unix.Fchmod(int(f.Fd()), uint32(mode.Perm())))
}

// writeAppFiles writes files into the .files folder of fs, and removes what else is there.
func writeAppFiles(fs *filesystem.Filesystem, files []environment.AppFileContent) error {
	u := fs.UnixFS()

	wanted := make(map[string]bool, len(files))
	for _, f := range files {
		wanted[f.Name] = true
	}

	if entries, err := u.ReadDir(environment.AppFilesDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || wanted[entry.Name()] {
				continue
			}
			if err := u.Remove(environment.AppFilePath(entry.Name())); err != nil && !errors.Is(err, ufs.ErrNotExist) {
				return errors.WithMessagef(err, "failed to remove the app file %q no longer asked for", entry.Name())
			}
		}
	} else if !errors.Is(err, ufs.ErrNotExist) && len(files) > 0 {
		return errors.WithMessage(err, "failed to read the app's files folder")
	}

	for _, f := range files {
		p := environment.AppFilePath(f.Name)

		// Whatever is there goes first: a symlink would make the write fail (or land elsewhere in
		// the folder), and Touch only sets the mode of a file it creates
		if st, err := u.Lstat(p); err == nil && !st.IsDir() {
			if err := u.Remove(p); err != nil {
				return errors.WithMessagef(err, "failed to replace the app file %q", f.Name)
			}
		}

		if err := fs.Write(p, bytes.NewReader(f.Data), int64(len(f.Data)), f.Mode); err != nil {
			return errors.WithMessagef(err, "failed to write the app file %q", f.Name)
		}
		if err := chmodNoFollow(u, p, 0, f.Mode); err != nil {
			return errors.WithMessagef(err, "failed to set the mode of the app file %q", f.Name)
		}
	}
	return nil
}
