package syncer

import (
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// BuildResource converts a Docker target into the NPM payload of its kind,
// stamping the ownership marker used for orphan detection.
//
// The certificate is passed in because it is resolved against the live
// certificate list first (see certificates.go): "auto" only becomes an id once
// the reconcile loop knows what exists.
func BuildResource(t *docker.Target, certificate npm.CertificateID, instance, prefix string) npm.Resource {
	meta := buildMeta(t, certificate, instance, prefix)
	ssl := normalizeSSL(t, certificate)

	switch t.Kind {
	case npm.KindProxy:
		return &npm.ProxyHost{
			DomainNames:           npm.NormalizeDomains(t.DomainNames),
			ForwardScheme:         t.ForwardScheme,
			ForwardHost:           t.ForwardHost,
			ForwardPort:           t.ForwardPort,
			CertificateID:         certificate,
			SSLForced:             ssl.forced,
			HSTSEnabled:           ssl.hsts,
			HSTSSubdomains:        ssl.hstsSubdomains,
			TrustForwardedProto:   t.TrustForwardedProto,
			HTTP2Support:          t.HTTP2Support,
			HTTP3Support:          t.HTTP3Support,
			BlockExploits:         t.BlockExploits,
			CachingEnabled:        t.Caching,
			AllowWebsocketUpgrade: t.Websockets,
			AccessListIDs:         t.AccessListIDs,
			AccessListNames:       t.AccessListNames,
			AccessListType:        t.AccessListType,
			AdvancedConfig:        t.AdvancedConfig,
			LocationConfig:        t.LocationConfig,
			Enabled:               npm.Flag(t.Enabled),
			Locations:             locations(t),
			Meta:                  meta,

			// The three inverted NPMplus switches: the label is positive
			// ("crowdsec_appsec: true" = active), the API field disables.
			NoIndex:                  t.NoIndex,
			DisableCrowdsecAppsec:    !t.CrowdsecAppsec,
			DisableRequestBuffering:  !t.RequestBuffering,
			DisableResponseBuffering: !t.ResponseBuffering,
			UpstreamCompression:      t.UpstreamCompression,
			FancyIndex:               t.FancyIndex,
			XFrameOptions:            t.XFrameOptions,
			AuthRequest:              t.AuthRequest,
			AuthRequestUpstream:      t.AuthRequestUpstream,
		}

	case npm.KindRedirect:
		return &npm.RedirectionHost{
			DomainNames:       npm.NormalizeDomains(t.DomainNames),
			ForwardScheme:     t.ForwardScheme,
			ForwardDomainName: t.ForwardDomainName,
			ForwardHTTPCode:   t.ForwardHTTPCode,
			PreservePath:      t.PreservePath,
			CertificateID:     certificate,
			SSLForced:         ssl.forced,
			HSTSEnabled:       ssl.hsts,
			HSTSSubdomains:    ssl.hstsSubdomains,
			HTTP2Support:      t.HTTP2Support,
			HTTP3Support:      t.HTTP3Support,
			BlockExploits:     t.BlockExploits,
			AdvancedConfig:    t.AdvancedConfig,
			Enabled:           npm.Flag(t.Enabled),
			Meta:              meta,
		}

	case npm.KindStream:
		return &npm.Stream{
			IncomingPort:   npm.PortOf(t.IncomingPort),
			ForwardingHost: t.ForwardingHost,
			ForwardingPort: npm.PortOf(t.ForwardingPort),
			TCPForwarding:  t.TCPForwarding,
			UDPForwarding:  t.UDPForwarding,
			CertificateID:  certificate,
			ProxyProtocol:  npm.ProxyProtocolLevel(t.ProxyProtocol),
			ProxyTLS:       t.ProxyTLS,
			AdvancedConfig: t.AdvancedConfig,
			Description:    t.Description,
			Enabled:        npm.Flag(t.Enabled),
			Meta:           meta,
		}

	case npm.KindDead:
		return &npm.DeadHost{
			DomainNames:    npm.NormalizeDomains(t.DomainNames),
			CertificateID:  certificate,
			SSLForced:      ssl.forced,
			HSTSEnabled:    ssl.hsts,
			HSTSSubdomains: ssl.hstsSubdomains,
			HTTP2Support:   t.HTTP2Support,
			HTTP3Support:   t.HTTP3Support,
			AdvancedConfig: t.AdvancedConfig,
			Enabled:        npm.Flag(t.Enabled),
			Meta:           meta,
		}

	default:
		return nil
	}
}

// sslSettings is the normalised TLS state of a target.
type sslSettings struct {
	forced         bool
	hsts           bool
	hstsSubdomains bool
}

// normalizeSSL applies the same cascade the server does before storing a host
// (NPMplus: internalHost.cleanSslHstsData):
//
//	no certificate     -> ssl_forced      = false
//	no ssl_forced      -> hsts_enabled    = false
//	no hsts_enabled    -> hsts_subdomains = false
//
// Without this the fingerprint is taken over the raw label values while the
// API stores the cleaned ones, the two never match, and every Docker event and
// every resync issues another pointless update.
//
// An unset ssl_forced ("auto") means "on as soon as a certificate is
// attached", which is the whole point of the automatic certificate selection.
func normalizeSSL(t *docker.Target, certificate npm.CertificateID) sslSettings {
	forced := !certificate.IsZero()
	if t.SSLForced != nil {
		forced = *t.SSLForced
	}
	s := sslSettings{
		forced:         forced,
		hsts:           t.HSTSEnabled,
		hstsSubdomains: t.HSTSSubdomains,
	}
	if certificate.IsZero() {
		s.forced = false
	}
	if !s.forced {
		s.hsts = false
	}
	if !s.hsts {
		s.hstsSubdomains = false
	}
	return s
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
//
// NPMplus takes the ACME account from its own ACME_EMAIL and ignores
// letsencrypt_email/letsencrypt_agree, so they are only written when the
// labels actually set them.
func buildMeta(t *docker.Target, certificate npm.CertificateID, instance, prefix string) npm.Meta {
	meta := npm.Meta{
		npm.MetaManagedBy: npm.ManagedByValue,
		npm.MetaContainer: t.ContainerName,
		npm.MetaIndex:     t.Index,
	}
	if t.ContainerID != "" {
		meta[npm.MetaContainerID] = t.ContainerID
	}
	if instance != "" {
		meta[npm.MetaInstance] = instance
	}
	if prefix != "" {
		meta[npm.MetaPrefix] = prefix
	}
	if certificate.New || t.LetsEncryptEmail != "" {
		if t.LetsEncryptEmail != "" {
			meta["letsencrypt_email"] = t.LetsEncryptEmail
			meta["letsencrypt_agree"] = t.LetsEncryptAgree
		}
		meta["dns_challenge"] = t.DNSChallenge
		if t.DNSChallenge {
			meta["dns_provider"] = t.DNSProvider
			meta["dns_provider_credentials"] = t.DNSCredentials
			meta["propagation_seconds"] = t.PropagationSeconds
		}
	}
	return meta
}
