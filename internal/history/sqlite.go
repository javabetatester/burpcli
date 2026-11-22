package history

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/javabetatester/burpcli/internal/flow"

	_ "modernc.org/sqlite"
)

// SQLite persists flows in a single-file pure-Go SQLite database.
type SQLite struct{ db *sql.DB }

// OpenSQLite opens (creating if needed) the database at path.
func OpenSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("history: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS flows (
		id INTEGER PRIMARY KEY,
		started INTEGER NOT NULL,
		host TEXT NOT NULL,
		method TEXT NOT NULL,
		status INTEGER NOT NULL,
		data BLOB NOT NULL)`)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("history: migrate sqlite: %w", err)
	}
	return &SQLite{db: db}, nil
}

// Save upserts a flow.
func (s *SQLite) Save(f *flow.Flow) error {
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("history: encode flow: %w", err)
	}
	status := 0
	if f.Response != nil {
		status = f.Response.Status
	}
	_, err = s.db.Exec(`INSERT INTO flows (id, started, host, method, status, data) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET started=excluded.started, host=excluded.host, method=excluded.method, status=excluded.status, data=excluded.data`,
		f.ID, f.Started.UnixNano(), f.Request.Hostname(), f.Request.Method, status, data)
	if err != nil {
		return fmt.Errorf("history: save flow %d: %w", f.ID, err)
	}
	return nil
}

// Load returns the newest limit flows, oldest first.
func (s *SQLite) Load(limit int) ([]*flow.Flow, error) {
	rows, err := s.db.Query(`SELECT data FROM (SELECT id, data FROM flows ORDER BY id DESC LIMIT ?) ORDER BY id`, limit)
	if err != nil {
		return nil, fmt.Errorf("history: query flows: %w", err)
	}
	defer rows.Close()
	var out []*flow.Flow
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("history: scan flow: %w", err)
		}
		f := new(flow.Flow)
		if err := json.Unmarshal(data, f); err != nil {
			return nil, fmt.Errorf("history: decode flow: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Close releases the database.
func (s *SQLite) Close() error { return s.db.Close() }
