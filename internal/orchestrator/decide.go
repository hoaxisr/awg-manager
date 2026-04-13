package orchestrator

// decide takes an event and current state, returns actions to execute.
// Pure function — no I/O, no side effects. All decision logic lives here.
func decide(event Event, state *State) []Action {
	switch event.Type {
	case EventBoot:
		return decideBoot(state)
	case EventReconnect:
		return decideReconnect(state)
	case EventStart:
		return decideStart(event, state)
	case EventStop:
		return decideStop(event, state)
	case EventDelete:
		return decideDelete(event, state)
	case EventRestart:
		return decideRestart(event, state)
	case EventNDMSHook:
		return decideNDMSHook(event, state)
	case EventWANUp:
		return decideWANUp(event, state)
	case EventWANDown:
		return decideWANDown(event, state)
	case EventPingCheckFailed:
		return decidePingCheckFailed(event, state)
	default:
		return nil
	}
}

func decideBoot(state *State) []Action {
	var actions []Action

	for _, t := range state.tunnels {
		if !t.Enabled {
			continue
		}

		switch t.Backend {
		case "kernel":
			// If process is already running (e.g., daemon restart, not router reboot),
			// reconcile around it. Otherwise cold start from scratch.
			if t.Running {
				actions = append(actions, Action{Type: ActionReconcileKernel, Tunnel: t.ID})
			} else {
				actions = append(actions, Action{Type: ActionColdStartKernel, Tunnel: t.ID})
			}
			actions = appendPostStartActions(actions, t)

		case "nativewg":
			if !state.supportsASC {
				actions = append(actions,
					Action{Type: ActionStopNativeWG, Tunnel: t.ID},
					Action{Type: ActionStartNativeWG, Tunnel: t.ID},
				)
				actions = appendPostStartActions(actions, t)
			}
		}
	}

	actions = append(actions,
		Action{Type: ActionReconcileStaticRoutes},
		Action{Type: ActionReconcileDNSRoutes},
	)

	return actions
}

func decideReconnect(state *State) []Action {
	var actions []Action

	actions = append(actions, Action{Type: ActionRestoreEndpointTracking})

	for _, t := range state.tunnels {
		if !t.Running {
			continue
		}

		switch t.Backend {
		case "kernel":
			// Re-apply NDMS config, firewall, routing around the running process.
			actions = append(actions, Action{Type: ActionReconcileKernel, Tunnel: t.ID})
		case "nativewg":
			if !state.supportsASC {
				actions = append(actions, Action{Type: ActionRestoreKmod, Tunnel: t.ID})
			}
		}

		if t.PingCheck != nil && t.PingCheck.Enabled {
			actions = append(actions, Action{Type: ActionStartMonitoring, Tunnel: t.ID})
		}
	}

	actions = append(actions,
		Action{Type: ActionReconcileStaticRoutes},
		Action{Type: ActionReconcileDNSRoutes},
	)

	return actions
}

func decideStart(event Event, state *State) []Action {
	t := state.tunnels[event.Tunnel]
	if t == nil || t.Running {
		return nil
	}

	var actions []Action

	switch t.Backend {
	case "kernel":
		actions = append(actions, Action{Type: ActionColdStartKernel, Tunnel: t.ID})
	case "nativewg":
		actions = append(actions, Action{Type: ActionStartNativeWG, Tunnel: t.ID})
	}

	actions = appendPostStartActions(actions, t)
	return actions
}

func decideStop(event Event, state *State) []Action {
	t := state.tunnels[event.Tunnel]
	if t == nil {
		return nil
	}
	// Note: we deliberately do NOT guard on !t.Running here.
	// A tunnel can be in NeedsStart (NDMS intent up, our process not yet
	// running — typical after router reboot when auto-start hasn't fired
	// or has failed). User clicks Stop to cancel that intent. All actions
	// below are idempotent, and decideNDMSHook("disabled") still guards
	// on !t.Running before calling us, so this won't fire phantom stops
	// from external NDMS hooks.

	var actions []Action

	// Stop monitoring first
	if t.Monitoring {
		actions = append(actions, Action{Type: ActionStopMonitoring, Tunnel: t.ID})
	}

	// Remove NDMS ping-check profile (NativeWG only)
	if t.Backend == "nativewg" && t.PingCheck != nil && t.PingCheck.Enabled {
		actions = append(actions, Action{Type: ActionRemovePingCheck, Tunnel: t.ID})
	}

	// Stop tunnel
	switch t.Backend {
	case "kernel":
		actions = append(actions, Action{Type: ActionStopKernel, Tunnel: t.ID})
	case "nativewg":
		actions = append(actions, Action{Type: ActionStopNativeWG, Tunnel: t.ID})
	}

	// Remove routing
	actions = append(actions,
		Action{Type: ActionRemoveStaticRoutes, Tunnel: t.ID},
		Action{Type: ActionRemoveClientRoutes, Tunnel: t.ID},
	)

	// Persist stopped state
	actions = append(actions, Action{Type: ActionPersistStopped, Tunnel: t.ID})

	return actions
}

// decideExternalStop handles externally-triggered conf=disabled for nativewg tunnels.
// Unlike decideStop, it does NOT generate ActionPersistStopped — the tunnel stays
// enabled in storage. Instead it generates ActionExternalRestart which will attempt
// to bring the tunnel back up.
//
// Rate-limited: after externalRestartMaxCount restarts within externalRestartWindow,
// falls back to normal decideStop (persists disabled) to prevent infinite loops.
func decideExternalStop(event Event, state *State, t *tunnelState) []Action {
	if !t.canExternalRestart() {
		return decideStop(event, state)
	}

	var actions []Action

	if t.Monitoring {
		actions = append(actions, Action{Type: ActionStopMonitoring, Tunnel: t.ID})
	}

	if t.PingCheck != nil && t.PingCheck.Enabled {
		actions = append(actions, Action{Type: ActionRemovePingCheck, Tunnel: t.ID})
	}

	actions = append(actions, Action{Type: ActionExternalRestart, Tunnel: t.ID})

	return actions
}

func decideNDMSHook(event Event, state *State) []Action {
	if event.Layer != "conf" {
		return nil
	}

	t := state.findByNDMSName(event.NDMSName)
	if t == nil {
		return nil
	}

	switch event.Level {
	case "running":
		if t.Running || !t.Enabled || !state.anyWANUp() {
			return nil
		}
		return decideStart(Event{Type: EventStart, Tunnel: t.ID}, state)

	case "disabled":
		if !t.Running {
			return nil
		}
		if t.Backend == "nativewg" {
			return decideExternalStop(Event{Type: EventStop, Tunnel: t.ID}, state, t)
		}
		return decideStop(Event{Type: EventStop, Tunnel: t.ID}, state)
	}

	return nil
}

func decideWANUp(event Event, state *State) []Action {
	var actions []Action

	for _, t := range state.tunnels {
		if !t.Enabled {
			continue
		}
		if !canStartOnWAN(t, event.WANIface) {
			continue
		}

		switch t.Backend {
		case "kernel":
			if t.Running {
				// Was suspended on WAN down. Choice depends on bind mode:
				// - Explicit bind (ISPInterface=="ethX"): Resume — same WAN came back, link up is enough.
				// - Auto mode (ISPInterface==""): Reconcile — different WAN may be available now,
				//   need to re-resolve WAN and refresh endpoint route via the new WAN.
				if t.ISPInterface == "" {
					actions = append(actions, Action{Type: ActionReconcileKernel, Tunnel: t.ID})
				} else {
					actions = append(actions, Action{Type: ActionResumeKernel, Tunnel: t.ID})
				}
			} else {
				// Stopped — start from scratch.
				actions = append(actions, Action{Type: ActionColdStartKernel, Tunnel: t.ID})
				actions = appendPostStartActions(actions, t)
			}

		case "nativewg":
			if state.supportsASC || t.Running {
				continue
			}
			actions = append(actions, Action{Type: ActionStartNativeWG, Tunnel: t.ID})
			actions = appendPostStartActions(actions, t)
		}
	}

	return actions
}

func decideWANDown(event Event, state *State) []Action {
	var actions []Action
	var nwgSuspended []string

	for _, t := range state.tunnels {
		if !t.Enabled || !t.Running {
			continue
		}
		if !affectedByWANDown(t, event.WANIface) {
			continue
		}

		switch t.Backend {
		case "kernel":
			actions = append(actions, Action{Type: ActionSuspendKernel, Tunnel: t.ID})

		case "nativewg":
			if state.supportsASC {
				continue // ASC handles failover natively
			}
			actions = append(actions, Action{Type: ActionSuspendProxy, Tunnel: t.ID})
			nwgSuspended = append(nwgSuspended, t.ID)
		}
	}

	// Immediate failover for NativeWG (without ASC): restart suspended tunnels
	// if another WAN is available. Kernel tunnels stay suspended until
	// WANUp event resumes them.
	if len(nwgSuspended) > 0 && state.anyWANUp() {
		for _, id := range nwgSuspended {
			t := state.tunnels[id]
			actions = append(actions, Action{Type: ActionStartNativeWG, Tunnel: t.ID})
			actions = appendPostStartActions(actions, t)
		}
	}

	return actions
}

// affectedByWANDown returns true if the tunnel is affected by a WAN going down.
// Auto mode (ISPInterface=""): affected if ActiveWAN matches the downed WAN.
// Explicit binding: affected if ISPInterface matches the downed WAN.
func affectedByWANDown(t *tunnelState, wanIface string) bool {
	if t.ISPInterface == "" {
		return t.ActiveWAN == wanIface
	}
	return t.ISPInterface == wanIface
}

// canStartOnWAN returns true if the tunnel can start when the given WAN comes up.
// Auto mode: can start on any WAN.
// Explicit binding: can only start when its specific WAN comes up.
func canStartOnWAN(t *tunnelState, wanIface string) bool {
	if t.ISPInterface == "" {
		return true
	}
	return t.ISPInterface == wanIface
}

func decideDelete(event Event, state *State) []Action {
	t := state.tunnels[event.Tunnel]
	if t == nil {
		return nil
	}

	var actions []Action

	// Stop monitoring
	if t.Monitoring {
		actions = append(actions, Action{Type: ActionStopMonitoring, Tunnel: t.ID})
	}

	// Remove NDMS ping-check profile if running NativeWG
	if t.Running && t.Backend == "nativewg" && t.PingCheck != nil && t.PingCheck.Enabled {
		actions = append(actions, Action{Type: ActionRemovePingCheck, Tunnel: t.ID})
	}

	// Remove all routing BEFORE deleting the NDMS interface.
	// OnTunnelDelete cleans storage + removes NDMS routes while interface still exists.
	actions = append(actions,
		Action{Type: ActionDeleteDNSRoutes, Tunnel: t.ID},
		Action{Type: ActionDeleteStaticRoutes, Tunnel: t.ID},
		Action{Type: ActionDeleteClientRoutes, Tunnel: t.ID},
	)

	// Delete (operator handles stop-if-running internally)
	switch t.Backend {
	case "kernel":
		actions = append(actions, Action{Type: ActionDeleteKernel, Tunnel: t.ID})
	case "nativewg":
		actions = append(actions, Action{Type: ActionDeleteNativeWG, Tunnel: t.ID})
	}

	return actions
}

func decideRestart(event Event, state *State) []Action {
	t := state.tunnels[event.Tunnel]
	if t == nil {
		return nil
	}

	var actions []Action

	// Stop phase (without PersistStopped — restart should not disable)
	if t.Running {
		if t.Monitoring {
			actions = append(actions, Action{Type: ActionStopMonitoring, Tunnel: t.ID})
		}
		if t.Backend == "nativewg" && t.PingCheck != nil && t.PingCheck.Enabled {
			actions = append(actions, Action{Type: ActionRemovePingCheck, Tunnel: t.ID})
		}
		switch t.Backend {
		case "kernel":
			actions = append(actions, Action{Type: ActionStopKernel, Tunnel: t.ID})
		case "nativewg":
			actions = append(actions, Action{Type: ActionStopNativeWG, Tunnel: t.ID})
		}
		// NOTE: no ActionRemoveStaticRoutes/ClientRoutes — will be re-applied after start
		// NOTE: no ActionPersistStopped — restart should not toggle Enabled flag
	}

	// Start phase
	switch t.Backend {
	case "kernel":
		actions = append(actions, Action{Type: ActionColdStartKernel, Tunnel: t.ID})
	case "nativewg":
		actions = append(actions, Action{Type: ActionStartNativeWG, Tunnel: t.ID})
	}
	actions = appendPostStartActions(actions, t)

	return actions
}

func decidePingCheckFailed(event Event, state *State) []Action {
	t := state.tunnels[event.Tunnel]
	if t == nil || !t.Running || t.Backend != "kernel" {
		return nil
	}
	return []Action{{Type: ActionLinkToggle, Tunnel: t.ID}}
}

// appendPostStartActions adds monitoring + routing actions after a tunnel start.
func appendPostStartActions(actions []Action, t *tunnelState) []Action {
	actions = append(actions,
		Action{Type: ActionApplyDNSRoutes, Tunnel: t.ID},
		Action{Type: ActionApplyStaticRoutes, Tunnel: t.ID, Iface: t.ifaceName()},
		Action{Type: ActionApplyClientRoutes, Tunnel: t.ID, Iface: t.ifaceName()},
	)

	if t.PingCheck != nil && t.PingCheck.Enabled {
		if t.Backend == "nativewg" {
			actions = append(actions, Action{Type: ActionConfigurePingCheck, Tunnel: t.ID})
		}
		actions = append(actions, Action{Type: ActionStartMonitoring, Tunnel: t.ID})
	}

	actions = append(actions, Action{Type: ActionPersistRunning, Tunnel: t.ID})

	return actions
}
