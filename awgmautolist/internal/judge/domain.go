package judge

import "strings"

var genericSLD = map[string]struct{}{
	"ac": {}, "biz": {}, "co": {}, "com": {}, "edu": {}, "gen": {}, "go": {},
	"gob": {}, "gov": {}, "info": {}, "ltd": {}, "me": {}, "mil": {}, "ne": {},
	"net": {}, "nic": {}, "nom": {}, "or": {}, "org": {}, "plc": {}, "sch": {},
}

var geographicSuffix = map[string]struct{}{
	"msk.ru": {}, "spb.ru": {}, "nov.ru": {}, "msk.su": {}, "spb.su": {},
	"kiev.ua": {}, "kharkov.ua": {},
}

var sharedSuffix = map[string]struct{}{
	"appspot.com": {}, "azurewebsites.net": {}, "blogspot.com": {}, "cloudfront.net": {},
	"firebaseapp.com": {}, "fly.dev": {}, "github.io": {}, "githubusercontent.com": {},
	"gitlab.io": {}, "herokuapp.com": {}, "netlify.app": {}, "onrender.com": {},
	"pages.dev": {}, "r2.dev": {}, "vercel.app": {}, "web.app": {}, "workers.dev": {},
	"amazonaws.com": {}, "cloudapp.net": {}, "azureedge.net": {}, "trafficmanager.net": {},
	"repl.co": {}, "myshopify.com": {}, "wixsite.com": {}, "tumblr.com": {},
	"wordpress.com": {}, "livejournal.com": {}, "narod.ru": {}, "ucoz.ru": {},
	"tilda.ws": {}, "googleusercontent.com": {}, "akamaized.net": {}, "akamaihd.net": {},
	"edgekey.net": {}, "edgesuite.net": {}, "fastly.net": {}, "b-cdn.net": {},
	"cloudflarestorage.com": {},
}

var sharedThreeLabelSuffix = map[string]struct{}{"core.windows.net": {}}

func inSet(s string, set map[string]struct{}) bool {
	_, ok := set[s]
	return ok
}

func asciiLowerLetter(c byte) bool { return c >= 'a' && c <= 'z' }

func twoLabelSuffix(s string) bool {
	if inSet(s, geographicSuffix) || inSet(s, sharedSuffix) {
		return true
	}
	dot := strings.IndexByte(s, '.')
	if dot < 0 || len(s)-dot-1 != 2 || !asciiLowerLetter(s[dot+1]) || !asciiLowerLetter(s[dot+2]) {
		return false
	}
	return inSet(s[:dot], genericSLD)
}

func addressLiteral(s string) bool {
	if strings.IndexByte(s, ':') >= 0 {
		return true
	}
	for i := range len(s) {
		if s[i] != '.' && (s[i] < '0' || s[i] > '9') {
			return false
		}
	}
	return true
}

func regDomain(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	host = cString(host)
	n := len(host)
	if n > 0 && host[n-1] == '.' {
		n--
	}
	if n == 0 || host[0] == '.' || addressLiteral(host[:n]) {
		return "", false
	}
	starts := [4]int{-1, -1, -1, -1}
	found, end := 0, n
	for found < 4 {
		i := end
		for i > 0 && host[i-1] != '.' {
			i--
		}
		if i == end {
			return "", false
		}
		starts[found] = i
		found++
		if i == 0 {
			break
		}
		end = i - 1
	}
	if found < 2 {
		return "", false
	}
	var take int
	if found >= 3 && inSet(host[starts[2]:n], sharedThreeLabelSuffix) {
		if found < 4 {
			return "", false
		}
		take = n - starts[3]
	} else if twoLabelSuffix(host[starts[1]:n]) {
		if found < 3 {
			return "", false
		}
		take = n - starts[2]
	} else {
		take = n - starts[1]
	}
	return host[n-take : n], true
}

// RegisteredDomain exposes the single host→rule-domain algorithm to autolist.
func RegisteredDomain(host string) (string, bool) { return regDomain(host) }

// IsSharedDomain reports a hosting suffix that must not become a broad rule.
func IsSharedDomain(domain string) bool { return regDomainShared(domain) }

func regDomainShared(domain string) bool {
	dot := strings.IndexByte(domain, '.')
	if dot < 0 {
		return false
	}
	suffix := domain[dot+1:]
	return inSet(suffix, sharedSuffix) || inSet(suffix, sharedThreeLabelSuffix)
}

func (j *Judge) domainFind(host string, take bool, now int64) *domainState {
	name, ok := regDomain(host)
	if !ok || regDomainShared(name) {
		return nil
	}
	freeAt, idle, oldest := -1, -1, -1
	for i := range j.domains {
		d := &j.domains[i]
		if d.used && d.name == name {
			d.seen = now
			return d
		}
		if !d.used {
			if freeAt < 0 {
				freeAt = i
			}
		} else if !j.domainCounting(d, now) {
			if idle < 0 || d.seen < j.domains[idle].seen {
				idle = i
			}
		} else if oldest < 0 || d.seen < j.domains[oldest].seen {
			oldest = i
		}
	}
	if !take {
		return nil
	}
	at := freeAt
	if at < 0 {
		at = idle
	}
	if at < 0 {
		at = oldest
	}
	if at < 0 {
		return nil
	}
	d := &j.domains[at]
	*d = domainState{
		used:   true,
		serial: j.nextSerial(),
		seen:   now,
		name:   name,
	}
	return d
}

func domainRestart(d *domainState) {
	d.fails = tally{}
	d.noConnect = tally{}
	d.notes = notes{}
	d.hostless = [kindCount]uint32{}
	d.nHosts = 0
}

func domainRestartKind(d *domainState, kind countKind) {
	switch kind {
	case kindFail:
		d.fails = tally{}
	case kindNoConnect:
		d.noConnect = tally{}
	case kindNote:
		d.notes = notes{}
	}
	d.hostless[kind] = 0
	for i := 0; i < int(d.nHosts); i++ {
		d.hosts[i].n[kind] = 0
	}
}

func (j *Judge) domainHost(e *domainHost) *hostState {
	if e.host < 0 || e.host >= len(j.hosts) {
		return nil
	}
	h := &j.hosts[e.host]
	if !h.used || h.serial != e.serial {
		return nil
	}
	return h
}

func (j *Judge) domainFire(d *domainState, kind countKind) {
	for i := 0; i < int(d.nHosts); i++ {
		h := j.domainHost(&d.hosts[i])
		if h == nil {
			continue
		}
		switch kind {
		case kindFail:
			hostRestart(h)
		case kindNoConnect:
			h.noConnect.n = 0
		case kindNote:
			h.notes.n = 0
		}
	}
	domainRestartKind(d, kind)
}

func slotTotal(e *domainHost) uint32 {
	return e.n[kindFail] + e.n[kindNoConnect] + e.n[kindNote]
}

func (j *Judge) flowNamedDomain(f *flowState, take bool, now int64) *domainState {
	if !f.domainNamed {
		return nil
	}
	var name, domain string
	if j.hintOnly(f.key.Client, f.key.Server, now, &name, &domain) != hintDomain {
		return nil
	}
	return j.domainFind(domain, take, now)
}

func (j *Judge) domainCredit(f *flowState, h *hostState, kind countKind, now int64) *domainState {
	var d *domainState
	if h != nil {
		d = j.domainFind(h.name, true, now)
	} else {
		d = j.flowNamedDomain(f, true, now)
	}
	if d == nil {
		return nil
	}
	d.seen = now
	if !j.domainCounting(d, now) {
		domainRestart(d)
	}
	if h == nil {
		d.hostless[kind]++
		return d
	}
	idx := f.host
	var slot *domainHost
	for i := 0; i < int(d.nHosts); i++ {
		if d.hosts[i].host == idx && d.hosts[i].serial == h.serial {
			slot = &d.hosts[i]
			break
		}
	}
	if slot == nil {
		if d.nHosts < domainHostCapacity {
			slot = &d.hosts[d.nHosts]
			d.nHosts++
		} else {
			slot = &d.hosts[0]
			for i := 1; i < domainHostCapacity; i++ {
				if slotTotal(&d.hosts[i]) < slotTotal(slot) {
					slot = &d.hosts[i]
				}
			}
		}
		*slot = domainHost{}
	}
	slot.host = idx
	slot.serial = h.serial
	slot.n[kind]++
	return d
}

func (j *Judge) domainMarker(d *domainState, kind countKind) string {
	top := d.name
	most := d.hostless[kind]
	for i := 0; i < int(d.nHosts); i++ {
		e := &d.hosts[i]
		if e.n[kind] <= most {
			continue
		}
		h := j.domainHost(e)
		if h == nil {
			continue
		}
		top = h.name
		most = e.n[kind]
	}
	return top
}
