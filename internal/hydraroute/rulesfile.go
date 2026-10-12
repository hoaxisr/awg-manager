package hydraroute

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"sort"
	"strings"
)

// RuleSet — содержимое domain.conf и ip.list в модели HRweb 1.65: правило =
// цель (политика Keenetic или интерфейс DirectRoute), внутри — группы.
// Разбор и запись повторяют parseDomainFile / parseCIDRFile / linkGroups /
// buildDomainFile / buildCIDRFile HRweb (F629). Сознательные расхождения:
// пустая строка в ip.list закрывает блок, как у демона (scan_cidrfile_blocks);
// `#` на CIDR-строке (релизы до 5434e2147, #1022) снимается, половина
// выключается; порядок CIDR-блоков цели следует порядку групп; пустые
// половины не пишутся; служебная секция пишется в конец; список доменов
// нормализуется.
type RuleSet struct {
	Targets   []Target
	Oversized []string // geoip-теги служебной секции `##impossible to use`
	Revision  string   // ruleRevision файлов, из которых собран документ
}

type Target struct {
	Name   string
	Groups []Group
}

// Group — группа HRweb: строка domain.conf и/или блок ip.list. Флаги половин
// независимы, как у HRweb, и переживают нашу запись без изменений.
type Group struct {
	Description     string
	Domains         []string
	Subnets         []string
	DomainsDisabled bool
	SubnetsDisabled bool
}

// Enabled — включена хотя бы одна присутствующая половина.
func (g Group) Enabled() bool {
	return (len(g.Domains) > 0 && !g.DomainsDisabled) || (len(g.Subnets) > 0 && !g.SubnetsDisabled)
}

// SetEnabled ставит обе половины разом — так работает переключатель группы.
func (g *Group) SetEnabled(on bool) {
	g.DomainsDisabled = !on
	g.SubnetsDisabled = !on
}

func (rs *RuleSet) Target(name string) *Target {
	for i := range rs.Targets {
		if rs.Targets[i].Name == name {
			return &rs.Targets[i]
		}
	}
	return nil
}

func (rs *RuleSet) ensureTarget(name string) *Target {
	if t := rs.Target(name); t != nil {
		return t
	}
	rs.Targets = append(rs.Targets, Target{Name: name})
	return &rs.Targets[len(rs.Targets)-1]
}

func ruleRevision(domainConf, ipList string) string {
	h := sha256.New()
	h.Write([]byte(domainConf))
	h.Write([]byte{0})
	h.Write([]byte(ipList))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// parsed* — промежуточные половины до склейки (linkGroups HRweb).
type parsedHalf struct {
	target      string
	description string
	header      bool // была явная строка `##` перед половиной
	items       []string
	disabled    bool
	linked      bool
}

func parseRuleFiles(domainConf, ipList string) RuleSet {
	doms := parseDomainHalves(domainConf)
	cidrs, oversized := parseCIDRHalves(ipList)

	rs := RuleSet{Oversized: oversized, Revision: ruleRevision(domainConf, ipList)}
	for _, d := range doms {
		g := Group{Description: d.description, Domains: d.items, DomainsDisabled: d.disabled}
		if d.header {
			for i := range cidrs {
				c := &cidrs[i]
				if !c.linked && c.header && c.target == d.target && c.description == d.description {
					c.linked = true
					g.Subnets, g.SubnetsDisabled = c.items, c.disabled
					break
				}
			}
		}
		t := rs.ensureTarget(d.target)
		t.Groups = append(t.Groups, g)
	}
	for _, c := range cidrs {
		if c.linked {
			continue
		}
		t := rs.ensureTarget(c.target)
		t.Groups = append(t.Groups, Group{Description: c.description, Subnets: c.items, SubnetsDisabled: c.disabled})
	}
	return rs
}

func ruleLines(content string) []string {
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return lines
}

func parseDomainHalves(content string) []parsedHalf {
	var out []parsedHalf
	desc, header := "", false
	for _, line := range ruleLines(content) {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "##") {
			desc, header = strings.TrimSpace(line[2:]), true
			continue
		}
		disabled := strings.HasPrefix(line, "#")
		if disabled {
			line = line[1:]
		}
		slash := strings.LastIndex(line, "/")
		if slash < 0 {
			continue
		}
		target := strings.TrimSpace(line[slash+1:])
		if target == "" {
			continue
		}
		out = append(out, parsedHalf{
			target: target, description: desc, header: header,
			items: splitNonEmpty(line[:slash], ","), disabled: disabled,
		})
		desc, header = "", false
	}
	return out
}

// legacyDisabledCIDR — остаток `#…` строки CIDR-блока, который писали
// релизы до 5434e2147: geoip-тег, подсеть или адрес. Прочее — комментарий.
func legacyDisabledCIDR(rest string) bool {
	if strings.HasPrefix(rest, "geoip:") {
		return true
	}
	if _, err := netip.ParsePrefix(rest); err == nil {
		return true
	}
	_, err := netip.ParseAddr(rest)
	return err == nil
}

func parseCIDRHalves(content string) (halves []parsedHalf, oversized []string) {
	var cur *parsedHalf
	desc, header := "", false
	service := false
	flush := func() {
		if cur != nil {
			halves = append(halves, *cur)
		}
		cur = nil
		desc, header, service = "", false, false
	}
	for _, line := range ruleLines(content) {
		switch {
		case line == "":
			// Демон закрывает блок пустой строкой; HRweb её пропускает.
			// Вне блока описание из `##` не сбрасываем — как HRweb (демон
			// описаний не читает).
			if cur != nil || service {
				flush()
			}
		case strings.HasPrefix(line, "##"):
			flush()
			desc, header = strings.TrimSpace(line[2:]), true
		case strings.HasPrefix(line, "/"), strings.HasPrefix(line, "#/"):
			disabled := strings.HasPrefix(line, "#")
			target := strings.TrimSpace(strings.TrimLeft(line, "#")[1:])
			d, h := desc, header
			flush()
			if target == "" {
				continue
			}
			if disabled && h && isOversizedSection(d, target) {
				service = true
				continue
			}
			cur = &parsedHalf{target: target, description: d, header: h, disabled: disabled}
		case service:
			if t := strings.TrimPrefix(line, "#"); strings.HasPrefix(t, "geoip:") {
				oversized = append(oversized, t)
			}
		case cur != nil:
			// Второе сознательное расхождение с HRweb: `#` на подсети писали
			// релизные awg-manager до 5434e2147 (#1022). Снимаем и выключаем
			// половину, следующая запись даёт формат HR Neo.
			// Только если остаток похож на CIDR/IP/geoip: прочее `#…` HRweb
			// держит как есть.
			if rest := strings.TrimPrefix(line, "#"); rest != line && legacyDisabledCIDR(rest) {
				line = rest
				cur.disabled = true
			}
			cur.items = append(cur.items, line)
		}
	}
	flush()
	return halves, oversized
}

func renderRuleFiles(rs RuleSet) (domainConf, ipList string) {
	targets := append([]Target(nil), rs.Targets...)
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })

	var dl, il []string
	for _, t := range targets {
		for _, g := range t.Groups {
			paired := len(g.Domains) > 0 && len(g.Subnets) > 0
			header := g.Description != "" || paired
			if len(g.Domains) > 0 {
				if header {
					dl = append(dl, "##"+g.Description)
				}
				line := strings.Join(g.Domains, ",") + "/" + t.Name
				if g.DomainsDisabled {
					line = "#" + line
				}
				dl = append(dl, line)
			}
			if len(g.Subnets) > 0 {
				if header {
					il = append(il, "##"+g.Description)
				}
				if g.SubnetsDisabled {
					il = append(il, "#/"+t.Name)
				} else {
					il = append(il, "/"+t.Name)
				}
				il = append(il, g.Subnets...)
			}
		}
	}
	il = append(il, oversizedLines(rs.Oversized)...)
	return strings.Join(dl, "\n"), strings.Join(il, "\n")
}

// oversizedLines — служебная секция HR Neo в его формате
// (cidrfile_migrate_oversized): теги в нижнем регистре, без повторов;
// без тегов секции нет.
func oversizedLines(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		if len(out) == 0 {
			out = append(out, "##"+oversizedSectionName, "#/"+oversizedSectionTarget)
		}
		out = append(out, t)
	}
	return out
}
