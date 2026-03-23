package lifecycle

// Decide returns the action to take for a given event and tunnel state.
// This is a pure function — no side effects, no I/O. All decision logic in one place.
func Decide(event Event, state TunnelState, ctx EventContext) Action {
	// Disabled by user — only user actions can change this
	if !ctx.StoredEnabled {
		switch event {
		case EventUserEnable:
			// User re-enabling — allow
		case EventAPIStart:
			// API start — allow
		case EventAPIRestart:
			// API restart — allow (bring to Running regardless)
		default:
			return ActionNone
		}
	}

	switch event {
	case EventBoot:
		return decideBoot(state)
	case EventDaemonRestart:
		return decideDaemonRestart(state, ctx)
	case EventWANUp:
		return decideWANUp(state)
	case EventWANDown:
		return decideWANDown(state, ctx)
	case EventUserEnable:
		return decideEnable(state)
	case EventUserDisable:
		return decideDisable(state)
	case EventAPIStart:
		return decideEnable(state)
	case EventAPIStop:
		return decideDisable(state)
	case EventPingDead:
		return decidePingDead(state)
	case EventPingRetry:
		return decidePingRetry(state)
	case EventAPIRestart:
		return decideRestart(state)
	default:
		return ActionNone
	}
}

func decideBoot(state TunnelState) Action {
	switch state {
	case StateRunning:
		return ActionNone
	case StateDisabled:
		return ActionStart // device exists (after our Stop), just bring up
	default:
		// BootReady, NotCreated, Dead, Broken, Suspended — need full creation
		return ActionColdStart
	}
}

func decideDaemonRestart(state TunnelState, ctx EventContext) Action {
	switch state {
	case StateRunning:
		if ctx.HasPeer {
			return ActionReconnect
		}
		return ActionReconfig
	case StateSuspended:
		return ActionResume
	case StateDisabled:
		return ActionStart // device exists, just bring up
	case StateDead:
		return ActionColdStart
	case StateBroken:
		return ActionReconfig
	default:
		// BootReady, NotCreated — need full creation
		return ActionColdStart
	}
}

func decideWANUp(state TunnelState) Action {
	switch state {
	case StateRunning:
		return ActionNone
	case StateSuspended:
		return ActionResume
	case StateDisabled:
		return ActionStart // device exists, just bring up
	case StateDead:
		return ActionColdStart
	case StateBootReady:
		return ActionColdStart
	default:
		return ActionNone
	}
}

func decideWANDown(state TunnelState, ctx EventContext) Action {
	switch state {
	case StateRunning:
		if ctx.HasOtherWAN {
			return ActionSwitchRoute
		}
		return ActionSuspend
	default:
		return ActionNone
	}
}

func decideEnable(state TunnelState) Action {
	switch state {
	case StateRunning:
		return ActionNone
	case StateSuspended:
		return ActionResume
	case StateBootReady:
		return ActionColdStart
	case StateNotCreated:
		return ActionColdStart // no device — need full creation
	case StateDead:
		return ActionColdStart // stopped by PingCheck, device may be gone
	case StateBroken:
		return ActionColdStart
	case StateDisabled:
		return ActionStart // device exists (after our Stop), just bring up
	default:
		return ActionColdStart
	}
}

func decideDisable(state TunnelState) Action {
	switch state {
	case StateDisabled, StateNotCreated:
		return ActionNone
	default:
		return ActionStop
	}
}

func decidePingDead(state TunnelState) Action {
	if state == StateRunning {
		return ActionStop
	}
	return ActionNone
}

func decidePingRetry(state TunnelState) Action {
	if state == StateDead {
		return ActionColdStart // device may be gone after PingCheck stop
	}
	return ActionNone
}

// decideRestart: bring to Running regardless of current state.
func decideRestart(state TunnelState) Action {
	switch state {
	case StateRunning, StateSuspended, StateDead, StateBroken:
		return ActionRestart // Stop + Start
	case StateDisabled:
		return ActionStart // device exists, just bring up
	default:
		// NotCreated, BootReady — need full creation
		return ActionColdStart
	}
}
