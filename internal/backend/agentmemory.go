// agentmemory.go — HTTP adapter for the agentmemory server (task board +
// mail). Port of node-legacy/src/backend/agentmemory.ts. One startup probe
// decides the mode; failures degrade silently to "none", where the backend
// derives board+mail from opencode events. NEVER throws: every fetch is
// bounded (2s) and wrapped.
package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/theboringhumane/grafeio/internal/state"
)

const defaultAgentmemoryBase = "http://localhost:3111"
const agentmemoryProbeTimeout = 2 * time.Second

// Probe order from the backend spec: first 2xx JSON wins the lane.
// The signals lane needs ?agentId= (bare /agentmemory/signals is a 400 on
// the live server), so it carries its default query.
var boardCandidates = []string{"/agentmemory/actions", "/agentmemory/frontier", "/agentmemory/mail"}
var mailCandidates = []string{"/agentmemory/signals?agentId=grafeio", "/agentmemory/mail"}

// amKind: "actions" when the board lane probed live, "none" otherwise.
type amHandle struct {
	kind      string // "actions" | "none"
	baseURL   string
	winner    string // e.g. "GET /agentmemory/actions"
	boardLane string
	mailLane  string
	client    *http.Client
}

// probeAgentmemory probes the agentmemory server once. The winner endpoint
// is surfaced via handle.winner in the backend status line. Falls back to
// kind "none" (empty lists) on any failure.
func probeAgentmemory(baseURL string) *amHandle {
	if baseURL == "" {
		baseURL = defaultAgentmemoryBase
	}
	base := strings.TrimSuffix(baseURL, "/")
	h := &amHandle{
		kind:    "none",
		baseURL: base,
		winner:  "none (agentmemory unreachable)",
		client:  &http.Client{Timeout: agentmemoryProbeTimeout},
	}
	h.boardLane = h.probe(boardCandidates)
	if h.boardLane == "" {
		return h
	}
	h.mailLane = h.probe(mailCandidates)
	h.kind = "actions"
	h.winner = "GET " + h.boardLane
	return h
}

// probe returns the first candidate path that answers 2xx JSON, "" if none.
func (h *amHandle) probe(candidates []string) string {
	for _, path := range candidates {
		if _, ok := h.getJSON(path); ok {
			return path
		}
	}
	return ""
}

// getJSON fetches base+path with the bounded client; ok=false on any
// network error, non-2xx, or undecodable body.
func (h *amHandle) getJSON(path string) (any, bool) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, h.baseURL+path, nil)
	if err != nil {
		return nil, false
	}
	res, err := h.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, false
	}
	var v any
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

// postJSON sends a JSON POST against the bounded 2s client; ok=false on
// any network error or non-2xx. Returns the decoded body on success.
func (h *amHandle) postJSON(path string, body any) (any, bool) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, false
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.baseURL+path, strings.NewReader(string(data)))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := h.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, false
	}
	var v any
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		return nil, true // 2xx with non-JSON body still counts as success
	}
	return v, true
}

// CreateAction mirrors a queued office item onto the agentmemory board as
// a PENDING action (POST /agentmemory/actions — live probe 2026-08-21:
// {"title","status":"pending","priority","tags":[...]} -> 201; provenance
// rides tags, e.g. "source:grafeio","queueItem:<id>"). Best-effort: in
// "none" mode this is a no-op success returning ""; on error the caller
// drops it (status line only). NEVER throws; all I/O bounded at 2s.
func (h *amHandle) CreateAction(title string, itemID string) (string, error) {
	if h.kind != "actions" {
		return "", nil
	}
	v, ok := h.postJSON("/agentmemory/actions", map[string]any{
		"title":    title,
		"status":   "pending",
		"priority": 5,
		"tags":     []string{"source:grafeio", "queueItem:" + itemID},
	})
	if !ok {
		return "", errors.New("agentmemory create action: POST /agentmemory/actions failed")
	}
	if obj, ok := v.(map[string]any); ok {
		if act, ok := obj["action"].(map[string]any); ok {
			if id := str(act["id"]); id != "" {
				return id, nil
			}
		}
	}
	return "", errors.New("agentmemory create action: response had no action.id")
}

// MarkAction updates a board action's status
// (POST /agentmemory/actions/update — live probe 2026-08-21:
// {"actionId","status"} -> 200 with the updated action; /actions/<id> is
// 404 on this server). Best-effort: no-op success in "none" mode or for an
// empty id (offline create returned ""). NEVER throws; bounded at 2s.
func (h *amHandle) MarkAction(id string, status string) error {
	if h.kind != "actions" || id == "" {
		return nil
	}
	_, ok := h.postJSON("/agentmemory/actions/update", map[string]any{
		"actionId": id,
		"status":   status,
	})
	if !ok {
		return errors.New("agentmemory mark action: POST /agentmemory/actions/update failed")
	}
	return nil
}

// listActions returns board rows; [] in "none" mode.
func (h *amHandle) listActions() []state.BoardTask {
	if h.kind != "actions" {
		return nil
	}
	v, ok := h.getJSON(h.boardLane)
	if !ok {
		return nil
	}
	return normalizeTasks(v)
}

// listMails returns mail items; [] when the mail lane never probed.
func (h *amHandle) listMails() []state.MailItem {
	if h.kind != "actions" || h.mailLane == "" {
		return nil
	}
	v, ok := h.getJSON(h.mailLane)
	if !ok {
		return nil
	}
	return normalizeMails(v)
}

// pickArray returns the first array found under any of the given keys (or a
// top-level array), filtered to objects.
func pickArray(v any, keys ...string) []map[string]any {
	toRows := func(arr []any) []map[string]any {
		rows := make([]map[string]any, 0, len(arr))
		for _, x := range arr {
			if m, ok := x.(map[string]any); ok {
				rows = append(rows, m)
			}
		}
		return rows
	}
	if arr, ok := v.([]any); ok {
		return toRows(arr)
	}
	if obj, ok := v.(map[string]any); ok {
		for _, k := range keys {
			if arr, ok := obj[k].([]any); ok {
				return toRows(arr)
			}
		}
	}
	return nil
}

func taskStatusFrom(raw any) state.TaskStatus {
	s := strings.ToLower(str(raw))
	switch s {
	case "in-progress", "in_progress", "active", "leased", "doing":
		return state.TaskInProgress
	case "done", "completed", "complete", "cancelled", "closed":
		return state.TaskDone
	default:
		return state.TaskPending
	}
}

// epochMs accepts numbers (ms epoch) or date strings; falls back to now.
func epochMs(raw any) int64 {
	switch n := raw.(type) {
	case float64:
		return int64(n)
	case string:
		if v, err := strconv.ParseInt(n, 10, 64); err == nil {
			return v
		}
		if t, err := time.Parse(time.RFC3339, n); err == nil {
			return t.UnixMilli()
		}
	}
	return nowMs()
}

func str(raw any) string {
	switch s := raw.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return fmt.Sprint(s)
	}
}

func sliceMax(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}

// normalizeTasks maps actions/frontier rows -> BoardTask. Field names are
// best-effort, exactly as the TS oracle did.
func normalizeTasks(v any) []state.BoardTask {
	rows := pickArray(v, "actions", "items", "data")
	unwrapped := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if inner, ok := row["action"].(map[string]any); ok {
			unwrapped = append(unwrapped, inner)
		} else {
			unwrapped = append(unwrapped, row)
		}
	}
	var tasks []state.BoardTask
	for _, row := range unwrapped {
		id := str(row["id"])
		if id == "" {
			continue
		}
		title := str(row["title"])
		if title == "" {
			title = str(row["name"])
		}
		if title == "" {
			title = "(untitled action)"
		}
		owner := ""
		if cb, ok := row["createdBy"].(string); ok && cb != "unknown" {
			owner = cb
		}
		atRaw, ok := row["createdAt"]
		if !ok || atRaw == nil {
			atRaw = row["updatedAt"]
		}
		tasks = append(tasks, state.BoardTask{
			ID:     id,
			Title:  sliceMax(title, 80),
			Status: taskStatusFrom(row["status"]),
			Owner:  owner,
			At:     epochMs(atRaw),
		})
	}
	return tasks
}

// normalizeMails maps signals rows -> MailItem. The live schema is loose;
// map defensively.
func normalizeMails(v any) []state.MailItem {
	rows := pickArray(v, "signals", "items", "data", "mail")
	var mails []state.MailItem
	for _, row := range rows {
		body := str(row["content"])
		if body == "" {
			body = str(row["body"])
		}
		if body == "" {
			body = str(row["text"])
		}
		body = sliceMax(body, 240)
		if body == "" {
			continue
		}
		kind := strings.ToLower(firstNonEmpty(str(row["type"]), str(row["kind"])))
		id := str(row["id"])
		if id == "" {
			id = str(row["signalId"])
		}
		if id == "" {
			id = "sig-" + itoa(len(mails))
		}
		from := firstNonEmpty(str(row["from"]), str(row["sender"]))
		if from == "" {
			from = "agentmemory"
		}
		to := firstNonEmpty(str(row["to"]), str(row["agentId"]))
		if to == "" {
			to = "manager"
		}
		atRaw, ok := row["createdAt"]
		if !ok || atRaw == nil {
			atRaw = row["at"]
		}
		subject := firstNonEmpty(str(row["subject"]), str(row["type"]), str(row["name"]))
		if subject == "" {
			subject = "signal"
		}
		mk := state.MailNotice
		if strings.Contains(kind, "return") {
			mk = state.MailReturn
		} else if strings.Contains(kind, "brief") {
			mk = state.MailBrief
		}
		mails = append(mails, state.MailItem{
			ID:      id,
			From:    from,
			To:      to,
			At:      epochMs(atRaw),
			Subject: sliceMax(subject, 80),
			Body:    body,
			Kind:    mk,
		})
	}
	return mails
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
