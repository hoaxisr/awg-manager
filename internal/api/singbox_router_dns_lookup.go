package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/response"
)

type SingboxDNSCertificateDTO struct {
	Subject                    string `json:"subject"`
	Issuer                     string `json:"issuer"`
	NotAfter                   string `json:"not_after"`
	CertificatePublicKeySHA256 string `json:"certificate_public_key_sha256"`
}

type SingboxDNSLookupData struct {
	IPs                        []string                   `json:"ips"`
	CertificatePublicKeySHA256 []string                   `json:"certificate_public_key_sha256"`
	Certificates               []SingboxDNSCertificateDTO `json:"certificates"`
}

type SingboxDNSLookupResponse struct {
	Success bool                 `json:"success"`
	Data    SingboxDNSLookupData `json:"data"`
}

// LookupDNSServer resolves a DNS-server hostname and reads the peer TLS
// certificate chain. Certificate verification is deliberately disabled for the
// probe: the purpose is to retrieve pins even when the current trust chain is
// invalid; sing-box applies the configured verification policy at runtime.
//
//	@Summary		Resolve DNS server and fetch TLS certificates
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Param			server		query	string	true	"DNS server hostname or IP"
//	@Param			server_port	query	int		false	"TLS port (default 853)"
//	@Param			server_name	query	string	false	"TLS SNI (defaults to server)"
//	@Success		200	{object}	SingboxDNSLookupResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		502	{object}	APIErrorEnvelope
//	@Router			/singbox/router/dns/servers/lookup [get]
func (h *SingboxRouterHandler) LookupDNSServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	server := strings.TrimSpace(r.URL.Query().Get("server"))
	if server == "" {
		response.BadRequest(w, "server is required")
		return
	}
	port := 853
	if rawPort := r.URL.Query().Get("server_port"); rawPort != "" {
		parsed, err := strconv.Atoi(rawPort)
		if err != nil || parsed < 1 || parsed > 65535 {
			response.BadRequest(w, "server_port must be between 1 and 65535")
			return
		}
		port = parsed
	}
	sni := strings.TrimSpace(r.URL.Query().Get("server_name"))
	if sni == "" && net.ParseIP(server) == nil {
		sni = server
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	result, err := lookupDNSServerTLS(ctx, server, port, sni)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadGateway, err.Error(), "DNS_TLS_LOOKUP_FAILED")
		return
	}
	response.Success(w, result)
}

func lookupDNSServerTLS(ctx context.Context, server string, port int, sni string) (SingboxDNSLookupData, error) {
	ips, err := net.DefaultResolver.LookupHost(ctx, server)
	if err != nil {
		return SingboxDNSLookupData{}, fmt.Errorf("resolve %q: %w", server, err)
	}
	if len(ips) == 0 {
		return SingboxDNSLookupData{}, fmt.Errorf("resolve %q: no IP addresses", server)
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(server, strconv.Itoa(port)), &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true, // #nosec G402 -- read-only certificate probe
	})
	if err != nil {
		return SingboxDNSLookupData{}, fmt.Errorf("TLS handshake with %s:%d: %w", server, port, err)
	}
	defer conn.Close()

	result := SingboxDNSLookupData{IPs: ips}
	seenPins := make(map[string]bool)
	for _, cert := range conn.ConnectionState().PeerCertificates {
		pin, err := publicKeySHA256(cert)
		if err != nil {
			return SingboxDNSLookupData{}, err
		}
		if !seenPins[pin] {
			result.CertificatePublicKeySHA256 = append(result.CertificatePublicKeySHA256, pin)
			seenPins[pin] = true
		}
		result.Certificates = append(result.Certificates, SingboxDNSCertificateDTO{
			Subject: cert.Subject.String(), Issuer: cert.Issuer.String(),
			NotAfter: cert.NotAfter.UTC().Format(time.RFC3339), CertificatePublicKeySHA256: pin,
		})
	}
	return result, nil
}

func publicKeySHA256(cert *x509.Certificate) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return "", fmt.Errorf("marshal certificate public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}
