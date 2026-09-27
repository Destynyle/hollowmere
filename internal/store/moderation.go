package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"hollowmere/internal/game"
)

// Moderation data: who may moderate (admins, keyed by resume key), the
// sanctions in force (bans and mutes) and an audit log of every action.

func (s *Store) migrateModeration() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	defer tx.Rollback()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS admins (
			key      TEXT PRIMARY KEY,
			role     TEXT NOT NULL CHECK (role IN ('moderator', 'admin')),
			added_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sanctions (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			kind       TEXT NOT NULL CHECK (kind IN ('ban', 'mute')),
			key        TEXT NOT NULL DEFAULT '',
			ip         TEXT NOT NULL DEFAULT '',
			name       TEXT NOT NULL DEFAULT '',
			reason     TEXT NOT NULL DEFAULT '',
			by         TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			until      INTEGER NOT NULL DEFAULT 0, -- 0 = permanent
			ip_until   INTEGER NOT NULL DEFAULT 0,
			lifted_at  INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS sanctions_active ON sanctions (lifted_at, kind)`,
		`CREATE TABLE IF NOT EXISTS modlog (
			id     INTEGER PRIMARY KEY AUTOINCREMENT,
			at     INTEGER NOT NULL,
			actor  TEXT NOT NULL,
			action TEXT NOT NULL,
			target TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('players') WHERE name = 'last_ip'`).Scan(&n); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	if n == 0 {
		if _, err := tx.Exec(`ALTER TABLE players ADD COLUMN last_ip TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	}
	return tx.Commit()
}

// Role returns "moderator", "admin" or "" for a resume key.
func (s *Store) Role(key string) (string, error) {
	var role string
	err := s.db.QueryRow(`SELECT role FROM admins WHERE key = ?`, key).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: role: %w", err)
	}
	return role, nil
}

// SetRole grants a role to the character called name; role "" removes it.
func (s *Store) SetRole(name, role string) error {
	key, err := s.NameOwner(name)
	if err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("store: no character called %q", name)
	}
	if role == "" {
		_, err = s.db.Exec(`DELETE FROM admins WHERE key = ?`, key)
	} else {
		_, err = s.db.Exec(`INSERT INTO admins (key, role, added_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET role = excluded.role`, key, role, time.Now().Unix())
	}
	if err != nil {
		return fmt.Errorf("store: set role: %w", err)
	}
	return s.Log("console", "role", name, role)
}

// Admin is one line of the admin list.
type Admin struct {
	Name string
	Role string
}

// Admins lists the characters holding a role.
func (s *Store) Admins() ([]Admin, error) {
	rows, err := s.db.Query(`SELECT COALESCE(p.name, '(deleted)'), a.role FROM admins a
		LEFT JOIN players p ON p.key = a.key ORDER BY a.role, p.name_lower`)
	if err != nil {
		return nil, fmt.Errorf("store: admins: %w", err)
	}
	defer rows.Close()
	var out []Admin
	for rows.Next() {
		var a Admin
		if err := rows.Scan(&a.Name, &a.Role); err != nil {
			return nil, fmt.Errorf("store: admins: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LastIP returns the address a character last played from.
func (s *Store) LastIP(key string) (string, error) {
	var ip string
	err := s.db.QueryRow(`SELECT last_ip FROM players WHERE key = ?`, key).Scan(&ip)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: last ip: %w", err)
	}
	return ip, nil
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// AddSanction records a ban or a mute and sets its ID.
func (s *Store) AddSanction(x *game.Sanction) error {
	res, err := s.db.Exec(`INSERT INTO sanctions (kind, key, ip, name, reason, by, created_at, until, ip_until)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.Kind, x.Key, x.IP, x.Name, x.Reason, x.By, unix(x.Created), unix(x.Until), unix(x.IPUntil))
	if err != nil {
		return fmt.Errorf("store: sanction: %w", err)
	}
	x.ID, _ = res.LastInsertId()
	return nil
}

// LiftSanctions ends every sanction of a kind on a key.
func (s *Store) LiftSanctions(kind, key string) (int, error) {
	res, err := s.db.Exec(`UPDATE sanctions SET lifted_at = ? WHERE kind = ? AND key = ? AND lifted_at = 0`,
		time.Now().Unix(), kind, key)
	if err != nil {
		return 0, fmt.Errorf("store: lift: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ActiveSanctions returns the sanctions still in force at now.
func (s *Store) ActiveSanctions(now time.Time) ([]game.Sanction, error) {
	rows, err := s.db.Query(`SELECT id, kind, key, ip, name, reason, by, created_at, until, ip_until
		FROM sanctions WHERE lifted_at = 0 AND (until = 0 OR until > ? OR ip_until > ?) ORDER BY id`,
		now.Unix(), now.Unix())
	if err != nil {
		return nil, fmt.Errorf("store: sanctions: %w", err)
	}
	defer rows.Close()
	var out []game.Sanction
	for rows.Next() {
		var x game.Sanction
		var created, until, ipUntil int64
		if err := rows.Scan(&x.ID, &x.Kind, &x.Key, &x.IP, &x.Name, &x.Reason, &x.By, &created, &until, &ipUntil); err != nil {
			return nil, fmt.Errorf("store: sanctions: %w", err)
		}
		x.Created, x.Until, x.IPUntil = fromUnix(created), fromUnix(until), fromUnix(ipUntil)
		out = append(out, x)
	}
	return out, rows.Err()
}

// Log appends a line to the moderation audit log.
func (s *Store) Log(actor, action, target, detail string) error {
	_, err := s.db.Exec(`INSERT INTO modlog (at, actor, action, target, detail) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Unix(), actor, action, target, detail)
	if err != nil {
		return fmt.Errorf("store: modlog: %w", err)
	}
	return nil
}
