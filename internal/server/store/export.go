package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// exportPage is how many rows one export query reads. The store has one
// database connection, so an export reads in short pages and releases it in
// between instead of holding it for as long as a slow client takes to
// download.
const exportPage = 500

// ExportAccount streams everything stored for an account to emit, one
// record at a time: the account, its machines, token metadata (never
// secrets or hashes), then for each session in (machine, session) order the
// session, its changed files and its events in seq order. typ names the
// record: account, machine, token, session, file or event. v marshals to a
// JSON object.
//
// It is not a snapshot: ingest that lands while it runs may or may not be
// included. A failure from emit stops the export and is returned.
func (s *Store) ExportAccount(ctx context.Context, accountID string, emit func(typ string, v any) error) error {
	if err := requireAccount(accountID); err != nil {
		return err
	}
	a, err := s.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if err := emit("account", struct {
		ID        string    `json:"id"`
		Email     string    `json:"email"`
		CreatedAt time.Time `json:"created_at"`
	}{a.ID, a.Email, a.CreatedAt}); err != nil {
		return err
	}

	machines, err := s.ListMachines(ctx, accountID)
	if err != nil {
		return err
	}
	for _, m := range machines {
		if err := emit("machine", m); err != nil {
			return err
		}
	}

	toks, err := s.accountTokens(ctx, accountID)
	if err != nil {
		return err
	}
	for _, t := range toks {
		if err := emit("token", t); err != nil {
			return err
		}
	}

	var lastMachine, lastSession string
	for {
		page, err := s.sessionPage(ctx, accountID, lastMachine, lastSession)
		if err != nil {
			return err
		}
		for _, se := range page {
			if err := emit("session", se); err != nil {
				return err
			}
			files, err := s.ListFiles(ctx, accountID, se.MachineID, se.SessionID)
			if err != nil {
				return err
			}
			for _, f := range files {
				if err := emit("file", struct {
					MachineID string `json:"machine_id"`
					SessionID string `json:"session_id"`
					SessionFile
				}{se.MachineID, se.SessionID, f}); err != nil {
					return err
				}
			}
			after := int64(-1)
			for {
				evs, more, err := s.ListEvents(ctx, accountID, se.MachineID, se.SessionID, after, exportPage)
				if err != nil {
					return err
				}
				for _, e := range evs {
					if err := emit("event", e); err != nil {
						return err
					}
					after = e.Seq
				}
				if !more {
					break
				}
			}
		}
		if len(page) < exportPage {
			return nil
		}
		last := page[len(page)-1]
		lastMachine, lastSession = last.MachineID, last.SessionID
	}
}

// sessionPage reads the sessions after (machine, session) in key order.
func (s *Store) sessionPage(ctx context.Context, accountID, machineID, sessionID string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionColumns+` FROM sessions s
WHERE s.account_id = ? AND (s.machine_id, s.session_id) > (?, ?)
ORDER BY s.machine_id, s.session_id LIMIT ?`, accountID, machineID, sessionID, exportPage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		se, _, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// ExportToken is a token's metadata. The secret is not stored and the hash
// is not exported.
type ExportToken struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Scope     string     `json:"scope"`
	MachineID string     `json:"machine_id"`
	CreatedAt *time.Time `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

func (s *Store) accountTokens(ctx context.Context, accountID string) ([]ExportToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, scope, machine_id, created_at, revoked_at
FROM tokens WHERE account_id = ? ORDER BY created_at, id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportToken
	for rows.Next() {
		var t ExportToken
		var created, revoked sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.Scope, &t.MachineID, &created, &revoked); err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
		}
		t.CreatedAt, t.RevokedAt = parseTime(created), parseTime(revoked)
		out = append(out, t)
	}
	return out, rows.Err()
}
