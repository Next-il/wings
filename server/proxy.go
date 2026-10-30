package server

import (
	"github.com/apex/log"

	"github.com/pelican/wings/config"
	"github.com/pelican/wings/environment"
	"github.com/pelican/wings/proxy"
)

// SyncProxyRoutes is NextHost's: it makes the node's proxy route the server's domains while the
// server is starting or running, and nothing otherwise. It writes the server's routes file for
// Traefik (proxy.WriteServerRoutes), or removes it when the server is stopping or offline, has no
// domains, or the proxy is off. Called on every state change and every sync with the remote, so a
// domain change applies while the server runs. Calls run one at a time and each reads the state as
// it is then, so the last one always wins with what is true.
func (s *Server) SyncProxyRoutes() {
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()

	var domains []environment.Domain
	if config.Get().Proxy.Enabled && s.Environment != nil && s.IsRunning() {
		valid, dropped := environment.ValidDomains(s.Config().Domains)
		for _, d := range dropped {
			s.Log().WithFields(log.Fields{
				"domain": d.Domain.Domain,
				"port":   d.Domain.Port,
				"path":   d.Domain.Path,
				"reason": d.Reason,
			}).Warn("not routing an invalid domain")
		}
		domains = valid
	}

	if err := proxy.WriteServerRoutes(s.ID(), domains); err != nil {
		s.Log().WithField("error", err).Warn("failed to update the server's routes on the domain proxy")
	}
}
