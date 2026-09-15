package docker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// buildTarget turns one field set into a validated Target.
func buildTarget(c Container, opts ParseOptions, key entryKey, f fieldSet) (*Target, error) {
	t := &Target{
		Kind:          key.kind,
		Index:         key.index,
		ContainerID:   c.ID,
		ContainerName: c.Name,
	}

	var err error
	if t.Enabled, err = f.boolean(fieldEnabled, true); err != nil {
		return nil, err
	}
	if f.has(fieldCertificateID) {
		if t.CertificateID, err = parseCertificateID(f.string(fieldCertificateID)); err != nil {
			return nil, fmt.Errorf("%s: %w", f.name(fieldCertificateID), err)
		}
	}
	if t.SSLForced, err = f.boolean(fieldSSLForced, false); err != nil {
		return nil, err
	}
	if t.HTTP2Support, err = f.boolean(fieldHTTP2, false); err != nil {
		return nil, err
	}
	if t.HSTSEnabled, err = f.boolean(fieldHSTS, false); err != nil {
		return nil, err
	}
	if t.HSTSSubdomains, err = f.boolean(fieldHSTSSubdomains, false); err != nil {
		return nil, err
	}
	if t.BlockExploits, err = f.boolean(fieldBlockExploits, true); err != nil {
		return nil, err
	}
	t.AdvancedConfig = f.string(fieldAdvancedConfig)

	t.LetsEncryptEmail = f.string(fieldLEEmail)
	if t.LetsEncryptAgree, err = f.boolean(fieldLEAgree, false); err != nil {
		return nil, err
	}
	if t.DNSChallenge, err = f.boolean(fieldLEDNSChallenge, false); err != nil {
		return nil, err
	}
	t.DNSProvider = f.string(fieldLEDNSProvider)
	t.DNSCredentials = f.string(fieldLEDNSCreds)
	if t.PropagationSeconds, err = f.integer(fieldLEPropagation, 0); err != nil {
		return nil, err
	}

	switch key.kind {
	case npm.KindProxy:
		err = buildProxy(t, c, opts, f)
	case npm.KindRedirect:
		err = buildRedirect(t, f)
	case npm.KindStream:
		err = buildStream(t, c, opts, f)
	case npm.KindDead:
		err = buildDead(t, f)
	default:
		err = fmt.Errorf("unknown resource type %q", key.kind)
	}
	if err != nil {
		return nil, err
	}

	if err := validateCertificate(t, f); err != nil {
		return nil, err
	}
	return t, nil
}

func buildProxy(t *Target, c Container, opts ParseOptions, f fieldSet) error {
	domains, err := f.domains(fieldHost)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return fmt.Errorf("%s is required for a proxy host", f.name(fieldHost))
	}
	t.DomainNames = domains

	port, err := f.port(fieldPort)
	if err != nil {
		return err
	}
	if port == 0 {
		return fmt.Errorf("%s is required for a proxy host", f.name(fieldPort))
	}
	t.ForwardPort = port

	scheme := strings.ToLower(f.stringDefault(fieldScheme, "http"))
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%s must be http or https, got %q", f.name(fieldScheme), scheme)
	}
	t.ForwardScheme = scheme

	if t.ForwardHost, err = resolveForwardHost(c, opts, f); err != nil {
		return err
	}
	if t.Websockets, err = f.boolean(fieldWebsockets, true); err != nil {
		return err
	}
	if t.Caching, err = f.boolean(fieldCaching, false); err != nil {
		return err
	}
	if t.AccessListID, err = f.integer(fieldAccessListID, 0); err != nil {
		return err
	}
	if t.Locations, err = parseLocations(t, f); err != nil {
		return err
	}
	return nil
}

// parseLocations reads custom location blocks written as
// "<prefix>.<index>.proxy.location.<n>.<field>". Everything except the path
// defaults to the proxy host's own upstream.
func parseLocations(t *Target, f fieldSet) ([]npm.Location, error) {
	groups := make(map[int]map[string]string)
	for field := range f.values {
		if !strings.HasPrefix(field, fieldLocation+".") {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(field, fieldLocation+"."), ".", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("%s: expected %s.<index>.<field>", f.name(field), fieldLocation)
		}
		n, err := strconv.Atoi(parts[0])
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%s: %q is not a valid location index", f.name(field), parts[0])
		}
		if groups[n] == nil {
			groups[n] = make(map[string]string)
		}
		groups[n][parts[1]] = f.values[field]
	}
	if len(groups) == 0 {
		return nil, nil
	}

	indexes := make([]int, 0, len(groups))
	for n := range groups {
		indexes = append(indexes, n)
	}
	sort.Ints(indexes)

	locations := make([]npm.Location, 0, len(indexes))
	for _, n := range indexes {
		values := groups[n]
		label := func(field string) string {
			return fmt.Sprintf("%s%d.%s.%s.%d.%s", f.prefix, f.index, f.kind, fieldLocation, n, field)
		}

		path := strings.TrimSpace(values["path"])
		if path == "" {
			return nil, fmt.Errorf("%s is required for a location block", label("path"))
		}
		if !strings.HasPrefix(path, "/") {
			return nil, fmt.Errorf("%s must start with a slash, got %q", label("path"), path)
		}

		scheme := strings.ToLower(strings.TrimSpace(values["forward_scheme"]))
		if scheme == "" {
			scheme = t.ForwardScheme
		}
		if scheme != "http" && scheme != "https" {
			return nil, fmt.Errorf("%s must be http or https, got %q", label("forward_scheme"), scheme)
		}

		host := strings.TrimSpace(values["forward_host"])
		if host == "" {
			host = t.ForwardHost
		}

		port := t.ForwardPort
		if raw := strings.TrimSpace(values["forward_port"]); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not a number", label("forward_port"), raw)
			}
			if parsed < 1 || parsed > 65535 {
				return nil, fmt.Errorf("%s: %d is out of range (1-65535)", label("forward_port"), parsed)
			}
			port = parsed
		}

		locations = append(locations, npm.Location{
			Path:           path,
			ForwardScheme:  scheme,
			ForwardHost:    host,
			ForwardPort:    port,
			AdvancedConfig: values["advanced_config"],
		})
	}
	return locations, nil
}

func buildRedirect(t *Target, f fieldSet) error {
	domains, err := f.domains(fieldHost)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return fmt.Errorf("%s is required for a redirection host", f.name(fieldHost))
	}
	t.DomainNames = domains

	forward := strings.ToLower(strings.TrimSpace(f.string(fieldForwardDomain)))
	if forward == "" {
		return fmt.Errorf("%s is required for a redirection host", f.name(fieldForwardDomain))
	}
	if strings.Contains(forward, "://") {
		return fmt.Errorf("%s must be a domain name without a scheme, got %q (use %s for the scheme)",
			f.name(fieldForwardDomain), forward, f.name(fieldScheme))
	}
	if strings.ContainsAny(forward, " \t") {
		return fmt.Errorf("%s: %q is not a valid domain name", f.name(fieldForwardDomain), forward)
	}
	t.ForwardDomainName = forward

	scheme := strings.ToLower(f.stringDefault(fieldScheme, "auto"))
	switch scheme {
	case "auto", "http", "https":
		t.ForwardScheme = scheme
	default:
		return fmt.Errorf("%s must be auto, http or https, got %q", f.name(fieldScheme), scheme)
	}

	code, err := f.integer(fieldHTTPCode, 301)
	if err != nil {
		return err
	}
	if code < 300 || code > 308 {
		return fmt.Errorf("%s: %d is not a redirect status code (300-308)", f.name(fieldHTTPCode), code)
	}
	t.ForwardHTTPCode = code

	if t.PreservePath, err = f.boolean(fieldPreservePath, true); err != nil {
		return err
	}
	return nil
}

func buildStream(t *Target, c Container, opts ParseOptions, f fieldSet) error {
	incoming, err := f.port(fieldIncomingPort)
	if err != nil {
		return err
	}
	if incoming == 0 {
		return fmt.Errorf("%s is required for a stream", f.name(fieldIncomingPort))
	}
	t.IncomingPort = incoming

	forward, err := f.port(fieldForwardPort)
	if err != nil {
		return err
	}
	if forward == 0 {
		// Streams usually forward to the same port they listen on.
		forward = incoming
	}
	t.ForwardingPort = forward

	if t.ForwardingHost, err = resolveForwardHost(c, opts, f); err != nil {
		return err
	}

	tcp, udp := true, false
	if protocol := strings.ToLower(f.string("protocol")); protocol != "" {
		switch protocol {
		case "tcp":
			tcp, udp = true, false
		case "udp":
			tcp, udp = false, true
		case "both", "tcp+udp", "tcp/udp", "tcp,udp":
			tcp, udp = true, true
		default:
			return fmt.Errorf("%s must be tcp, udp or both, got %q", f.name("protocol"), protocol)
		}
	}
	if tcp, err = f.boolean(fieldTCP, tcp); err != nil {
		return err
	}
	if udp, err = f.boolean(fieldUDP, udp); err != nil {
		return err
	}
	if !tcp && !udp {
		return fmt.Errorf("%s and %s cannot both be false", f.name(fieldTCP), f.name(fieldUDP))
	}
	t.TCPForwarding, t.UDPForwarding = tcp, udp
	return nil
}

func buildDead(t *Target, f fieldSet) error {
	domains, err := f.domains(fieldHost)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return fmt.Errorf("%s is required for a 404 host", f.name(fieldHost))
	}
	t.DomainNames = domains
	return nil
}

// resolveForwardHost applies the explicit label, otherwise the container IP,
// otherwise the container name.
func resolveForwardHost(c Container, opts ParseOptions, f fieldSet) (string, error) {
	if explicit := f.string(fieldForwardHost); explicit != "" {
		return explicit, nil
	}

	resolveIP, err := f.boolean(fieldResolveIP, opts.ResolveIP)
	if err != nil {
		return "", err
	}
	host := c.ResolveHost(ParseOptions{ResolveIP: resolveIP, PreferNetworks: opts.PreferNetworks})
	if host == "" {
		return "", fmt.Errorf("cannot determine the upstream host: set %s", f.name(fieldForwardHost))
	}
	return host, nil
}

// validateCertificate rejects combinations NPM would reject (or silently
// break) anyway.
func validateCertificate(t *Target, f fieldSet) error {
	if t.CertificateID.New {
		if t.LetsEncryptEmail == "" {
			return fmt.Errorf("%s is required when requesting a new certificate", f.name(fieldLEEmail))
		}
		if !t.LetsEncryptAgree {
			return fmt.Errorf("%s must be true when requesting a new certificate", f.name(fieldLEAgree))
		}
	}
	if t.SSLForced && t.CertificateID.IsZero() {
		return fmt.Errorf("%s requires %s", f.name(fieldSSLForced), f.name(fieldCertificateID))
	}
	if t.DNSChallenge && t.DNSProvider == "" {
		return fmt.Errorf("%s is required for a DNS-01 challenge", f.name(fieldLEDNSProvider))
	}
	return nil
}

func parseCertificateID(raw string) (npm.CertificateID, error) {
	switch strings.ToLower(raw) {
	case npm.CertificateNew, "letsencrypt", "le":
		return npm.NewCertificate(), nil
	case "", "0", "none", "false":
		return npm.CertificateID{}, nil
	}
	id, err := strconv.Atoi(raw)
	if err != nil {
		return npm.CertificateID{}, fmt.Errorf("%q is neither a certificate id nor %q", raw, npm.CertificateNew)
	}
	if id < 0 {
		return npm.CertificateID{}, fmt.Errorf("certificate id %d must not be negative", id)
	}
	return npm.CertificateRef(id), nil
}
