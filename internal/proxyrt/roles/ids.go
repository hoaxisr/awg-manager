// Package roles — словарь деклараций прокси-ролей: идентификаторы ресурсов,
// метки владения, конфиги и строители argv. Ресурсы и сами роли живут в
// подпакетах; здесь — только данные, общие для всех.
package roles

import "github.com/hoaxisr/awg-manager/internal/proxyrt"

// Идентификаторы ресурсов. Стабильные: уходят в API и журнал.
const (
	RProcess          proxyrt.ResourceID = "process"
	RNdmsIface        proxyrt.ResourceID = "ndms_interface"
	RPolicyExit       proxyrt.ResourceID = "policy_exit"
	RNdmsAddress      proxyrt.ResourceID = "ndms_address"
	RAdminState       proxyrt.ResourceID = "ndms_admin_state"
	RTunHandoff       proxyrt.ResourceID = "tun_handoff"
	RPolicyMembership proxyrt.ResourceID = "policy_membership"
	RClientRoutes     proxyrt.ResourceID = "client_routes"
	RRoutableExit     proxyrt.ResourceID = "routable_exit"
	RLinkedEndpoint   proxyrt.ResourceID = "linked_endpoint"
	RListenPort       proxyrt.ResourceID = "listen_port"
	RInputPort        proxyrt.ResourceID = "input_port"
	RNatRules         proxyrt.ResourceID = "nat_rules"
	RForwardRules     proxyrt.ResourceID = "forward_rules"
	RMssClamp         proxyrt.ResourceID = "mss_clamp"
	RNetfilterHook    proxyrt.ResourceID = "netfilter_hook"
	RIngressRefs      proxyrt.ResourceID = "ingress_refs"
	RNdmsAccess       proxyrt.ResourceID = "ndms_access"
)

// Sub различает два одноимённых ресурса одной декларации: у wdtt-сервера две
// NDMS-половины. Дубль ResourceID молча перенаправляет шаг на чужой объект
// (финальное ревью плана 1, I2) — поэтому суффикс обязателен.
func Sub(id proxyrt.ResourceID, suffix string) proxyrt.ResourceID {
	return id + proxyrt.ResourceID(":"+suffix)
}

// Значения impl и role протокола (§1 спеки протокола): зашиты в бинари,
// сверяются в hello и в пробе --awgm-protocol.
const (
	ImplWtClient   = "wt-client"
	ImplWdttServer = "wdtt-server"
	ImplFtClient   = "freeturn-client"
	ImplFtServer   = "freeturn-server"

	RoleClient = "client"
	RoleServer = "server"
)

// RuntimeDir — каталог сокетов и журналов обвязки (§2/§3 протокола, tmpfs).
const RuntimeDir = "/tmp/awgm"
