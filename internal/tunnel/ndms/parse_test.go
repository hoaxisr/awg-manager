package ndms

import (
	"testing"
)

// Test data captured from real hardware: KN-1810, firmware 5.0.4

// Test 1: Running tunnel (before reboot)
const showInterfaceRunning = `
           uptime: 540
               id: OpkgTun10
            index: 10
   interface-name: OpkgTun10
             type: OpkgTun
      description: WARPm1_63

           traits: Ip

           traits: Ip6

           traits: OpkgTun

             link: up
        connected: yes
            state: up
              mtu: 1280
  tx-queue-length: 500
       admin-only: no
          address: 172.16.0.2
             mask: 255.255.255.255
           global: yes
        defaultgw: no
         priority: 32766
   security-level: public

          summary:
                layer:
                     conf: running
                     link: running
                     ipv4: running
                     ipv6: disabled
                     ctrl: running
`

// Test 1: After reboot (was running) — process NOT started yet
const showInterfaceAfterRebootWasUp = `
           uptime: 0
               id: OpkgTun10
            index: 10
   interface-name: OpkgTun10
             type: OpkgTun
      description: WARPm1_63

           traits: Ip

           traits: Ip6

           traits: OpkgTun

             link: down
        connected: no
            state: up
              mtu: 1280
  tx-queue-length: 150
       admin-only: no
          address: 172.16.0.2
             mask: 255.255.255.255
           global: yes
        defaultgw: no
         priority: 32766
   security-level: public

          summary:
                layer:
                     conf: running
                     link: pending
                     ipv4: pending
                     ipv6: disabled
                     ctrl: pending
`

// Test 2: After Stop via awg-manager (BEFORE reboot)
const showInterfaceAfterStop = `
           uptime: 0
               id: OpkgTun10
            index: 10
   interface-name: OpkgTun10
             type: OpkgTun
      description: WARPm1_63

           traits: Ip

           traits: Ip6

           traits: OpkgTun

             link: down
        connected: no
            state: error
       admin-only: no
           global: yes
        defaultgw: no
         priority: 32766
   security-level: public

          summary:
                layer:
                     conf: disabled
                     link: disabled
                     ipv4: disabled
                     ipv6: disabled
                     ctrl: disabled
`

// Test 2: After reboot (was stopped)
const showInterfaceAfterRebootWasDown = `
           uptime: 0
               id: OpkgTun10
            index: 10
   interface-name: OpkgTun10
             type: OpkgTun
      description: WARPm1_63

           traits: Ip

           traits: Ip6

           traits: OpkgTun

             link: down
        connected: no
            state: down
              mtu: 1280
  tx-queue-length: 150
       admin-only: no
           global: yes
        defaultgw: no
         priority: 32766
   security-level: public

          summary:
                layer:
                     conf: disabled
                     link: disabled
                     ipv4: disabled
                     ipv6: disabled
                     ctrl: disabled
`

// Test 3: Kill process (NDMS still wants it up)
const showInterfaceAfterKillProcess = `
           uptime: 0
               id: OpkgTun10
            index: 10
   interface-name: OpkgTun10
             type: OpkgTun
      description: WARPm1_63

           traits: Ip

           traits: Ip6

           traits: OpkgTun

             link: down
        connected: no
            state: up
              mtu: 1280
  tx-queue-length: 150
       admin-only: no
          address: 172.16.0.2
             mask: 255.255.255.255
           global: yes
        defaultgw: no
         priority: 32766
   security-level: public

          summary:
                layer:
                     conf: running
                     link: pending
                     ipv4: pending
                     ipv6: disabled
                     ctrl: pending
`

// Test 5: Toggle OFF in router UI (process still alive)
// state: down, conf: disabled (NDMS set it via admin action)
const showInterfaceToggleOffInRouter = `
           uptime: 0
               id: OpkgTun10
            index: 10
   interface-name: OpkgTun10
             type: OpkgTun
      description: WARPm1_63

           traits: Ip

           traits: Ip6

           traits: OpkgTun

             link: down
        connected: no
            state: down
              mtu: 1280
  tx-queue-length: 150
       admin-only: no
           global: yes
        defaultgw: no
         priority: 32766
   security-level: public

          summary:
                layer:
                     conf: disabled
                     link: disabled
                     ipv4: disabled
                     ipv6: disabled
                     ctrl: disabled
`

// Kernel mode: ip link set down (link dropped, but conf: running preserved)
const showInterfaceKernelLinkDown = `
           uptime: 0
               id: OpkgTun10
            index: 10
   interface-name: OpkgTun10
             type: OpkgTun
      description: WARPm1_63

           traits: Ip

           traits: Ip6

           traits: OpkgTun

             link: down
        connected: no
            state: down
              mtu: 1280
  tx-queue-length: 1000
       admin-only: no
           global: yes
        defaultgw: no
         priority: 32766
   security-level: public

          summary:
                layer:
                     conf: running
                     link: pending
                     ipv4: pending
                     ipv6: disabled
                     ctrl: pending
`

func TestParseInterfaceInfo_Running(t *testing.T) {
	info, err := ParseInterfaceInfo(showInterfaceRunning)
	if err != nil {
		t.Fatalf("ParseInterfaceInfo() error = %v", err)
	}

	if info.State != "up" {
		t.Errorf("State = %q, want %q", info.State, "up")
	}
	if info.Link != "up" {
		t.Errorf("Link = %q, want %q", info.Link, "up")
	}
	if !info.Connected {
		t.Error("Connected should be true")
	}
	if info.ConfLayer != "running" {
		t.Errorf("ConfLayer = %q, want %q", info.ConfLayer, "running")
	}
}

func TestParseInterfaceInfo_AfterRebootWasUp(t *testing.T) {
	info, err := ParseInterfaceInfo(showInterfaceAfterRebootWasUp)
	if err != nil {
		t.Fatalf("ParseInterfaceInfo() error = %v", err)
	}

	if info.State != "up" {
		t.Errorf("State = %q, want %q", info.State, "up")
	}
	if info.Link != "down" {
		t.Errorf("Link = %q, want %q", info.Link, "down")
	}
	if info.Connected {
		t.Error("Connected should be false")
	}
	if info.ConfLayer != "running" {
		t.Errorf("ConfLayer = %q, want %q", info.ConfLayer, "running")
	}
}

func TestParseInterfaceInfo_AfterStop(t *testing.T) {
	info, err := ParseInterfaceInfo(showInterfaceAfterStop)
	if err != nil {
		t.Fatalf("ParseInterfaceInfo() error = %v", err)
	}

	// After our Stop: state: error (bug), but conf: disabled = admin turned off
	if info.State != "error" {
		t.Errorf("State = %q, want %q", info.State, "error")
	}
	if info.Link != "down" {
		t.Errorf("Link = %q, want %q", info.Link, "down")
	}
	if info.Connected {
		t.Error("Connected should be false")
	}
	if info.ConfLayer != "disabled" {
		t.Errorf("ConfLayer = %q, want %q", info.ConfLayer, "disabled")
	}
}

func TestParseInterfaceInfo_AfterRebootWasDown(t *testing.T) {
	info, err := ParseInterfaceInfo(showInterfaceAfterRebootWasDown)
	if err != nil {
		t.Fatalf("ParseInterfaceInfo() error = %v", err)
	}

	if info.State != "down" {
		t.Errorf("State = %q, want %q", info.State, "down")
	}
	if info.ConfLayer != "disabled" {
		t.Errorf("ConfLayer = %q, want %q", info.ConfLayer, "disabled")
	}
}

func TestParseInterfaceInfo_AfterKillProcess(t *testing.T) {
	info, err := ParseInterfaceInfo(showInterfaceAfterKillProcess)
	if err != nil {
		t.Fatalf("ParseInterfaceInfo() error = %v", err)
	}

	// NDMS still wants it up (state: up, conf: running) — link just dropped
	if info.State != "up" {
		t.Errorf("State = %q, want %q", info.State, "up")
	}
	if info.Link != "down" {
		t.Errorf("Link = %q, want %q", info.Link, "down")
	}
	if info.ConfLayer != "running" {
		t.Errorf("ConfLayer = %q, want %q", info.ConfLayer, "running")
	}
}

func TestParseInterfaceInfo_ToggleOffInRouter(t *testing.T) {
	info, err := ParseInterfaceInfo(showInterfaceToggleOffInRouter)
	if err != nil {
		t.Fatalf("ParseInterfaceInfo() error = %v", err)
	}

	// Admin explicitly disabled in router UI
	if info.State != "down" {
		t.Errorf("State = %q, want %q", info.State, "down")
	}
	if info.ConfLayer != "disabled" {
		t.Errorf("ConfLayer = %q, want %q", info.ConfLayer, "disabled")
	}
}

func TestParseInterfaceInfo_KernelLinkDown(t *testing.T) {
	info, err := ParseInterfaceInfo(showInterfaceKernelLinkDown)
	if err != nil {
		t.Fatalf("ParseInterfaceInfo() error = %v", err)
	}

	// Key test: ip link set down in kernel mode preserves conf: running
	if info.State != "down" {
		t.Errorf("State = %q, want %q", info.State, "down")
	}
	if info.Link != "down" {
		t.Errorf("Link = %q, want %q", info.Link, "down")
	}
	if info.ConfLayer != "running" {
		t.Errorf("ConfLayer = %q, want %q", info.ConfLayer, "running")
	}
}

// TestParseInterfaceInfo_IntentDerivation tests that we correctly derive
// NDMS intent from the parsed info. This is the core of the new state detection.
func TestParseInterfaceInfo_IntentDerivation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  InterfaceIntent
	}{
		// conf: running = IntentUP regardless of state field
		{
			name:  "running tunnel",
			input: showInterfaceRunning,
			want:  IntentUp,
		},
		{
			name:  "after reboot was up",
			input: showInterfaceAfterRebootWasUp,
			want:  IntentUp,
		},
		{
			name:  "after kill process",
			input: showInterfaceAfterKillProcess,
			want:  IntentUp,
		},
		{
			name:  "kernel ip link down",
			input: showInterfaceKernelLinkDown,
			want:  IntentUp,
		},

		// conf: disabled = IntentDOWN regardless of state field
		{
			name:  "after stop via awg-manager",
			input: showInterfaceAfterStop,
			want:  IntentDown,
		},
		{
			name:  "after reboot was down",
			input: showInterfaceAfterRebootWasDown,
			want:  IntentDown,
		},
		{
			name:  "toggle off in router UI",
			input: showInterfaceToggleOffInRouter,
			want:  IntentDown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := ParseInterfaceInfo(tt.input)
			if err != nil {
				t.Fatalf("ParseInterfaceInfo() error = %v", err)
			}
			got := info.Intent()
			if got != tt.want {
				t.Errorf("Intent() = %v, want %v (state=%q, conf=%q)",
					got, tt.want, info.State, info.ConfLayer)
			}
		})
	}
}

// TestParseInterfaceInfo_LinkUp tests the LinkUp() helper.
func TestParseInterfaceInfo_LinkUp(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"running tunnel", showInterfaceRunning, true},
		{"after reboot was up", showInterfaceAfterRebootWasUp, false},
		{"after stop", showInterfaceAfterStop, false},
		{"after kill process", showInterfaceAfterKillProcess, false},
		{"kernel link down", showInterfaceKernelLinkDown, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := ParseInterfaceInfo(tt.input)
			if err != nil {
				t.Fatalf("ParseInterfaceInfo() error = %v", err)
			}
			if info.LinkUp() != tt.want {
				t.Errorf("LinkUp() = %v, want %v (link=%q)", info.LinkUp(), tt.want, info.Link)
			}
		})
	}
}

func TestParseInterfaceInfo_EmptyInput(t *testing.T) {
	_, err := ParseInterfaceInfo("")
	if err == nil {
		t.Error("Expected error for empty input")
	}
}

func TestParseInterfaceInfo_NotFoundOutput(t *testing.T) {
	_, err := ParseInterfaceInfo("interface not found\n")
	if err == nil {
		t.Error("Expected error for 'not found' output")
	}
}

// JSON fixtures — RCI equivalents of the text fixtures above.
// Used to verify parseInterfaceInfoJSON produces identical InterfaceInfo.

const showInterfaceRunningJSON = `{"state":"up","link":"up","connected":"yes","interface-name":"OpkgTun10","type":"OpkgTun","description":"WARPm1_63","address":"172.16.0.2","mask":"255.255.255.255","security-level":"public","priority":32766,"summary":{"layer":{"conf":"running","link":"running","ipv4":"running","ipv6":"disabled"}}}`

const showInterfaceAfterRebootWasUpJSON = `{"state":"up","link":"down","connected":"no","interface-name":"OpkgTun10","type":"OpkgTun","description":"WARPm1_63","address":"172.16.0.2","mask":"255.255.255.255","security-level":"public","priority":32766,"summary":{"layer":{"conf":"running","link":"pending","ipv4":"pending","ipv6":"disabled"}}}`

const showInterfaceAfterStopJSON = `{"state":"error","link":"down","connected":"no","interface-name":"OpkgTun10","type":"OpkgTun","description":"WARPm1_63","security-level":"public","priority":32766,"summary":{"layer":{"conf":"disabled","link":"disabled","ipv4":"disabled","ipv6":"disabled"}}}`

const showInterfaceAfterRebootWasDownJSON = `{"state":"down","link":"down","connected":"no","interface-name":"OpkgTun10","type":"OpkgTun","description":"WARPm1_63","security-level":"public","priority":32766,"summary":{"layer":{"conf":"disabled","link":"disabled","ipv4":"disabled","ipv6":"disabled"}}}`

const showInterfaceAfterKillProcessJSON = `{"state":"up","link":"down","connected":"no","interface-name":"OpkgTun10","type":"OpkgTun","description":"WARPm1_63","address":"172.16.0.2","mask":"255.255.255.255","security-level":"public","priority":32766,"summary":{"layer":{"conf":"running","link":"pending","ipv4":"pending","ipv6":"disabled"}}}`

const showInterfaceToggleOffInRouterJSON = `{"state":"down","link":"down","connected":"no","interface-name":"OpkgTun10","type":"OpkgTun","description":"WARPm1_63","security-level":"public","priority":32766,"summary":{"layer":{"conf":"disabled","link":"disabled","ipv4":"disabled","ipv6":"disabled"}}}`

const showInterfaceKernelLinkDownJSON = `{"state":"down","link":"down","connected":"no","interface-name":"OpkgTun10","type":"OpkgTun","description":"WARPm1_63","security-level":"public","priority":32766,"summary":{"layer":{"conf":"running","link":"pending","ipv4":"pending","ipv6":"disabled"}}}`

// TestParseInterfaceInfoJSON verifies JSON format produces the same InterfaceInfo as text format.
func TestParseInterfaceInfoJSON(t *testing.T) {
	tests := []struct {
		name string
		text string
		json string
	}{
		{"running", showInterfaceRunning, showInterfaceRunningJSON},
		{"after reboot was up", showInterfaceAfterRebootWasUp, showInterfaceAfterRebootWasUpJSON},
		{"after stop", showInterfaceAfterStop, showInterfaceAfterStopJSON},
		{"after reboot was down", showInterfaceAfterRebootWasDown, showInterfaceAfterRebootWasDownJSON},
		{"after kill process", showInterfaceAfterKillProcess, showInterfaceAfterKillProcessJSON},
		{"toggle off in router", showInterfaceToggleOffInRouter, showInterfaceToggleOffInRouterJSON},
		{"kernel link down", showInterfaceKernelLinkDown, showInterfaceKernelLinkDownJSON},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			textInfo, err := ParseInterfaceInfo(tt.text)
			if err != nil {
				t.Fatalf("text parse error: %v", err)
			}
			jsonInfo, err := ParseInterfaceInfo(tt.json)
			if err != nil {
				t.Fatalf("JSON parse error: %v", err)
			}
			if textInfo != jsonInfo {
				t.Errorf("mismatch:\n  text: %+v\n  json: %+v", textInfo, jsonInfo)
			}
		})
	}
}

// TestParseInterfaceInfoJSON_IntentDerivation verifies JSON intent matches text intent.
func TestParseInterfaceInfoJSON_IntentDerivation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  InterfaceIntent
	}{
		{"running JSON", showInterfaceRunningJSON, IntentUp},
		{"after reboot was up JSON", showInterfaceAfterRebootWasUpJSON, IntentUp},
		{"after kill process JSON", showInterfaceAfterKillProcessJSON, IntentUp},
		{"kernel link down JSON", showInterfaceKernelLinkDownJSON, IntentUp},
		{"after stop JSON", showInterfaceAfterStopJSON, IntentDown},
		{"after reboot was down JSON", showInterfaceAfterRebootWasDownJSON, IntentDown},
		{"toggle off in router JSON", showInterfaceToggleOffInRouterJSON, IntentDown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := ParseInterfaceInfo(tt.input)
			if err != nil {
				t.Fatalf("ParseInterfaceInfo() error = %v", err)
			}
			if info.Intent() != tt.want {
				t.Errorf("Intent() = %v, want %v (conf=%q)", info.Intent(), tt.want, info.ConfLayer)
			}
		})
	}
}

func TestParseInterfaceInfoJSON_InvalidJSON(t *testing.T) {
	_, err := ParseInterfaceInfo(`{invalid json}`)
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestParseInterfaceInfoJSON_EmptyState(t *testing.T) {
	_, err := ParseInterfaceInfo(`{"link":"up"}`)
	if err == nil {
		t.Error("Expected error for missing state field")
	}
}
