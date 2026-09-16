package syncer

import (
	"context"
	"errors"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/certs"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

const notBefore = "2099-01-01 00:00:00"

func certificate(id int, domains ...string) npm.Certificate {
	return npm.Certificate{ID: id, Provider: "letsencrypt", NiceName: domains[0], DomainNames: domains, ExpiresOn: notBefore}
}

// A host without any certificate label gets the wildcard that covers it, and
// ssl_forced follows automatically.
func TestReconcileSelectsWildcardAutomatically(t *testing.T) {
	t.Parallel()

	target := proxyTarget("homepage", "homepage2.home.example.com", 3000)
	target.Certificate = certs.AutoSpec

	api := newFakeAPI()
	api.certificates = []npm.Certificate{certificate(12, "*.home.example.com")}

	w := NewWorker(api, newFakeSource(target), defaultOptions(), nil)
	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	host, ok := api.get(npm.KindProxy, 1).(*npm.ProxyHost)
	if !ok {
		t.Fatal("no proxy host was created")
	}
	if host.CertificateID.ID != 12 {
		t.Errorf("certificate = %s, want 12", host.CertificateID)
	}
	if !host.SSLForced {
		t.Error("ssl_forced should follow an attached certificate")
	}
}

// An exact certificate wins over a wildcard, and a certificate that is already
// attached and still fits is kept.
func TestReconcileCertificateStability(t *testing.T) {
	t.Parallel()

	target := proxyTarget("app", "app.home.example.com", 80)
	target.Certificate = certs.AutoSpec

	api := newFakeAPI()
	api.certificates = []npm.Certificate{certificate(1, "*.home.example.com")}
	w := NewWorker(api, newFakeSource(target), defaultOptions(), nil)
	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// A second wildcard changes nothing.
	api.certificates = append(api.certificates, certificate(2, "*.home.example.com"))
	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.Updated != 0 {
		t.Errorf("Result = %+v, want no update for an equivalent certificate", res)
	}

	// An exact certificate does.
	api.certificates = append(api.certificates, certificate(3, "app.home.example.com"))
	res, err = w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.Updated != 1 {
		t.Fatalf("Result = %+v, want the exact certificate to be applied", res)
	}
	host, _ := api.get(npm.KindProxy, 1).(*npm.ProxyHost)
	if host.CertificateID.ID != 3 {
		t.Errorf("certificate = %s, want the exact one (3)", host.CertificateID)
	}
}

// A new certificate has to reach its hosts without a restart: the poll reports
// the change that triggers the reconcile.
func TestPollCertificatesDetectsChanges(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	api.certificates = []npm.Certificate{certificate(1, "*.home.example.com")}
	w := NewWorker(api, newFakeSource(), defaultOptions(), nil)

	if !w.PollCertificates(context.Background()) {
		t.Fatal("the first poll must report a change")
	}
	if w.PollCertificates(context.Background()) {
		t.Fatal("an unchanged list must not trigger a reconcile")
	}
	api.certificates = append(api.certificates, certificate(2, "app.home.example.com"))
	if !w.PollCertificates(context.Background()) {
		t.Fatal("a new certificate must trigger a reconcile")
	}
}

// A certificate list that cannot be fetched must not strip the hosts of their
// certificates.
func TestCertificateListFailureKeepsTheLastKnownList(t *testing.T) {
	t.Parallel()

	target := proxyTarget("app", "app.home.example.com", 80)
	target.Certificate = certs.AutoSpec

	api := newFakeAPI()
	api.certificates = []npm.Certificate{certificate(1, "*.home.example.com")}
	w := NewWorker(api, newFakeSource(target), defaultOptions(), nil)
	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	api.mu.Lock()
	api.certificates = nil
	api.mu.Unlock()
	w.refMu.Lock()
	kept := len(w.certificates)
	w.refMu.Unlock()
	if kept != 1 {
		t.Fatalf("cached certificates = %d, want the previous list", kept)
	}
}

// An access list named in a label is resolved into an id; an unknown name must
// never silently produce a public host.
func TestReconcileResolvesAccessListNames(t *testing.T) {
	t.Parallel()

	t.Run("known name", func(t *testing.T) {
		t.Parallel()

		target := proxyTarget("kuma", "kuma.example.com", 3001)
		target.AccessListNames = []string{"Intern"}
		target.AccessListType = npm.AccessListCustom

		api := newFakeAPI()
		api.accessLists = []npm.AccessList{{ID: 4, Name: "Intern"}}
		w := NewWorker(api, newFakeSource(target), defaultOptions(), nil)
		if _, err := w.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		host, ok := api.get(npm.KindProxy, 1).(*npm.ProxyHost)
		if !ok {
			t.Fatal("no proxy host was created")
		}
		if len(host.AccessListIDs) != 1 || host.AccessListIDs[0] != 4 {
			t.Errorf("access lists = %v, want [4]", host.AccessListIDs)
		}
	})

	t.Run("unknown name", func(t *testing.T) {
		t.Parallel()

		target := proxyTarget("kuma", "kuma.example.com", 3001)
		target.AccessListNames = []string{"Typo"}
		target.AccessListType = npm.AccessListCustom

		api := newFakeAPI()
		api.accessLists = []npm.AccessList{{ID: 4, Name: "Intern"}}
		w := NewWorker(api, newFakeSource(target), defaultOptions(), nil)

		res, _ := w.Reconcile(context.Background())
		if res.Created != 0 || res.Failed != 1 {
			t.Fatalf("Result = %+v, want the host skipped", res)
		}
		if len(api.resources[npm.KindProxy]) != 0 {
			t.Error("a host with an unresolved access list must not be created")
		}
	})
}

// A domain another host already serves is reported in our own words instead of
// being handed to the API for a "domain already in use" error.
func TestReconcileSkipsForeignDomainConflicts(t *testing.T) {
	t.Parallel()

	foreign := &npm.ProxyHost{
		ID: 9,
		// The primary domain differs, so this is not the host we would adopt -
		// it merely also serves the domain our target wants.
		DomainNames: []string{"aaa.example.com", "app.example.com"},
		ForwardHost: "manual", ForwardPort: 80, ForwardScheme: "http",
		Meta: npm.Meta{npm.MetaManagedBy: "someone-else"},
	}
	target := proxyTarget("app", "app.example.com", 80)

	api := newFakeAPI(foreign)
	w := NewWorker(api, newFakeSource(target), defaultOptions(), nil)

	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.Skipped != 1 || res.Created != 0 {
		t.Errorf("Result = %+v, want the conflicting host skipped", res)
	}
}

// A resource the API keeps rejecting is retried with a growing delay instead of
// on every event.
func TestFailedResourceBacksOff(t *testing.T) {
	t.Parallel()

	target := proxyTarget("web", "web.example.com", 80)
	api := newFakeAPI()
	api.createErr = errors.New("database is locked")
	w := NewWorker(api, newFakeSource(target), defaultOptions(), nil)

	if res, _ := w.Reconcile(context.Background()); res.Failed != 1 {
		t.Fatalf("Result = %+v, want the failure counted", res)
	}
	before := api.callCount("create proxy web.example.com")

	// The second run is inside the backoff window.
	if res, _ := w.Reconcile(context.Background()); res.Skipped != 1 {
		t.Fatalf("Result = %+v, want the resource deferred", res)
	}
	if api.callCount("create proxy web.example.com") != before {
		t.Error("a deferred resource must not hit the API again")
	}

	// Changing the definition clears the backoff immediately.
	target.ForwardPort = 8080
	if res, _ := w.Reconcile(context.Background()); res.Failed != 1 {
		t.Fatalf("Result = %+v, want the changed definition retried at once", res)
	}
}
