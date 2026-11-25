package tgwebproxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeMTProtoSecret(t *testing.T) {
	rawHex := "00112233445566778899aabbccddeeff"
	if got, err := NormalizeMTProtoSecret("  " + strings.ToUpper(rawHex) + " "); err != nil || got != rawHex {
		t.Fatalf("raw 32-hex failed: got=%q err=%v", got, err)
	}
	if got, err := NormalizeMTProtoSecret("dd" + rawHex); err != nil || got != rawHex {
		t.Fatalf("dd secret failed: got=%q err=%v", got, err)
	}
	fakeTLS := "ee" + rawHex + hex.EncodeToString([]byte("yandex.ru"))
	if got, err := NormalizeMTProtoSecret(fakeTLS); err != nil || got != rawHex {
		t.Fatalf("ee Fake-TLS secret failed: got=%q err=%v", got, err)
	}

	for _, bad := range []string{"", "1234", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", "ee" + rawHex, "aa" + rawHex} {
		if got, err := NormalizeMTProtoSecret(bad); !errors.Is(err, ErrInvalidSecret) {
			t.Fatalf("expected ErrInvalidSecret for %q, got=%q err=%v", bad, got, err)
		}
	}
}

func TestUserSecretEditRegenerateAndDeleteLastUser(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)
	svc.randReader = rand.Reader

	customHex := "11112222333344445555666677778888"
	if err := svc.UpdateUserWithOptions("default", UpdateUserOptions{
		Secret: "ee" + customHex + hex.EncodeToString([]byte("www.google.com")),
	}); err != nil {
		t.Fatalf("UpdateUserWithOptions manual secret failed: %v", err)
	}

	rev1, err := svc.RevealUserSecret("default")
	if err != nil {
		t.Fatalf("RevealUserSecret failed: %v", err)
	}
	if rev1.Users[0].Secret != customHex {
		t.Fatalf("expected secret %q, got %q", customHex, rev1.Users[0].Secret)
	}
	if !strings.HasPrefix(rev1.Users[0].MtproxySecret, "ee"+customHex) {
		t.Fatalf("expected mtproxy_secret to start with ee+customHex, got %q", rev1.Users[0].MtproxySecret)
	}

	// Create second user with custom secret; duplicate must fail cleanly.
	if err := svc.CreateUserWithSecret("Перенесённый", customHex); !errors.Is(err, ErrDuplicateSecret) {
		t.Fatalf("expected ErrDuplicateSecret, got %v", err)
	}
	otherHex := "aaaabbbbccccddddeeeeffff00001111"
	if err := svc.CreateUserWithSecret("Перенесённый", otherHex); err != nil {
		t.Fatalf("CreateUserWithSecret failed: %v", err)
	}
	st := svc.GetStatus()
	if len(st.Users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(st.Users))
	}
	secondID := st.Users[1].ID

	// Regenerate secret on second user
	if err := svc.UpdateUserWithOptions(secondID, UpdateUserOptions{RegenerateSecret: true}); err != nil {
		t.Fatalf("UpdateUserWithOptions RegenerateSecret failed: %v", err)
	}
	rev2, err := svc.RevealUserSecret(secondID)
	if err != nil {
		t.Fatalf("RevealUserSecret second user failed: %v", err)
	}
	var secondRevealed string
	for _, u := range rev2.Users {
		if u.ID == secondID {
			secondRevealed = u.Secret
		}
	}
	if len(secondRevealed) != 32 || secondRevealed == otherHex {
		t.Fatalf("expected regenerated 32-hex secret different from %q, got %q", otherHex, secondRevealed)
	}

	// Delete default user, then delete the last remaining user.
	if err := svc.DeleteUser("default"); err != nil {
		t.Fatalf("DeleteUser default failed: %v", err)
	}
	if err := svc.DeleteUser(secondID); err != nil {
		t.Fatalf("DeleteUser last user failed: %v", err)
	}
	if stEmpty := svc.GetStatus(); len(stEmpty.Users) != 0 {
		t.Fatalf("expected 0 users after deleting last user, got %+v", stEmpty.Users)
	}
	if secs := svc.GetInternalConfig().ActiveSecrets(); len(secs) != 0 {
		t.Fatalf("expected 0 active secrets after deleting last user, got %v", secs)
	}

	// Verify profiles.json still renders a valid dummy fallback with 0 users, and persists cleanly on reload.
	profBytes, err := GenerateProfilesJSON(svc.GetInternalConfig())
	if err != nil {
		t.Fatalf("GenerateProfilesJSON with 0 users failed: %v", err)
	}
	var profDoc struct {
		Profiles []struct {
			Secret string `json:"secret"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(profBytes, &profDoc); err != nil || len(profDoc.Profiles) == 0 || profDoc.Profiles[0].Secret == "" {
		t.Fatalf("unexpected profiles.json with 0 users: %s (err=%v)", string(profBytes), err)
	}

	// Reload from disk: empty users list must NOT resurrect "default".
	reloaded := &Service{dataDir: tempDir}
	if err := reloaded.loadAndMigrateConfig(); err != nil {
		t.Fatalf("reload config with 0 users failed: %v", err)
	}
	if len(reloaded.GetInternalConfig().GetUsers()) != 0 {
		t.Fatalf("expected 0 users after reload, got %+v", reloaded.GetInternalConfig().GetUsers())
	}

	// RevealUserSecret with 0 users returns empty Users slice cleanly.
	revEmpty, err := svc.RevealUserSecret("")
	if err != nil || len(revEmpty.Users) != 0 {
		t.Fatalf("RevealUserSecret on empty users failed: rev=%+v err=%v", revEmpty, err)
	}

	// Creating a new user after 0 users restores active users cleanly.
	if err := svc.CreateUserWithSecret("Новый", ""); err != nil {
		t.Fatalf("CreateUserWithSecret after 0 users failed: %v", err)
	}
	if got := svc.GetStatus().Users; len(got) != 1 || got[0].Remark != "Новый" {
		t.Fatalf("expected 1 recreated user, got %+v", got)
	}
}

func TestUpstreams_RuntimeSetAndValidationAndUpdateConfig(t *testing.T) {
	svc, _, _ := setupTestService(t)

	cfg := Config{
		Upstreams: []UpstreamSpec{
			{ID: "up-1", Type: "direct", Device: "nwg1", Weight: 10, Enabled: true},
			{ID: "up-2", Type: "socks5", Address: "127.0.0.1:1099", Username: "u1", Password: "p1", Weight: 2, Enabled: true},
			{ID: "up-3", Type: "direct", Device: "nwg9", Weight: 5, Enabled: false},
		},
	}

	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatalf("UpdateConfig multi-upstream failed: %v", err)
	}

	byID := map[string]UpstreamSpec{}
	for _, u := range svc.GetInternalConfig().EnabledUpstreams() {
		byID[u.ID] = u
	}
	if u := byID["up-1"]; u.Device != "nwg1" || u.Weight != 10 {
		t.Fatalf("expected nwg1 direct upstream in runtime set, got %+v", byID)
	}
	if u := byID["up-2"]; u.Address != "127.0.0.1:1099" || u.Username != "u1" || u.Password != "p1" || u.Weight != 2 {
		t.Fatalf("expected socks5 upstream in runtime set, got %+v", byID)
	}
	if _, ok := byID["up-3"]; ok {
		t.Fatalf("disabled upstream nwg9 must not be in runtime set: %+v", byID)
	}

	// Validation rejects bad SOCKS5 address, bad weight, all-disabled list.
	for _, invalid := range [][]UpstreamSpec{
		{{Type: "socks5", Address: "not-host-port", Weight: 1, Enabled: true}},
		{{Type: "direct", Device: "eth0", Weight: 70000, Enabled: true}},
		{{Type: "direct", Device: "eth0", Weight: 1, Enabled: false}},
	} {
		if err := ValidateUpstreams(invalid); !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("expected ErrInvalidUpstream for %+v, got %v", invalid, err)
		}
	}

	// Partial failover allowed: one invalid direct iface + one healthy direct WAN succeeds.
	err := svc.UpdateConfig(Config{
		Upstreams: []UpstreamSpec{
			{ID: "u-bad", Type: "direct", Device: "invalid_dev", Weight: 5, Enabled: true},
			{ID: "u-wan", Type: "direct", Device: "direct", Weight: 1, Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("expected multi-upstream with healthy backup to succeed, got %v", err)
	}

	// When all enabled upstreams fail validation, UpdateConfig must reject.
	err = svc.UpdateConfig(Config{
		Upstreams: []UpstreamSpec{
			{ID: "u-bad", Type: "direct", Device: "invalid_dev", Weight: 1, Enabled: true},
		},
	})
	if err == nil {
		t.Fatalf("expected error when all enabled upstreams fail validation")
	}
}

func startEchoOnceListener(t *testing.T, tag string) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen %s: %v", tag, err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte(tag))
			_ = c.Close()
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func startMinimalSocks5Server(t *testing.T, wantUser, wantPass string) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(client net.Conn) {
				defer client.Close()
				hdr := make([]byte, 2)
				if _, err := io.ReadFull(client, hdr); err != nil {
					return
				}
				methods := make([]byte, int(hdr[1]))
				if _, err := io.ReadFull(client, methods); err != nil {
					return
				}
				if wantUser != "" {
					_, _ = client.Write([]byte{0x05, 0x02})
					authHdr := make([]byte, 2)
					if _, err := io.ReadFull(client, authHdr); err != nil {
						return
					}
					u := make([]byte, int(authHdr[1]))
					if _, err := io.ReadFull(client, u); err != nil {
						return
					}
					pLen := make([]byte, 1)
					if _, err := io.ReadFull(client, pLen); err != nil {
						return
					}
					p := make([]byte, int(pLen[0]))
					if _, err := io.ReadFull(client, p); err != nil {
						return
					}
					if string(u) != wantUser || string(p) != wantPass {
						_, _ = client.Write([]byte{0x01, 0x01})
						return
					}
					_, _ = client.Write([]byte{0x01, 0x00})
				} else {
					_, _ = client.Write([]byte{0x05, 0x00})
				}

				reqHdr := make([]byte, 4)
				if _, err := io.ReadFull(client, reqHdr); err != nil {
					return
				}
				var host string
				switch reqHdr[3] {
				case 0x01: // IPv4
					ip := make([]byte, 4)
					if _, err := io.ReadFull(client, ip); err != nil {
						return
					}
					host = net.IP(ip).String()
				case 0x03: // Domain
					l := make([]byte, 1)
					if _, err := io.ReadFull(client, l); err != nil {
						return
					}
					d := make([]byte, int(l[0]))
					if _, err := io.ReadFull(client, d); err != nil {
						return
					}
					host = string(d)
				default:
					return
				}
				portBuf := make([]byte, 2)
				if _, err := io.ReadFull(client, portBuf); err != nil {
					return
				}
				port := int(portBuf[0])<<8 | int(portBuf[1])
				target, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 2*time.Second)
				if err != nil {
					_, _ = client.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
					return
				}
				defer target.Close()
				_, _ = client.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0})
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(target, client); done <- struct{}{} }()
				go func() { _, _ = io.Copy(client, target); done <- struct{}{} }()
				<-done
			}(c)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func TestMtgUpstreamPool_FailoverCooldownSocks5AndWeights(t *testing.T) {
	// 1. Dead primary SOCKS5 + healthy backup SOCKS5 -> automatic failover and cooldown.
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := deadLn.Addr().String()
	_ = deadLn.Close()

	socksAddr, closeSocks := startMinimalSocks5Server(t, "tguser", "tgpass")
	defer closeSocks()

	targetAddr, closeTarget := startEchoOnceListener(t, "OK_VIA_BACKUP")
	defer closeTarget()

	now := time.Unix(1700000000, 0)
	pool := newMtgUpstreamPool([]UpstreamSpec{
		{ID: "dead", Type: "socks5", Address: deadAddr, Weight: 1000, Enabled: true},
		{ID: "backup", Type: "socks5", Address: socksAddr, Username: "tguser", Password: "tgpass", Weight: 1, Enabled: true},
	})
	pool.nowFn = func() time.Time { return now }
	pool.rng = mrand.New(mrand.NewSource(42))

	c, err := pool.DialContext(context.Background(), "tcp", targetAddr)
	if err != nil {
		t.Fatalf("DialContext failover failed: %v", err)
	}
	buf, _ := io.ReadAll(c)
	_ = c.Close()
	if string(buf) != "OK_VIA_BACKUP" {
		t.Fatalf("unexpected payload via failover: %q", string(buf))
	}

	// Failed primary must now be in cooldown so healthy backup is ranked first.
	order := pool.selectOrder()
	if order[0].spec.ID != "backup" {
		t.Fatalf("expected healthy backup first after dead primary cooldown, got %q", order[0].spec.ID)
	}

	// 2. Weighted distribution favors heavy weight (100 vs 1).
	wPool := newMtgUpstreamPool([]UpstreamSpec{
		{ID: "heavy", Type: "direct", Weight: 100, Enabled: true},
		{ID: "light", Type: "direct", Weight: 1, Enabled: true},
	})
	wPool.rng = mrand.New(mrand.NewSource(7))
	heavyFirst := 0
	for range 100 {
		if wPool.selectOrder()[0].spec.ID == "heavy" {
			heavyFirst++
		}
	}
	if heavyFirst < 85 {
		t.Fatalf("expected heavy upstream weight=100 to win majority of draws vs weight=1, won %d/100", heavyFirst)
	}
}

func TestDeleteLastUser_SurvivesCandidateStopStartAndReload(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)
	if err := svc.UpdateConfig(Config{
		Enabled:    true,
		Scenario:   ScenarioDirectFakeTLS,
		DirectHost: "127.0.0.1",
		DirectPort: 8443,
	}); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}
	if err := svc.RotateSecret(); err != nil {
		t.Fatalf("RotateSecret failed: %v", err)
	}
	if svc.GetInternalConfig().LegacySecret == "" {
		t.Fatal("expected LegacySecret to be set before deleting user")
	}

	if err := svc.DeleteUser("default"); err != nil {
		t.Fatalf("DeleteUser default failed: %v", err)
	}
	cfgAfterDelete := svc.GetInternalConfig()
	if len(svc.GetStatus().Users) != 0 || cfgAfterDelete.Secret != "" || cfgAfterDelete.LegacySecret != "" || len(cfgAfterDelete.ActiveSecrets()) != 0 {
		t.Fatalf("expected 0 users and cleared secrets after DeleteUser, got cfg=%+v statusUsers=%+v", cfgAfterDelete, svc.GetStatus().Users)
	}

	// Stop via Coordinator/Candidate lifecycle must NOT mint a new Secret or resurrect "default".
	stopCfg := cfgAfterDelete
	stopCfg.Enabled = false
	txStop, err := svc.PrepareCandidate("tx-stop-empty-users", stopCfg)
	if err != nil {
		t.Fatalf("PrepareCandidate stop failed: %v", err)
	}
	if err := svc.CommitPrepared(txStop); err != nil {
		t.Fatalf("CommitPrepared stop failed: %v", err)
	}
	if err := svc.FinalizePrepared(txStop); err != nil {
		t.Fatalf("FinalizePrepared stop failed: %v", err)
	}
	if st := svc.GetStatus(); len(st.Users) != 0 || svc.GetInternalConfig().Secret != "" || len(svc.GetInternalConfig().ActiveSecrets()) != 0 {
		t.Fatalf("stop via candidate resurrected user/secret: cfg=%+v statusUsers=%+v", svc.GetInternalConfig(), st.Users)
	}

	// Reload from disk (Users becomes nil because of json omitempty) and start via candidate lifecycle.
	workers, _ := newFakeWorkers()
	reloaded := &Service{
		dataDir:         tempDir,
		tc:              NewTransactionCoordinator(filepath.Join(tempDir, "tproxy")),
		workers:         workers,
		tproxyValidator: func(string) error { return nil },
		stopCh:          make(chan struct{}),
	}
	if err := reloaded.loadAndMigrateConfig(); err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if got := reloaded.GetInternalConfig(); len(got.GetUsers()) != 0 || got.Secret != "" {
		t.Fatalf("reload resurrected user: %+v", got)
	}

	startCfg := reloaded.GetInternalConfig()
	startCfg.Enabled = true
	txStart, err := reloaded.PrepareCandidate("tx-start-empty-users", startCfg)
	if err != nil {
		t.Fatalf("PrepareCandidate start failed: %v", err)
	}
	if err := reloaded.CommitPrepared(txStart); err != nil {
		t.Fatalf("CommitPrepared start failed: %v", err)
	}
	if err := reloaded.FinalizePrepared(txStart); err != nil {
		t.Fatalf("FinalizePrepared start failed: %v", err)
	}
	if st := reloaded.GetStatus(); len(st.Users) != 0 || reloaded.GetInternalConfig().Secret != "" || len(reloaded.GetInternalConfig().ActiveSecrets()) != 0 {
		t.Fatalf("start via candidate after reload resurrected user/secret: cfg=%+v statusUsers=%+v", reloaded.GetInternalConfig(), st.Users)
	}

	// v2 config with 0 users triggering field-fill migration (e.g. empty carrier_mode) must preserve 0 users.
	v2Dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(v2Dir, "tproxy"), 0700); err != nil {
		t.Fatal(err)
	}
	v2Raw := []byte(`{"schema_version":2,"enabled":false,"scenario":"direct_fake_tls","listen_port":8085,"admin_port":8086,"direct_port":8443}`)
	if err := os.WriteFile(filepath.Join(v2Dir, "tproxy", "tgwebproxy-settings.json"), v2Raw, 0600); err != nil {
		t.Fatal(err)
	}
	v2Svc := &Service{dataDir: v2Dir}
	if err := v2Svc.loadAndMigrateConfig(); err != nil {
		t.Fatalf("v2 migration reload failed: %v", err)
	}
	if got := v2Svc.GetInternalConfig(); len(got.GetUsers()) != 0 || got.Secret != "" {
		t.Fatalf("v2 field migration resurrected default user: %+v", got)
	}

	// RotateSecret with 0 users must fail with ErrUserNotFound instead of recreating "default".
	if err := svc.RotateSecret(); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound on RotateSecret with 0 users, got %v", err)
	}
	if len(svc.GetStatus().Users) != 0 || svc.GetInternalConfig().Secret != "" {
		t.Fatalf("failed RotateSecret mutated users: %+v", svc.GetInternalConfig())
	}
}

// Кандидат без пользователей и секрета применяется как есть: секрет чеканят
// первый запуск, создание пользователя и мастер, а не подготовка.
// Мутация: вернуть чеканку в PrepareCandidate → красный.
func TestPrepareCandidate_DoesNotMintSecret(t *testing.T) {
	svc, _, _ := setupTestService(t)
	cand := svc.GetInternalConfig()
	cand.Enabled, cand.Secret, cand.Users = true, "", nil
	txID, err := svc.PrepareCandidate("tx-no-mint", cand)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CommitPrepared(txID); err != nil {
		t.Fatal(err)
	}
	if got := svc.GetInternalConfig(); got.Secret != "" || len(got.GetUsers()) != 0 {
		t.Fatalf("подготовка создала секрет: %+v", got)
	}
}
