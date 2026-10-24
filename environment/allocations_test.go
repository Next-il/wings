package environment

import (
	"encoding/json"
	"testing"

	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pelican/wings/config"
)

func setDockerNetwork(t *testing.T, iface string, ispn bool) {
	t.Helper()
	config.Set(&config.Configuration{
		AuthenticationToken: "test",
		Docker: config.DockerConfiguration{
			Network: config.DockerNetworkConfiguration{Interface: iface, ISPN: ispn},
		},
	})
}

func TestAllocationsContainerPortsJSON(t *testing.T) {
	t.Run("parses the NextHost field", func(t *testing.T) {
		var a Allocations
		err := json.Unmarshal([]byte(`{
			"force_outgoing_ip": false,
			"default": {"ip": "10.0.0.1", "port": 43666},
			"mappings": {"10.0.0.1": [43666, 43667]},
			"container_ports": {"10.0.0.1": {"43666": 3306}}
		}`), &a)
		require.NoError(t, err)

		assert.Equal(t, map[string]map[int]int{"10.0.0.1": {43666: 3306}}, a.ContainerPorts)
		assert.Equal(t, 3306, a.ContainerPort("10.0.0.1", 43666))
		assert.Equal(t, 43667, a.ContainerPort("10.0.0.1", 43667))
	})

	t.Run("a configuration without it is upstream's", func(t *testing.T) {
		var a Allocations
		err := json.Unmarshal([]byte(`{
			"default": {"ip": "10.0.0.1", "port": 27015},
			"mappings": {"10.0.0.1": [27015]}
		}`), &a)
		require.NoError(t, err)

		assert.Nil(t, a.ContainerPorts)
		assert.Equal(t, 27015, a.DefaultContainerPort())
		assert.Equal(t, 27015, a.DefaultPublicPort())
	})
}

func TestAllocationsContainerPort(t *testing.T) {
	a := Allocations{
		ContainerPorts: map[string]map[int]int{
			"10.0.0.1": {43666: 3306, 43667: 0, 43668: 70000, 43669: -1},
		},
	}

	assert.Equal(t, 3306, a.ContainerPort("10.0.0.1", 43666))
	// Out of range values are ignored rather than handed to Docker
	assert.Equal(t, 43667, a.ContainerPort("10.0.0.1", 43667))
	assert.Equal(t, 43668, a.ContainerPort("10.0.0.1", 43668))
	assert.Equal(t, 43669, a.ContainerPort("10.0.0.1", 43669))
	// Keyed by IP: the same port on another address is not mapped
	assert.Equal(t, 43666, a.ContainerPort("10.0.0.2", 43666))

	t.Run("default allocation", func(t *testing.T) {
		a.DefaultMapping = &DefaultAllocationMapping{Ip: "10.0.0.1", Port: 43666}
		assert.Equal(t, 3306, a.DefaultContainerPort())
		assert.Equal(t, 43666, a.DefaultPublicPort())

		a.DefaultMapping = &DefaultAllocationMapping{Ip: "10.0.0.1", Port: 43670}
		assert.Equal(t, 43670, a.DefaultContainerPort())
		assert.Equal(t, 43670, a.DefaultPublicPort())
	})

	t.Run("no default allocation", func(t *testing.T) {
		empty := Allocations{}
		assert.Equal(t, 0, empty.DefaultContainerPort())
		assert.Equal(t, 0, empty.DefaultPublicPort())
	})
}

func TestAllocationsBindings(t *testing.T) {
	t.Run("without container ports every port maps to itself", func(t *testing.T) {
		a := Allocations{Mappings: map[string][]int{"10.0.0.1": {27015, 27020}}}

		assert.Equal(t, nat.PortMap{
			"27015/tcp": {{HostIP: "10.0.0.1", HostPort: "27015"}},
			"27015/udp": {{HostIP: "10.0.0.1", HostPort: "27015"}},
			"27020/tcp": {{HostIP: "10.0.0.1", HostPort: "27020"}},
			"27020/udp": {{HostIP: "10.0.0.1", HostPort: "27020"}},
		}, a.Bindings())
	})

	t.Run("a mapped allocation binds its public port to the container port", func(t *testing.T) {
		a := Allocations{
			Mappings:       map[string][]int{"10.0.0.1": {43666, 43667}},
			ContainerPorts: map[string]map[int]int{"10.0.0.1": {43666: 3306}},
		}

		assert.Equal(t, nat.PortMap{
			"3306/tcp":  {{HostIP: "10.0.0.1", HostPort: "43666"}},
			"3306/udp":  {{HostIP: "10.0.0.1", HostPort: "43666"}},
			"43667/tcp": {{HostIP: "10.0.0.1", HostPort: "43667"}},
			"43667/udp": {{HostIP: "10.0.0.1", HostPort: "43667"}},
		}, a.Bindings())
	})

	t.Run("two addresses mapped to one container port are both bound to it", func(t *testing.T) {
		a := Allocations{
			Mappings: map[string][]int{"10.0.0.1": {43666}, "10.0.0.2": {50000}},
			ContainerPorts: map[string]map[int]int{
				"10.0.0.1": {43666: 5432},
				"10.0.0.2": {50000: 5432},
			},
		}

		bindings := a.Bindings()
		assert.Len(t, bindings, 2)
		assert.ElementsMatch(t, []nat.PortBinding{
			{HostIP: "10.0.0.1", HostPort: "43666"},
			{HostIP: "10.0.0.2", HostPort: "50000"},
		}, bindings["5432/tcp"])
		assert.ElementsMatch(t, bindings["5432/tcp"], bindings["5432/udp"])
	})

	t.Run("invalid allocation ports are still skipped", func(t *testing.T) {
		a := Allocations{
			Mappings:       map[string][]int{"10.0.0.1": {0, 70000}},
			ContainerPorts: map[string]map[int]int{"10.0.0.1": {0: 3306, 70000: 3306}},
		}

		assert.Empty(t, a.Bindings())
	})
}

func TestAllocationsDockerBindings(t *testing.T) {
	a := Allocations{
		Mappings:       map[string][]int{"127.0.0.1": {43666}, "10.0.0.1": {43667}},
		ContainerPorts: map[string]map[int]int{"127.0.0.1": {43666: 6379}},
	}

	t.Run("a local allocation moves to the Docker interface and keeps its container port", func(t *testing.T) {
		setDockerNetwork(t, "172.18.0.1", false)

		assert.Equal(t, nat.PortMap{
			"6379/tcp":  {{HostIP: "172.18.0.1", HostPort: "43666"}},
			"6379/udp":  {{HostIP: "172.18.0.1", HostPort: "43666"}},
			"43667/tcp": {{HostIP: "10.0.0.1", HostPort: "43667"}},
			"43667/udp": {{HostIP: "10.0.0.1", HostPort: "43667"}},
		}, a.DockerBindings())

		assert.Equal(t, nat.PortSet{
			"6379/tcp":  {},
			"6379/udp":  {},
			"43667/tcp": {},
			"43667/udp": {},
		}, a.Exposed())
	})

	t.Run("ISPN still drops a local allocation", func(t *testing.T) {
		setDockerNetwork(t, "172.18.0.1", true)

		bindings := a.DockerBindings()
		assert.Empty(t, bindings["6379/tcp"])
		assert.Empty(t, bindings["6379/udp"])
		assert.Equal(t, []nat.PortBinding{{HostIP: "10.0.0.1", HostPort: "43667"}}, bindings["43667/tcp"])
	})
}
