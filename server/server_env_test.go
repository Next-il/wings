package server

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pelican/wings/config"
	"github.com/pelican/wings/environment"
)

func testServer(allocations environment.Allocations) *Server {
	config.Set(&config.Configuration{AuthenticationToken: "test", System: config.SystemConfiguration{Timezone: "UTC"}})

	return &Server{cfg: Configuration{
		Invocation:  "./run --port {{SERVER_PORT}} --advertise {{SERVER_IP}}:{{SERVER_PUBLIC_PORT}} --memory {{SERVER_MEMORY}}",
		EnvVars:     environment.Variables{},
		Allocations: allocations,
		Build:       environment.Limits{MemoryLimit: 1024},
	}}
}

func TestGetEnvironmentVariablesPorts(t *testing.T) {
	t.Run("without container ports SERVER_PORT is the allocation's own port", func(t *testing.T) {
		s := testServer(environment.Allocations{
			DefaultMapping: &environment.DefaultAllocationMapping{Ip: "10.0.0.1", Port: 27015},
			Mappings:       map[string][]int{"10.0.0.1": {27015}},
		})

		env := s.GetEnvironmentVariables()
		assert.Contains(t, env, "SERVER_PORT=27015")
		assert.Contains(t, env, "SERVER_PUBLIC_PORT=27015")
		assert.Contains(t, env, "SERVER_IP=10.0.0.1")
		assert.Contains(t, env, "STARTUP=./run --port 27015 --advertise 10.0.0.1:27015 --memory 1024")
	})

	t.Run("a mapped default allocation listens on its container port", func(t *testing.T) {
		s := testServer(environment.Allocations{
			DefaultMapping: &environment.DefaultAllocationMapping{Ip: "10.0.0.1", Port: 43666},
			Mappings:       map[string][]int{"10.0.0.1": {43666, 43667}},
			ContainerPorts: map[string]map[int]int{"10.0.0.1": {43666: 3306, 43667: 8080}},
		})

		env := s.GetEnvironmentVariables()
		assert.Contains(t, env, "SERVER_PORT=3306")
		assert.Contains(t, env, "SERVER_PUBLIC_PORT=43666")
		assert.Contains(t, env, "STARTUP=./run --port 3306 --advertise 10.0.0.1:43666 --memory 1024")
	})

	t.Run("only the default allocation decides SERVER_PORT", func(t *testing.T) {
		s := testServer(environment.Allocations{
			DefaultMapping: &environment.DefaultAllocationMapping{Ip: "10.0.0.1", Port: 43666},
			Mappings:       map[string][]int{"10.0.0.1": {43666, 43667}},
			ContainerPorts: map[string]map[int]int{"10.0.0.1": {43667: 8080}},
		})

		env := s.GetEnvironmentVariables()
		assert.Contains(t, env, "SERVER_PORT=43666")
		assert.Contains(t, env, "SERVER_PUBLIC_PORT=43666")
	})

	t.Run("an egg variable cannot replace SERVER_PUBLIC_PORT", func(t *testing.T) {
		s := testServer(environment.Allocations{
			DefaultMapping: &environment.DefaultAllocationMapping{Ip: "10.0.0.1", Port: 43666},
			Mappings:       map[string][]int{"10.0.0.1": {43666}},
			ContainerPorts: map[string]map[int]int{"10.0.0.1": {43666: 3306}},
		})
		s.cfg.EnvVars = environment.Variables{"SERVER_PUBLIC_PORT": "1", "DB_NAME": "app"}

		env := s.GetEnvironmentVariables()
		assert.Contains(t, env, "SERVER_PUBLIC_PORT=43666")
		assert.NotContains(t, env, "SERVER_PUBLIC_PORT=1")
		assert.Contains(t, env, "DB_NAME=app")
	})

	t.Run("a server without an allocation", func(t *testing.T) {
		s := testServer(environment.Allocations{
			DefaultMapping: &environment.DefaultAllocationMapping{Ip: "127.0.0.1", Port: 0},
			Mappings:       map[string][]int{"": {}},
		})

		env := s.GetEnvironmentVariables()
		assert.Contains(t, env, "SERVER_PORT=0")
		assert.Contains(t, env, "SERVER_PUBLIC_PORT=0")
	})
}
