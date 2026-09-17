package docker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/certs"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/fields"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// buildTarget turns one field set into a validated Target.
func buildTarget(c Container, opts ParseOptions, key entryKey, f fieldSet) (*Target, error) {
	t := &Target{
		Kind:          key.kind,
		Index:         key.index,
		ContainerID:   c.ID,
		ContainerName: c.Name,
		Running:       c.Running(),
		ExplicitPlus:  explicitPlus(key.kind, f),
	}

	var err error
	if t.Enabled, err = f.boolean(fields.Enabled); err != nil {
		return nil, err
	}
	if t.Certificate, err = certificateSpec(f); err != nil {
		return nil, err
	}
	if t.SSLForced, err = f.booleanAuto(fields.SSLForced); err != nil {
		return nil, err
	}
	if t.HTTP2Support, err = f.boolean(fields.HTTP2); err != nil {
		return nil, err
	}
	if t.HTTP3Support, err = f.boolean(fields.HTTP3); err != nil {
		return nil, err
	}
	if t.HSTSEnabled, err = f.boolean(fields.HSTS); err != nil {
		return nil, err
	}
	if t.HSTSSubdomains, err = f.boolean(fields.HSTSSubdomains); err != nil {
		return nil, err
	}
	t.AdvancedConfig = f.string(fields.AdvancedConfig)

	t.LetsEncryptEmail = f.string(fields.LEEmail)
	if t.LetsEncryptAgree, err = f.boolean(fields.LEAgree); err != nil {
		return nil, err
	}
	if t.DNSChallenge, err = f.boolean(fields.LEDNSChallenge); err != nil {
		return nil, err
	}
	t.DNSProvider = f.string(fields.LEDNSProvider)
	t.DNSCredentials = f.string(fields.LEDNSCreds)
	if t.PropagationSeconds, err = f.integer(fields.LEPropagation); err != nil {
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

// explicitPlus lists the NPMplus-only fields the labels set explicitly, in
// canonical spelling. Defaults do not count: a warning has to mean the user
// asked for something the server cannot do.
func explicitPlus(kind npm.Kind, f fieldSet) []string {
	var out []string
	for name := range f.values {
		definition, ok := fields.Lookup(kind, name)
		if ok && definition.Plus {
			out = append(out, definition.Name)
		}
	}
	sort.Strings(out)
	return out
}

func buildProxy(t *Target, c Container, opts ParseOptions, f fieldSet) error {
	domains, err := f.domains(fields.Domains)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return missingDomains(f)
	}
	t.DomainNames = domains

	if t.ForwardHost, err = resolveForwardHost(c, opts, f); err != nil {
		return err
	}
	if t.ForwardPort, err = resolvePort(c, opts, f, fields.ForwardPort, 0); err != nil {
		return err
	}
	if t.ForwardScheme, err = f.enum(fields.ForwardScheme); err != nil {
		return err
	}
	if t.ForwardScheme == "" {
		t.ForwardScheme = "http"
	}

	if t.Websockets, err = f.boolean(fields.Websockets); err != nil {
		return err
	}
	if t.Caching, err = f.boolean(fields.Caching); err != nil {
		return err
	}
	if t.BlockExploits, err = f.boolean(fields.BlockExploits); err != nil {
		return err
	}
	if t.TrustForwardedProto, err = f.boolean(fields.TrustForwardedProto); err != nil {
		return err
	}
	if t.NoIndex, err = f.boolean(fields.NoIndex); err != nil {
		return err
	}
	if t.CrowdsecAppsec, err = f.boolean(fields.CrowdsecAppsec); err != nil {
		return err
	}
	if t.RequestBuffering, err = f.boolean(fields.RequestBuffering); err != nil {
		return err
	}
	if t.ResponseBuffering, err = f.boolean(fields.ResponseBuffering); err != nil {
		return err
	}
	if t.UpstreamCompression, err = f.boolean(fields.UpstreamCompression); err != nil {
		return err
	}
	if t.FancyIndex, err = f.boolean(fields.FancyIndex); err != nil {
		return err
	}
	if t.XFrameOptions, err = xFrameOptions(f); err != nil {
		return err
	}
	if t.AuthRequest, err = f.enum(fields.AuthRequest); err != nil {
		return err
	}
	t.AuthRequestUpstream = f.string(fields.AuthRequestUpstream)
	t.LocationConfig = f.string(fields.LocationConfig)

	if t.AccessListIDs, t.AccessListNames, t.AccessListType, err = parseAccessList(f, npm.AccessListPublic); err != nil {
		return err
	}
	if t.Locations, err = parseLocations(t, f); err != nil {
		return err
	}
	return nil
}

// missingDomains explains the v1.0.0-beta.3 breaking change instead of
// silently creating a host for the wrong name: `host` used to be the domain
// and is now the upstream, as it is in Redth/npm-docker-sync.
func missingDomains(f fieldSet) error {
	if label, ok := f.labels[fields.Normalize(fields.ForwardHost)]; ok && isDomainish(f.raw(fields.ForwardHost)) {
		return fmt.Errorf("%s is required: since v1.0.0-beta.3 %s is the upstream target, "+
			"not the domain - move %q to %s",
			f.name(fields.Domains), label, f.raw(fields.ForwardHost), f.name(fields.Domains))
	}
	return fmt.Errorf("%s is required for a %s", f.name(fields.Domains), f.kind.Label())
}

// isDomainish reports whether a value looks like a host name rather than an
// address a proxy would forward to.
func isDomainish(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || !strings.Contains(value, ".") {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && r != '.' {
			return true // not a bare IPv4 address
		}
	}
	return false
}

// certificateSpec parses the `certificate` label (or its default).
func certificateSpec(f fieldSet) (certs.Spec, error) {
	spec, err := certs.ParseSpec(f.string(fields.Certificate))
	if err != nil {
		return certs.Spec{}, fmt.Errorf("%s: %w", f.source(fields.Certificate), err)
	}
	return spec, nil
}

// xFrameOptions returns the NPMplus spelling of the header setting; an unset
// value leaves whatever NPMplus defaults to.
func xFrameOptions(f fieldSet) (string, error) {
	value, err := f.enum(fields.XFrameOptions)
	if err != nil {
		return "", err
	}
	switch value {
	case "deny":
		return "DENY", nil
	case "sameorigin":
		return "SAMEORIGIN", nil
	default:
		return value, nil
	}
}

// parseAccessList reads the access list labels of a host or a location.
//
// Values may be numeric ids or access list names; names are resolved against
// the NPM API during reconciliation. The type defaults to "custom" as soon as
// a list is given, and to defaultType otherwise ("public" for a host,
// "global" - inherit from the host - for a location).
func parseAccessList(f fieldSet, defaultType string) (ids []int, names []string, listType string, err error) {
	for _, raw := range splitList(f.string(fields.AccessList)) {
		if id, convErr := strconv.Atoi(raw); convErr == nil {
			if id < 1 {
				return nil, nil, "", fmt.Errorf("%s: access list id %d must be positive", f.source(fields.AccessList), id)
			}
			ids = append(ids, id)
			continue
		}
		names = append(names, raw)
	}

	listType = strings.ToLower(f.string(fields.AccessListType))
	switch listType {
	case "":
		if len(ids)+len(names) > 0 {
			listType = npm.AccessListCustom
		} else {
			listType = defaultType
		}
	case npm.AccessListPublic:
		ids, names = nil, nil
	case npm.AccessListCustom:
		if len(ids)+len(names) == 0 {
			return nil, nil, "", fmt.Errorf("%s=%s requires %s",
				f.name(fields.AccessListType), npm.AccessListCustom, f.name(fields.AccessList))
		}
	case npm.AccessListGlobal:
		if defaultType != npm.AccessListGlobal {
			return nil, nil, "", fmt.Errorf("%s: %q is only valid for a location block",
				f.name(fields.AccessListType), npm.AccessListGlobal)
		}
		ids, names = nil, nil
	default:
		return nil, nil, "", fmt.Errorf("%s: unknown access list type %q", f.name(fields.AccessListType), listType)
	}
	return ids, names, listType, nil
}

// parseLocations reads custom location blocks written as
// "<prefix>.<kind>[.<index>].location.<n>.<field>". Everything except the path
// defaults to the proxy host's own settings.
func parseLocations(t *Target, f fieldSet) ([]npm.Location, error) {
	groups := make(map[int]map[string]string)
	for field := range f.values {
		// "location_config" normalises into this namespace but is a field of
		// the host, not a location block.
		if !strings.HasPrefix(field, fields.Location+".") || !looksLikeLocationBlock(field) {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(field, fields.Location+"."), ".", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("%s: expected %s.<index>.<field>", f.name(field), fields.Location)
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
		location, err := buildLocation(t, f, n, groups[n])
		if err != nil {
			return nil, err
		}
		locations = append(locations, location)
	}
	return locations, nil
}

// buildLocation assembles one location block. Unset switches are inherited
// from the proxy host, which is what the NPMplus UI does as well.
func buildLocation(t *Target, f fieldSet, n int, values map[string]string) (npm.Location, error) {
	label := func(field string) string {
		return fmt.Sprintf("%s.%s.%s.%d.%s", f.prefix, f.kind, fields.Location, n, field)
	}
	sub := fieldSet{
		prefix: f.prefix, kind: f.kind, index: f.index,
		values: values,
		labels: locationLabels(f, n, values),
	}

	path := strings.TrimSpace(values["path"])
	if path == "" {
		return npm.Location{}, fmt.Errorf("%s is required for a location block", label("path"))
	}
	locationType, err := locationModifier(values["type"])
	if err != nil {
		return npm.Location{}, fmt.Errorf("%s: %w", label("type"), err)
	}
	if locationType != npm.LocationNamed && !strings.HasPrefix(path, "/") {
		return npm.Location{}, fmt.Errorf("%s must start with a slash, got %q", label("path"), path)
	}

	scheme := strings.ToLower(strings.TrimSpace(values[fields.Normalize(fields.ForwardScheme)]))
	if scheme == "" {
		scheme = t.ForwardScheme
	}
	if _, err := (fields.Field{Enum: fields.SchemeValues}).ValidateEnum(scheme); err != nil {
		return npm.Location{}, fmt.Errorf("%s: %w", label(fields.ForwardScheme), err)
	}

	host := strings.TrimSpace(values[fields.Normalize(fields.ForwardHost)])
	if host == "" {
		host = t.ForwardHost
	}

	port := t.ForwardPort
	if raw := strings.TrimSpace(values[fields.Normalize(fields.ForwardPort)]); raw != "" {
		parsed, convErr := strconv.Atoi(raw)
		if convErr != nil {
			return npm.Location{}, fmt.Errorf("%s: %q is not a number", label(fields.ForwardPort), raw)
		}
		if parsed < 1 || parsed > 65535 {
			return npm.Location{}, fmt.Errorf("%s: %d is out of range (1-65535)", label(fields.ForwardPort), parsed)
		}
		port = parsed
	}

	ids, names, listType, err := parseAccessList(sub, npm.AccessListGlobal)
	if err != nil {
		return npm.Location{}, err
	}

	// Every switch is inherited from the proxy host unless the location
	// overrides it. The three inverted NPMplus fields are negated on the way
	// into the payload, so the labels stay positive.
	enabled := true
	noIndex, crowdsec := t.NoIndex, t.CrowdsecAppsec
	requestBuffering, responseBuffering := t.RequestBuffering, t.ResponseBuffering
	upstreamCompression, fancyIndex := t.UpstreamCompression, t.FancyIndex
	xFrame, authRequest, authUpstream := t.XFrameOptions, t.AuthRequest, t.AuthRequestUpstream

	booleans := []struct {
		field  string
		target *bool
	}{
		{fields.Enabled, &enabled},
		{fields.NoIndex, &noIndex},
		{fields.CrowdsecAppsec, &crowdsec},
		{fields.RequestBuffering, &requestBuffering},
		{fields.ResponseBuffering, &responseBuffering},
		{fields.UpstreamCompression, &upstreamCompression},
		{fields.FancyIndex, &fancyIndex},
	}
	for _, b := range booleans {
		raw := strings.TrimSpace(values[fields.Normalize(b.field)])
		if raw == "" {
			continue
		}
		parsed, parseErr := fields.ParseBool(raw)
		if parseErr != nil {
			return npm.Location{}, fmt.Errorf("%s: %w", label(b.field), parseErr)
		}
		*b.target = parsed
	}

	if raw := strings.TrimSpace(values[fields.Normalize(fields.XFrameOptions)]); raw != "" {
		value, enumErr := (fields.Field{Enum: fields.XFrameOptionsValues}).ValidateEnum(raw)
		if enumErr != nil {
			return npm.Location{}, fmt.Errorf("%s: %w", label(fields.XFrameOptions), enumErr)
		}
		xFrame = npm.XFrameOptionsValue(value)
	}
	if raw := strings.TrimSpace(values[fields.Normalize(fields.AuthRequest)]); raw != "" {
		value, enumErr := (fields.Field{Enum: fields.AuthRequestValues}).ValidateEnum(raw)
		if enumErr != nil {
			return npm.Location{}, fmt.Errorf("%s: %w", label(fields.AuthRequest), enumErr)
		}
		authRequest = value
	}
	if raw := strings.TrimSpace(values[fields.Normalize(fields.AuthRequestUpstream)]); raw != "" {
		authUpstream = raw
	}

	return npm.Location{
		Path:                     path,
		LocationType:             locationType,
		ForwardScheme:            scheme,
		ForwardHost:              host,
		ForwardPort:              port,
		AdvancedConfig:           values[fields.Normalize(fields.AdvancedConfig)],
		LocationConfig:           values[fields.Normalize(fields.LocationConfig)],
		AccessListIDs:            ids,
		AccessListNames:          names,
		AccessListType:           listType,
		Enabled:                  &enabled,
		NoIndex:                  npm.Flag(noIndex),
		DisableCrowdsecAppsec:    npm.Flag(!crowdsec),
		DisableRequestBuffering:  npm.Flag(!requestBuffering),
		DisableResponseBuffering: npm.Flag(!responseBuffering),
		UpstreamCompression:      npm.Flag(upstreamCompression),
		FancyIndex:               npm.Flag(fancyIndex),
		XFrameOptions:            xFrame,
		AuthRequest:              authRequest,
		AuthRequestUpstream:      authUpstream,
	}, nil
}

// locationModifier maps the friendly location type to the nginx modifier
// NPMplus stores.
func locationModifier(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", "prefix":
		return npm.LocationPrefix, nil
	case "exact":
		return npm.LocationExact, nil
	case "regex":
		return npm.LocationRegex, nil
	case "iregex":
		return npm.LocationIRegex, nil
	case "prefer":
		return npm.LocationPrefer, nil
	case "named":
		return npm.LocationNamed, nil
	default:
		return "", fmt.Errorf("%q is not one of: %s", raw, strings.Join(fields.LocationTypeValues, ", "))
	}
}

// locationLabels reconstructs the original label names of a location block so
// errors point at the label the user actually wrote.
func locationLabels(f fieldSet, n int, values map[string]string) map[string]string {
	labels := make(map[string]string, len(values))
	for field := range values {
		labels[field] = fmt.Sprintf("%s.%s.%s.%d.%s", f.prefix, f.kind, fields.Location, n, field)
	}
	return labels
}

func buildRedirect(t *Target, f fieldSet) error {
	domains, err := f.domains(fields.Domains)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return missingDomains(f)
	}
	t.DomainNames = domains

	forward := strings.ToLower(strings.TrimSpace(f.string(fields.ForwardDomain)))
	if forward == "" {
		return fmt.Errorf("%s is required for a redirection host", f.name(fields.ForwardDomain))
	}
	if strings.Contains(forward, "://") {
		return fmt.Errorf("%s must be a domain name without a scheme, got %q (use %s for the scheme)",
			f.name(fields.ForwardDomain), forward, f.name(fields.ForwardScheme))
	}
	if strings.ContainsAny(forward, " \t") {
		return fmt.Errorf("%s: %q is not a valid domain name", f.name(fields.ForwardDomain), forward)
	}
	t.ForwardDomainName = forward

	if t.ForwardScheme, err = f.enum(fields.ForwardScheme); err != nil {
		return err
	}
	if t.ForwardScheme == "" {
		t.ForwardScheme = fields.Auto
	}

	code, err := f.integer(fields.HTTPCode)
	if err != nil {
		return err
	}
	if code < 300 || code > 308 {
		return fmt.Errorf("%s: %d is not a redirect status code (300-308)", f.source(fields.HTTPCode), code)
	}
	t.ForwardHTTPCode = code

	if t.PreservePath, err = f.boolean(fields.PreservePath); err != nil {
		return err
	}
	if t.BlockExploits, err = f.boolean(fields.BlockExploits); err != nil {
		return err
	}
	return nil
}

func buildStream(t *Target, c Container, opts ParseOptions, f fieldSet) error {
	incoming, err := f.port(fields.IncomingPort)
	if err != nil {
		return err
	}
	if incoming <= 0 {
		return fmt.Errorf("%s is required for a stream", f.name(fields.IncomingPort))
	}
	t.IncomingPort = incoming

	// Streams usually forward to the port they listen on.
	if t.ForwardingPort, err = resolvePort(c, opts, f, fields.ForwardPort, incoming); err != nil {
		return err
	}
	if t.ForwardingHost, err = resolveForwardHost(c, opts, f); err != nil {
		return err
	}

	tcp, udp := true, false
	protocol, err := f.enum(fields.Protocol)
	if err != nil {
		return err
	}
	switch protocol {
	case "tcp":
		tcp, udp = true, false
	case "udp":
		tcp, udp = false, true
	case "both":
		tcp, udp = true, true
	}
	if f.has(fields.TCP) || protocol == "" {
		if tcp, err = f.boolean(fields.TCP); err != nil {
			return err
		}
	}
	if f.has(fields.UDP) || protocol == "" {
		if udp, err = f.boolean(fields.UDP); err != nil {
			return err
		}
	}
	if !tcp && !udp {
		return fmt.Errorf("%s and %s cannot both be false", f.name(fields.TCP), f.name(fields.UDP))
	}
	t.TCPForwarding, t.UDPForwarding = tcp, udp

	proxyProtocol, err := f.enum(fields.ProxyProtocol)
	if err != nil {
		return err
	}
	switch proxyProtocol {
	case "v1":
		t.ProxyProtocol = 1
	case "v2":
		t.ProxyProtocol = 2
	default:
		t.ProxyProtocol = 0
	}
	if t.ProxyTLS, err = f.boolean(fields.ProxyTLS); err != nil {
		return err
	}

	t.Description = f.string(fields.Description)
	if t.Description == "" {
		t.Description = c.Name
	}
	if len(t.Description) > npm.MaxDescriptionLength {
		t.Description = t.Description[:npm.MaxDescriptionLength]
	}
	return nil
}

func buildDead(t *Target, f fieldSet) error {
	domains, err := f.domains(fields.Domains)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return missingDomains(f)
	}
	t.DomainNames = domains
	return nil
}

// resolvePort returns the upstream port, resolving "auto" from the container's
// exposed ports.
func resolvePort(c Container, opts ParseOptions, f fieldSet, field string, fallback int) (int, error) {
	port, err := f.port(field)
	if err != nil {
		return 0, err
	}
	switch {
	case port > 0:
		return port, nil
	case port == 0 && fallback > 0:
		return fallback, nil
	case port == 0 && (!c.Running() || opts.Offline):
		return 0, nil
	case port == 0:
		return 0, fmt.Errorf("%s is required for a %s", f.name(field), f.kind.Label())
	}

	// "auto": read it from the container.
	if guessed, ok := c.GuessPort(opts.PortPreference); ok {
		return guessed, nil
	}
	if fallback > 0 {
		return fallback, nil
	}
	if !c.Running() || opts.Offline {
		// A stopped container reports no ports, and a compose file does not
		// have them at all. The host either exists already (and is only
		// enabled or disabled from here on) or this is a dry check.
		return 0, nil
	}
	if ports := c.ExposedPorts(); len(ports) > 0 {
		return 0, fmt.Errorf("cannot pick an upstream port: the container exposes %s, "+
			"set %s or NPM_PORT_PREFERENCE", joinInts(ports), f.name(field))
	}
	return 0, fmt.Errorf("cannot determine the upstream port: the container exposes none, set %s", f.name(field))
}

func joinInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ", ")
}

// resolveForwardHost applies the explicit label, otherwise the container IP,
// otherwise the container name.
func resolveForwardHost(c Container, opts ParseOptions, f fieldSet) (string, error) {
	if explicit := f.string(fields.ForwardHost); explicit != "" {
		return explicit, nil
	}

	resolveIP := opts.ResolveIP
	if f.has(fields.ResolveIP) {
		parsed, err := f.boolean(fields.ResolveIP)
		if err != nil {
			return "", err
		}
		resolveIP = parsed
	}

	host := c.ResolveHost(ParseOptions{
		ResolveIP:      resolveIP,
		PreferNetworks: opts.PreferNetworks,
		StrictNetworks: opts.StrictNetworks,
	})
	if host == "" && (!c.Running() || opts.Offline) {
		// A stopped container has no address. Reporting that as an error
		// would mark the container as broken and, worse, hide the fact that
		// its host is merely idle.
		return "", nil
	}
	if host == "" {
		if opts.StrictNetworks {
			return "", fmt.Errorf("container is not attached to %s (attached to: %s); "+
				"join that network or set %s explicitly",
				strings.Join(opts.PreferNetworks, ", "),
				joinOrNone(c.NetworkNames()),
				f.name(fields.ForwardHost))
		}
		return "", fmt.Errorf("cannot determine the upstream host: set %s", f.name(fields.ForwardHost))
	}
	return host, nil
}

func joinOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// validateCertificate rejects combinations NPM would reject (or silently
// break) anyway. NPMplus takes the ACME account from its own ACME_EMAIL, so
// the Let's Encrypt labels are only mandatory for upstream NPM - which is
// checked when the payload is built, where the flavour is known.
func validateCertificate(t *Target, f fieldSet) error {
	if t.DNSChallenge && t.DNSProvider == "" {
		return fmt.Errorf("%s is required for a DNS-01 challenge", f.name(fields.LEDNSProvider))
	}
	if t.Certificate.Mode == certs.ModeNone && t.SSLForced != nil && *t.SSLForced {
		return fmt.Errorf("%s requires a certificate, but %s is none",
			f.source(fields.SSLForced), f.source(fields.Certificate))
	}
	return nil
}
