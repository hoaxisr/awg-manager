package lifecycle

// TunnelState represents the lifecycle state of a kernel tunnel.
type TunnelState int

const (
	StateNotCreated TunnelState = iota
	StateDisabled               // stopped by user or our Stop
	StateBootReady              // after router reboot, NDMS created tun-type interface
	StateRunning                // amneziawg, link up, working
	StateSuspended              // we set link down (WAN down etc)
	StateDead                   // pingcheck determined dead, stopped
	StateBroken                 // inconsistent state
)

func (s TunnelState) String() string {
	switch s {
	case StateNotCreated:
		return "not_created"
	case StateDisabled:
		return "disabled"
	case StateBootReady:
		return "boot_ready"
	case StateRunning:
		return "running"
	case StateSuspended:
		return "suspended"
	case StateDead:
		return "dead"
	case StateBroken:
		return "broken"
	default:
		return "unknown"
	}
}

// Event represents something that happened to a tunnel.
type Event int

const (
	EventBoot          Event = iota // Router rebooted, daemon starting
	EventDaemonRestart              // Daemon restarted, kernel state intact
	EventWANUp                      // WAN interface came up
	EventWANDown                    // WAN interface went down
	EventUserEnable                 // User toggled ON in Keenetic UI (NDMS hook)
	EventUserDisable                // User toggled OFF in Keenetic UI (NDMS hook)
	EventAPIStart                   // User pressed Start in our UI
	EventAPIStop                    // User pressed Stop in our UI
	EventAPIRestart                 // User pressed Restart in our UI
)

func (e Event) String() string {
	switch e {
	case EventBoot:
		return "boot"
	case EventDaemonRestart:
		return "daemon_restart"
	case EventWANUp:
		return "wan_up"
	case EventWANDown:
		return "wan_down"
	case EventUserEnable:
		return "user_enable"
	case EventUserDisable:
		return "user_disable"
	case EventAPIStart:
		return "api_start"
	case EventAPIStop:
		return "api_stop"
	case EventAPIRestart:
		return "api_restart"
	default:
		return "unknown"
	}
}

// Action is what LifecycleManager decides to do.
type Action int

const (
	ActionNone        Action = iota // Do nothing
	ActionColdStart                 // ip link del (tun) + ip link add amneziawg + full config
	ActionStart                     // ip link add amneziawg + full config (after our Stop)
	ActionStop                      // Full stop: InterfaceDown + ip link del
	ActionSuspend                   // ip link set down
	ActionResume                    // ip link set up
	ActionReconfig                  // awg setconf (re-apply WG config without recreating)
	ActionReconnect                 // Restore in-memory tracking only
	ActionSwitchRoute               // Change endpoint route to different WAN
	ActionRestart                   // Stop (if alive) + Start — forced recreation
)

func (a Action) String() string {
	switch a {
	case ActionNone:
		return "none"
	case ActionColdStart:
		return "cold_start"
	case ActionStart:
		return "start"
	case ActionStop:
		return "stop"
	case ActionSuspend:
		return "suspend"
	case ActionResume:
		return "resume"
	case ActionReconfig:
		return "reconfig"
	case ActionReconnect:
		return "reconnect"
	case ActionSwitchRoute:
		return "switch_route"
	case ActionRestart:
		return "restart"
	default:
		return "unknown"
	}
}

// EventContext carries additional info about the event.
type EventContext struct {
	TunnelID      string
	WANInterface  string // for WAN up/down: which WAN
	HasOtherWAN   bool   // for WAN down auto-mode: another real WAN available
	StoredEnabled bool   // from our JSON storage
	HasPeer       bool   // WG peer configured (for daemon restart)
}
