package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Session headers in the order CLIProxyAPI itself prefers them.
var sessionHeaders = []string{"X-Claude-Code-Session-Id", "Session-Id", "Session_id", "X-Session-Id"}

// modelClaim matches a per-model weekly limit named in the representative claim, seven_day_<family>; multi-word claims such as seven_day_overage_included are not model limits.
var modelClaim = regexp.MustCompile(`^seven_day_([a-z]+)$`)

// scopedWeeklyStatus matches the status header of a per-model weekly bucket, Anthropic-Ratelimit-Unified-7d_<scope>-*, such as 7d_oi for the top model.
// Claude sends the bucket only on requests to the model it scopes, so the request's model names the family, not the scope token.
var scopedWeeklyStatus = regexp.MustCompile(`^anthropic-ratelimit-unified-7d_([a-z]+)-status$`)

type config struct {
	Mode         string        `yaml:"mode"`          // shadow (default) or active
	TTL          time.Duration `yaml:"ttl"`           // idle time before a binding is forgotten
	RebindAfter  time.Duration `yaml:"rebind_after"`  // how long a bound account may be missing before the session moves
	MinRemaining float64       `yaml:"min_remaining"` // weekly percent below which new sessions avoid an account
	StateFile    string        `yaml:"state_file"`
}

func defaultConfig() config {
	return config{Mode: "shadow", TTL: 168 * time.Hour, RebindAfter: 15 * time.Minute, MinRemaining: 5, StateFile: "plugins/expiring-quota-first-data/state.json"}
}

type binding struct {
	AuthID       string    `json:"auth_id"`
	LastSeen     time.Time `json:"last_seen"`
	MissingSince time.Time `json:"missing_since,omitzero"`
}

// account holds what response headers have said about one credential.
type account struct {
	WeeklyReset time.Time `json:"weekly_reset,omitzero"`
	WeeklyUsed  float64   `json:"weekly_used"` // 0-1, meaningful while WeeklyReset is ahead
	HasUsed     bool      `json:"has_used,omitempty"`
	// FullUntil is the reset of a weekly window seen used up. It stays set after that time passes, until headers show a new window.
	FullUntil      time.Time            `json:"full_until,omitzero"`
	ModelFullUntil map[string]time.Time `json:"model_full_until,omitempty"` // by family; cleared by a successful request for that family after the reset
	AuthFailed     int                  `json:"auth_failed,omitempty"`      // 401 or 403 with no success since
	AuthFailedAt   time.Time            `json:"auth_failed_at,omitzero"`
	Served         bool                 `json:"served,omitempty"` // answered at least once, so missing weekly headers mean it sends none
}

// authFailedFor bounds how long a rejected login keeps an account unusable; past it the account is only a last resort until a request succeeds.
const authFailedFor = 30 * time.Minute

type decision struct {
	At      time.Time `json:"at"`
	Session string    `json:"session"`
	Model   string    `json:"model,omitempty"`
	AuthID  string    `json:"auth_id"`
	Reason  string    `json:"reason"`
	Applied bool      `json:"applied"`
}

type state struct {
	Mode     string              `json:"mode"`
	Bindings map[string]*binding `json:"bindings"`
	Accounts map[string]*account `json:"accounts"`
	Recent   []decision          `json:"recent_decisions"`
}

type router struct {
	mu     sync.Mutex
	saveMu sync.Mutex // one writer of the state file at a time
	cfg    config
	st     state
	dirty  bool
	now    func() time.Time
}

func newRouter(cfg config) *router {
	r := &router{cfg: cfg, now: func() time.Time { return time.Now().UTC() }, st: state{Mode: cfg.Mode, Bindings: map[string]*binding{}, Accounts: map[string]*account{}}}
	raw, err := os.ReadFile(cfg.StateFile)
	if err != nil {
		return r
	}
	var loaded state
	if err := json.Unmarshal(raw, &loaded); err != nil {
		// Kept aside rather than overwritten by the next save, so a bad file can still be looked at.
		log.Printf("expiring-quota-first: unreadable state file, starting fresh and keeping it as %s.corrupt: %v", cfg.StateFile, err)
		_ = os.Rename(cfg.StateFile, cfg.StateFile+".corrupt")
		return r
	}
	// Shadow-mode bindings name accounts CLIProxyAPI never actually used, so an active run starts without them.
	if loaded.Mode == cfg.Mode {
		for k, b := range loaded.Bindings {
			if b != nil && b.AuthID != "" {
				r.st.Bindings[k] = b
			}
		}
	}
	for k, a := range loaded.Accounts {
		if a != nil {
			r.st.Accounts[k] = a
		}
	}
	r.st.Recent = loaded.Recent
	r.dirty = loaded.Mode != cfg.Mode
	return r
}

// standing ranks how fit an account is for a new session, best first.
type standing int

const (
	ready    standing = iota // room left, or nothing known yet
	awaiting                 // only traffic can confirm it is back: its reset passed but no header has shown the new window yet, or its login was rejected and no request has succeeded since
	low                      // under min_remaining of its weekly window
	unusable                 // used up for this model, or its login was rejected recently
)

// standing returns the account's rank for a new session on model, with the reason whenever it is not ready.
// offered tells whether CLIProxyAPI still lists the account. CLIProxyAPI refreshes an expired token after its first 401 and drops a revoked login from the candidates, so a 401 makes the account unusable only once CLIProxyAPI has dropped it.
func (r *router) standing(id, model string, offered bool, now time.Time) (standing, string) {
	a := r.st.Accounts[id]
	if a == nil {
		return ready, ""
	}
	if recent := now.Sub(a.AuthFailedAt) < authFailedFor; recent && (a.AuthFailed == 403 || a.AuthFailed == 401 && !offered) {
		return unusable, "login rejected (" + strconv.Itoa(a.AuthFailed) + ")"
	}
	if !a.FullUntil.IsZero() && now.Before(a.FullUntil) {
		return unusable, "weekly limit used up"
	}
	if fam, until, ok := a.modelFull(model); ok && now.Before(until) {
		return unusable, fam + " weekly limit used up"
	}
	if !a.FullUntil.IsZero() {
		return awaiting, "weekly reset not yet confirmed"
	}
	if _, _, ok := a.modelFull(model); ok {
		return awaiting, "model weekly reset not yet confirmed"
	}
	if a.AuthFailed != 0 {
		return awaiting, "login rejected once, not yet recovered"
	}
	if a.HasUsed && a.WeeklyReset.After(now) && (1-a.WeeklyUsed)*100 < r.cfg.MinRemaining {
		return low, "under " + strconv.FormatFloat(r.cfg.MinRemaining, 'f', -1, 64) + "% weekly left"
	}
	return ready, ""
}

func (a *account) modelFull(model string) (string, time.Time, bool) {
	m := strings.ToLower(model)
	for fam, until := range a.ModelFullUntil {
		if strings.Contains(m, fam) {
			return fam, until, true
		}
	}
	return "", time.Time{}, false
}

func (r *router) pick(req pluginapi.SchedulerPickRequest) pluginapi.SchedulerPickResponse {
	unhandled := pluginapi.SchedulerPickResponse{}
	provider := routeProvider(req)
	if provider != "claude" && provider != "codex" || len(req.Candidates) == 0 {
		return unhandled
	}
	key, parentKey := sessionKeys(provider, req)
	if key == "" {
		return unhandled
	}
	key = shortHash(key)
	if parentKey != "" {
		parentKey = shortHash(parentKey)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	offered := map[string]bool{}
	for _, c := range req.Candidates {
		offered[c.ID] = true
	}
	fit := func(id string) bool { s, _ := r.standing(id, req.Model, offered[id], now); return s != unusable }

	b := r.st.Bindings[key]
	if b != nil && now.Sub(b.LastSeen) > r.cfg.TTL {
		b = nil
	}
	var authID, reason string
	switch {
	case b != nil && !fit(b.AuthID):
		_, why := r.standing(b.AuthID, req.Model, offered[b.AuthID], now)
		if authID, reason = r.best(req.Candidates, req.Model, now); authID != "" {
			reason = "bound account " + why + ", rebinding; " + reason
			r.st.Bindings[key] = &binding{AuthID: authID, LastSeen: now}
		} else {
			reason = "bound account " + why + ", no other account to move to"
			b.LastSeen = now
		}
	case b != nil && offered[b.AuthID]:
		authID, reason = b.AuthID, "bound"
		b.LastSeen, b.MissingSince = now, time.Time{}
	case b != nil:
		if b.MissingSince.IsZero() {
			b.MissingSince = now
		}
		authID, reason = r.best(req.Candidates, req.Model, now)
		if authID == "" || now.Sub(b.MissingSince) < r.cfg.RebindAfter {
			reason = "bound account unavailable, failing over for this request; " + reason
		} else {
			reason = "bound account unavailable too long, rebinding; " + reason
			r.st.Bindings[key] = &binding{AuthID: authID, LastSeen: now}
		}
	default:
		if p := r.st.Bindings[parentKey]; parentKey != "" && p != nil && offered[p.AuthID] && fit(p.AuthID) && now.Sub(p.LastSeen) <= r.cfg.TTL {
			authID, reason = p.AuthID, "subagent, inherits parent's account"
		} else {
			authID, reason = r.best(req.Candidates, req.Model, now)
			reason = "new session; " + reason
		}
		if authID != "" {
			r.st.Bindings[key] = &binding{AuthID: authID, LastSeen: now}
		}
	}

	active := r.cfg.Mode == "active" && authID != ""
	r.record(decision{At: now, Session: key, Model: req.Model, AuthID: authID, Reason: reason, Applied: active})
	if !active {
		return unhandled
	}
	return pluginapi.SchedulerPickResponse{AuthID: authID, Handled: true}
}

// best picks the fittest standing, then within it accounts with no current reset time first, so traffic learns it, then the soonest reset, then accounts that never report one.
// It returns "" when every candidate is unusable, leaving the request to CLIProxyAPI.
func (r *router) best(cands []pluginapi.SchedulerAuthCandidate, model string, now time.Time) (string, string) {
	type option struct {
		id    string
		s     standing
		reset time.Time // zero when unknown
		mute  bool      // served without weekly headers, so its reset will stay unknown
	}
	opts := make([]option, 0, len(cands))
	for _, c := range cands {
		o := option{id: c.ID}
		o.s, _ = r.standing(c.ID, model, true, now)
		if a := r.st.Accounts[c.ID]; a != nil && a.WeeklyReset.After(now) {
			o.reset = a.WeeklyReset
		} else if a != nil {
			o.mute = a.Served && a.WeeklyReset.IsZero()
		}
		opts = append(opts, o)
	}
	sort.Slice(opts, func(i, j int) bool {
		a, b := opts[i], opts[j]
		if a.s != b.s {
			return a.s < b.s
		}
		if a.mute != b.mute {
			return b.mute
		}
		if a.reset.IsZero() != b.reset.IsZero() {
			return a.reset.IsZero()
		}
		if !a.reset.Equal(b.reset) {
			return a.reset.Before(b.reset)
		}
		return a.id < b.id
	})
	o := opts[0]
	switch {
	case o.s == unusable:
		return "", "no usable account, left to CLIProxyAPI"
	case o.s == awaiting:
		return o.id, "no account ready, trying one whose reset passed"
	case o.s == low:
		return o.id, "every account under " + strconv.FormatFloat(r.cfg.MinRemaining, 'f', -1, 64) + "% weekly left, soonest reset"
	case o.mute:
		return o.id, "only accounts that report no weekly reset left"
	case o.reset.IsZero():
		return o.id, "weekly reset not known yet"
	}
	return o.id, "soonest weekly reset"
}

func (r *router) observe(rec pluginapi.UsageRecord) {
	if rec.AuthID == "" {
		return
	}
	now := r.now()
	h := rec.ResponseHeaders
	w, hasWeekly := weeklyWindow(h, now)
	claimFam, claimUntil, hasClaim := rejectedClaim(h)
	status := rec.Failure.StatusCode

	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.st.Accounts[rec.AuthID]
	if a == nil {
		a = &account{}
		r.st.Accounts[rec.AuthID] = a
	}
	before, _ := json.Marshal(a)

	switch {
	case rec.Failed && (status == 401 || status == 403) && !cloudflareChallenge(rec):
		a.AuthFailed, a.AuthFailedAt = status, now
	case !rec.Failed:
		a.AuthFailed, a.AuthFailedAt, a.Served = 0, time.Time{}, true
	}
	if hasWeekly {
		a.WeeklyReset = w.reset
		a.WeeklyUsed, a.HasUsed = w.used, w.hasUsed
		switch {
		case w.full:
			a.FullUntil = w.reset
		case a.FullUntil.IsZero():
		// Once the marked reset has passed, a window with room is the new window.
		case !now.Before(a.FullUntil):
			a.FullUntil = time.Time{}
		// Before that, only a served request from another window lifts the mark. A failed one proves nothing, however its reset differs: during vendor incidents Codex has reported resets days apart on consecutive responses (openai/codex#23190).
		// Same-window successes do not count either, since a long request started before the limit was hit can finish after it; an hour covers the drift of Codex's relative resets.
		case !rec.Failed && w.reset.Sub(a.FullUntil).Abs() > time.Hour:
			a.FullUntil = time.Time{}
		}
	}
	markModelFull := func(fam string, until time.Time) {
		if a.ModelFullUntil == nil {
			a.ModelFullUntil = map[string]time.Time{}
		}
		a.ModelFullUntil[fam] = until
	}
	scopedUntil, scopedFull := scopedWeeklyFull(h)
	switch {
	case hasClaim && claimFam == "":
		a.FullUntil = claimUntil
	case hasClaim:
		markModelFull(claimFam, claimUntil)
	}
	switch fam := modelFamily(rec.Model); {
	case scopedFull && fam != "":
		markModelFull(fam, scopedUntil)
	case !rec.Failed && !hasClaim:
		if fam, until, ok := a.modelFull(rec.Model); ok && !now.Before(until) {
			delete(a.ModelFullUntil, fam)
		}
	}

	if after, _ := json.Marshal(a); string(after) != string(before) {
		r.dirty = true
	}
}

// cloudflareChallenge tells Cloudflare's bot challenge, served as a 403, from a rejected login, by the marks CLIProxyAPI itself looks for (isCloudflareChallengeErrorMessage in sdk/cliproxy/auth/conductor_cooldown.go).
func cloudflareChallenge(rec pluginapi.UsageRecord) bool {
	if header(rec.ResponseHeaders, "Cf-Mitigated") != "" {
		return true
	}
	body := strings.ToLower(rec.Failure.Body)
	return strings.Contains(body, "challenge-platform") || strings.Contains(body, "cf-mitigated") || strings.Contains(body, "cloudflare challenge") ||
		strings.Contains(body, "just a moment") && strings.Contains(body, "cloudflare")
}

type weekly struct {
	reset   time.Time
	used    float64
	hasUsed bool
	full    bool
}

// weeklyWindow reads Claude's 7-day window or whichever Codex window lasts a week.
func weeklyWindow(h map[string][]string, now time.Time) (weekly, bool) {
	get := func(name string) string { return header(h, name) }
	if reset, ok := unixHeader(get("Anthropic-Ratelimit-Unified-7d-Reset")); ok {
		w := weekly{reset: reset}
		if u, err := strconv.ParseFloat(get("Anthropic-Ratelimit-Unified-7d-Utilization"), 64); err == nil && u >= 0 {
			w.used, w.hasUsed = min(u, 1), true
		}
		w.full = w.hasUsed && w.used >= 1 || strings.EqualFold(get("Anthropic-Ratelimit-Unified-7d-Status"), "rejected")
		return w, true
	}
	for _, slot := range []string{"Primary", "Secondary"} {
		base := "X-Codex-" + slot + "-"
		if get(base+"Window-Minutes") != "10080" {
			continue
		}
		w := weekly{}
		if t, ok := unixHeader(get(base + "Reset-At")); ok {
			w.reset = t
		} else if sec, err := strconv.ParseInt(get(base+"Reset-After-Seconds"), 10, 64); err == nil {
			w.reset = now.Add(time.Duration(sec) * time.Second).UTC().Truncate(time.Minute)
		} else {
			continue
		}
		if p, err := strconv.ParseFloat(get(base+"Used-Percent"), 64); err == nil && p >= 0 {
			w.used, w.hasUsed = min(p/100, 1), true
		}
		w.full = w.hasUsed && w.used >= 1
		return w, true
	}
	return weekly{}, false
}

// rejectedClaim reads which Claude weekly limit refused a request: family "" for the shared weekly window, or the model family of a per-model limit.
func rejectedClaim(h map[string][]string) (family string, until time.Time, ok bool) {
	if !strings.EqualFold(header(h, "Anthropic-Ratelimit-Unified-Status"), "rejected") {
		return "", time.Time{}, false
	}
	until, ok = unixHeader(header(h, "Anthropic-Ratelimit-Unified-Reset"))
	if !ok {
		return "", time.Time{}, false
	}
	claim := header(h, "Anthropic-Ratelimit-Unified-Representative-Claim")
	if claim == "seven_day" {
		return "", until, true
	}
	if m := modelClaim.FindStringSubmatch(claim); m != nil {
		return m[1], until, true
	}
	return "", time.Time{}, false
}

// scopedWeeklyFull reports whether a per-model weekly bucket in the response is used up, and until when.
func scopedWeeklyFull(h map[string][]string) (time.Time, bool) {
	for k := range h {
		m := scopedWeeklyStatus.FindStringSubmatch(strings.ToLower(k))
		if m == nil {
			continue
		}
		base := "Anthropic-Ratelimit-Unified-7d_" + m[1] + "-"
		full := strings.EqualFold(header(h, base+"Status"), "rejected")
		if u, err := strconv.ParseFloat(header(h, base+"Utilization"), 64); err == nil && u >= 1 {
			full = true
		}
		if !full {
			continue
		}
		if until, ok := unixHeader(header(h, base+"Reset")); ok {
			return until, true
		}
	}
	return time.Time{}, false
}

// modelFamily reads the family of a Claude model id: claude-fable-5-1 is fable, claude-haiku-4-5-20251001 is haiku. Other ids have none.
func modelFamily(model string) string {
	rest, ok := strings.CutPrefix(strings.ToLower(model), "claude-")
	if !ok {
		return ""
	}
	fam, _, _ := strings.Cut(rest, "-")
	return fam
}

func unixHeader(v string) (time.Time, bool) {
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, 0).UTC(), true
}

// routeProvider reads the single provider of the route; CLIProxyAPI v8.0.33 picks every request through its mixed path, which leaves Provider empty and lists the route's providers in Providers.
func routeProvider(req pluginapi.SchedulerPickRequest) string {
	if req.Provider != "" {
		return req.Provider
	}
	if len(req.Providers) == 1 {
		return req.Providers[0]
	}
	return ""
}

// sessionKeys prefers CLIProxyAPI's own session identity, which tells a Claude Code subagent ("claude:<sid>:agent:<id>") from its parent ("claude:<sid>").
// The headers are a fallback for requests where CLIProxyAPI did not set it, shaped to match its format for Claude Code main sessions.
func sessionKeys(provider string, req pluginapi.SchedulerPickRequest) (key, parentKey string) {
	sid, _ := req.Options.Metadata["canonical_session_id"].(string)
	parent, _ := req.Options.Metadata["parent_session_id"].(string)
	if sid == "" {
		for _, name := range sessionHeaders {
			if v := header(req.Options.Headers, name); v != "" {
				sid = v
				if name == "X-Claude-Code-Session-Id" {
					sid = "claude:" + v
				}
				break
			}
		}
	}
	if sid == "" {
		return "", ""
	}
	scope, _ := req.Options.Metadata["caller_scope"].(string)
	key = provider + "|" + scope + "|" + sid
	if parent != "" && parent != sid {
		parentKey = provider + "|" + scope + "|" + parent
	}
	return key, parentKey
}

func header(h map[string][]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
	}
	return ""
}

func (r *router) record(d decision) {
	r.st.Recent = append(r.st.Recent, d)
	if len(r.st.Recent) > 200 {
		r.st.Recent = r.st.Recent[len(r.st.Recent)-200:]
	}
	r.dirty = true
}

func (r *router) save() error {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return nil
	}
	now := r.now()
	for k, b := range r.st.Bindings {
		if now.Sub(b.LastSeen) > r.cfg.TTL {
			delete(r.st.Bindings, k)
		}
	}
	raw, err := json.Marshal(r.st)
	r.dirty = false
	r.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := r.cfg.StateFile + ".tmp"
	err = os.MkdirAll(filepath.Dir(r.cfg.StateFile), 0o700)
	if err == nil {
		err = writeSynced(tmp, raw)
	}
	if err == nil {
		err = os.Rename(tmp, r.cfg.StateFile)
	}
	if err != nil {
		r.mu.Lock()
		r.dirty = true // retry on the next tick
		r.mu.Unlock()
	}
	return err
}

// writeSynced flushes before the rename, so a power cut leaves the old file or the new one, never an empty one.
func writeSynced(path string, raw []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if errClose := f.Close(); err == nil {
		err = errClose
	}
	return err
}

// shortHash keeps raw session IDs out of the state file and the decision log.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}
