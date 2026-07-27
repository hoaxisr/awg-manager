package freeturn

import (
	"net/url"
	"strings"
)

const DefaultCaptchaPort = 8765

// CaptchaClientStatus describes captcha state for one freeturn-client instance.
type CaptchaClientStatus struct {
	ClientID   string `json:"clientId"`
	ClientName string `json:"clientName"`
	// Waiting: client log indicates manual captcha is required (auto failed or -manual-captcha).
	Waiting bool `json:"waiting"`
	// Active: this client's process currently owns the local captcha HTTP server (:8765).
	Active bool `json:"active"`
	// Queued: waiting but another client holds :8765 — solve the active one first.
	Queued bool `json:"queued"`
	// CanOpen: UI may open the proxied captcha page for this client.
	CanOpen bool   `json:"canOpen"`
	URL     string `json:"url,omitempty"`
	// PendingStreams: VK-auth streams in this client still waiting on manual captcha.
	PendingStreams int `json:"pendingStreams,omitempty"`
	// PortContention: manual captcha port :8765 is busy with another stream's session.
	PortContention bool `json:"portContention,omitempty"`
	// CaptchaSession: manual captcha (re)starts in this process run — bump to reload iframe.
	CaptchaSession int `json:"captchaSession,omitempty"`
}

// CaptchaOverview aggregates captcha state across all client instances.
type CaptchaOverview struct {
	PortOpen      bool                  `json:"portOpen"`
	OwnerClientID string                `json:"ownerClientId,omitempty"`
	OwnerName     string                `json:"ownerName,omitempty"`
	Clients       []CaptchaClientStatus `json:"clients"`
}

var captchaLogMarkers = []string{
	"MANUAL CAPTCHA SOLVING NEEDED",
	"Triggering manual captcha fallback",
	"ACTION REQUIRED: MANUAL CAPTCHA",
}

// Lines after a captcha marker that mean the challenge was solved and the client moved on.
var captchaResolvedMarkers = []string{
	"[Captcha] received success token from browser",
	"Established DTLS connection",
}

// logIndicatesCaptchaWaiting reports whether recent process output shows that
// manual captcha solving is in progress. Only the tail is checked so an old
// captcha banner from a previous run does not keep the UI open forever.
// If a captcha marker is followed by a resolved marker (token received, DTLS
// up), the client is no longer waiting even though the marker remains in log.
func logIndicatesCaptchaWaiting(log string) bool {
	if log == "" {
		return false
	}
	if summary := analyzeCaptchaLog(log); summary.PendingStreams > 0 {
		return true
	}
	lines := captchaLogTailLines(log, 40)
	lastMarker := -1
	for i, line := range lines {
		for _, marker := range captchaLogMarkers {
			if strings.Contains(line, marker) {
				lastMarker = i
			}
		}
	}
	if lastMarker < 0 {
		return false
	}
	for _, line := range lines[lastMarker+1:] {
		for _, resolved := range captchaResolvedMarkers {
			if strings.Contains(line, resolved) {
				return false
			}
		}
	}
	return true
}

func captchaLogTailLines(log string, maxLines int) []string {
	lines := strings.Split(strings.TrimSpace(log), "\n")
	if maxLines <= 0 || len(lines) <= maxLines {
		return lines
	}
	return lines[len(lines)-maxLines:]
}

func captchaProxyPath(clientID string) string {
	return "/api/freeturn/clients/" + url.PathEscape(clientID) + "/captcha/"
}

// CaptchaStatus inspects running client processes, logs and the shared captcha
// listen port (8765 — hardcoded in upstream free-turn-proxy).
func (s *Service) CaptchaStatus() CaptchaOverview {
	cfg, err := s.store.Load()
	if err != nil {
		cfg = DefaultConfig()
	}

	candidatePIDs := make([]int, 0, len(cfg.Clients))
	clientStatuses := make([]ProcessStatus, 0, len(cfg.Clients))
	for _, inst := range cfg.Clients {
		st := s.clientProcs.get(inst.ID).Status()
		clientStatuses = append(clientStatuses, st)
		if st.Running && st.PID != 0 {
			candidatePIDs = append(candidatePIDs, st.PID)
		}
	}

	ownerPID, portOpen := captchaListenerPIDAmong(candidatePIDs)

	out := CaptchaOverview{
		PortOpen: portOpen,
		Clients:  make([]CaptchaClientStatus, 0, len(cfg.Clients)),
	}

	waitingIDs := make([]string, 0, len(cfg.Clients))
	for i, inst := range cfg.Clients {
		st := clientStatuses[i]
		captchaSummary := analyzeCaptchaLog(st.Log)
		waiting := st.Running && logIndicatesCaptchaWaiting(st.Log)
		active := portOpen && st.Running && st.PID != 0 && st.PID == ownerPID

		if active {
			out.OwnerClientID = inst.ID
			out.OwnerName = inst.Name
		}
		if waiting {
			waitingIDs = append(waitingIDs, inst.ID)
		}

		out.Clients = append(out.Clients, CaptchaClientStatus{
			ClientID:       inst.ID,
			ClientName:     inst.Name,
			Waiting:        waiting,
			Active:         active,
			PendingStreams: captchaSummary.PendingStreams,
			PortContention: captchaSummary.PortContention,
			CaptchaSession: captchaSummary.CaptchaSession,
		})
	}

	multipleWaiting := len(waitingIDs) > 1
	for i := range out.Clients {
		c := &out.Clients[i]
		switch {
		case !portOpen || !c.Waiting:
			continue
		case c.Active:
			c.CanOpen = true
			c.URL = captchaProxyPath(c.ClientID)
		case multipleWaiting && out.OwnerClientID != "" && out.OwnerClientID != c.ClientID:
			c.Queued = true
		default:
			// Single waiting client, or owner PID could not be resolved.
			c.CanOpen = true
			c.URL = captchaProxyPath(c.ClientID)
		}
	}

	return out
}

// CaptchaStatusForClient returns one client's slice of CaptchaOverview.
func (s *Service) CaptchaStatusForClient(id string) (CaptchaClientStatus, bool) {
	all := s.CaptchaStatus()
	for _, c := range all.Clients {
		if c.ClientID == id {
			return c, true
		}
	}
	return CaptchaClientStatus{}, false
}

// CaptchaPortOpen is a cheap probe used before proxying.
func CaptchaPortOpen() bool {
	_, ok := captchaListenerPIDAmong(nil)
	return ok
}

func captchaListenerPIDAmong(candidatePIDs []int) (int, bool) {
	return socketListenerPIDAmong("127.0.0.1", DefaultCaptchaPort, candidatePIDs)
}
