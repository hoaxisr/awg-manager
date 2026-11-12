package warpscout

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// FileName — файл модуля в dataDir. Отдельно от settings.json: секреты
// учётной записи не должны попадать в снимки настроек.
const FileName = "warpscout.json"

// maxHistory — сколько записей журнала автоподмен храним.
const maxHistory = 50

// maxCheckSamples — сколько точек живой серии проверок держим в памяти на профиль.
const maxCheckSamples = 40

// Cipher шифрует секреты на диске (storage.DeviceCipher).
type Cipher interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(token string) (string, error)
}

type accountDisk struct {
	ID               string    `json:"id"`
	TokenCipher      string    `json:"tokenCipher"`
	PrivateKeyCipher string    `json:"privateKeyCipher"`
	LicenseCipher    string    `json:"licenseCipher,omitempty"`
	PublicKey        string    `json:"publicKey"`
	PeerPublicKey    string    `json:"peerPublicKey"`
	AddressV4        string    `json:"addressV4"`
	AddressV6        string    `json:"addressV6,omitempty"`
	ClientID         string    `json:"clientId,omitempty"`
	AccountType      string    `json:"accountType,omitempty"`
	WarpPlus         bool      `json:"warpPlus"`
	CreatedAt        time.Time `json:"createdAt"`
}

type fileState struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Account       *accountDisk            `json:"account,omitempty"`
	Config        Config                  `json:"config"`
	History       []SwapEvent             `json:"history,omitempty"`
	Stats         map[string]ProfileStats `json:"stats,omitempty"`
}

// Store — атомарное хранилище модуля.
type Store struct {
	path   string
	cipher Cipher

	mu    sync.Mutex
	state fileState
	acct  *Account // расшифрованная копия
}

// NewStore открывает (или создаёт пустым) файл в dataDir.
func NewStore(dataDir string, cipher Cipher) (*Store, error) {
	s := &Store{path: filepath.Join(dataDir, FileName), cipher: cipher}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	s.state = fileState{SchemaVersion: 1, Config: DefaultConfig()}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("warpscout: read %s: %w", s.path, err)
	}
	var st fileState
	if err := json.Unmarshal(data, &st); err != nil {
		storage.QuarantineCorrupt(s.path, err)
		return nil
	}
	st.Config = normalizeConfig(st.Config)
	s.state = st
	if st.Account != nil {
		acct, err := s.decryptAccount(st.Account)
		if err != nil {
			// Секрет устройства сменился (восстановление на другом роутере):
			// учётка непригодна, но файл не трогаем — пусть перерегистрируют.
			s.acct = nil
		} else {
			s.acct = acct
		}
	}
	return nil
}

func (s *Store) decryptAccount(d *accountDisk) (*Account, error) {
	tok, err := s.cipher.Decrypt(d.TokenCipher)
	if err != nil {
		return nil, err
	}
	pk, err := s.cipher.Decrypt(d.PrivateKeyCipher)
	if err != nil {
		return nil, err
	}
	lic := ""
	if d.LicenseCipher != "" {
		lic, _ = s.cipher.Decrypt(d.LicenseCipher)
	}
	return &Account{
		ID: d.ID, Token: tok, PrivateKey: pk, License: lic,
		PublicKey: d.PublicKey, PeerPublicKey: d.PeerPublicKey,
		AddressV4: d.AddressV4, AddressV6: d.AddressV6, ClientID: d.ClientID,
		AccountType: d.AccountType, WarpPlus: d.WarpPlus, CreatedAt: d.CreatedAt,
	}, nil
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return storage.AtomicWritePerm(s.path, data, 0o600)
}

// Account — копия учётной записи или nil.
func (s *Store) Account() *Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acct == nil {
		return nil
	}
	c := *s.acct
	return &c
}

// AccountUnreadable — запись на диске есть, но не расшифровывается.
func (s *Store) AccountUnreadable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Account != nil && s.acct == nil
}

// SetAccount шифрует и сохраняет учётную запись (nil — удалить).
func (s *Store) SetAccount(a *Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a == nil {
		s.state.Account = nil
		s.acct = nil
		return s.saveLocked()
	}
	tok, err := s.cipher.Encrypt(a.Token)
	if err != nil {
		return fmt.Errorf("encrypt token: %w", err)
	}
	pk, err := s.cipher.Encrypt(a.PrivateKey)
	if err != nil {
		return fmt.Errorf("encrypt private key: %w", err)
	}
	lic := ""
	if a.License != "" {
		if lic, err = s.cipher.Encrypt(a.License); err != nil {
			return fmt.Errorf("encrypt license: %w", err)
		}
	}
	s.state.Account = &accountDisk{
		ID: a.ID, TokenCipher: tok, PrivateKeyCipher: pk, LicenseCipher: lic,
		PublicKey: a.PublicKey, PeerPublicKey: a.PeerPublicKey,
		AddressV4: a.AddressV4, AddressV6: a.AddressV6, ClientID: a.ClientID,
		AccountType: a.AccountType, WarpPlus: a.WarpPlus, CreatedAt: a.CreatedAt,
	}
	c := *a
	s.acct = &c
	return s.saveLocked()
}

// Config — копия конфигурации.
func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneConfig(s.state.Config)
}

// SetConfig сохраняет уже провалидированную конфигурацию.
func (s *Store) SetConfig(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Config = cloneConfig(normalizeConfig(c))
	return s.saveLocked()
}

// History — копия журнала автоподмен (новые первыми).
func (s *Store) History() []SwapEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SwapEvent(nil), s.state.History...)
}

// AppendHistory добавляет запись в начало журнала.
func (s *Store) AppendHistory(ev SwapEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.History = append([]SwapEvent{ev}, s.state.History...)
	if len(s.state.History) > maxHistory {
		s.state.History = s.state.History[:maxHistory]
	}
	return s.saveLocked()
}

// Stats — копия сохранённой статистики профилей с диска.
func (s *Store) Stats() map[string]ProfileStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]ProfileStats, len(s.state.Stats))
	for k, v := range s.state.Stats {
		out[k] = cloneProfileStats(v, false)
	}
	return out
}

// SaveStats сохраняет на диск агрегированную статистику профилей (без живых Samples).
func (s *Store) SaveStats(stats map[string]ProfileStats) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(stats) == 0 {
		s.state.Stats = nil
		return s.saveLocked()
	}
	clean := make(map[string]ProfileStats, len(stats))
	for k, v := range stats {
		if k == "" {
			continue
		}
		clean[k] = cloneProfileStats(v, false)
	}
	s.state.Stats = clean
	return s.saveLocked()
}

func cloneProfileStats(in ProfileStats, withSamples bool) ProfileStats {
	out := in
	if len(in.Colos) > 0 {
		out.Colos = make(map[string]int, len(in.Colos))
		for k, v := range in.Colos {
			out.Colos[k] = v
		}
	} else {
		out.Colos = nil
	}
	if withSamples && len(in.Samples) > 0 {
		out.Samples = append([]CheckSample(nil), in.Samples...)
	} else {
		out.Samples = nil
	}
	return out
}

// DefaultConfig — стартовая конфигурация.
func DefaultConfig() Config {
	return Config{
		ProfileMode: ProfileModeSingle,
		Profiles:    []Profile{},
		AWG: AWGParams{
			SignatureProfile: "quic_initial",
			Jc:               4,
			Jmin:             40,
			Jmax:             70,
		},
	}
}

// DefaultFilters — фильтры по умолчанию: без московских узлов.
func DefaultFilters() Filters {
	return Filters{
		Protocol:         ProtocolAWG,
		ExcludeColos:     []string{"DME", "LED"},
		AllowedCountries: []string{},
		MaxRTTMs:         0,
	}
}

func normalizeFilters(f Filters) Filters {
	if f.Protocol != ProtocolWG {
		f.Protocol = ProtocolAWG
	}
	f.ExcludeColos = normCodes(f.ExcludeColos)
	f.AllowedCountries = normCodes(f.AllowedCountries)
	if f.MaxRTTMs < 0 {
		f.MaxRTTMs = 0
	}
	if f.MaxCandidates < 0 {
		f.MaxCandidates = 0
	}
	ports := f.Ports[:0:0]
	for _, p := range f.Ports {
		if p > 0 && p < 65536 {
			ports = append(ports, p)
		}
	}
	f.Ports = ports
	return f
}

func normalizeProfile(p Profile) Profile {
	p.Name = strings.TrimSpace(p.Name)
	p.TargetID = strings.TrimSpace(p.TargetID)
	p.Filters = normalizeFilters(p.Filters)
	if p.CheckIntervalSec <= 0 {
		p.CheckIntervalSec = 60
	}
	if p.CheckIntervalSec < 15 {
		p.CheckIntervalSec = 15
	}
	if p.FailThreshold <= 0 {
		p.FailThreshold = 2
	}
	if p.CooldownSec <= 0 {
		p.CooldownSec = 120
	}
	return p
}

func normalizeConfig(c Config) Config {
	if c.ProfileMode != ProfileModeMulti {
		c.ProfileMode = ProfileModeSingle
	}
	c.ScanInterface = sanitizeIfaceName(c.ScanInterface)
	if c.Profiles == nil {
		c.Profiles = []Profile{}
	}
	for i := range c.Profiles {
		c.Profiles[i] = normalizeProfile(c.Profiles[i])
	}
	if c.AWG.Jc < 0 {
		c.AWG.Jc = 0
	}
	if c.AWG.Jc > 128 {
		c.AWG.Jc = 128
	}
	if c.AWG.Jmin < 0 {
		c.AWG.Jmin = 0
	}
	if c.AWG.Jmax < c.AWG.Jmin {
		c.AWG.Jmax = c.AWG.Jmin
	}
	if c.AWG.Jmax > 1280 {
		c.AWG.Jmax = 1280
	}
	if c.AWG.SignatureProfile == "" {
		c.AWG.SignatureProfile = "none"
	}
	// Режим single: включённым может остаться только активный профиль.
	if c.ProfileMode == ProfileModeSingle {
		active := c.ActiveProfileID
		if active == "" {
			for _, p := range c.Profiles {
				if p.Enabled {
					active = p.ID
					break
				}
			}
		}
		found := false
		for i := range c.Profiles {
			if c.Profiles[i].ID == active {
				found = true
			} else {
				c.Profiles[i].Enabled = false
			}
		}
		if !found {
			active = ""
		}
		c.ActiveProfileID = active
	}
	return c
}

func cloneConfig(c Config) Config {
	out := c
	out.Profiles = make([]Profile, len(c.Profiles))
	for i, p := range c.Profiles {
		cp := p
		cp.Filters.ExcludeColos = append([]string{}, p.Filters.ExcludeColos...)
		cp.Filters.AllowedCountries = append([]string{}, p.Filters.AllowedCountries...)
		cp.Filters.Ports = append([]int(nil), p.Filters.Ports...)
		out.Profiles[i] = cp
	}
	return out
}

func normCodes(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.ToUpper(strings.TrimSpace(v))
		if v == "" || len(v) > 8 || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func sanitizeIfaceName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 0 || len(s) > 32 {
		return ""
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
		if !ok {
			return ""
		}
	}
	return s
}

func decodeB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return b
}
