package api

import (
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	sysports "github.com/hoaxisr/awg-manager/internal/sys/ports"
)

// minClashPort — нижняя граница выбора. Привилегированные порты (<1024)
// отсекаем: awg-manager на роутере ходит от root, так что технически
// занять их смог бы, но соседство с системными службами — лишний риск.
const minClashPort = 1024

// clashPortInspector — подмножество sys/ports.Scanner, нужное валидации.
// Интерфейс, а не структура, чтобы тест подставлял свою занятость.
type clashPortInspector interface {
	InspectPort(port int, proto string) ([]sysports.Binding, error)
}

// reservedClashPort возвращает название СВОЕГО слушателя, за которым уже
// закреплён порт, или "" — если порт нам не принадлежит. Карта резервов
// читается из единственных источников правды (router, singbox), а не копией
// чисел: разъехаться нечему.
func reservedClashPort(port, serverPort int) string {
	firstTunnelPort, lastTunnelPort := singbox.TunnelInboundPortRange()
	switch {
	case port == serverPort:
		return "веб-интерфейс awg-manager"
	case port == router.TPROXYPort:
		return "TPROXY sing-box"
	case port == router.RedirectPort:
		return "REDIRECT sing-box"
	case port >= firstTunnelPort && port <= lastTunnelPort:
		return "входы туннелей sing-box и прокси для устройств"
	case inQoSPortRange(router.QoSTPROXYPortBase, port):
		return "QoS-классы (TPROXY)"
	case inQoSPortRange(router.QoSRedirectPortBase, port):
		return "QoS-классы (REDIRECT)"
	}
	return ""
}

func inQoSPortRange(base, port int) bool {
	return port >= base && port < base+router.MaxQoSClasses
}

// validateClashPort проверяет выбранный порт Clash API: диапазон, пересечение
// с нашей картой резервов и занятость чужим процессом. Возвращает текст
// ошибки для пользователя или "" — если порт годен.
//
// Занятость проверяется на СЕРВЕРЕ, а не на фронте: это валидация на границе
// доверия, и HTTP-ручки /system/ports/* закрыты expert-гейтом, то есть
// обычному пользователю недоступны (issue #788).
//
// Цена ошибки высокая: SIGHUP в sing-box — это Close + пересоздание инстанса,
// и если новый слушающий сокет занять не удалось, процесс выходит целиком.
//
// ponytail: между проверкой и рестартом остаётся TOCTOU-окно — порт может
// занять кто-то ещё в эти доли секунды. Принято осознанно: закрывать его
// пришлось бы вторым путём записи конфига с откатом, а видимость у отказа
// есть — FATAL уезжает в /logs через LogForwarder.
func validateClashPort(port, serverPort int, insp clashPortInspector) string {
	effective := singbox.EffectiveClashPort(port)
	if effective < minClashPort || effective > 65535 {
		return fmt.Sprintf("порт Clash API должен быть в диапазоне %d-65535", minClashPort)
	}
	if owner := reservedClashPort(effective, serverPort); owner != "" {
		return fmt.Sprintf("порт %d уже зарезервирован под %s", effective, owner)
	}
	if insp == nil {
		return ""
	}
	bindings, err := insp.InspectPort(effective, "tcp")
	if err != nil || len(bindings) == 0 {
		// Ошибку скана не считаем поводом отказать: /proc может быть
		// недоступен, а запрет из-за этого выглядел бы как отказ по занятости.
		return ""
	}
	return fmt.Sprintf("порт %d занят: %s", effective, describeBinding(bindings[0]))
}

// describeBinding собирает человекочитаемое имя держателя порта из того, что
// нашёл сканер: имя процесса, PID, путь и init.d-сервис заполнены не всегда.
func describeBinding(b sysports.Binding) string {
	name := b.ProcessName
	if name == "" {
		name = b.Exe
	}
	if name == "" {
		name = "неизвестный процесс"
	}
	if b.PID > 0 {
		name = fmt.Sprintf("%s (PID %d)", name, b.PID)
	}
	if b.Service != "" {
		name = fmt.Sprintf("%s, сервис %s", name, b.Service)
	}
	return name
}
