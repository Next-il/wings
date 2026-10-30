package environment

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	gojson "github.com/goccy/go-json"
)

func TestAppMountPath(t *testing.T) {
	for _, tc := range []struct {
		dataPath string
		want     string
	}{
		{"", "/data"},
		{"/data", "/data"},
		{"/app/storage/", "/app/storage"},
		{"/srv/uploads", "/srv/uploads"},
		{"/", "/data"},
		{"relative", "/data"},
		{"/etc", "/data"},
		{"/etc/nginx", "/data"},
		{"/usr/local/app", "/data"},
		{"/proc/1", "/data"},
		{"/data/../etc", "/data"},
		{"/data with space", "/data"},
		{"/tmp", "/data"},
	} {
		app := &App{Enabled: true, DataPath: tc.dataPath}
		if got := app.MountPath(); got != tc.want {
			t.Errorf("MountPath(%q) = %q, want %q", tc.dataPath, got, tc.want)
		}
	}
	var none *App
	if none.IsEnabled() || none.MountPath() != DefaultAppDataPath {
		t.Error("a server without app settings is not an app")
	}
	if (&App{Enabled: false}).IsEnabled() {
		t.Error("enabled false is not an app")
	}
}

func TestAppCapDropKeepsWhatImagesUse(t *testing.T) {
	for _, kept := range []string{"chown", "dac_override", "fowner", "setuid", "setgid", "net_bind_service", "kill"} {
		for _, dropped := range AppCapDrop {
			if dropped == kept {
				t.Errorf("%s is dropped, but images rely on it (entrypoints that chown or switch user, ports under 1024)", kept)
			}
		}
	}
}

func b64(n int) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), n))
}

func storageProblems(t *testing.T, app *App, taken ...string) (AppStorage, map[string]string) {
	t.Helper()
	storage, problems := app.Storage(taken...)
	byName := map[string]string{}
	for _, p := range problems {
		if p.Reason == "" {
			t.Errorf("no reason for %+v", p)
		}
		byName[p.Kind+":"+p.Name+":"+p.Target] = p.Reason
	}
	return storage, byName
}

// The JSON decides the layout: no volumes key (or null) keeps the folder at data_path, a list - even
// an empty one - replaces it. goccy/go-json is what reads a server's configuration.
func TestAppVolumesFromJSON(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		folder string
	}{
		{`{"enabled":true,"data_path":"/srv"}`, "/srv"},
		{`{"enabled":true,"data_path":"/srv","volumes":null}`, "/srv"},
		{`{"enabled":true,"data_path":"/srv","volumes":[]}`, ""},
		{`{"enabled":true,"volumes":[{"name":"data","target":"/data"}]}`, ""},
	} {
		for name, unmarshal := range map[string]func([]byte, any) error{"goccy": gojson.Unmarshal, "encoding/json": json.Unmarshal} {
			var app App
			if err := unmarshal([]byte(tc.raw), &app); err != nil {
				t.Fatal(err)
			}
			storage, _ := app.Storage()
			if storage.FolderTarget != tc.folder {
				t.Errorf("%s %s: folder target %q, want %q", name, tc.raw, storage.FolderTarget, tc.folder)
			}
		}
	}
}

func TestAppStorageWithoutVolumesIsTheFolder(t *testing.T) {
	app := &App{Enabled: true, DataPath: "/data"}
	storage, problems := app.Storage()
	if len(problems) != 0 || storage.FolderTarget != "/data" || len(storage.Volumes) != 0 || len(storage.Files) != 0 {
		t.Fatalf("storage %+v problems %+v", storage, problems)
	}
	want := []Mount{{Default: true, Target: "/data", Source: "/srv/root"}}
	if got := storage.Mounts("/srv/root"); !reflect.DeepEqual(got, want) {
		t.Fatalf("mounts %+v, want %+v", got, want)
	}

	var none *App
	if s, p := none.Storage(); s.FolderTarget != "" || p != nil {
		t.Error("a server that is not an app has no app storage")
	}
}

func TestAppStorageMounts(t *testing.T) {
	app := &App{
		Enabled:  true,
		DataPath: "/ignored",
		Volumes:  []AppVolume{{Name: "data", Target: "/data"}, {Name: "pg", Target: "/var/lib/postgresql/data"}},
		Files: []AppFile{
			{Name: "config", Target: "/app/config.json", Content: base64.StdEncoding.EncodeToString([]byte(`{"a":1}`))},
			{Name: "nginx", Target: "/etc/nginx/conf.d/default.conf", Content: "", Mode: 0o600},
		},
	}
	storage, problems := app.Storage()
	if len(problems) != 0 {
		t.Fatalf("problems %+v", problems)
	}
	if storage.FolderTarget != "" {
		t.Fatal("with volumes the folder itself is not mounted")
	}
	if string(storage.Files[0].Data) != `{"a":1}` || storage.Files[0].Mode != 0o644 || storage.Files[1].Mode != 0o600 || len(storage.Files[1].Data) != 0 {
		t.Fatalf("files %+v", storage.Files)
	}

	want := []Mount{
		{Target: "/data", Source: "/srv/root", Subpath: "data"},
		{Target: "/var/lib/postgresql/data", Source: "/srv/root", Subpath: "pg"},
		{Target: "/app/config.json", Source: "/srv/root", Subpath: ".files/config", ReadOnly: true},
		{Target: "/etc/nginx/conf.d/default.conf", Source: "/srv/root", Subpath: ".files/nginx", ReadOnly: true},
	}
	if got := storage.Mounts("/srv/root"); !reflect.DeepEqual(got, want) {
		t.Fatalf("mounts\n%+v\nwant\n%+v", got, want)
	}

	// Files work with the folder layout too
	app.Volumes = nil
	storage, _ = app.Storage()
	got := storage.Mounts("/srv/root")
	if len(got) != 3 || got[0].Target != "/ignored" || got[0].Subpath != "" || !got[0].Default || got[1].Subpath != ".files/config" {
		t.Fatalf("mounts %+v", got)
	}
}

func TestAppStorageTargets(t *testing.T) {
	for _, tc := range []struct {
		target string
		ok     bool
	}{
		{"/data", true},
		{"/app/config.json", true},
		{"/etc/nginx/conf.d/default.conf", true},
		{"/etc/letsencrypt", true},
		{"/var/lib/postgresql/data", true},
		{"/usr/share/nginx/html", true},
		{"/tmp/cache", true},
		{"/run/app", true},
		{"/", false},
		{"", false},
		{"data", false},
		{"/etc", false},
		{"/usr", false},
		{"/bin", false},
		{"/tmp", false},
		{"/var/run", false},
		{"/run", false},
		{"/proc", false},
		{"/proc/sys/kernel", false},
		{"/sys", false},
		{"/sys/fs/cgroup", false},
		{"/dev", false},
		{"/dev/shm", false},
		{"/data/", false},
		{"/data/../etc", false},
		{"/a//b", false},
		{"/a/./b", false},
		{"/data with space", false},
		{"/data:ro", false},
	} {
		volume := &App{Enabled: true, Volumes: []AppVolume{{Name: "v", Target: tc.target}}}
		storage, _ := volume.Storage()
		if (len(storage.Volumes) == 1) != tc.ok {
			t.Errorf("volume target %q: mounted %v, want %v", tc.target, len(storage.Volumes) == 1, tc.ok)
		}
		file := &App{Enabled: true, Volumes: []AppVolume{}, Files: []AppFile{{Name: "f", Target: tc.target}}}
		storage, _ = file.Storage()
		if (len(storage.Files) == 1) != tc.ok {
			t.Errorf("file target %q: mounted %v, want %v", tc.target, len(storage.Files) == 1, tc.ok)
		}
	}
}

func TestAppStorageNames(t *testing.T) {
	for name, ok := range map[string]bool{
		"data": true, "a": true, "0": true, "my-app_data2": true, strings.Repeat("a", 40): true,
		"": false, "Data": false, "-data": false, "_data": false, ".files": false, "a/b": false, "..": false,
		"a b": false, strings.Repeat("a", 41): false,
	} {
		app := &App{Enabled: true, Volumes: []AppVolume{{Name: name, Target: "/v"}}, Files: []AppFile{{Name: name, Target: "/f"}}}
		storage, _ := app.Storage()
		if (len(storage.Volumes) == 1) != ok || (len(storage.Files) == 1) != ok {
			t.Errorf("name %q: volumes %d files %d, want ok %v", name, len(storage.Volumes), len(storage.Files), ok)
		}
	}
}

func TestAppStorageDuplicates(t *testing.T) {
	app := &App{
		Enabled:  true,
		DataPath: "/data",
		Volumes: []AppVolume{
			{Name: "data", Target: "/data"},
			{Name: "data", Target: "/other"},       // name taken
			{Name: "more", Target: "/data"},        // target taken
			{Name: "machine", Target: "/etc/mine"}, // taken by another mount
			{Name: "ok", Target: "/ok"},
		},
		Files: []AppFile{
			{Name: "data", Target: "/data.json"},  // a file may share a volume's name
			{Name: "data", Target: "/data2.json"}, // not another file's
			{Name: "over", Target: "/ok"},         // a volume's target
			{Name: "same", Target: "/data.json"},  // another file's target
		},
	}
	storage, problems := storageProblems(t, app, "/etc/mine")

	if got := len(storage.Volumes); got != 2 || storage.Volumes[0].Target != "/data" || storage.Volumes[1].Name != "ok" {
		t.Errorf("volumes %+v", storage.Volumes)
	}
	if got := len(storage.Files); got != 1 || storage.Files[0].Target != "/data.json" {
		t.Errorf("files %+v", storage.Files)
	}
	for _, key := range []string{
		"volume:data:/other", "volume:more:/data", "volume:machine:/etc/mine",
		"file:data:/data2.json", "file:over:/ok", "file:same:/data.json",
	} {
		if problems[key] == "" {
			t.Errorf("%s is not reported: %v", key, problems)
		}
	}

	// In the folder layout the folder's own target is taken
	legacy := &App{Enabled: true, DataPath: "/srv", Files: []AppFile{{Name: "f", Target: "/srv"}}}
	if storage, _ := legacy.Storage(); len(storage.Files) != 0 {
		t.Error("a file cannot go where the folder is")
	}
}

func TestAppStorageLimits(t *testing.T) {
	app := &App{Enabled: true}
	for i := 0; i < MaxAppVolumes+1; i++ {
		app.Volumes = append(app.Volumes, AppVolume{Name: fmt.Sprintf("v%d", i), Target: fmt.Sprintf("/v%d", i)})
	}
	for i := 0; i < MaxAppFiles+1; i++ {
		app.Files = append(app.Files, AppFile{Name: fmt.Sprintf("f%d", i), Target: fmt.Sprintf("/f%d", i)})
	}
	storage, problems := storageProblems(t, app)
	if len(storage.Volumes) != MaxAppVolumes || len(storage.Files) != MaxAppFiles {
		t.Fatalf("volumes %d files %d", len(storage.Volumes), len(storage.Files))
	}
	if problems[fmt.Sprintf("volume:v%d:/v%d", MaxAppVolumes, MaxAppVolumes)] == "" || problems[fmt.Sprintf("file:f%d:/f%d", MaxAppFiles, MaxAppFiles)] == "" {
		t.Fatalf("the ones over the limit are reported: %v", problems)
	}

	// An invalid entry does not count towards the limit
	app = &App{Enabled: true, Volumes: []AppVolume{{Name: "BAD", Target: "/bad"}}}
	for i := 0; i < MaxAppVolumes; i++ {
		app.Volumes = append(app.Volumes, AppVolume{Name: fmt.Sprintf("v%d", i), Target: fmt.Sprintf("/v%d", i)})
	}
	if storage, _ := app.Storage(); len(storage.Volumes) != MaxAppVolumes {
		t.Fatalf("volumes %d", len(storage.Volumes))
	}
}

func TestAppStorageFileContent(t *testing.T) {
	for _, tc := range []struct {
		desc    string
		content string
		mode    int
		ok      bool
		size    int
		want    os.FileMode
	}{
		{"empty", "", 0, true, 0, 0o644},
		{"at the limit", b64(MaxAppFileSize), 0, true, MaxAppFileSize, 0o644},
		{"over the limit", b64(MaxAppFileSize + 1), 0, false, 0, 0},
		{"far over the limit", b64(4 * MaxAppFileSize), 0, false, 0, 0},
		{"not base64", "not base64!", 0, false, 0, 0},
		{"url alphabet", base64.URLEncoding.EncodeToString([]byte{0xfb, 0xff}), 0, false, 0, 0},
		{"mode", b64(3), 0o600, true, 3, 0o600},
		{"executable", b64(3), 0o755, true, 3, 0o755},
		{"setuid is not permission bits", b64(3), 0o4755, false, 0, 0},
		{"negative mode", b64(3), -1, false, 0, 0},
	} {
		app := &App{Enabled: true, Files: []AppFile{{Name: "f", Target: "/f", Content: tc.content, Mode: tc.mode}}}
		storage, problems := app.Storage()
		if (len(storage.Files) == 1) != tc.ok {
			t.Errorf("%s: files %d problems %+v, want ok %v", tc.desc, len(storage.Files), problems, tc.ok)
			continue
		}
		if tc.ok && (len(storage.Files[0].Data) != tc.size || storage.Files[0].Mode != tc.want) {
			t.Errorf("%s: size %d mode %o, want %d %o", tc.desc, len(storage.Files[0].Data), storage.Files[0].Mode, tc.size, tc.want)
		}
	}
}

func TestAppVolumeName(t *testing.T) {
	if got := AppVolumeName("8f2c1a9b-0000-4000-8000-000000000000"); got != "nexthost-app-8f2c1a9b-0000-4000-8000-000000000000" {
		t.Fatalf("got %s", got)
	}
}
