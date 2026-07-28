package wdtt

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const wdttPanelDDL = `
CREATE TABLE IF NOT EXISTS wdtt_global (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 main_password TEXT NOT NULL DEFAULT '',
 admin_id TEXT NOT NULL DEFAULT '',
 bot_token TEXT NOT NULL DEFAULT '',
 users_rev INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS wdtt_users (
 password TEXT PRIMARY KEY,
 device_id TEXT NOT NULL DEFAULT '',
 max_devices INTEGER NOT NULL DEFAULT 0,
 expires_at INTEGER NOT NULL DEFAULT 0,
 down_bytes INTEGER NOT NULL DEFAULT 0,
 up_bytes INTEGER NOT NULL DEFAULT 0,
 total_bytes INTEGER NOT NULL DEFAULT 0,
 max_down_mbps REAL NOT NULL DEFAULT 0,
 max_up_mbps REAL NOT NULL DEFAULT 0,
 is_deactivated INTEGER NOT NULL DEFAULT 0,
 comment TEXT NOT NULL DEFAULT '',
 ports TEXT NOT NULL DEFAULT '',
 vk_hash TEXT NOT NULL DEFAULT '',
 sub_id TEXT NOT NULL DEFAULT '',
 last_seen_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS wdtt_user_devices (
 password TEXT NOT NULL,
 device_id TEXT NOT NULL,
 sort_order INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (password, device_id)
);
CREATE TABLE IF NOT EXISTS wdtt_devices (
 device_id TEXT PRIMARY KEY,
 ip TEXT NOT NULL DEFAULT '',
 priv_key TEXT NOT NULL DEFAULT '',
 pub_key TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS wdtt_inbound (id INTEGER PRIMARY KEY);
`

// PanelUserEntry is one WDTT client password row from panel.db.
type PanelUserEntry struct {
	Password      string `json:"password"`
	Comment       string `json:"comment,omitempty"`
	VkHash        string `json:"vkHash,omitempty"`
	IsMain        bool   `json:"isMain"`
	IsDeactivated bool   `json:"isDeactivated"`
	DeviceCount   int    `json:"deviceCount"`
	LastSeenAt    int64  `json:"lastSeenAt,omitempty"`
}

// PanelUsersStatus is returned by the panel users API.
type PanelUsersStatus struct {
	PanelDBPath string           `json:"panelDbPath,omitempty"`
	Available   bool             `json:"available"`
	Users       []PanelUserEntry `json:"users"`
}

func panelDBPath(configDir string) string {
	return filepath.Join(strings.TrimSpace(configDir), "panel.db")
}

func panelDSNWrite(path string) string {
	return path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(3000)"
}

func panelDSNReadOnly(path string) string {
	return path + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)"
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "sqlite_busy")
}

func withPanelDBRetry(fn func() error) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		last = fn()
		if last == nil || !isSQLiteBusy(last) {
			return last
		}
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
	if isSQLiteBusy(last) {
		return fmt.Errorf("panel.db занят wdtt-server — повторите через секунду")
	}
	return last
}

// syncPanelMainPassword ensures panel.db exists and stores the server main password.
func syncPanelMainPassword(configDir, mainPassword string) error {
	mainPassword = strings.TrimSpace(mainPassword)
	if mainPassword == "" {
		return nil
	}
	return withPanelDBRetry(func() error {
		db, err := openPanelDBWrite(configDir)
		if err != nil {
			return err
		}
		defer db.Close()
		return ensureMainPassword(db, mainPassword)
	})
}

func openPanelDBReadOnly(path string) (*sql.DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("panel.db: путь не задан")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", panelDSNReadOnly(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func openPanelDBWrite(configDir string) (*sql.DB, error) {
	dir := strings.TrimSpace(configDir)
	if dir == "" {
		return nil, fmt.Errorf("config-dir не задан")
	}
	path := panelDBPath(dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	newDB := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		newDB = true
	}
	db, err := sql.Open("sqlite", panelDSNWrite(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if newDB {
		if err := ensurePanelSchema(db); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func ensurePanelSchema(db *sql.DB) error {
	if _, err := db.Exec(wdttPanelDDL); err != nil {
		return err
	}
	for _, stmt := range []string{
		`ALTER TABLE wdtt_global ADD COLUMN admin_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE wdtt_global ADD COLUMN bot_token TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE wdtt_users ADD COLUMN sub_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE wdtt_users ADD COLUMN last_seen_at INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	return nil
}

func ensureMainPassword(db *sql.DB, mainPassword string) error {
	mainPassword = strings.TrimSpace(mainPassword)
	if mainPassword == "" {
		return fmt.Errorf("пароль сервера не задан")
	}
	var existing string
	err := db.QueryRow(`SELECT main_password FROM wdtt_global WHERE id = 1`).Scan(&existing)
	switch {
	case err == sql.ErrNoRows:
		_, err = db.Exec(`INSERT INTO wdtt_global (id, main_password) VALUES (1, ?)`, mainPassword)
		return err
	case err != nil:
		return err
	case strings.TrimSpace(existing) == "":
		_, err = db.Exec(`UPDATE wdtt_global SET main_password = ? WHERE id = 1`, mainPassword)
		return err
	default:
		return nil
	}
}

func bumpUsersRev(db *sql.DB) error {
	_, err := db.Exec(`UPDATE wdtt_global SET users_rev = users_rev + 1 WHERE id = 1`)
	return err
}

func scanPanelUsers(db *sql.DB, fallbackMain string) ([]PanelUserEntry, string, error) {
	var dbMain string
	err := db.QueryRow(`SELECT main_password FROM wdtt_global WHERE id = 1`).Scan(&dbMain)
	if err == sql.ErrNoRows {
		dbMain = strings.TrimSpace(fallbackMain)
	} else if err != nil {
		return nil, "", err
	}
	dbMain = strings.TrimSpace(dbMain)
	if dbMain == "" {
		dbMain = strings.TrimSpace(fallbackMain)
	}

	rows, err := db.Query(`SELECT u.password, u.comment, u.vk_hash, u.is_deactivated, u.last_seen_at,
 COALESCE((SELECT COUNT(*) FROM wdtt_user_devices d WHERE d.password = u.password), 0)
 FROM wdtt_users u ORDER BY u.comment, u.password`)
	if err != nil {
		return nil, dbMain, err
	}
	defer rows.Close()

	var users []PanelUserEntry
	for rows.Next() {
		var e PanelUserEntry
		var deactivated int
		if err := rows.Scan(&e.Password, &e.Comment, &e.VkHash, &deactivated, &e.LastSeenAt, &e.DeviceCount); err != nil {
			return nil, dbMain, err
		}
		e.IsDeactivated = deactivated != 0
		e.IsMain = e.Password == dbMain
		if e.IsMain && e.Comment == "" {
			e.Comment = "ADMIN"
		}
		users = append(users, e)
	}
	return users, dbMain, rows.Err()
}

func loadPanelUsers(configDir, mainPassword string) (PanelUsersStatus, error) {
	st := PanelUsersStatus{
		PanelDBPath: panelDBPath(configDir),
		Users:       []PanelUserEntry{},
	}
	mainPassword = strings.TrimSpace(mainPassword)
	if mainPassword == "" {
		return st, nil
	}
	path := panelDBPath(configDir)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return st, nil
	}
	err := withPanelDBRetry(func() error {
		st.Users = []PanelUserEntry{}
		st.Available = false
		db, err := openPanelDBReadOnly(path)
		if err != nil {
			return err
		}
		defer db.Close()
		users, _, err := scanPanelUsers(db, mainPassword)
		if err != nil {
			return err
		}
		st.Available = true
		st.Users = users
		return nil
	})
	return st, err
}

func randomPanelPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func addPanelUser(configDir, mainPassword, password, comment, vkHash string) (PanelUsersStatus, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		var err error
		password, err = randomPanelPassword()
		if err != nil {
			return PanelUsersStatus{}, err
		}
	}
	mainPassword = strings.TrimSpace(mainPassword)
	if mainPassword == "" {
		return PanelUsersStatus{}, fmt.Errorf("пароль сервера не задан")
	}
	if password == mainPassword {
		return PanelUsersStatus{}, fmt.Errorf("используйте основной пароль сервера или сгенерируйте отдельный")
	}

	var out PanelUsersStatus
	err := withPanelDBRetry(func() error {
		db, err := openPanelDBWrite(configDir)
		if err != nil {
			return err
		}
		defer db.Close()

		if err := ensureMainPassword(db, mainPassword); err != nil {
			return err
		}

		deact := 0
		_, err = db.Exec(`INSERT INTO wdtt_users (
 password, device_id, max_devices, expires_at, down_bytes, up_bytes, total_bytes,
 max_down_mbps, max_up_mbps, is_deactivated, comment, ports, vk_hash, sub_id, last_seen_at
 ) VALUES (?,?,1,0,0,0,0,0,0,?,?,'',?,'',0)
 ON CONFLICT(password) DO UPDATE SET
 comment=excluded.comment,
 vk_hash=CASE WHEN excluded.vk_hash != '' THEN excluded.vk_hash ELSE wdtt_users.vk_hash END`,
			password, "", deact, strings.TrimSpace(comment), strings.TrimSpace(vkHash))
		if err != nil {
			return err
		}
		if err := bumpUsersRev(db); err != nil {
			return err
		}
		var loadErr error
		out, loadErr = loadPanelUsers(configDir, mainPassword)
		return loadErr
	})
	if err != nil {
		return PanelUsersStatus{}, err
	}
	return out, nil
}

func removePanelUser(configDir, mainPassword, password string) (PanelUsersStatus, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return PanelUsersStatus{}, fmt.Errorf("пароль клиента не задан")
	}
	mainPassword = strings.TrimSpace(mainPassword)
	if password == mainPassword {
		return PanelUsersStatus{}, fmt.Errorf("нельзя удалить основной пароль сервера")
	}

	var out PanelUsersStatus
	err := withPanelDBRetry(func() error {
		db, err := openPanelDBWrite(configDir)
		if err != nil {
			return err
		}
		defer db.Close()

		var deviceIDs []string
		drows, err := db.Query(`SELECT device_id FROM wdtt_user_devices WHERE password = ?`, password)
		if err != nil {
			return err
		}
		for drows.Next() {
			var id string
			if err := drows.Scan(&id); err != nil {
				drows.Close()
				return err
			}
			deviceIDs = append(deviceIDs, id)
		}
		drows.Close()

		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		if _, err := tx.Exec(`DELETE FROM wdtt_user_devices WHERE password = ?`, password); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM wdtt_users WHERE password = ?`, password)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("клиент не найден")
		}
		for _, id := range deviceIDs {
			if id == "" {
				continue
			}
			if _, err := tx.Exec(`DELETE FROM wdtt_devices WHERE device_id = ?`, id); err != nil {
				return err
			}
		}
		if err := bumpUsersRevInTx(tx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		var loadErr error
		out, loadErr = loadPanelUsers(configDir, mainPassword)
		return loadErr
	})
	if err != nil {
		return PanelUsersStatus{}, err
	}
	return out, nil
}

func bumpUsersRevInTx(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE wdtt_global SET users_rev = users_rev + 1 WHERE id = 1`)
	return err
}
