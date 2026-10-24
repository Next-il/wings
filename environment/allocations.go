package environment

import (
	"fmt"
	"strconv"

	"github.com/docker/go-connections/nat"

	"github.com/pelican/wings/config"
)

// DefaultAllocationMapping defines the IP and port mapping for a server
type DefaultAllocationMapping struct {
	Ip   string `json:"ip"`
	Port int    `json:"port"`
}

// Defines the allocations available for a given server. When using the Docker environment
// driver these correspond to mappings for the container that allow external connections.
type Allocations struct {
	// ForceOutgoingIP causes a dedicated bridge network to be created for the
	// server with a special option, causing Docker to SNAT outgoing traffic to
	// the DefaultMapping's IP. This is important to servers which rely on external
	// services that check the IP of the server (Source Engine servers, for example).
	ForceOutgoingIP bool `json:"force_outgoing_ip"`

	// Defines the default allocation that should be used for this server. This is
	// what will be used for {SERVER_IP} and {SERVER_PORT} when modifying configuration
	// files or the startup arguments for a server. Its port is the public one: when
	// ContainerPorts maps it, {SERVER_PORT} is the container port and the public port is
	// {SERVER_PUBLIC_PORT}.
	DefaultMapping *DefaultAllocationMapping `json:"default"`

	// Mappings contains all the ports that should be assigned to a given server
	// attached to the IP they correspond to.
	Mappings map[string][]int `json:"mappings"`

	// ContainerPorts is a NextHost addition: for an allocation in Mappings, the port
	// inside the container its traffic goes to, keyed by IP and then by the allocation's
	// (public) port. An allocation without an entry keeps the same port number inside the
	// container, so a remote that does not send this field gets the upstream behaviour,
	// and upstream Wings ignores the field.
	//
	//	"container_ports": {"10.0.0.1": {"43666": 3306}}
	ContainerPorts map[string]map[int]int `json:"container_ports,omitempty"`
}

// ContainerPort returns the port inside the container that the allocation at the given
// IP and port is bound to: its ContainerPorts entry, or the same port when it has no
// valid one.
func (a *Allocations) ContainerPort(ip string, port int) int {
	if p, ok := a.ContainerPorts[ip][port]; ok && p >= 1 && p <= 65535 {
		return p
	}
	return port
}

// DefaultContainerPort returns the port inside the container of the default allocation,
// which is what the server listens on and what SERVER_PORT is set to. Zero when the
// server has no allocation.
func (a *Allocations) DefaultContainerPort() int {
	if a.DefaultMapping == nil {
		return 0
	}
	return a.ContainerPort(a.DefaultMapping.Ip, a.DefaultMapping.Port)
}

// DefaultPublicPort returns the port of the default allocation as clients connect to it,
// which is what SERVER_PUBLIC_PORT is set to. Zero when the server has no allocation.
func (a *Allocations) DefaultPublicPort() int {
	if a.DefaultMapping == nil {
		return 0
	}
	return a.DefaultMapping.Port
}

// Converts the server allocation mappings into a format that can be understood by Docker. While
// we do strive to support multiple environments, using Docker's standardized format for the
// bindings certainly makes life a little easier for managing things.
//
// You'll want to use DockerBindings() if you need to re-map 127.0.0.1 to the Docker interface.
func (a *Allocations) Bindings() nat.PortMap {
	out := nat.PortMap{}

	for ip, ports := range a.Mappings {
		for _, port := range ports {
			// Skip over invalid ports.
			if port < 1 || port > 65535 {
				continue
			}

			binding := nat.PortBinding{
				HostIP:   ip,
				HostPort: strconv.Itoa(port),
			}

			// The host side is always the allocation itself; the container side is where
			// NextHost asked for its traffic to go, the same port when it did not say.
			containerPort := a.ContainerPort(ip, port)
			tcp := nat.Port(fmt.Sprintf("%d/tcp", containerPort))
			udp := nat.Port(fmt.Sprintf("%d/udp", containerPort))

			out[tcp] = append(out[tcp], binding)
			out[udp] = append(out[udp], binding)
		}
	}

	return out
}

// Returns the bindings for the server in a way that is supported correctly by Docker. This replaces
// any reference to 127.0.0.1 with the IP of the pelican0 network interface which will allow the
// server to operate on a local address while still being accessible by other containers.
func (a *Allocations) DockerBindings() nat.PortMap {
	iface := config.Get().Docker.Network.Interface

	out := a.Bindings()
	// Loop over all the bindings for this container, and convert any that reference 127.0.0.1
	// to use the pelican0 network interface IP, as that is the true local for what people are
	// trying to do when creating servers.
	for p, binds := range out {
		for i, alloc := range binds {
			if alloc.HostIP != "127.0.0.1" {
				continue
			}

			// If using ISPN just delete the local allocation from the server.
			if config.Get().Docker.Network.ISPN {
				out[p] = append(out[p][:i], out[p][i+1:]...)
			} else {
				out[p][i] = nat.PortBinding{
					HostIP:   iface,
					HostPort: alloc.HostPort,
				}
			}
		}
	}

	return out
}

// Converts the server allocation mappings into a PortSet that can be understood
// by Docker. This formatting is slightly different than "Bindings" as it should
// return an empty struct rather than a binding.
//
// To accomplish this, we'll just get the values from "DockerBindings" and then set them
// to empty structs. Because why not.
func (a *Allocations) Exposed() nat.PortSet {
	out := nat.PortSet{}

	for port := range a.DockerBindings() {
		out[port] = struct{}{}
	}

	return out
}
