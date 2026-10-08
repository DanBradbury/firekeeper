package reporter

import (
	"context"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
)

type heartbeatSession struct {
	SessionID      string     `json:"session_id"`
	State          string     `json:"state"`
	LastActivityAt *time.Time `json:"last_activity_at"`
}

type heartbeatRequest struct {
	Machine  machine            `json:"machine"`
	Sessions []heartbeatSession `json:"sessions"`
}

// Heartbeat sends POST /v1/heartbeat for the sessions a pass read, marking
// the machine online even when there were no new events. It sends nothing
// when cfg cannot upload. Sessions the pass skipped (a provider not
// allowlisted, an ignored directory) are left out, so a heartbeat reveals
// no more than an upload would.
func Heartbeat(ctx context.Context, cfg Config, summary Summary) error {
	if !cfg.Uploading() {
		return nil
	}
	r, err := newRun(cfg)
	if err != nil {
		return err
	}
	id := summary.MachineID
	if id == "" {
		if id, err = session.MachineID(r.home); err != nil {
			return err
		}
	}
	m := r.machine
	m.ID = id
	req := heartbeatRequest{Machine: m, Sessions: []heartbeatSession{}}
	for _, s := range summary.Sessions {
		if s.Skipped != "" || s.SessionID == "" || s.State == "" {
			continue
		}
		req.Sessions = append(req.Sessions, heartbeatSession{SessionID: s.SessionID, State: s.State, LastActivityAt: s.LastActivityAt})
	}
	_, err = r.postJSON(ctx, "/v1/heartbeat", "heartbeat", req)
	return err
}
