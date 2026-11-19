package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

func (s *Server) hubUpdateRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/hub-update", s.tailnetOnly(s.hubUpdateStatus))
	m.HandleFunc("GET /api/hub-update/health", s.tailnetOnly(s.hubUpdateHealth))
	m.HandleFunc("POST /api/hub-update", s.tailnetOnly(s.sameSite(s.hubUpdateAction)))
}
func (s *Server) hubUpdateHealth(w http.ResponseWriter, r *http.Request) {
	ready, reason := s.hubRestartReady(r.Context())
	writeJSON(w, 200, map[string]any{"version": s.HubUpdateVersion, "ready": ready, "reason": reason})
}
func (s *Server) hubRestartReady(ctx context.Context) (bool, string) {
	if s.hubMigrationBusy() {
		return false, "A session migration is active"
	}
	var n int
	err := s.Store.DB.QueryRowContext(ctx, "SELECT count(*) FROM agent_sessions WHERE status='running'").Scan(&n)
	if err != nil {
		return false, "Could not verify active managed work"
	}
	if n > 0 {
		return false, "Managed agent work is active"
	}
	err = s.Store.DB.QueryRowContext(ctx, "SELECT count(*) FROM commands WHERE status IN ('claimed','running') AND kind NOT IN ('term_attach','term_capture','term_send','term_resize')").Scan(&n)
	if err != nil {
		return false, "Could not verify active commands"
	}
	if n > 0 {
		return false, "A hub command is active"
	}
	return true, ""
}
func (s *Server) hubUpdateRun(w http.ResponseWriter, r *http.Request, args ...string) {
	helper := s.HubUpdateCommand
	if helper == "" {
		exe, err := os.Executable()
		if err != nil {
			jsonErr(w, 500, err)
			return
		}
		helper = filepath.Join(filepath.Dir(exe), "brain-hub-update")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	args = append([]string{"--state", s.StateDir}, args...)
	out, err := exec.CommandContext(ctx, helper, args...).Output()
	if err != nil {
		jsonErr(w, 502, errors.New("Hub updater unavailable or failed; inspect hub update status and install the updater service"))
		return
	}
	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		jsonErr(w, 502, errors.New("Invalid hub updater reply"))
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) hubUpdateStatus(w http.ResponseWriter, r *http.Request) {
	s.hubUpdateRun(w, r, "--status")
}
func (s *Server) hubUpdateAction(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Action   string `json:"action"`
		Auto     *bool  `json:"auto_enabled"`
		Interval *int   `json:"interval_hours"`
	}
	d := json.NewDecoder(io.LimitReader(r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		jsonErr(w, 400, err)
		return
	}
	switch a.Action {
	case "check":
		if a.Auto != nil || a.Interval != nil {
			jsonErr(w, 400, errors.New("settings require action=settings"))
			return
		}
		s.hubUpdateRun(w, r, "--check")
	case "install":
		if a.Auto != nil || a.Interval != nil {
			jsonErr(w, 400, errors.New("settings require action=settings"))
			return
		}
		s.hubUpdateRun(w, r, "--request")
	case "settings":
		if a.Auto == nil && a.Interval == nil {
			jsonErr(w, 400, errors.New("give auto_enabled or interval_hours"))
			return
		}
		args := []string{}
		if a.Auto != nil {
			v := "off"
			if *a.Auto {
				v = "on"
			}
			args = append(args, "--auto", v)
		}
		if a.Interval != nil {
			if *a.Interval < 1 || *a.Interval > 168 {
				jsonErr(w, 400, errors.New("interval_hours must be 1–168"))
				return
			}
			args = append(args, "--interval-hours", strconv.Itoa(*a.Interval))
		}
		s.hubUpdateRun(w, r, args...)
	default:
		jsonErr(w, 400, errors.New("action must be check, install or settings"))
	}
}
