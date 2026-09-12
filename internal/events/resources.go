package events

// EventResourceInvalidated — тип SSE-события с подсказкой инвалидации.
const EventResourceInvalidated = "resource:invalidated"

// Resource — ключ подсказки инвалидации. Набор ЗАКРЫТ: каждое значение
// обязано быть известно фронту (union `ResourceKey` в
// `frontend/src/lib/stores/storeRegistry.ts`), иначе публикация уходит в
// никуда — `invalidateResource` на неизвестном ключе молча ничего не делает.
// Сверку держит TestResourceKeys_KnownToFrontend.
//
// Набор живёт здесь, а не в internal/api, потому что публикуют его и те
// пакеты, которым api импортировать нельзя (цикл): deviceproxy,
// singbox/router, ndms/command, orchestrator, pingcheck. Пока ключи лежали в
// api, эти пятеро писали их строковыми литералами и своими копиями хелпера
// в обход общего набора.
type Resource string

const (
	ResourceTunnels        Resource = "tunnels"
	ResourceServers        Resource = "servers"
	ResourceSettings       Resource = "settings"
	ResourceSysInfo        Resource = "sysInfo"
	ResourcePingcheck      Resource = "pingcheck"
	ResourceSaveStatus     Resource = "saveStatus"
	ResourceAwg3           Resource = "awg3"
	ResourceProxyInstances Resource = "proxyrt.instances"
	ResourceMcpKeys        Resource = "mcpKeys"

	// ResourceAmneziaPremiumKey — состояние ключа подписки Amnezia Premium
	// (есть ли сохранённый шифротекст и читается ли он). Своё, а не
	// ResourceSettings: шифротекст в ответ настроек не входит, и перечитывать
	// по нему нечего — состояние ключа отдаёт отдельная ручка.
	ResourceAmneziaPremiumKey Resource = "amneziaPremium.key"

	// ResourceAmneziaPremiumCatalog — данные подписки у ПОРТАЛА: счётчик
	// устройств и список уже выданных конфигураций. Меняет их выдача
	// конфигурации страны — расходная операция. Своё, а не ключ выше: там
	// наше состояние (лежит ли шифротекст у нас), здесь чужое, и читает его
	// отдельная ручка /amnezia/premium/catalog.
	ResourceAmneziaPremiumCatalog Resource = "amneziaPremium.catalog"

	// ResourceAmneziaPremiumMirror — адрес зеркала Amnezia. Своё, а не
	// ResourceSettings: поле ушло из общего ответа настроек и отдаётся
	// отдельной ручкой /amnezia/premium/mirror, так что подсказка по
	// настройкам его читателя не разбудит.
	ResourceAmneziaPremiumMirror Resource = "amneziaPremium.mirror"

	ResourceSingboxStatus        Resource = "singbox.status"
	ResourceSingboxTunnels       Resource = "singbox.tunnels"
	ResourceSingboxRouterStaging Resource = "singbox.router.staging"
	ResourceSingboxRouterRules   Resource = "singbox.router.rules"
	ResourceBypassSet            Resource = "bypass-set"

	ResourceRoutingTunnels          Resource = "routing.tunnels"
	ResourceRoutingDnsRoutes        Resource = "routing.dnsRoutes"
	ResourceRoutingStaticRoutes     Resource = "routing.staticRoutes"
	ResourceRoutingAccessPolicies   Resource = "routing.accessPolicies"
	ResourceRoutingPolicyDevices    Resource = "routing.policyDevices"
	ResourceRoutingPolicyInterfaces Resource = "routing.policyInterfaces"
	ResourceRoutingClientRoutes     Resource = "routing.clientRoutes"
	ResourceRoutingHydrarouteStatus Resource = "routing.hydrarouteStatus"

	ResourceDeviceProxyConfig  Resource = "deviceproxy.config"
	ResourceDeviceProxyRuntime Resource = "deviceproxy.runtime"
)

// AllResources — перечень всего набора для сверки с фронтом.
// Новый ключ добавляется И сюда, иначе TestResourceKeys_Inventory краснеет.
var AllResources = []Resource{
	ResourceTunnels,
	ResourceServers,
	ResourceSettings,
	ResourceSysInfo,
	ResourcePingcheck,
	ResourceSaveStatus,
	ResourceAwg3,
	ResourceProxyInstances,
	ResourceMcpKeys,
	ResourceAmneziaPremiumKey,
	ResourceAmneziaPremiumCatalog,
	ResourceAmneziaPremiumMirror,
	ResourceSingboxStatus,
	ResourceSingboxTunnels,
	ResourceSingboxRouterStaging,
	ResourceSingboxRouterRules,
	ResourceBypassSet,
	ResourceRoutingTunnels,
	ResourceRoutingDnsRoutes,
	ResourceRoutingStaticRoutes,
	ResourceRoutingAccessPolicies,
	ResourceRoutingPolicyDevices,
	ResourceRoutingPolicyInterfaces,
	ResourceRoutingClientRoutes,
	ResourceRoutingHydrarouteStatus,
	ResourceDeviceProxyConfig,
	ResourceDeviceProxyRuntime,
}

// Publisher — минимум шины, которого хватает для подсказки инвалидации.
// Нужен тем, кто держит не *Bus, а свой узкий интерфейс публикации
// (proxyrt, singbox/router).
type Publisher interface {
	Publish(eventType string, data any)
}

// PublishInvalidated — единственный способ послать подсказку инвалидации.
// Безопасен на nil-шине: обработчики в тестах конструируются без неё.
func (b *Bus) PublishInvalidated(res Resource, reason string) {
	if b == nil {
		return
	}
	PublishInvalidatedTo(b, res, reason)
}

// PublishInvalidatedTo — то же самое для держателей интерфейса.
func PublishInvalidatedTo(pub Publisher, res Resource, reason string) {
	if pub == nil {
		return
	}
	pub.Publish(EventResourceInvalidated, ResourceInvalidatedEvent{
		Resource: res,
		Reason:   reason,
	})
}
