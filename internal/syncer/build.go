package syncer

import (
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// BuildResource converts a Docker target into the NPM payload of its kind,
// stamping the ownership marker used for orphan detection.
func BuildResource(t *docker.Target) npm.Resource {
	meta := buildMeta(t)

	switch t.Kind {
	case npm.KindProxy:
		return &npm.ProxyHost{
			DomainNames:           npm.NormalizeDomains(t.DomainNames),
			ForwardScheme:         t.ForwardScheme,
			ForwardHost:           t.ForwardHost,
			ForwardPort:           t.ForwardPort,
			CertificateID:         t.CertificateID,
			SSLForced:             t.SSLForced,
			HSTSEnabled:           t.HSTSEnabled,
			HSTSSubdomains:        t.HSTSSubdomains,
			HTTP2Support:          t.HTTP2Support,
			BlockExploits:         t.BlockExploits,
			CachingEnabled:        t.Caching,
			AllowWebsocketUpgrade: t.Websockets,
			AccessListID:          t.AccessListID,
			AdvancedConfig:        t.AdvancedConfig,
			Enabled:               npm.Flag(t.Enabled),
			Locations:             locations(t),
			Meta:                  meta,
		}

	case npm.KindRedirect:
		return &npm.RedirectionHost{
			DomainNames:       npm.NormalizeDomains(t.DomainNames),
			ForwardScheme:     t.ForwardScheme,
			ForwardDomainName: t.ForwardDomainName,
			ForwardHTTPCode:   t.ForwardHTTPCode,
			PreservePath:      t.PreservePath,
			CertificateID:     t.CertificateID,
			SSLForced:         t.SSLForced,
			HSTSEnabled:       t.HSTSEnabled,
			HSTSSubdomains:    t.HSTSSubdomains,
			HTTP2Support:      t.HTTP2Support,
			BlockExploits:     t.BlockExploits,
			AdvancedConfig:    t.AdvancedConfig,
			Enabled:           npm.Flag(t.Enabled),
			Meta:              meta,
		}

	case npm.KindStream:
		return &npm.Stream{
			IncomingPort:   t.IncomingPort,
			ForwardingHost: t.ForwardingHost,
			ForwardingPort: t.ForwardingPort,
			TCPForwarding:  t.TCPForwarding,
			UDPForwarding:  t.UDPForwarding,
			CertificateID:  t.CertificateID,
			Enabled:        npm.Flag(t.Enabled),
			Meta:           meta,
		}

	case npm.KindDead:
		return &npm.DeadHost{
			DomainNames:    npm.NormalizeDomains(t.DomainNames),
			CertificateID:  t.CertificateID,
			SSLForced:      t.SSLForced,
			HSTSEnabled:    t.HSTSEnabled,
			HSTSSubdomains: t.HSTSSubdomains,
			HTTP2Support:   t.HTTP2Support,
			AdvancedConfig: t.AdvancedConfig,
			Enabled:        npm.Flag(t.Enabled),
			Meta:           meta,
		}

	default:
		return nil
	}
}

// locations returns the custom location blocks, never nil: NPM expects an
// array and a nil slice would marshal to null.
func locations(t *docker.Target) []npm.Location {
	if len(t.Locations) == 0 {
		return []npm.Location{}
	}
	return t.Locations
}

// buildMeta stamps ownership and, when a certificate is requested, the
// Let's Encrypt settings NPM expects in the meta object.
func buildMeta(t *docker.Target) npm.Meta {
	meta := npm.Meta{
		npm.MetaManagedBy: npm.ManagedByValue,
		npm.MetaContainer: t.ContainerName,
		npm.MetaIndex:     t.Index,
	}
	if t.CertificateID.New || t.LetsEncryptEmail != "" {
		meta["letsencrypt_agree"] = t.LetsEncryptAgree
		meta["letsencrypt_email"] = t.LetsEncryptEmail
		meta["dns_challenge"] = t.DNSChallenge
		if t.DNSChallenge {
			meta["dns_provider"] = t.DNSProvider
			meta["dns_provider_credentials"] = t.DNSCredentials
			meta["propagation_seconds"] = t.PropagationSeconds
		}
	}
	return meta
}
