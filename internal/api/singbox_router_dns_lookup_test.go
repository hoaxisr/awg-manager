package api

import (
	"context"
	"net"
	"net/http/httptest"
	"testing"
)

func TestLookupDNSServerTLSReturnsIPAndPins(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	defer server.Close()

	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := net.LookupPort("tcp", portText)
	if err != nil {
		t.Fatal(err)
	}

	result, err := lookupDNSServerTLS(context.Background(), host, port, "localhost")
	if err != nil {
		t.Fatalf("lookupDNSServerTLS: %v", err)
	}
	if len(result.IPs) == 0 || len(result.CertificatePublicKeySHA256) == 0 || len(result.Certificates) == 0 {
		t.Fatalf("incomplete result: %#v", result)
	}
	if result.Certificates[0].CertificatePublicKeySHA256 != result.CertificatePublicKeySHA256[0] {
		t.Fatalf("certificate pin not reflected in pin list: %#v", result)
	}
}
