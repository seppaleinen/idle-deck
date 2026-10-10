package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/seppaleinen/idle-deck/config"
	"github.com/seppaleinen/idle-deck/queue"
	"github.com/google/uuid"
)

type statusResponse struct {
	GeneratedAt time.Time `json:"generated_at"`
	Schema      int       `json:"schema_version"`
	Heartbeat   Heartbeat `json:"heartbeat"`
	Repos       []Repo    `json:"repos"`
	Harness     Harness   `json:"harness"`
	Queue       Queue     `json:"queue"`
	Active      *Active   `json:"active"`
	Config      string    `json:"config"`
}

type Heartbeat struct {
	BeatAt   time.Time `json:"beat_at"`
	Stale    bool      `json:"stale"`
	LockHeld bool      `json:"lock_held"`
}

type Repo struct {
	Repo       string    `json:"repo"`
	Watermark  string    `json:"watermark"`
	LastPoll   time.Time `json:"last_poll"`
	PollSeeded bool      `json:"poll_seeded"`
}

type Harness struct {
	URL        string    `json:"url"`
	Reachable  bool      `json:"reachable"`
	Status     int       `json:"status_code,omitempty"`
	Error      string    `json:"error,omitempty"`
	ProbedAt   time.Time `json:"probed_at"`
	Body       string    `json:"body,omitempty"`
}

type Queue struct {
	TotalByState map[string]int `json:"total_by_state"`
	ByStateTier  []StateTier    `json:"by_state_tier"`
}

type StateTier struct {
	State string `json:"state"`
	Tier  string `json:"tier"`
	Count int    `json:"count"`
}

type Active struct {
	TaskID    string    `json:"task_id"`
	Tier      string    `json:"tier"`
	Role      string    `json:"role"`
	AttemptID string    `json:"attempt_id"`
	StartedAt time.Time `json:"started_at"`
	Elapsed   string    `json:"elapsed"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := statusResponse{
		GeneratedAt: time.Now(),
	}
	out.Queue.TotalByState = map[string]int{}

	var schema int
	_ = s.dbQueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&schema)
	out.Schema = schema

	var beatUnix int64
	hbErr := s.dbQueryRow(ctx, `SELECT beat_at FROM heartbeat WHERE id = 1`).Scan(&beatUnix)
	if hbErr == nil {
		out.Heartbeat.BeatAt = time.Unix(0, beatUnix)
		if time.Since(out.Heartbeat.BeatAt) > 3*s.cfg.PollInterval {
			out.Heartbeat.Stale = true
		}
	}
	_ = s.dbQueryRow(ctx, `SELECT run_lock FROM heartbeat WHERE id = 1`).Scan(&out.Heartbeat.LockHeld)

	repos, err := config.ParseRepos(s.cfg.Repos)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "parse repos: "+err.Error())
		return
	}
	for _, repo := range repos {
		rr := Repo{Repo: repo}
		wmErr := s.dbQueryRow(ctx, `SELECT watermark FROM tracker_watermarks WHERE repo = ?`, repo).Scan(&rr.Watermark)
		if wmErr == sql.ErrNoRows {
			rr.Watermark = "(never observed)"
		} else if wmErr != nil {
			rr.Watermark = "(error)"
		}
		var pollUnix int64
		pollErr := s.dbQueryRow(ctx, `SELECT last_poll FROM tracker_polls WHERE repo = ?`, repo).Scan(&pollUnix)
		if pollErr == nil {
			rr.LastPoll = time.Unix(0, pollUnix)
			rr.PollSeeded = true
		} else {
			rr.LastPoll = time.Time{}
		}
		out.Repos = append(out.Repos, rr)
	}

	rows, err := s.dbQuery(ctx, `SELECT state, tier, COUNT(*) FROM tasks GROUP BY state, tier ORDER BY state, tier`)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "query queue: "+err.Error())
		return
	}
	for rows.Next() {
		var st StateTier
		if err := rows.Scan(&st.State, &st.Tier, &st.Count); err != nil {
			rows.Close()
			s.respondError(w, r, http.StatusInternalServerError, "scan queue: "+err.Error())
			return
		}
		out.Queue.ByStateTier = append(out.Queue.ByStateTier, st)
		out.Queue.TotalByState[st.State] += st.Count
	}
	rows.Close()

	row := s.dbQueryRow(ctx, `
		SELECT t.id, t.tier, a.id, a.started_at
		FROM tasks t
		JOIN attempts a ON a.task_id = t.id AND a.outcome = ''
		WHERE t.state = 'running'
		LIMIT 1`)
	var a Active
	var startedAt int64
	if err := row.Scan(&a.TaskID, &a.Tier, &a.AttemptID, &startedAt); err == nil {
		a.StartedAt = time.Unix(0, startedAt)
		a.Elapsed = time.Since(a.StartedAt).Round(time.Second).String()
		a.Role = string(queue.RoleForTier(queue.TaskTier(a.Tier)))
		out.Active = &a
	} else if err != sql.ErrNoRows {
		s.respondError(w, r, http.StatusInternalServerError, "query active: "+err.Error())
		return
	}

	out.Harness = s.probeHarness(ctx)
	out.Config = config.Format(config.Redact(s.cfg))

	s.respondJSON(w, r, http.StatusOK, out)
}

func (s *Server) probeHarness(ctx context.Context) Harness {
	h := Harness{URL: s.cfg.HarnessURL, ProbedAt: time.Now()}
	if s.cfg.HarnessURL == "" {
		h.Error = "IDLE_DECK_HARNESS_URL not set"
		return h
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, s.cfg.HarnessURL+"/v1/health", nil)
	if err != nil {
		h.Error = err.Error()
		return h
	}
	if s.cfg.HarnessToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.HarnessToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.Error = err.Error()
		return h
	}
	defer resp.Body.Close()
	h.Status = resp.StatusCode
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	h.Body = string(body)
	h.Reachable = resp.StatusCode == http.StatusOK
	return h
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.respondJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repos, err := config.ParseRepos(s.cfg.Repos)
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, "parse repos: "+err.Error())
		return
	}
	var out []Repo
	for _, repo := range repos {
		rr := Repo{Repo: repo}
		wmErr := s.dbQueryRow(ctx, `SELECT watermark FROM tracker_watermarks WHERE repo = ?`, repo).Scan(&rr.Watermark)
		if wmErr == sql.ErrNoRows {
			rr.Watermark = "(never observed)"
		} else if wmErr != nil {
			rr.Watermark = "(error)"
		}
		var pollUnix int64
		if pollErr := s.dbQueryRow(ctx, `SELECT last_poll FROM tracker_polls WHERE repo = ?`, repo).Scan(&pollUnix); pollErr == nil {
			rr.LastPoll = time.Unix(0, pollUnix)
			rr.PollSeeded = true
		}
		out = append(out, rr)
	}
	s.respondJSON(w, r, http.StatusOK, out)
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := Queue{TotalByState: map[string]int{}}
	rows, err := s.dbQuery(ctx, `SELECT state, tier, COUNT(*) FROM tasks GROUP BY state, tier ORDER BY state, tier`)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "query queue: "+err.Error())
		return
	}
	for rows.Next() {
		var st StateTier
		if err := rows.Scan(&st.State, &st.Tier, &st.Count); err != nil {
			rows.Close()
			s.respondError(w, r, http.StatusInternalServerError, "scan queue: "+err.Error())
			return
		}
		q.ByStateTier = append(q.ByStateTier, st)
		q.TotalByState[st.State] += st.Count
	}
	rows.Close()
	s.respondJSON(w, r, http.StatusOK, q)
}

func (s *Server) handleAttempts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	const limit = 50
	rows, err := s.dbQuery(ctx, `
		SELECT a.id, a.task_id, a.ordinal, a.outcome, a.started_at, a.finished_at,
		       a.remote_session_id, a.artifacts, t.tier, t.state, t.ticket_url
		FROM attempts a
		JOIN tasks t ON t.id = a.task_id
		ORDER BY COALESCE(a.finished_at, a.started_at) DESC
		LIMIT ?`, limit)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "query attempts: "+err.Error())
		return
	}
	type attempt struct {
		ID              string     `json:"id"`
		TaskID          string     `json:"task_id"`
		Tier            string     `json:"tier"`
		TaskState       string     `json:"task_state"`
		TicketURL       string     `json:"ticket_url"`
		Ordinal         int        `json:"ordinal"`
		Outcome         string     `json:"outcome"`
		StartedAt       *time.Time `json:"started_at,omitempty"`
		FinishedAt      *time.Time `json:"finished_at,omitempty"`
		RemoteSessionID string     `json:"remote_session_id,omitempty"`
		Artifacts       []ArtifactRow `json:"artifacts"`
	}
	var out []attempt
	for rows.Next() {
		var a attempt
		var startedAt, finishedAt sql.NullInt64
		var outcome, remoteSession, artifactsJSON sql.NullString
		if err := rows.Scan(&a.ID, &a.TaskID, &a.Ordinal, &outcome, &startedAt, &finishedAt, &remoteSession, &artifactsJSON, &a.Tier, &a.TaskState, &a.TicketURL); err != nil {
			rows.Close()
			s.respondError(w, r, http.StatusInternalServerError, "scan attempts: "+err.Error())
			return
		}
		a.Outcome = outcome.String
		a.RemoteSessionID = remoteSession.String
		if startedAt.Valid {
			t := time.Unix(0, startedAt.Int64)
			a.StartedAt = &t
		}
		if finishedAt.Valid {
			t := time.Unix(0, finishedAt.Int64)
			a.FinishedAt = &t
		}
		if artifactsJSON.Valid && artifactsJSON.String != "" {
			_ = json.Unmarshal([]byte(artifactsJSON.String), &a.Artifacts)
		}
		out = append(out, a)
	}
	rows.Close()
	if out == nil {
		out = []attempt{}
	}
	s.respondJSON(w, r, http.StatusOK, out)
}

type ArtifactRow struct {
	ID            string `json:"id"`
	AttemptID     string `json:"attempt_id"`
	Kind          string `json:"kind"`
	BranchPurpose string `json:"branch_purpose,omitempty"`
	URI           string `json:"uri"`
	ProducedAt    int64  `json:"produced_at"`
}

func (s *Server) handleArtifacts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	const limit = 100
	rows, err := s.dbQuery(ctx, `
		SELECT art.id, art.attempt_id, art.kind, art.branch_purpose, art.uri, art.produced_at,
		       t.tier
		FROM artifacts art
		JOIN attempts a ON a.id = art.attempt_id
		JOIN tasks t ON t.id = a.task_id
		ORDER BY art.produced_at DESC
		LIMIT ?`, limit)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "query artifacts: "+err.Error())
		return
	}
	type artifactRow struct {
		ArtifactRow
		TaskTier string `json:"tier"`
	}
	var out []artifactRow
	for rows.Next() {
		var ar artifactRow
		var purpose sql.NullString
		var producedAt int64
		if err := rows.Scan(&ar.ID, &ar.AttemptID, &ar.Kind, &purpose, &ar.URI, &producedAt, &ar.TaskTier); err != nil {
			rows.Close()
			s.respondError(w, r, http.StatusInternalServerError, "scan artifacts: "+err.Error())
			return
		}
		ar.BranchPurpose = purpose.String
		ar.ProducedAt = producedAt
		out = append(out, ar)
	}
	rows.Close()
	if out == nil {
		out = []artifactRow{}
	}
	s.respondJSON(w, r, http.StatusOK, out)
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hb := Heartbeat{}
	var beatUnix int64
	if err := s.dbQueryRow(ctx, `SELECT beat_at FROM heartbeat WHERE id = 1`).Scan(&beatUnix); err == nil {
		hb.BeatAt = time.Unix(0, beatUnix)
		if time.Since(hb.BeatAt) > 3*s.cfg.PollInterval {
			hb.Stale = true
		}
	}
	_ = s.dbQueryRow(ctx, `SELECT run_lock FROM heartbeat WHERE id = 1`).Scan(&hb.LockHeld)
	s.respondJSON(w, r, http.StatusOK, hb)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	redacted := config.Redact(s.cfg)
	s.respondJSON(w, r, http.StatusOK, map[string]string{"config": config.Format(redacted)})
}

// handleTasks returns the full task list with pagination and optional
// state/tier filters. Empty result is `[]`, never `null`.
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := parseInt(v); err == nil && n > 0 {
			limit = n
		} else {
			s.respondError(w, r, http.StatusBadRequest, "limit: must be a positive integer")
			return
		}
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		if n, err := parseInt(v); err == nil && n >= 0 {
			offset = n
		} else {
			s.respondError(w, r, http.StatusBadRequest, "offset: must be a non-negative integer")
			return
		}
	}
	stateFilter := q.Get("state")
	tierFilter := q.Get("tier")
	if stateFilter != "" && !validTaskState(stateFilter) {
		s.respondError(w, r, http.StatusBadRequest, "state: invalid value")
		return
	}
	if tierFilter != "" && !validTaskTier(tierFilter) {
		s.respondError(w, r, http.StatusBadRequest, "tier: invalid value")
		return
	}

	// Build the WHERE clause dynamically.
	var where []string
	var args []interface{}
	if stateFilter != "" {
		where = append(where, "t.state = ?")
		args = append(args, stateFilter)
	}
	if tierFilter != "" {
		where = append(where, "t.tier = ?")
		args = append(args, tierFilter)
	}
	// Count total for pagination metadata.
	var total int
	countQuery := `SELECT COUNT(*) FROM tasks t`
	if len(where) > 0 {
		countQuery += " WHERE " + joinWhere(where)
	}
	if err := s.dbQueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "count tasks: "+err.Error())
		return
	}

	query := `
		SELECT t.id, t.repository_id, t.tier, t.state, t.prompt,
		       t.ticket_url, t.created_at, t.timeout_seconds, t.budget,
		       a.id, a.outcome, a.started_at, a.finished_at
		FROM tasks t
		LEFT JOIN (
			SELECT task_id, id, outcome, started_at, finished_at,
			       ROW_NUMBER() OVER (PARTITION BY task_id ORDER BY ordinal DESC) AS rn
			FROM attempts
		) a ON a.task_id = t.id AND a.rn = 1
	`
	if len(where) > 0 {
		query += " WHERE " + joinWhere(where)
	}
	query += " ORDER BY t.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.dbQuery(ctx, query, args...)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "query tasks: "+err.Error())
		return
	}
	defer rows.Close()

	type taskRow struct {
		ID            string     `json:"id"`
		RepositoryID  string     `json:"repository_id"`
		Tier          string     `json:"tier"`
		State         string     `json:"state"`
		Prompt        string     `json:"prompt"`
		TicketURL     string     `json:"ticket_url"`
		CreatedAt     int64      `json:"created_at"`
		TimeoutSecs   int        `json:"timeout_seconds"`
		Budget        int        `json:"budget"`
		AttemptID     string     `json:"attempt_id,omitempty"`
		Outcome       string     `json:"outcome,omitempty"`
		StartedAt     *time.Time `json:"started_at,omitempty"`
		FinishedAt    *time.Time `json:"finished_at,omitempty"`
	}
	var out []taskRow
	for rows.Next() {
		var tr taskRow
		var attemptID sql.NullString
		var outcome sql.NullString
		var startedAt, finishedAt sql.NullInt64
		if err := rows.Scan(&tr.ID, &tr.RepositoryID, &tr.Tier, &tr.State, &tr.Prompt,
			&tr.TicketURL, &tr.CreatedAt, &tr.TimeoutSecs, &tr.Budget,
			&attemptID, &outcome, &startedAt, &finishedAt); err != nil {
			s.respondError(w, r, http.StatusInternalServerError, "scan task: "+err.Error())
			return
		}
		tr.AttemptID = attemptID.String
		tr.Outcome = outcome.String
		if startedAt.Valid {
			t := time.Unix(0, startedAt.Int64)
			tr.StartedAt = &t
		}
		if finishedAt.Valid {
			t := time.Unix(0, finishedAt.Int64)
			tr.FinishedAt = &t
		}
		out = append(out, tr)
	}
	if out == nil {
		out = []taskRow{}
	}
	s.respondJSON(w, r, http.StatusOK, map[string]interface{}{
		"tasks": out,
		"total": total,
		"limit": limit,
		"offset": offset,
	})
}

func validTaskState(s string) bool {
	switch s {
	case "queued", "running", "succeeded", "escalated":
		return true
	default:
		return false
	}
}

func validTaskTier(s string) bool {
	switch s {
	case "P0", "P1", "P2", "P3":
		return true
	default:
		return false
	}
}

func joinWhere(parts []string) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString(p)
	}
	return b.String()
}

func parseInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errInvalidInt
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

var errInvalidInt = &intParseError{}

type intParseError struct{}

func (e *intParseError) Error() string { return "invalid integer" }

// Action payloads
type actionPollPayload struct {
	Repo string `json:"repo"`
}

type actionCancelPayload struct {
	TaskID string `json:"task_id"`
}

type actionRetryPayload struct {
	TaskID string `json:"task_id"`
}

// handleActionPoll writes a "poll" action to the dashboard_actions table.
func (s *Server) handleActionPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.respondError(w, r, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !s.cfg.DashboardActions {
		s.respondError(w, r, http.StatusForbidden, "dashboard actions disabled (set IDLE_DECK_DASHBOARD_ACTIONS=true)")
		return
	}
	var p actionPollPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		s.respondError(w, r, http.StatusBadRequest, "invalid JSON")
		return
	}
	if p.Repo == "" {
		s.respondError(w, r, http.StatusBadRequest, "repo required")
		return
	}
	actionID := uuid.New().String()
	now := time.Now().UnixNano()
	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO dashboard_actions (id, kind, payload, status, created_at) VALUES (?, 'poll', ?, 'pending', ?)`,
		actionID, `{"repo":"`+p.Repo+`"}`, now)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "insert action: "+err.Error())
		return
	}
	s.respondJSON(w, r, http.StatusOK, map[string]string{"action_id": actionID})
}

// handleActionCancel writes a "cancel" action to the dashboard_actions table.
func (s *Server) handleActionCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.respondError(w, r, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !s.cfg.DashboardActions {
		s.respondError(w, r, http.StatusForbidden, "dashboard actions disabled (set IDLE_DECK_DASHBOARD_ACTIONS=true)")
		return
	}
	var p actionCancelPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		s.respondError(w, r, http.StatusBadRequest, "invalid JSON")
		return
	}
	if p.TaskID == "" {
		s.respondError(w, r, http.StatusBadRequest, "task_id required")
		return
	}
	actionID := uuid.New().String()
	now := time.Now().UnixNano()
	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO dashboard_actions (id, kind, payload, status, created_at) VALUES (?, 'cancel', ?, 'pending', ?)`,
		actionID, `{"task_id":"`+p.TaskID+`"}`, now)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "insert action: "+err.Error())
		return
	}
	s.respondJSON(w, r, http.StatusOK, map[string]string{"action_id": actionID})
}

// handleActionRetry writes a "retry" action to the dashboard_actions table.
func (s *Server) handleActionRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.respondError(w, r, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !s.cfg.DashboardActions {
		s.respondError(w, r, http.StatusForbidden, "dashboard actions disabled (set IDLE_DECK_DASHBOARD_ACTIONS=true)")
		return
	}
	var p actionRetryPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		s.respondError(w, r, http.StatusBadRequest, "invalid JSON")
		return
	}
	if p.TaskID == "" {
		s.respondError(w, r, http.StatusBadRequest, "task_id required")
		return
	}
	actionID := uuid.New().String()
	now := time.Now().UnixNano()
	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO dashboard_actions (id, kind, payload, status, created_at) VALUES (?, 'retry', ?, 'pending', ?)`,
		actionID, `{"task_id":"`+p.TaskID+`"}`, now)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "insert action: "+err.Error())
		return
	}
	s.respondJSON(w, r, http.StatusOK, map[string]string{"action_id": actionID})
}
