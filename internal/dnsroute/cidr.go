package dnsroute

import (
	"net"
	"net/netip"
)

// cidrCovers returns true if network a fully contains network b.
func cidrCovers(a, b *net.IPNet) bool {
	aOnes, aBits := a.Mask.Size()
	bOnes, bBits := b.Mask.Size()
	if aBits != bBits {
		return false
	}
	return aOnes <= bOnes && a.Contains(b.IP)
}

type parsedSubnet struct {
	raw      string
	net      *net.IPNet
	listID   string
	listName string
	// holes carries the owner list's ExcludeSubnets — pre-parsed so we
	// don't repeat work per candidate. A candidate that falls into any
	// hole is NOT covered by this list, even if cidrCovers reports true.
	holes []*net.IPNet
}

// candidateInHoles reports whether `c` falls inside any of `holes`,
// matching either an exact CIDR or a parent CIDR (subtree semantics).
func candidateInHoles(c *net.IPNet, holes []*net.IPNet) bool {
	for _, h := range holes {
		if cidrCovers(h, c) {
			return true
		}
	}
	return false
}

// subnetIndex — сети в порядке добавления с двумя ключами: каноничная
// строка (n.String()) для точного совпадения и netip.Prefix для поиска
// покрывающих. Ищется не перебором, а проходом по длинам префикса
// кандидата: сеть, покрывающая кандидата, равна его адресу, обрезанному по
// её длине (F699: перебор был O(n·(m+n)), 20 тыс. подсетей — секунды).
// Ключ-Prefix сравним и без выделений памяти; строка на каждую длину
// префикса стоила миллион выделений на 20 тыс. кандидатов.
type subnetIndex struct {
	byRaw    map[string][]int       // строка → номера в entries, по возрастанию
	byPrefix map[netip.Prefix][]int // Prefix → номера в entries, по возрастанию
	// lens — длины префикса, что есть в индексе, по разрядности маски
	// (32/128): пустые длины не ищутся.
	lens    map[int]*[129]bool
	entries []parsedSubnet
}

func newSubnetIndex() *subnetIndex {
	return &subnetIndex{
		byRaw:    make(map[string][]int),
		byPrefix: make(map[netip.Prefix][]int),
		lens:     make(map[int]*[129]bool),
	}
}

// subnetPrefix — n как netip.Prefix. v4-mapped адрес остаётся 4in6 (16 байт
// у net.ParseCIDR), так что IPv4 и v4-mapped не путаются ключом; что
// покрывает на самом деле, решает cidrCovers.
func subnetPrefix(n *net.IPNet) netip.Prefix {
	addr, _ := netip.AddrFromSlice(n.IP)
	ones, _ := n.Mask.Size()
	return netip.PrefixFrom(addr, ones).Masked()
}

func (x *subnetIndex) add(p parsedSubnet) {
	i := len(x.entries)
	x.entries = append(x.entries, p)
	x.byRaw[p.raw] = append(x.byRaw[p.raw], i)
	key := subnetPrefix(p.net)
	x.byPrefix[key] = append(x.byPrefix[key], i)
	ones, bits := p.net.Mask.Size()
	if x.lens[bits] == nil {
		x.lens[bits] = new([129]bool)
	}
	x.lens[bits][ones] = true
}

// first возвращает запись, на которой прежний перебор по порядку
// остановился бы первым: точное совпадение строки (дыры не смотрятся) или
// покрытие (cidrCovers) вне дыр владельца. -1 — такой нет.
func (x *subnetIndex) first(n *net.IPNet, normalized string) int {
	// Точное совпадение — по строке, без оглядки на разрядность: v4-mapped
	// сеть печатается как IPv4 ("::ffff:a.b.c.d/120" → "a.b.c.d/24"), и
	// прежний перебор сравнивал именно строки.
	best := -1
	if ids := x.byRaw[normalized]; len(ids) > 0 {
		best = ids[0]
	}
	ones, bits := n.Mask.Size()
	lens := x.lens[bits]
	if lens == nil {
		return best
	}
	addr, _ := netip.AddrFromSlice(n.IP)
	for p := 0; p <= ones; p++ {
		if !lens[p] {
			continue
		}
		parent, err := addr.Prefix(p)
		if err != nil {
			continue
		}
		for _, i := range x.byPrefix[parent] {
			if best != -1 && i >= best {
				break
			}
			e := x.entries[i]
			// cidrCovers отсекает то, что совпало ключом, но не покрывает по
			// правилам net.IPNet.Contains (обычный v6 против v4-mapped).
			if e.raw == normalized || (cidrCovers(e.net, n) && !candidateInHoles(n, e.holes)) {
				best = i
				break
			}
		}
	}
	return best
}

func dedupSubnets(input []string, currentListID, currentListName string, existingLists []DomainList) ([]string, DedupeReport) {
	report := DedupeReport{TotalInput: len(input)}
	if len(input) == 0 {
		return nil, report
	}

	existing := newSubnetIndex()
	for i := range existingLists {
		if existingLists[i].ID == currentListID {
			continue
		}
		var holes []*net.IPNet
		for _, h := range existingLists[i].ExcludeSubnets {
			if _, hn, err := net.ParseCIDR(h); err == nil {
				holes = append(holes, hn)
			}
		}
		for _, s := range existingLists[i].Subnets {
			_, n, err := net.ParseCIDR(s)
			if err != nil {
				continue
			}
			existing.add(parsedSubnet{
				raw:      n.String(),
				net:      n,
				listID:   existingLists[i].ID,
				listName: existingLists[i].Name,
				holes:    holes,
			})
		}
	}

	var kept []string
	keptIdx := newSubnetIndex()

	for _, raw := range input {
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			report.TotalInput--
			continue
		}
		normalized := n.String()

		// Совпадение с чужим списком важнее совпадения внутри своего —
		// как в прежнем порядке проверок.
		var by *parsedSubnet
		listID, listName := currentListID, currentListName
		if i := existing.first(n, normalized); i >= 0 {
			by = &existing.entries[i]
			listID, listName = by.listID, by.listName
		} else if i := keptIdx.first(n, normalized); i >= 0 {
			by = &keptIdx.entries[i]
		}
		if by != nil {
			report.TotalRemoved++
			reason := "subnet_covered"
			if by.raw == normalized {
				reason = "exact"
				report.ExactDupes++
			} else {
				report.WildcardDupes++
			}
			report.Items = append(report.Items, DedupeItem{Domain: normalized, Reason: reason, CoveredBy: by.raw, ListID: listID, ListName: listName})
			continue
		}

		kept = append(kept, normalized)
		keptIdx.add(parsedSubnet{raw: normalized, net: n, listID: currentListID, listName: currentListName})
	}

	report.TotalKept = len(kept)
	return kept, report
}
