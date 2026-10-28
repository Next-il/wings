package environment

import "testing"

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
