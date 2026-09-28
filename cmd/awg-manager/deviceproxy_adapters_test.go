package main

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

type fakeCompositeLister []router.CompositeOutboundView

func (f fakeCompositeLister) ListCompositeOutbounds(context.Context) ([]router.CompositeOutboundView, error) {
	return f, nil
}

// Direct на opkgtun7 вырезается из эффективного конфига, пока интерфейс не
// отмечен сторонним; каталог device-proxy обязан это повторять (issue #935).
func TestDeviceproxyRouterOutbounds_ForeignDirectListed(t *testing.T) {
	src := fakeCompositeLister{
		{Source: "router", Outbound: router.Outbound{Type: "direct", Tag: "csqtt", BindInterface: "opkgtun7"}},
	}
	tags := func(foreign func() []string) []string {
		a := &deviceproxyRouterOutboundsAdapter{src: src, foreign: foreign}
		var out []string
		for _, o := range a.ListDeviceProxyRouterOutbounds() {
			out = append(out, o.Tag)
		}
		return out
	}
	if got := tags(func() []string { return []string{"opkgtun7"} }); len(got) != 1 || got[0] != "csqtt" {
		t.Fatalf("отмеченный: %v, ждали [csqtt]", got)
	}
	if got := tags(nil); len(got) != 0 {
		t.Fatalf("без отметки: %v, ждали пусто", got)
	}
}
