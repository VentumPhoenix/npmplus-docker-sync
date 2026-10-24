package syncer

import (
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// The server turns HTTP/2 off for a host without a certificate. NPM 2.11
// enforces that on write and 2.12 does not, so the desired state has to carry
// the cleaned value - otherwise every run against 2.11 writes the host again.
func TestNormalizeSSLClampsHTTP2WithoutACertificate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		certificate npm.CertificateID
		http2       bool
		wantHTTP2   bool
		wantForced  bool
	}{
		{"no certificate clears http2", npm.CertificateID{}, true, false, false},
		{"a certificate keeps http2", npm.CertificateID{ID: 7}, true, true, true},
		{"http2 off stays off", npm.CertificateID{ID: 7}, false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeSSL(&docker.Target{HTTP2Support: tc.http2}, tc.certificate)
			if got.http2 != tc.wantHTTP2 {
				t.Errorf("http2 = %v, want %v", got.http2, tc.wantHTTP2)
			}
			if got.forced != tc.wantForced {
				t.Errorf("forced = %v, want %v", got.forced, tc.wantForced)
			}
		})
	}
}

// NPMplus switches HTTP/3 off for a host without a certificate - a pending
// certificate request counts as one - from the release that retired the meta
// object on. Asking for it anyway would rewrite every certificate-less host on
// every run.
func TestNormalizeSSLClampsHTTP3WithoutACertificate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		certificate npm.CertificateID
		want        bool
	}{
		{"no certificate clears http3", npm.CertificateID{}, false},
		{"a certificate keeps http3", npm.CertificateID{ID: 7}, true},
		{"a certificate request keeps http3", npm.NewCertificate(), true},
	} {
		if got := normalizeSSL(&docker.Target{HTTP3Support: true}, tc.certificate).http3; got != tc.want {
			t.Errorf("%s: http3 = %v, want %v", tc.name, got, tc.want)
		}
	}
}
