// Package peersubnet — чистая логика сетей клиента WG-сервера (#713): строка
// AllowedIPs клиента, сети за клиентом (site-to-site) и их применение на
// роутере. Без RCI и хранилища: пакет используют оба пути (системный сервер и
// managed), поэтому у него только stdlib.
package peersubnet

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// DefaultClientAllowedIPs — AllowedIPs клиента при пустом поле: весь трафик
// через туннель, как было до #713.
const DefaultClientAllowedIPs = "0.0.0.0/0, ::/0"

var (
	// ErrInvalidClientAllowedIPs — элемент не CIDR (в т.ч. хост без префикса).
	ErrInvalidClientAllowedIPs = errors.New("AllowedIPs клиента: список CIDR через запятую")
	// ErrInvalidRemoteSubnets — не IPv4 CIDR, 0.0.0.0/0 или пересечение внутри списка.
	ErrInvalidRemoteSubnets = errors.New("сети за клиентом: IPv4 CIDR без пересечений")
	// ErrRemoteSubnetOverlap — пересечение с занятой сетью; текст называет её.
	ErrRemoteSubnetOverlap = errors.New("сеть за клиентом занята")
)

// ValidateClientAllowedIPs канонизирует список CIDR через запятую (v4 и v6
// вперемешку); "" → "". Пересечения внутри списка допустимы: WireGuard берёт
// самый длинный префикс. Значение уезжает в .conf строкой — поэтому ничего,
// кроме разобранных CIDR, наружу не выходит (та же причина, что у ValidatePeerDNS).
func ValidateClientAllowedIPs(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			return "", fmt.Errorf("%w: %q", ErrInvalidClientAllowedIPs, p)
		}
		out = append(out, n.String())
	}
	return strings.Join(out, ", "), nil
}

// Occupied — занятая сеть с меткой для текста ошибки («LAN Home»,
// «пир «office» сервера Wireguard0»).
type Occupied struct {
	Net   *net.IPNet
	Label string
}

// Overlaps — сети пересекаются (одна содержит адрес другой). Обе — с
// обнулёнными хостовыми битами, как отдаёт net.ParseCIDR.
func Overlaps(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

func parseV4Subnet(s string) (*net.IPNet, error) {
	ip, n, err := net.ParseCIDR(strings.TrimSpace(s))
	if err != nil || ip.To4() == nil || len(n.Mask) != net.IPv4len {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRemoteSubnets, strings.TrimSpace(s))
	}
	if ones, _ := n.Mask.Size(); ones == 0 {
		return nil, fmt.Errorf("%w: %q — весь адресный диапазон", ErrInvalidRemoteSubnets, strings.TrimSpace(s))
	}
	return n, nil
}

// ValidateRemoteSubnets — канонический список сетей за клиентом. Пустые
// элементы пропускаются. Порядок проверок на элемент: форма → пересечение
// внутри списка → пересечение с occupied.
func ValidateRemoteSubnets(subnets []string, occupied []Occupied) ([]string, error) {
	nets := make([]*net.IPNet, 0, len(subnets))
	out := make([]string, 0, len(subnets))
	for _, s := range subnets {
		if strings.TrimSpace(s) == "" {
			continue
		}
		n, err := parseV4Subnet(s)
		if err != nil {
			return nil, err
		}
		for i, prev := range nets {
			if Overlaps(prev, n) {
				return nil, fmt.Errorf("%w: %s и %s пересекаются", ErrInvalidRemoteSubnets, out[i], n)
			}
		}
		for _, o := range occupied {
			if o.Net != nil && Overlaps(o.Net, n) {
				return nil, fmt.Errorf("%w: %s пересекается с %s (%s)", ErrRemoteSubnetOverlap, n, o.Net, o.Label)
			}
		}
		nets = append(nets, n)
		out = append(out, n.String())
	}
	return out, nil
}

// Diff — добавленные и ушедшие сети между двумя каноническими списками,
// в порядке появления.
func Diff(old, new []string) (added, removed []string) {
	oldSet := make(map[string]bool, len(old))
	for _, s := range old {
		oldSet[s] = true
	}
	newSet := make(map[string]bool, len(new))
	for _, s := range new {
		newSet[s] = true
		if !oldSet[s] {
			added = append(added, s)
		}
	}
	for _, s := range old {
		if !newSet[s] {
			removed = append(removed, s)
		}
	}
	return added, removed
}

// Exclude — минимальное покрытие IPv4-сети base без сетей minus: сеть делится
// пополам, пока половина либо не пересекается с minus (в результат), либо
// целиком лежит в minus (выбрасывается). Результат по возрастанию адресов.
func Exclude(base *net.IPNet, minus []*net.IPNet) []*net.IPNet {
	ip4 := base.IP.To4()
	if ip4 == nil {
		return nil
	}
	var out []*net.IPNet
	var walk func(n *net.IPNet)
	walk = func(n *net.IPNet) {
		ones, bits := n.Mask.Size()
		hit := false
		for _, m := range minus {
			mOnes, _ := m.Mask.Size()
			if m.Contains(n.IP) && mOnes <= ones { // m покрывает n целиком
				return
			}
			hit = hit || Overlaps(m, n)
		}
		if !hit {
			out = append(out, n)
			return
		}
		if ones == bits {
			return
		}
		mask := net.CIDRMask(ones+1, bits)
		lo := &net.IPNet{IP: n.IP.Mask(mask), Mask: mask}
		hiIP := make(net.IP, len(lo.IP))
		copy(hiIP, lo.IP)
		hiIP[ones/8] |= 1 << (7 - uint(ones%8))
		walk(lo)
		walk(&net.IPNet{IP: hiIP, Mask: mask})
	}
	baseMask := net.CIDRMask(maskOnes(base), 32)
	walk(&net.IPNet{IP: ip4.Mask(baseMask), Mask: baseMask})
	return out
}

func maskOnes(n *net.IPNet) int {
	ones, _ := n.Mask.Size()
	return ones
}

// Presets — строки для поля «AllowedIPs клиента» в его формате.
type Presets struct {
	RouterOnly   string `json:"routerOnly"`
	ExceptRouter string `json:"exceptRouter"`
}

// BuildPresets: RouterOnly = подсеть сервера + LAN-сети (без дублей);
// ExceptRouter = 0.0.0.0/0 минус они + /32 каждого IPv4-резолвера, попавшего
// в вычтенное (иначе клиент теряет DNS: резолвер по умолчанию — LAN-адрес
// роутера), + ::/0 целиком.
func BuildPresets(serverSubnet *net.IPNet, lanSubnets []*net.IPNet, dnsIPs []net.IP) Presets {
	var router []*net.IPNet
	seen := map[string]bool{}
	for _, n := range append([]*net.IPNet{serverSubnet}, lanSubnets...) {
		if n == nil || n.IP.To4() == nil {
			continue
		}
		// Сети интерфейсов приходят адресом с маской (10.9.0.1/24), а
		// IPNet.String() хост-биты не сбрасывает.
		n = &net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask}
		if seen[n.String()] {
			continue
		}
		seen[n.String()] = true
		router = append(router, n)
	}
	_, all, _ := net.ParseCIDR("0.0.0.0/0")
	rest := Exclude(all, router)
	except := make([]string, 0, len(rest)+len(dnsIPs)+1)
	for _, n := range rest {
		except = append(except, n.String())
	}
	seenDNS := map[string]bool{}
	for _, ip := range dnsIPs {
		ip4 := ip.To4()
		if ip4 == nil || seenDNS[ip4.String()] {
			continue
		}
		seenDNS[ip4.String()] = true
		for _, r := range router {
			if r.Contains(ip4) {
				except = append(except, ip4.String()+"/32")
				break
			}
		}
	}
	except = append(except, "::/0")
	routerStrs := make([]string, len(router))
	for i, n := range router {
		routerStrs[i] = n.String()
	}
	return Presets{RouterOnly: strings.Join(routerStrs, ", "), ExceptRouter: strings.Join(except, ", ")}
}

// RouteComment — метка нашего маршрута: первые 8 символов ключа как есть
// (`+` и `/` роутер хранит, стенд 5.02.A.11, 27.09.2026).
func RouteComment(pubkey string) string {
	if len(pubkey) > 8 {
		pubkey = pubkey[:8]
	}
	return "awgm-peer:" + pubkey
}
