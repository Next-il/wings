package docker

import (
	"reflect"
	"testing"

	"github.com/docker/docker/api/types/mount"

	"github.com/pelican/wings/environment"
)

func TestConvertMounts(t *testing.T) {
	id := "8f2c1a9b-0000-4000-8000-000000000000"
	app := &environment.App{
		Enabled: true,
		Volumes: []environment.AppVolume{{Name: "data", Target: "/data"}},
		Files:   []environment.AppFile{{Name: "config", Target: "/app/config.json"}},
	}
	storage, problems := app.Storage()
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	mounts := append(storage.Mounts("/var/lib/pelican/volumes/"+id),
		environment.Mount{Target: "/etc/machine-id", Source: "/etc/pelican/machine-id/" + id, ReadOnly: true})

	e := &Environment{Id: id, Configuration: environment.NewConfiguration(environment.Settings{Mounts: mounts, App: app}, nil)}
	got := e.convertMounts()

	want := []mount.Mount{
		{
			Type: mount.TypeVolume, Source: "nexthost-app-" + id, Target: "/data",
			VolumeOptions: &mount.VolumeOptions{NoCopy: true, Subpath: "data"},
		},
		{
			Type: mount.TypeVolume, Source: "nexthost-app-" + id, Target: "/app/config.json", ReadOnly: true,
			VolumeOptions: &mount.VolumeOptions{NoCopy: true, Subpath: ".files/config"},
		},
		{Type: mount.TypeBind, Source: "/etc/pelican/machine-id/" + id, Target: "/etc/machine-id", ReadOnly: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}

	// The server's folder itself, as every server and an app without volumes has it, is a bind mount
	e = &Environment{Id: id, Configuration: environment.NewConfiguration(environment.Settings{Mounts: []environment.Mount{
		{Default: true, Target: "/home/container", Source: "/var/lib/pelican/volumes/" + id},
	}}, nil)}
	if got := e.convertMounts(); len(got) != 1 || got[0].Type != mount.TypeBind || got[0].VolumeOptions != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestAppVolumeOptions(t *testing.T) {
	want := map[string]string{"type": "none", "o": "bind", "device": "/var/lib/pelican/volumes/x"}
	if got := appVolumeOptions("/var/lib/pelican/volumes/x"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestCapDrop(t *testing.T) {
	if got := capDrop(&environment.App{Enabled: true}); !reflect.DeepEqual(got, environment.AppCapDrop) {
		t.Errorf("an app drops AppCapDrop: %v", got)
	}
	if got := capDrop(nil); len(got) <= len(environment.AppCapDrop) {
		t.Errorf("a game server drops more: %v", got)
	}
}
