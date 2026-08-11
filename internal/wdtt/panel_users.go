//go:build !mips && !mipsle

package wdtt

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
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
func syncPanelMainPassword(configDir, mainPassword string, clients []ServerClient) error {
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
		return setMainPassword(db, mainPassword, clients)
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
	db, err := sql.Open("sqlite", panelDSNWrite(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	// Схема идемпотентна: гоняем всегда, иначе panel.db, потерявшая таблицы
	// (обрыв на создании, чужая перезапись), больше никогда не чинится.
	if err := ensurePanelSchema(db); err != nil {
		db.Close()
		return nil, err
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

// setMainPassword делает главный пароль panel.db тем, что стоит в конфиге:
// им владеет awgm, он же отдаёт его серверу флагом -password. Строку прежнего
// главного пароля снимаем — сам wdtt-server её не убирает, а это валидная
// учётка, которую смена пароля должна была отозвать.
func setMainPassword(db *sql.DB, mainPassword string, clients []ServerClient) error {
	mainPassword = strings.TrimSpace(mainPassword)
	if mainPassword == "" {
		return fmt.Errorf("пароль сервера не задан")
	}
	var existing string
	err := db.QueryRow(`SELECT main_password FROM wdtt_global WHERE id = 1`).Scan(&existing)
	if err == sql.ErrNoRows {
		_, err = db.Exec(`INSERT INTO wdtt_global (id, main_password) VALUES (1, ?)`, mainPassword)
		return err
	}
	if err != nil {
		return err
	}
	existing = strings.TrimSpace(existing)
	if existing == mainPassword {
		return nil
	}
	if _, err := db.Exec(`UPDATE wdtt_global SET main_password = ? WHERE id = 1`, mainPassword); err != nil {
		return err
	}
	if existing == "" || knownClient(existing, clients) {
		return nil
	}
	if _, err := db.Exec(`DELETE FROM wdtt_user_devices WHERE password = ?`, existing); err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM wdtt_users WHERE password = ?`, existing)
	return err
}

func knownClient(password string, clients []ServerClient) bool {
	for _, c := range clients {
		if c.Password == password {
			return true
		}
	}
	return false
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

// upsertPanelUser проецирует одного клиента из wdtt.json в panel.db.
func upsertPanelUser(db *sql.DB, c ServerClient) error {
	_, err := db.Exec(`INSERT INTO wdtt_users (
 password, device_id, max_devices, expires_at, down_bytes, up_bytes, total_bytes,
 max_down_mbps, max_up_mbps, is_deactivated, comment, ports, vk_hash, sub_id, last_seen_at
 ) VALUES (?,'',1,0,0,0,0,0,0,0,?,'',?,'',0)
 ON CONFLICT(password) DO UPDATE SET
 comment=excluded.comment,
 vk_hash=CASE WHEN excluded.vk_hash != '' THEN excluded.vk_hash ELSE wdtt_users.vk_hash END`,
		c.Password, strings.TrimSpace(c.Comment), strings.TrimSpace(c.VkHash))
	return err
}

// restorePanelUsers пересобирает panel.db из списка клиентов wdtt.json и
// возвращает строки, которых в этом списке нет (наследие старых версий или
// правки через телеграм-бота) — их зовущий усыновляет в конфиг.
//
// Обратной чистки нет намеренно: удалить из конфига то, чего нет в panel.db,
// значит потерять всех клиентов ровно в тот момент, когда базу затёрли или
// потеряли, — то есть в сценарии, ради которого конфиг и стал источником
// правды. Цена: удаление клиента на стороне самого wdtt-server (бот, панель)
// мы не подхватываем, такой клиент вернётся на следующем старте.
func restorePanelUsers(configDir, mainPassword string, clients []ServerClient) ([]ServerClient, error) {
	mainPassword = strings.TrimSpace(mainPassword)
	if mainPassword == "" {
		return nil, fmt.Errorf("пароль сервера не задан")
	}
	var extra []ServerClient
	err := withPanelDBRetry(func() error {
		extra = nil
		db, err := openPanelDBWrite(configDir)
		if err != nil {
			return err
		}
		defer db.Close()

		if err := setMainPassword(db, mainPassword, clients); err != nil {
			return err
		}
		known := map[string]bool{mainPassword: true}
		for _, c := range clients {
			c.Password = strings.TrimSpace(c.Password)
			if c.Password == "" || c.Password == mainPassword {
				continue
			}
			if err := upsertPanelUser(db, c); err != nil {
				return err
			}
			known[c.Password] = true
		}
		if len(known) > 1 {
			if err := bumpUsersRev(db); err != nil {
				return err
			}
		}

		rows, err := db.Query(`SELECT password, comment, vk_hash FROM wdtt_users`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c ServerClient
			if err := rows.Scan(&c.Password, &c.Comment, &c.VkHash); err != nil {
				return err
			}
			if !known[c.Password] {
				extra = append(extra, c)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return extra, nil
}

func addPanelUser(configDir, mainPassword, password, comment, vkHash string) (ServerClient, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		var err error
		password, err = randomPanelPassword()
		if err != nil {
			return ServerClient{}, err
		}
	}
	mainPassword = strings.TrimSpace(mainPassword)
	if mainPassword == "" {
		return ServerClient{}, fmt.Errorf("пароль сервера не задан")
	}
	if password == mainPassword {
		return ServerClient{}, fmt.Errorf("используйте основной пароль сервера или сгенерируйте отдельный")
	}

	client := ServerClient{Password: password, Comment: strings.TrimSpace(comment), VkHash: strings.TrimSpace(vkHash)}
	err := withPanelDBRetry(func() error {
		db, err := openPanelDBWrite(configDir)
		if err != nil {
			return err
		}
		defer db.Close()

		if err := setMainPassword(db, mainPassword, []ServerClient{client}); err != nil {
			return err
		}
		if err := upsertPanelUser(db, client); err != nil {
			return err
		}
		return bumpUsersRev(db)
	})
	if err != nil {
		return ServerClient{}, err
	}
	return client, nil
}

// removePanelUser удаляет клиента из panel.db. Отсутствие строки не ошибка:
// источник правды — wdtt.json, panel.db могла её потерять.
func removePanelUser(configDir, mainPassword, password string) error {
	password = strings.TrimSpace(password)
	if password == "" {
		return fmt.Errorf("пароль клиента не задан")
	}
	mainPassword = strings.TrimSpace(mainPassword)
	if password == mainPassword {
		return fmt.Errorf("нельзя удалить основной пароль сервера")
	}

	return withPanelDBRetry(func() error {
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
		if _, err := tx.Exec(`DELETE FROM wdtt_users WHERE password = ?`, password); err != nil {
			return err
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
		return tx.Commit()
	})
}

func bumpUsersRevInTx(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE wdtt_global SET users_rev = users_rev + 1 WHERE id = 1`)
	return err
}

// purgeGatewayIPDevices removes panel.db rows where client IP equals the OpkgTun gateway.
func purgeGatewayIPDevices(configDir string) (int, error) {
	gateway := DefaultWdttServerGatewayAddr
	var removed int
	err := withPanelDBRetry(func() error {
		db, err := openPanelDBWrite(configDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		defer db.Close()
		rows, err := db.Query(`SELECT device_id FROM wdtt_devices WHERE ip = ?`, gateway)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, id := range ids {
			if _, err := tx.Exec(`DELETE FROM wdtt_user_devices WHERE device_id = ?`, id); err != nil {
				return err
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
		removed = len(ids)
		return nil
	})
	return removed, err
}
