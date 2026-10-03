package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/mihomonative"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// InspectNative enriches a Mihomo inspection with the shared router DNS
// configuration. The HTTP endpoint and internal diagnostics use this same
// path so their route/DNS explanations cannot drift.
func (h *MihomoHandler) InspectNative(ctx context.Context, req mihomonative.InspectInput) (mihomonative.InspectData, error) {
	if h.nativeStore == nil {
		return mihomonative.InspectData{}, fmt.Errorf("mihomo native store is not initialized")
	}
	if h.routerSvc != nil {
		if st, err := h.routerSvc.GetSettings(ctx); err == nil {
			req.KeeneticCloudTunnel = st.KeeneticCloudTunnel
			req.KeeneticCloudOutbound = st.KeeneticCloudOutbound
		}
		if srvs, err := h.routerSvc.ListDNSServers(ctx); err == nil {
			for _, server := range srvs {
				sni := ""
				if server.TLS != nil {
					sni = server.TLS.ServerName
				}
				req.DNSServers = append(req.DNSServers, mihomonative.DNSServerSpec{
					Tag: server.Tag, Type: server.Type, Server: server.Server,
					ServerPort: server.ServerPort, Detour: server.Detour, SNI: sni,
				})
			}
		}
		if rules, err := h.routerSvc.ListDNSRules(ctx); err == nil {
			for _, rule := range rules {
				req.DNSRules = append(req.DNSRules, mihomonative.DNSRuleSpec{
					Domain: rule.Domain, DomainSuffix: rule.DomainSuffix,
					DomainKeyword: rule.DomainKeyword, RuleSet: rule.RuleSet, Server: rule.Server,
				})
			}
		}
	}
	return h.nativeStore.Inspect(ctx, req)
}

func (h *MihomoHandler) handleMihomoInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req mihomonative.InspectInput
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if strings.TrimSpace(req.Domain) == "" {
		response.Error(w, "domain обязателен", "MISSING_DOMAIN")
		return
	}
	if err := validateInspectParams(req.Port, req.Protocol); err != nil {
		response.Error(w, err.message, err.code)
		return
	}
	if h.nativeStore == nil {
		response.Error(w, "mihomo native store is not initialized", "STORE_NOT_READY")
		return
	}
	res, err := h.InspectNative(r.Context(), req)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, res)
}

func (h *MihomoHandler) handleMihomoInspectStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		response.Error(w, "streaming not supported", "SSE_NOT_SUPPORTED")
		return
	}
	domain := r.URL.Query().Get("domain")
	if strings.TrimSpace(domain) == "" {
		response.Error(w, "domain обязателен", "MISSING_DOMAIN")
		return
	}
	port := 0
	if raw := r.URL.Query().Get("port"); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil {
			response.Error(w, "port должен быть числом от 0 до 65535", "INVALID_PORT")
			return
		}
		port = p
	}
	protocol := r.URL.Query().Get("protocol")
	if err := validateInspectParams(port, protocol); err != nil {
		response.Error(w, err.message, err.code)
		return
	}
	if h.nativeStore == nil {
		response.Error(w, "mihomo native store is not initialized", "STORE_NOT_READY")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	sendEvent := func(eventType string, data interface{}) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, b)
		flusher.Flush()
	}

	// Stream progress
	rules := h.nativeStore.ListRules()
	total := len(rules)
	ruleTotalPtr := &total

	sendEvent("progress", mihomonative.InspectStreamEventDTO{
		Type: "progress",
		Progress: &mihomonative.InspectProgressDTO{
			Phase:     "start",
			Message:   fmt.Sprintf("Проверяем %s по правилам Mihomo...", domain),
			RuleTotal: ruleTotalPtr,
		},
	})
	time.Sleep(20 * time.Millisecond)

	for i, rItem := range rules {
		idx := i
		sendEvent("progress", mihomonative.InspectStreamEventDTO{
			Type: "progress",
			Progress: &mihomonative.InspectProgressDTO{
				Phase:     "rule_start",
				Message:   fmt.Sprintf("Проверяем правило #%d: %s, %s → %s", i, rItem.Type, rItem.Payload, rItem.Outbound),
				RuleIndex: &idx,
				RuleTotal: ruleTotalPtr,
			},
		})
	}

	var dnsServers []mihomonative.DNSServerSpec
	var dnsRules []mihomonative.DNSRuleSpec
	cloudTunnel := false
	cloudOutbound := ""
	if h.routerSvc != nil {
		if st, err := h.routerSvc.GetSettings(r.Context()); err == nil {
			cloudTunnel = st.KeeneticCloudTunnel
			cloudOutbound = st.KeeneticCloudOutbound
		}
		if srvs, err := h.routerSvc.ListDNSServers(r.Context()); err == nil {
			for _, s := range srvs {
				sni := ""
				if s.TLS != nil {
					sni = s.TLS.ServerName
				}
				dnsServers = append(dnsServers, mihomonative.DNSServerSpec{
					Tag:        s.Tag,
					Type:       s.Type,
					Server:     s.Server,
					ServerPort: s.ServerPort,
					Detour:     s.Detour,
					SNI:        sni,
				})
			}
		}
		if rls, err := h.routerSvc.ListDNSRules(r.Context()); err == nil {
			for _, r := range rls {
				dnsRules = append(dnsRules, mihomonative.DNSRuleSpec{
					Domain:        r.Domain,
					DomainSuffix:  r.DomainSuffix,
					DomainKeyword: r.DomainKeyword,
					RuleSet:       r.RuleSet,
					Server:        r.Server,
				})
			}
		}
	}

	res, err := h.nativeStore.Inspect(r.Context(), mihomonative.InspectInput{
		Domain:                domain,
		Port:                  port,
		Protocol:              protocol,
		DNSServers:            dnsServers,
		DNSRules:              dnsRules,
		KeeneticCloudTunnel:   cloudTunnel,
		KeeneticCloudOutbound: cloudOutbound,
	})
	if err != nil {
		sendEvent("inspect-error", mihomonative.InspectStreamEventDTO{
			Type:  "error",
			Error: err.Error(),
		})
		return
	}

	sendEvent("progress", mihomonative.InspectStreamEventDTO{
		Type: "progress",
		Progress: &mihomonative.InspectProgressDTO{
			Phase:     "done",
			Message:   fmt.Sprintf("Маршрут определен: %s", res.Destination),
			Final:     res.Final,
			RuleTotal: ruleTotalPtr,
		},
	})

	sendEvent("result", mihomonative.InspectStreamEventDTO{
		Type:   "result",
		Result: &res,
	})
}
