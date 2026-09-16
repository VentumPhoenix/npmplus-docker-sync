package docker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/network"
)

// mockAPI implements APIClient for tests.
type mockAPI struct {
	mu sync.Mutex

	containers []container.Summary
	listErr    error
	pingErr    error

	// streams is consumed one subscription at a time so reconnect behaviour
	// can be exercised deterministically.
	streams    []stream
	subscribes int
	closed     bool
}

type stream struct {
	msgs []events.Message
	err  error
	// keepOpen leaves the channels open until the context is cancelled.
	keepOpen bool
}

func (m *mockAPI) ContainerList(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.containers, m.listErr
}

func (m *mockAPI) Ping(_ context.Context) (types.Ping, error) {
	return types.Ping{APIVersion: "1.45"}, m.pingErr
}

func (m *mockAPI) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockAPI) Events(ctx context.Context, opts events.ListOptions) (<-chan events.Message, <-chan error) {
	m.mu.Lock()
	idx := m.subscribes
	m.subscribes++
	var s stream
	if idx < len(m.streams) {
		s = m.streams[idx]
	} else {
		s = stream{keepOpen: true}
	}
	m.mu.Unlock()

	// Server-side filtering is the security-relevant part: assert it is set.
	if !opts.Filters.Contains("type") {
		panic("event subscription without a type filter")
	}

	msgs := make(chan events.Message, len(s.msgs))
	errs := make(chan error, 1)
	go func() {
		defer close(msgs)
		defer close(errs)
		for _, msg := range s.msgs {
			select {
			case msgs <- msg:
			case <-ctx.Done():
				return
			}
		}
		if s.err != nil {
			select {
			case errs <- s.err:
			case <-ctx.Done():
			}
			return
		}
		if s.keepOpen {
			<-ctx.Done()
		}
	}()
	return msgs, errs
}

func (m *mockAPI) subscriptions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.subscribes
}

func containerEvent(action, name string) events.Message {
	return events.Message{
		Type:   events.ContainerEventType,
		Action: events.Action(action),
		Actor:  events.Actor{ID: "deadbeef", Attributes: map[string]string{"name": "/" + name}},
	}
}

func TestListenerTargets(t *testing.T) {
	t.Parallel()

	api := &mockAPI{containers: []container.Summary{
		{
			ID:    "1111111111112222",
			Names: []string{"/whoami"},
			Labels: map[string]string{
				"npm.enable": "true", "npm.host": "whoami.example.com", "npm.port": "80",
			},
		},
		{ID: "2222", Names: []string{"/database"}},
		{
			ID:     "3333",
			Names:  []string{"/broken"},
			Labels: map[string]string{"npm.enable": "true"}, // invalid: no host/port
		},
	}}

	listener := NewListener(api, ParseOptions{Prefix: "npm"}, nil)
	targets, err := listener.Targets(context.Background())
	if err != nil {
		t.Fatalf("Targets() error = %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %+v, want 1", targets)
	}
	if targets[0].ForwardHost != "whoami" {
		t.Errorf("ForwardHost = %q, want the container name", targets[0].ForwardHost)
	}
}

func TestListenerTargetsPropagatesListError(t *testing.T) {
	t.Parallel()

	api := &mockAPI{listErr: errors.New("permission denied")}
	if _, err := NewListener(api, ParseOptions{Prefix: "npm"}, nil).Targets(context.Background()); err == nil {
		t.Fatal("Targets() error = nil, want the docker error")
	}
}

func TestListenerWatchForwardsEvents(t *testing.T) {
	t.Parallel()

	api := &mockAPI{streams: []stream{{
		msgs:     []events.Message{containerEvent("start", "web"), containerEvent("die", "web")},
		keepOpen: true,
	}}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	trigger := make(chan Event, 4)
	done := make(chan error, 1)
	go func() { done <- NewListener(api, ParseOptions{Prefix: "npm"}, nil).Watch(ctx, trigger) }()

	for _, want := range []string{"start", "die"} {
		select {
		case ev := <-trigger:
			if ev.Action != want {
				t.Errorf("action = %q, want %q", ev.Action, want)
			}
			if ev.Name != "web" {
				t.Errorf("name = %q, want web (slash stripped)", ev.Name)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for the %q event", want)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Watch() error = %v, want nil on cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch() did not return after cancellation")
	}
}

func TestListenerWatchReconnects(t *testing.T) {
	t.Parallel()

	api := &mockAPI{streams: []stream{
		{msgs: []events.Message{containerEvent("start", "first")}, err: errors.New("stream broken")},
		{msgs: []events.Message{containerEvent("start", "second")}, keepOpen: true},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	trigger := make(chan Event, 4)
	go func() { _ = NewListener(api, ParseOptions{Prefix: "npm"}, nil).Watch(ctx, trigger) }()

	var got []string
	deadline := time.After(10 * time.Second)
	for len(got) < 2 {
		select {
		case ev := <-trigger:
			got = append(got, ev.Name)
		case <-deadline:
			t.Fatalf("timed out, received %v", got)
		}
	}
	if got[0] != "first" || got[1] != "second" {
		t.Errorf("events = %v, want [first second]", got)
	}
	if api.subscriptions() < 2 {
		t.Errorf("subscriptions = %d, want the listener to reconnect", api.subscriptions())
	}
}

func TestEventFilters(t *testing.T) {
	t.Parallel()

	f := EventFilters()
	if !f.ExactMatch("type", "container") {
		t.Error("filters must restrict the stream to container events")
	}
	for _, action := range []string{"start", "die", "stop"} {
		if !f.ExactMatch("event", action) {
			t.Errorf("filters must include the %q action", action)
		}
	}
	if f.ExactMatch("event", "exec_start") {
		t.Error("filters must not subscribe to exec events")
	}
}

func TestContainerName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   container.Summary
		want string
	}{
		{name: "named", in: container.Summary{ID: "abc", Names: []string{"/web"}}, want: "web"},
		{name: "compose style", in: container.Summary{ID: "abc", Names: []string{"/stack-web-1"}}, want: "stack-web-1"},
		{name: "unnamed falls back to short id", in: container.Summary{ID: "0123456789abcdef"}, want: "0123456789ab"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := containerName(tc.in); got != tc.want {
				t.Errorf("containerName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListenerResolvesContainerIP(t *testing.T) {
	t.Parallel()

	api := &mockAPI{containers: []container.Summary{{
		ID:    "abc",
		Names: []string{"/app"},
		Labels: map[string]string{
			"npm.enable": "true", "npm.host": "app.example.com", "npm.port": "8080",
		},
		NetworkSettings: &container.NetworkSettingsSummary{
			Networks: map[string]*network.EndpointSettings{
				"npm":     {IPAddress: "172.20.0.5"},
				"backend": {IPAddress: "172.21.0.9"},
			},
		},
	}}}

	tests := []struct {
		name string
		opts ParseOptions
		want string
	}{
		{
			name: "prefers the configured network",
			opts: ParseOptions{Prefix: "npm", ResolveIP: true, PreferNetworks: []string{"npm"}},
			want: "172.20.0.5",
		},
		{
			name: "falls back to the first network alphabetically",
			opts: ParseOptions{Prefix: "npm", ResolveIP: true},
			want: "172.21.0.9", // "backend" sorts before "npm"
		},
		{
			name: "uses the container name when resolution is off",
			opts: ParseOptions{Prefix: "npm", ResolveIP: false},
			want: "app",
		},
		{
			name: "unknown preferred network falls back",
			opts: ParseOptions{Prefix: "npm", ResolveIP: true, PreferNetworks: []string{"nonexistent"}},
			want: "172.21.0.9",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			targets, err := NewListener(api, tc.opts, nil).Targets(context.Background())
			if err != nil {
				t.Fatalf("Targets() error = %v", err)
			}
			if len(targets) != 1 {
				t.Fatalf("targets = %d, want 1", len(targets))
			}
			if got := targets[0].ForwardHost; got != tc.want {
				t.Errorf("ForwardHost = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListenerFallsBackToNameWithoutIP(t *testing.T) {
	t.Parallel()

	api := &mockAPI{containers: []container.Summary{{
		ID:    "abc",
		Names: []string{"/app"},
		Labels: map[string]string{
			"npm.enable": "true", "npm.host": "app.example.com", "npm.port": "8080",
		},
		// Host networking: no per-network IP address is reported.
		NetworkSettings: &container.NetworkSettingsSummary{
			Networks: map[string]*network.EndpointSettings{"host": {}},
		},
	}}}

	targets, err := NewListener(api, ParseOptions{Prefix: "npm", ResolveIP: true}, nil).Targets(context.Background())
	if err != nil {
		t.Fatalf("Targets() error = %v", err)
	}
	if got := targets[0].ForwardHost; got != "app" {
		t.Errorf("ForwardHost = %q, want the container name as fallback", got)
	}
}

func TestListenerLabelOverridesResolvedIP(t *testing.T) {
	t.Parallel()

	api := &mockAPI{containers: []container.Summary{{
		ID:    "abc",
		Names: []string{"/app"},
		Labels: map[string]string{
			"npm.enable": "true", "npm.host": "app.example.com", "npm.port": "8080",
			"npm.proxy.forward_host": "custom-upstream",
		},
		NetworkSettings: &container.NetworkSettingsSummary{
			Networks: map[string]*network.EndpointSettings{"npm": {IPAddress: "172.20.0.5"}},
		},
	}}}

	targets, err := NewListener(api, ParseOptions{Prefix: "npm", ResolveIP: true}, nil).Targets(context.Background())
	if err != nil {
		t.Fatalf("Targets() error = %v", err)
	}
	if got := targets[0].ForwardHost; got != "custom-upstream" {
		t.Errorf("ForwardHost = %q, want the explicit label to win", got)
	}
}

func TestListenerProducesMultipleTargetsPerContainer(t *testing.T) {
	t.Parallel()

	api := &mockAPI{containers: []container.Summary{{
		ID:    "abc",
		Names: []string{"/multi"},
		Labels: map[string]string{
			"npm.enable":                 "true",
			"npm.0.proxy.host":           "app.example.com",
			"npm.0.proxy.port":           "80",
			"npm.1.stream.incoming_port": "5432",
			"npm.2.404.host":             "parked.example.com",
		},
		NetworkSettings: &container.NetworkSettingsSummary{
			Networks: map[string]*network.EndpointSettings{"npm": {IPAddress: "172.20.0.7"}},
		},
	}}}

	targets, err := NewListener(api, ParseOptions{Prefix: "npm", ResolveIP: true}, nil).Targets(context.Background())
	if err != nil {
		t.Fatalf("Targets() error = %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("targets = %d, want 3 resources from one container", len(targets))
	}
	if targets[0].ForwardHost != "172.20.0.7" || targets[1].ForwardingHost != "172.20.0.7" {
		t.Errorf("upstreams = %q / %q, want the resolved IP", targets[0].ForwardHost, targets[1].ForwardingHost)
	}
}

func TestIPAddressSkipsInvalidAddresses(t *testing.T) {
	t.Parallel()

	c := Container{Name: "c", Networks: []Network{
		{Name: "a", IPv4: ""},
		{Name: "b", IPv4: "not-an-ip"},
		{Name: "c", IPv6: "fd00::1"},
		{Name: "d", IPv4: "10.0.0.4"},
	}}
	if got := c.IPAddress(nil, false); got != "10.0.0.4" {
		t.Errorf("IPAddress() = %q, want the only valid IPv4 address", got)
	}
}
