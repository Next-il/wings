package server

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelican/wings/config"
	"github.com/pelican/wings/environment"
	"github.com/pelican/wings/server/filesystem"
)

func appServer(t *testing.T, app *environment.App) *Server {
	t.Helper()
	c := &config.Configuration{AuthenticationToken: "test", System: config.SystemConfiguration{Timezone: "UTC"}}
	// Files are chowned to Wings' user: here the one running the test
	c.System.User.Uid = os.Getuid()
	c.System.User.Gid = os.Getgid()
	config.Set(c)

	fs, err := filesystem.New(filepath.Join(t.TempDir(), "root"), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: Configuration{Uuid: "8f2c1a9b-0000-4000-8000-000000000000", App: app}, fs: fs}
}

func TestPrepareAppStorage(t *testing.T) {
	app := &environment.App{
		Enabled: true,
		Volumes: []environment.AppVolume{{Name: "data", Target: "/data"}, {Name: "uploads", Target: "/app/uploads"}},
		Files: []environment.AppFile{
			{Name: "config", Target: "/app/config.json", Content: base64.StdEncoding.EncodeToString([]byte(`{"a":1}`))},
			{Name: "key", Target: "/app/key.pem", Content: base64.StdEncoding.EncodeToString([]byte("secret")), Mode: 0o600},
		},
	}
	s := appServer(t, app)
	root := s.Filesystem().Path()

	// One volume's folder is there already, with data, and closed
	if err := os.MkdirAll(filepath.Join(root, "uploads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "uploads", "a.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file from an earlier start that is no longer asked for, and one that is, stale
	if err := os.MkdirAll(filepath.Join(root, ".files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".files", "old"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".files", "key"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.prepareAppStorage(); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"data", "uploads"} {
		st, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !st.IsDir() || st.Mode().Perm() != 0o777 {
			t.Errorf("volume %s: %v %v", name, st, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "uploads", "a.png")); err != nil || string(data) != "png" {
		t.Errorf("a volume's data stays: %q %v", data, err)
	}

	for name, want := range map[string]struct {
		data string
		mode os.FileMode
	}{"config": {`{"a":1}`, 0o644}, "key": {"secret", 0o600}} {
		p := filepath.Join(root, ".files", name)
		data, err := os.ReadFile(p)
		st, _ := os.Lstat(p)
		if err != nil || string(data) != want.data || st.Mode().Perm() != want.mode {
			t.Errorf("file %s: %q %v %v", name, data, st.Mode(), err)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, ".files", "old")); !os.IsNotExist(err) {
		t.Errorf("a file no longer asked for is removed: %v", err)
	}

	// The container gets the volumes and the files, not the folder
	mounts := s.Mounts()
	if len(mounts) != 4 {
		t.Fatalf("mounts %+v", mounts)
	}
	for _, m := range mounts {
		if m.Subpath == "" || m.Source != root || m.Default {
			t.Errorf("mount %+v", m)
		}
	}
}

// A symlink the customer put where a volume or file goes is never followed out of the folder
func TestPrepareAppStorageRefusesSymlinks(t *testing.T) {
	outside := t.TempDir()
	if err := os.Chmod(outside, 0o700); err != nil {
		t.Fatal(err)
	}

	s := appServer(t, &environment.App{Enabled: true, Volumes: []environment.AppVolume{{Name: "data", Target: "/data"}}})
	root := s.Filesystem().Path()
	if err := os.Symlink(outside, filepath.Join(root, "data")); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareAppStorage(); err == nil {
		t.Error("a symlink where a volume goes must fail the start")
	}
	if st, _ := os.Stat(outside); st.Mode().Perm() != 0o700 {
		t.Errorf("the symlink's target was changed: %v", st.Mode())
	}

	target := filepath.Join(outside, "passwd")
	if err := os.WriteFile(target, []byte("root"), 0o600); err != nil {
		t.Fatal(err)
	}
	s = appServer(t, &environment.App{Enabled: true, Volumes: []environment.AppVolume{}, Files: []environment.AppFile{
		{Name: "config", Target: "/app/config.json", Content: base64.StdEncoding.EncodeToString([]byte("customer"))},
	}})
	root = s.Filesystem().Path()
	if err := os.MkdirAll(filepath.Join(root, ".files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".files", "config")); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareAppStorage(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "root" {
		t.Errorf("a file outside the folder was written: %q", data)
	}
	if st, err := os.Lstat(filepath.Join(root, ".files", "config")); err != nil || !st.Mode().IsRegular() {
		t.Errorf("the symlink is replaced by the file: %v %v", st, err)
	}

	// The files folder itself a symlink out of the folder
	s = appServer(t, &environment.App{Enabled: true, Volumes: []environment.AppVolume{}, Files: []environment.AppFile{
		{Name: "passwd", Target: "/app/x", Content: base64.StdEncoding.EncodeToString([]byte("customer"))},
	}})
	root = s.Filesystem().Path()
	if err := os.Symlink(outside, filepath.Join(root, ".files")); err != nil {
		t.Fatal(err)
	}
	_ = s.prepareAppStorage()
	if data, _ := os.ReadFile(target); string(data) != "root" {
		t.Errorf("a file outside the folder was written through a symlinked .files: %q", data)
	}
}

func TestAppMountsKeepTheFolderWithoutVolumes(t *testing.T) {
	s := appServer(t, &environment.App{Enabled: true, DataPath: "/srv"})
	mounts := s.Mounts()
	if len(mounts) != 1 || !mounts[0].Default || mounts[0].Target != "/srv" || mounts[0].Source != s.Filesystem().Path() || mounts[0].Subpath != "" {
		t.Fatalf("mounts %+v", mounts)
	}

	s = appServer(t, nil)
	mounts = s.Mounts()
	if len(mounts) != 1 || mounts[0].Target != "/home/container" {
		t.Fatalf("a game server keeps /home/container: %+v", mounts)
	}
}
