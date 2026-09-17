package syncer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// serverAPI is the fake that behaves like a real server: it stores what the
// *payload* would have written, not the model the worker built. Everything a
// request body leaves out is therefore gone on the next read - which is how a
// fingerprint that includes a field the flavour cannot store shows up as an
// endless update loop.
type serverAPI struct {
	*fakeAPI
	flavour npm.Flavour
}

func (s *serverAPI) Flavour() npm.Flavour { return s.flavour }

func (s *serverAPI) Create(ctx context.Context, resource npm.Resource) (npm.Resource, error) {
	stored, err := s.roundTrip(resource)
	if err != nil {
		return nil, err
	}
	return s.fakeAPI.Create(ctx, stored)
}

func (s *serverAPI) Update(ctx context.Context, id int, resource npm.Resource) (npm.Resource, error) {
	stored, err := s.roundTrip(resource)
	if err != nil {
		return nil, err
	}
	return s.fakeAPI.Update(ctx, id, stored)
}

// roundTrip sends a resource through its request payload and back, which is
// exactly what the server stores and returns.
func (s *serverAPI) roundTrip(resource npm.Resource) (npm.Resource, error) {
	payload, err := resource.Payload(s.flavour)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	fresh := newResourceOf(resource.Kind())
	if err := json.Unmarshal(raw, fresh); err != nil {
		return nil, err
	}
	fresh.SetResourceID(resource.ResourceID())
	fresh.SetEnabled(true) // both APIs create in the enabled state
	return fresh, nil
}

func newResourceOf(kind npm.Kind) npm.Resource {
	switch kind {
	case npm.KindProxy:
		return &npm.ProxyHost{}
	case npm.KindRedirect:
		return &npm.RedirectionHost{}
	case npm.KindStream:
		return &npm.Stream{}
	default:
		return &npm.DeadHost{}
	}
}

// convergeCases are label sets that have to reach a fixed point.
var convergeCases = map[string]map[string]string{
	"minimal proxy": {
		"npm.proxy.domains": "app.example.com",
		"npm.proxy.port":    "80",
	},
	"proxy with everything": {
		"npm.proxy.domains":             "app.example.com,www.app.example.com",
		"npm.proxy.port":                "8080",
		"npm.proxy.scheme":              "https",
		"npm.proxy.ssl.hsts":            "true",
		"npm.proxy.ssl.hsts_subdomains": "true",
		"npm.proxy.certificate":         "5",
		"npm.proxy.access_list":         "2",
		"npm.proxy.auth_request":        "authelia",
		"npm.proxy.x_frame_options":     "deny",
		"npm.proxy.noindex":             "true",
		"npm.proxy.crowdsec_appsec":     "false",
		"npm.proxy.advanced_config":     "client_max_body_size 0;",
		"npm.proxy.location_config":     "add_header X-Test 1;",
	},
	"proxy with locations": {
		"npm.proxy.domains":               "app.example.com",
		"npm.proxy.port":                  "80",
		"npm.proxy.location.0.path":       "/api",
		"npm.proxy.location.0.port":       "3000",
		"npm.proxy.location.0.type":       "prefer",
		"npm.proxy.location.1.path":       "/static",
		"npm.proxy.location.1.fancyindex": "true",
	},
	"proxy without certificate": {
		"npm.proxy.domains":     "plain.example.com",
		"npm.proxy.port":        "80",
		"npm.proxy.certificate": "none",
	},
	"disabled proxy": {
		"npm.proxy.domains": "off.example.com",
		"npm.proxy.port":    "80",
		"npm.proxy.enabled": "false",
	},
	"redirect": {
		"npm.redirect.domains":        "old.example.com",
		"npm.redirect.forward_domain": "app.example.com",
		"npm.redirect.http_code":      "308",
		"npm.redirect.certificate":    "5",
	},
	"stream": {
		"npm.stream.incoming_port":  "5432",
		"npm.stream.forward_port":   "5432",
		"npm.stream.udp":            "true",
		"npm.stream.proxy_protocol": "v2",
	},
	"404 host": {
		"npm.404.domains":     "parked.example.com",
		"npm.404.certificate": "5",
	},
}

// TestReconcileConverges is the property every reconciler owes its users: the
// second run must have nothing left to do. A field the server normalises, a
// default the flavour cannot store, a list the API reorders - each of them
// would show up here as an update on every single event.
func TestReconcileConverges(t *testing.T) {
	t.Parallel()

	for _, flavour := range []npm.Flavour{npm.FlavourNPMplus, npm.FlavourNPM} {
		for name, labels := range convergeCases {
			t.Run(string(flavour)+"/"+name, func(t *testing.T) {
				t.Parallel()

				c := docker.Container{
					ID: "app-id", Name: "app", State: "running",
					Labels: labels,
					Ports:  []docker.PortBinding{{Private: 80, Type: "tcp"}},
				}
				snapshot, res := docker.Scan([]docker.Container{c},
					docker.ParseOptions{Prefix: "npm", ExposedByDefault: true})
				if len(res.Errors) > 0 {
					t.Fatalf("parse errors: %v", res.Errors)
				}

				api := &serverAPI{fakeAPI: newFakeAPI(), flavour: flavour}
				api.certificates = []npm.Certificate{{
					ID: 5, Provider: "letsencrypt", DomainNames: []string{"*.example.com"},
					ExpiresOn: notBefore,
				}}
				api.accessLists = []npm.AccessList{{ID: 2, Name: "Intern"}}

				src := newFakeSource(snapshot.Targets...)
				w := NewWorker(api, src, defaultOptions(), nil)

				first, err := w.Reconcile(context.Background())
				if err != nil {
					t.Fatalf("first Reconcile() error = %v", err)
				}
				if first.Created == 0 {
					t.Fatalf("first run created nothing: %+v", first)
				}

				second, err := w.Reconcile(context.Background())
				if err != nil {
					t.Fatalf("second Reconcile() error = %v", err)
				}
				if second.Created != 0 || second.Updated != 0 || second.Deleted != 0 {
					t.Errorf("second run is not a no-op: %+v", second)
				}

				// A fresh worker has no cache to fall back on, so this is the
				// stricter test: the stored resource itself must hash like the
				// desired one.
				cold := NewWorker(api, src, defaultOptions(), nil)
				third, err := cold.Reconcile(context.Background())
				if err != nil {
					t.Fatalf("cold Reconcile() error = %v", err)
				}
				if third.Updated != 0 || third.Created != 0 || third.Deleted != 0 {
					t.Errorf("a restart would rewrite the resource: %+v", third)
				}
			})
		}
	}
}
