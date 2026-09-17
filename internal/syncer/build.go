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
			SSLForced:             npm.Flag(ssl.forced),
			HSTSEnabled:           npm.Flag(ssl.hsts),
			HSTSSubdomains:        npm.Flag(ssl.hstsSubdomains),
			TrustForwardedProto:   npm.Flag(t.TrustForwardedProto),
			HTTP2Support:          npm.Flag(ssl.http2),
			HTTP3Support:          npm.Flag(t.HTTP3Support),
			BlockExploits:         npm.Flag(t.BlockExploits),
			CachingEnabled:        npm.Flag(t.Caching),
			AllowWebsocketUpgrade: npm.Flag(t.Websockets),
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
			NoIndex:                  npm.Flag(t.NoIndex),
			DisableCrowdsecAppsec:    npm.Flag(!t.CrowdsecAppsec),
			DisableRequestBuffering:  npm.Flag(!t.RequestBuffering),
			DisableResponseBuffering: npm.Flag(!t.ResponseBuffering),
			UpstreamCompression:      npm.Flag(t.UpstreamCompression),
			FancyIndex:               npm.Flag(t.FancyIndex),
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
			PreservePath:      npm.Flag(t.PreservePath),
			CertificateID:     certificate,
			SSLForced:         npm.Flag(ssl.forced),
			HSTSEnabled:       npm.Flag(ssl.hsts),
			HSTSSubdomains:    npm.Flag(ssl.hstsSubdomains),
			HTTP2Support:      npm.Flag(ssl.http2),
			HTTP3Support:      npm.Flag(t.HTTP3Support),
			BlockExploits:     npm.Flag(t.BlockExploits),
			AdvancedConfig:    t.AdvancedConfig,
			Enabled:           npm.Flag(t.Enabled),
			Meta:              meta,
		}

	case npm.KindStream:
		return &npm.Stream{
			IncomingPort:   npm.PortOf(t.IncomingPort),
			ForwardingHost: t.ForwardingHost,
			ForwardingPort: npm.PortOf(t.ForwardingPort),
			TCPForwarding:  npm.Flag(t.TCPForwarding),
			UDPForwarding:  npm.Flag(t.UDPForwarding),
			CertificateID:  certificate,
			ProxyProtocol:  npm.ProxyProtocolLevel(t.ProxyProtocol),
			ProxyTLS:       npm.Flag(t.ProxyTLS),
			AdvancedConfig: t.AdvancedConfig,
			Description:    t.Description,
			Enabled:        npm.Flag(t.Enabled),
			Meta:           meta,
		}

	case npm.KindDead:
		return &npm.DeadHost{
			DomainNames:    npm.NormalizeDomains(t.DomainNames),
			CertificateID:  certificate,
			SSLForced:      npm.Flag(ssl.forced),
			HSTSEnabled:    npm.Flag(ssl.hsts),
			HSTSSubdomains: npm.Flag(ssl.hstsSubdomains),
			HTTP2Support:   npm.Flag(ssl.http2),
			HTTP3Support:   npm.Flag(t.HTTP3Support),
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
	http2          bool
}

// normalizeSSL applies the same cascade the server does before storing a host
// (NPMplus: internalHost.cleanSslHstsData):
//
//	no certificate     -> ssl_forced      = false
//	no certificate     -> http2_support   = false
//	no ssl_forced      -> hsts_enabled    = false
//	no hsts_enabled    -> hsts_subdomains = false
//
// The two upstream releases disagree about the http2 half of that rule even
// though they ship the same cleanSslHstsData: NPM 2.11 stores 0 for a host
// without a certificate, 2.12 stores what it was sent. Applying the rule here
// converges against both - and against whichever way a later release settles,
// because the tool then already asks for what the rule prescribes. HTTP/2
// needs TLS, so a certificate-less host loses nothing by it.
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
		http2:          t.HTTP2Support,
	}
	if certificate.IsZero() {
		s.forced = false
		s.http2 = false
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
