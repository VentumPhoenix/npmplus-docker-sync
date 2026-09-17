package syncer

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/fields"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// plusLabels is a label value per NPMplus-only field, so the test below can
// set each of them in turn. Every field with Plus: true in the table must
// appear here - the test fails otherwise, which is what keeps the two in step.
var plusLabels = map[npm.Kind]map[string]string{
	npm.KindProxy: {
		fields.HTTP3:               "true",
		fields.NoIndex:             "true",
		fields.CrowdsecAppsec:      "false",
		fields.RequestBuffering:    "false",
		fields.ResponseBuffering:   "false",
		fields.UpstreamCompression: "true",
		fields.FancyIndex:          "true",
		fields.XFrameOptions:       "deny",
		fields.AuthRequest:         "authelia",
		fields.AuthRequestUpstream: "http://authelia:9091",
		fields.LocationConfig:      "add_header X-Test 1;",
		fields.AccessListType:      "public",
	},
	npm.KindRedirect: {
		fields.HTTP3: "true",
	},
	npm.KindDead: {
		fields.HTTP3: "true",
	},
	npm.KindStream: {
		fields.ProxyProtocol:  "v2",
		fields.ProxyTLS:       "true",
		fields.AdvancedConfig: "proxy_timeout 30s;",
		fields.Description:    "a stream",
	},
}

// baseLabels is the minimum a resource of each kind needs.
var baseLabels = map[npm.Kind]map[string]string{
	npm.KindProxy:    {"npm.proxy.domains": "app.example.com", "npm.proxy.port": "80"},
	npm.KindRedirect: {"npm.redirect.domains": "old.example.com", "npm.redirect.forward_domain": "app.example.com"},
	npm.KindDead:     {"npm.404.domains": "parked.example.com"},
	npm.KindStream:   {"npm.stream.incoming_port": "5432"},
}

// TestEveryPlusFieldIsCovered walks the field table and checks that each
// NPMplus-only field is both reported and removed when talking to upstream
// nginx-proxy-manager. A field added to the table without teaching
// npmplusOnly() and stripPlus() about it fails here.
func TestEveryPlusFieldIsCovered(t *testing.T) {
	t.Parallel()

	for _, kind := range npm.Kinds {
		for _, f := range fields.List(kind) {
			if !f.Plus {
				continue
			}
			value, ok := plusLabels[kind][f.Name]
			if !ok {
				t.Errorf("%s/%s is NPMplus-only but has no test value; add it to plusLabels", kind, f.Name)
				continue
			}

			t.Run(string(kind)+"/"+f.Name, func(t *testing.T) {
				t.Parallel()

				labels := map[string]string{}
				for k, v := range baseLabels[kind] {
					labels[k] = v
				}
				labels["npm."+string(kind)+"."+f.Name] = value

				resource, target := buildFrom(t, kind, labels)
				if !contains(npm.UnsupportedByNPM(resource), f.Name) {
					t.Errorf("UnsupportedByNPM() = %v, want it to name %s",
						npm.UnsupportedByNPM(resource), f.Name)
				}
				if !contains(target.ExplicitPlus, f.Name) {
					t.Errorf("ExplicitPlus = %v, want it to name %s", target.ExplicitPlus, f.Name)
				}

				// After stripping, the payload must be indistinguishable from
				// one that never carried the field.
				bare, _ := buildFrom(t, kind, baseLabels[kind])
				npm.StripNPMplus(resource)
				npm.StripNPMplus(bare)
				if got, want := resource.Fingerprint(), bare.Fingerprint(); got != want {
					t.Errorf("%s survived StripNPMplus:\n got %s\nwant %s", f.Name, describeJSON(resource), describeJSON(bare))
				}
			})
		}
	}
}

// TestDefaultsDoNotWarnAgainstUpstreamNPM: the built-in defaults switch HTTP/3
// and the NPMplus buffering options on. A user who set none of them must not
// be told about them on every host.
func TestDefaultsDoNotWarnAgainstUpstreamNPM(t *testing.T) {
	t.Parallel()

	for kind, labels := range baseLabels {
		_, target := buildFrom(t, kind, labels)
		if len(target.ExplicitPlus) != 0 {
			t.Errorf("%s: ExplicitPlus = %v, want nothing for a default configuration",
				kind, target.ExplicitPlus)
		}
	}
}

// TestUpstreamNPMPayloadIsStableAcrossRuns: with the NPMplus fields stripped,
// the desired state matches what upstream NPM stores and returns - otherwise
// every run would see a difference and update again.
func TestUpstreamNPMPayloadIsStableAcrossRuns(t *testing.T) {
	t.Parallel()

	resource, _ := buildFrom(t, npm.KindProxy, baseLabels[npm.KindProxy])
	npm.StripNPMplus(resource)

	payload, err := resource.Payload(npm.FlavourNPM)
	if err != nil {
		t.Fatalf("Payload(npm) error = %v", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// What upstream NPM would return for that body.
	live := &npm.ProxyHost{}
	if err := json.Unmarshal(raw, live); err != nil {
		t.Fatalf("decode: %v", err)
	}
	live.Meta = resource.ResourceMeta()
	if live.Fingerprint() != resource.Fingerprint() {
		t.Errorf("the echo of upstream NPM differs from the desired state:\n sent %s\n back %s",
			describeJSON(resource), describeJSON(live))
	}
}

// buildFrom parses labels into a resource of the given kind.
func buildFrom(t *testing.T, kind npm.Kind, labels map[string]string) (npm.Resource, *docker.Target) {
	t.Helper()

	c := docker.Container{ID: "app-id", Name: "app", Labels: labels, State: "running"}
	res := docker.Parse(c, docker.ParseOptions{Prefix: "npm", ExposedByDefault: true})
	if len(res.Errors) > 0 {
		t.Fatalf("parse errors: %v", res.Errors)
	}
	for _, target := range res.Targets {
		if target.Kind != kind {
			continue
		}
		return BuildResource(target, npm.CertificateID{}, "", "npm"), target
	}
	t.Fatalf("no %s target from %v", kind, labels)
	return nil, nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func describeJSON(r npm.Resource) string {
	raw, err := json.Marshal(r)
	if err != nil {
		return err.Error()
	}
	return strings.TrimSpace(string(raw))
}
