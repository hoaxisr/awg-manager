package router

import (
	"regexp"
	"testing"
)

// Метки правил iptables (--comment) обязаны быть из [_-0-9a-zA-Z]: iptables-save такие не
// закавычивает, и removeCommentTaggedRulesFromTable (strings.Fields по строке -S) снимает
// их корректно. Метка с пробелом/кавычкой уехала бы в `-D` двумя аргументами и не снялась бы.
func TestCommentTags_AlphabetSafeForFieldsSplit(t *testing.T) {
	safe := regexp.MustCompile(`^[_\-0-9a-zA-Z]+$`)
	for name, tag := range map[string]string{
		"DNSRescueTag":     DNSRescueTag,
		"IngressTag":       IngressTag,
		"DNSNoPolicyTag":   DNSNoPolicyTag,
		"FakeIPIngressTag": FakeIPIngressTag,
		"PolicyTunDNSTag":  PolicyTunDNSTag,
	} {
		if !safe.MatchString(tag) {
			t.Errorf("%s = %q содержит символ вне [_-0-9a-zA-Z]", name, tag)
		}
	}
}
