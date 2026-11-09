package autolist

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestPatchConfigPreservesOmittedFields(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	if err := eng.LoadConfig(t.TempDir() + "/config.json"); err != nil {
		t.Fatal(err)
	}
	initial := Config{
		AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0", PacketEnabled: true,
		ExcludeDomains: []string{"keep.example"}, ResolveVia: "system",
	}
	if err := eng.SetConfig(initial); err != nil {
		t.Fatal(err)
	}
	if err := eng.PatchConfig([]byte(`{"verbose":true}`)); err != nil {
		t.Fatal(err)
	}
	got := eng.Config()
	if !got.AutoAdd || !got.TunnelProbe || got.TargetInterface != "nwg0" || !got.PacketEnabled || got.ResolveVia != "system" || !reflect.DeepEqual(got.ExcludeDomains, initial.ExcludeDomains) {
		t.Fatalf("omitted fields changed: %+v", got)
	}
	if err := eng.PatchConfig([]byte(`{"autoAdd":false,"excludeDomains":[]}`)); err != nil {
		t.Fatal(err)
	}
	got = eng.Config()
	if got.AutoAdd || len(got.ExcludeDomains) != 0 || got.TargetInterface != "nwg0" || !got.TunnelProbe || !got.PacketEnabled {
		t.Fatalf("explicit false/empty or preserved fields wrong: %+v", got)
	}
}

func TestPatchConfigClearsExplicitEmptyStrings(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	initial := Config{
		AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0", TargetOutbound: "legacy",
		ProbeInterface: "eth3", WANCheckURL: "https://old.example/check", ResolveVia: "system",
	}
	if err := eng.SetConfig(initial); err != nil {
		t.Fatal(err)
	}
	if err := eng.PatchConfig([]byte(`{"autoAdd":false,"targetInterface":"","targetOutbound":"","probeInterface":"","wanCheckUrl":"","resolveVia":""}`)); err != nil {
		t.Fatal(err)
	}
	got := eng.Config()
	if got.TargetInterface != "" || got.TargetOutbound != "" || got.ProbeInterface != "" || got.WANCheckURL != "" || got.ResolveVia != "" {
		t.Fatalf("explicitly cleared fields survived merge: %+v", got)
	}
}

func TestConcurrentConfigUpdatesDoNotLoseChanges(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	if err := eng.LoadConfig(t.TempDir() + "/config.json"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		if err := eng.PatchConfig([]byte(`{"verbose":true}`)); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		if err := eng.ExcludeDomains([]string{"concurrent.example"}); err != nil {
			t.Error(err)
		}
	}()
	close(start)
	wg.Wait()
	cfg := eng.Config()
	if !cfg.Verbose || len(cfg.ExcludeDomains) != 1 || cfg.ExcludeDomains[0] != "concurrent.example" {
		t.Fatalf("concurrent update lost changes: %+v", cfg)
	}
}

func TestConfigAndDismissDoNotWaitForRouterWrite(t *testing.T) {
	awgm := newFakeAWGM(t)
	awgm.ruleSetListEntered = make(chan struct{})
	awgm.ruleSetListRelease = make(chan struct{})
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	addDone := make(chan struct{})
	go func() {
		_ = eng.AddToTunnel([]string{"blocking-router.example"})
		close(addDone)
	}()
	select {
	case <-awgm.ruleSetListEntered:
	case <-time.After(time.Second):
		t.Fatal("router write did not start")
	}
	for name, operation := range map[string]func() error{
		"config": func() error { return eng.SetConfig(Config{Verbose: true}) },
		"dismiss": func() error {
			eng.Dismiss("other.example")
			return nil
		},
	} {
		done := make(chan error, 1)
		go func() { done <- operation() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s failed: %v", name, err)
			}
		case <-time.After(300 * time.Millisecond):
			t.Fatalf("%s blocked behind router HTTP", name)
		}
	}
	close(awgm.ruleSetListRelease)
	select {
	case <-addDone:
	case <-time.After(time.Second):
		t.Fatal("router write did not finish after release")
	}
}
