package router

// statePolicyTun — третий RoutingMode: захват через NDMS-политику (OpkgTun как
// policy-выход), ingress sing-box — tun-инбаунд в слоте 20. XOR с tproxy и
// fakeip-tun (spec 2026-08-03). stateOff/stateTProxy/stateFakeIPTun — в
// fakeip_transition.go.
const statePolicyTun = "policy-tun"

// policyTunDescription — NDMS-описание policy-tun OpkgTun. LOAD-BEARING:
// reap-скан по описанию матчит точную строку (см. fakeIPTunDescription).
const policyTunDescription = "awgm policy-tun"

// usesTunInbound: режимы, где sing-box слушает через tun (carrier-readiness
// вместо socket-probe, tun-aware reload и т.п.).
func usesTunInbound(mode string) bool {
	return mode == stateFakeIPTun || mode == statePolicyTun
}
