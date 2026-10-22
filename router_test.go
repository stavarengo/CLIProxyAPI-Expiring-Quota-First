package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func testRouter(t *testing.T, now *time.Time) *router {
	cfg := defaultConfig()
	cfg.Mode = "active"
	cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	r := newRouter(cfg)
	r.now = func() time.Time { return *now }
	return r
}

func pickReq(session string, ids ...string) pluginapi.SchedulerPickRequest {
	// Shaped like what CLIProxyAPI v8.0.33 actually sends: Provider empty, the route's provider in Providers.
	req := pluginapi.SchedulerPickRequest{Providers: []string{"claude"}, Options: pluginapi.SchedulerOptions{Headers: map[string][]string{"X-Claude-Code-Session-Id": {session}}}}
	for _, id := range ids {
		req.Candidates = append(req.Candidates, pluginapi.SchedulerAuthCandidate{ID: id, Provider: "claude"})
	}
	return req
}

func claudeReset(r *router, id string, at time.Time) {
	r.observe(pluginapi.UsageRecord{AuthID: id, ResponseHeaders: map[string][]string{"Anthropic-Ratelimit-Unified-7d-Reset": {strconv.FormatInt(at.Unix(), 10)}}})
}

func TestNewSessionsGoToSoonestResetAndStay(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeReset(r, "a", now.Add(96*time.Hour))
	claudeReset(r, "b", now.Add(20*time.Hour))

	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "b" {
		t.Fatalf("new session picked %q, want b (resets soonest)", got)
	}
	// b's reset passes, a Codex-style reset moves it a week out: s1 must stay, a new session moves.
	claudeReset(r, "b", now.Add(168*time.Hour))
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "b" {
		t.Fatalf("existing session moved to %q", got)
	}
	if got := r.pick(pickReq("s2", "a", "b")).AuthID; got != "a" {
		t.Fatalf("new session picked %q, want a", got)
	}
}

func TestUnknownAccountsGetASessionFirst(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeReset(r, "a", now.Add(time.Hour))
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "b" {
		t.Fatalf("picked %q, want b (no reset time known yet)", got)
	}
}

func TestFailoverThenRebind(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	r.pick(pickReq("s1", "a"))
	if got := r.pick(pickReq("s1", "b")).AuthID; got != "b" {
		t.Fatalf("failover picked %q", got)
	}
	if got := r.st.Bindings[keyOf(pickReq("s1"))].AuthID; got != "a" {
		t.Fatalf("short outage rebound session to %q", got)
	}
	now = now.Add(20 * time.Minute)
	r.pick(pickReq("s1", "b"))
	if got := r.st.Bindings[keyOf(pickReq("s1"))].AuthID; got != "b" {
		t.Fatalf("long outage kept binding on %q", got)
	}
}

func TestBindingsSurviveRestart(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeReset(r, "a", now.Add(time.Hour))
	claudeReset(r, "b", now.Add(2*time.Hour))
	r.pick(pickReq("s1", "a", "b"))
	if err := r.save(); err != nil {
		t.Fatal(err)
	}
	r2 := newRouter(r.cfg)
	r2.now = r.now
	claudeReset(r2, "b", now.Add(30*time.Minute))
	if got := r2.pick(pickReq("s1", "a", "b")).AuthID; got != "a" {
		t.Fatalf("after restart session moved to %q", got)
	}
}

func TestShadowModeDoesNotRoute(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	r.cfg.Mode = "shadow"
	if resp := r.pick(pickReq("s1", "a")); resp.Handled {
		t.Fatal("shadow mode handled the pick")
	}
	if len(r.st.Recent) != 1 || r.st.Recent[0].Applied {
		t.Fatalf("shadow decision not recorded: %+v", r.st.Recent)
	}
}

func TestCodexWeeklyWindow(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	got, ok := weeklyWindow(map[string][]string{
		"X-Codex-Primary-Window-Minutes":        {"300"},
		"X-Codex-Primary-Reset-At":              {"1"},
		"X-Codex-Primary-Used-Percent":          {"100"},
		"X-Codex-Secondary-Window-Minutes":      {"10080"},
		"X-Codex-Secondary-Reset-After-Seconds": {"3600"},
		"X-Codex-Secondary-Used-Percent":        {"40"},
	}, now)
	if !ok || !got.reset.Equal(now.Add(time.Hour)) || got.used != 0.4 || got.full {
		t.Fatalf("got %+v %v, want the weekly window an hour out at 40%%", got, ok)
	}
}

func TestNoSessionHeaderLeavesItToCPA(t *testing.T) {
	now := time.Now()
	r := testRouter(t, &now)
	req := pickReq("", "a")
	req.Options.Headers = nil
	if r.pick(req).Handled {
		t.Fatal("handled a request without a session")
	}
}

func keyOf(req pluginapi.SchedulerPickRequest) string {
	key, _ := sessionKeys("claude", req)
	return shortHash(key)
}

func TestSubagentInheritsParentAccount(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeReset(r, "a", now.Add(96*time.Hour))
	claudeReset(r, "b", now.Add(20*time.Hour))
	parent := pickReq("s1", "a", "b")
	parent.Options.Metadata = map[string]any{"canonical_session_id": "claude:s1"}
	if got := r.pick(parent).AuthID; got != "b" {
		t.Fatalf("parent picked %q", got)
	}
	// The ranking flips, but a new subagent of s1 must follow its parent, not the ranking.
	claudeReset(r, "b", now.Add(168*time.Hour))
	sub := pickReq("s1", "a", "b")
	sub.Options.Metadata = map[string]any{"canonical_session_id": "claude:s1:agent:x", "parent_session_id": "claude:s1"}
	if got := r.pick(sub).AuthID; got != "b" {
		t.Fatalf("subagent picked %q, want its parent's b", got)
	}
}

func TestHeaderFallbackMatchesCanonicalKey(t *testing.T) {
	withMeta := pickReq("s1")
	withMeta.Options.Metadata = map[string]any{"canonical_session_id": "claude:s1"}
	if keyOf(withMeta) != keyOf(pickReq("s1")) {
		t.Fatal("header fallback and CLIProxyAPI identity give different keys for the same main session")
	}
}

func TestShadowBindingsDoNotCarryIntoActive(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	r.cfg.Mode, r.st.Mode = "shadow", "shadow"
	claudeReset(r, "a", now.Add(time.Hour))
	r.pick(pickReq("s1", "a"))
	if err := r.save(); err != nil {
		t.Fatal(err)
	}
	cfg := r.cfg
	cfg.Mode = "active"
	r2 := newRouter(cfg)
	if len(r2.st.Bindings) != 0 {
		t.Fatalf("shadow bindings leaked into active mode: %v", r2.st.Bindings)
	}
	if _, ok := r2.st.Accounts["a"]; !ok {
		t.Fatal("reset times learned in shadow mode were dropped")
	}
}

func TestCorruptBindingIsIgnored(t *testing.T) {
	cfg := defaultConfig()
	cfg.Mode = "active"
	cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(cfg.StateFile, []byte(`{"mode":"active","bindings":{"k":null}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRouter(cfg)
	r.dirty = true
	if err := r.save(); err != nil {
		t.Fatal(err)
	}
}

func TestUnreadableStateFileIsKeptAside(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(cfg.StateFile, []byte(`{"mode":`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRouter(cfg)
	r.dirty = true
	if err := r.save(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(cfg.StateFile + ".corrupt"); err != nil || string(raw) != `{"mode":` {
		t.Fatalf("damaged file not kept aside: %q, %v", raw, err)
	}
}

func TestReconfigureKeepsWhatTheOldRouterLearned(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	conf := func(mode string) {
		raw := []byte(`{"config_yaml":"` + base64.StdEncoding.EncodeToString([]byte("mode: "+mode+"\nstate_file: "+file)) + `"}`)
		if err := configure(raw); err != nil {
			t.Fatal(err)
		}
	}
	conf("shadow")
	t.Cleanup(stop)
	claudeReset(active(), "a", time.Now().Add(time.Hour))
	conf("active") // no tick has saved yet: only the old router's final save carries a
	if _, ok := active().st.Accounts["a"]; !ok {
		t.Fatal("reconfigure lost the account the old router had just learned")
	}
}

func claudeWeekly(r *router, id string, at time.Time, used string) {
	r.observe(pluginapi.UsageRecord{AuthID: id, ResponseHeaders: map[string][]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":       {strconv.FormatInt(at.Unix(), 10)},
		"Anthropic-Ratelimit-Unified-7d-Utilization": {used},
	}})
}

func rejected(r *router, id, model, claim string, until time.Time) {
	r.observe(pluginapi.UsageRecord{AuthID: id, Model: model, Failed: true, Failure: pluginapi.UsageFailure{StatusCode: 429}, ResponseHeaders: map[string][]string{
		"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
		"Anthropic-Ratelimit-Unified-Representative-Claim": {claim},
		"Anthropic-Ratelimit-Unified-Reset":                {strconv.FormatInt(until.Unix(), 10)},
	}})
}

func modelReq(session, model string, ids ...string) pluginapi.SchedulerPickRequest {
	req := pickReq(session, ids...)
	req.Model = model
	return req
}

func TestUsedUpAccountGetsNoNewSessionUntilANewWindowShows(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeWeekly(r, "a", now.Add(2*time.Hour), "1.0")
	claudeWeekly(r, "b", now.Add(96*time.Hour), "0.2")
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "b" {
		t.Fatalf("new session picked %q, want b (a is used up)", got)
	}
	// a's reset passes with no header since: it may still be full, so b keeps winning.
	now = now.Add(3 * time.Hour)
	if got := r.pick(pickReq("s2", "a", "b")).AuthID; got != "b" {
		t.Fatalf("picked %q before a's new window was confirmed", got)
	}
	// With nothing else left, the unconfirmed account is the only way to find out.
	if got := r.pick(pickReq("s3", "a")).AuthID; got != "a" {
		t.Fatalf("picked %q, want a as the last resort", got)
	}
	// A 200 without weekly headers proves nothing (extra usage can serve a full window).
	r.observe(pluginapi.UsageRecord{AuthID: "a"})
	if s, _ := r.standing("a", "", true, now); s != awaiting {
		t.Fatalf("headerless success cleared the used-up mark: %v", s)
	}
	claudeWeekly(r, "a", now.Add(165*time.Hour), "0.01")
	if s, _ := r.standing("a", "", true, now); s != ready {
		t.Fatalf("new weekly window did not clear the mark: %v", s)
	}
}

func TestRejectedWeeklyClaimMovesBoundSessionNow(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeReset(r, "a", now.Add(time.Hour))
	claudeReset(r, "b", now.Add(2*time.Hour))
	r.pick(pickReq("s1", "a", "b"))
	rejected(r, "a", "claude-opus-5", "seven_day", now.Add(time.Hour))
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "b" {
		t.Fatalf("session stayed on used-up account: %q", got)
	}
	if got := r.st.Bindings[keyOf(pickReq("s1"))].AuthID; got != "b" {
		t.Fatalf("binding not moved: %q", got)
	}
}

func TestFiveHourRejectionIsNotAWeeklyLimit(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	rejected(r, "a", "claude-opus-5", "five_hour", now.Add(time.Hour))
	rejected(r, "a", "claude-opus-5", "seven_day_overage_included", now.Add(time.Hour))
	if s, why := r.standing("a", "claude-opus-5", true, now); s != ready {
		t.Fatalf("non-weekly rejection marked a: %v %s", s, why)
	}
}

func TestLowHeadroomAvoidedForNewSessionsOnly(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeWeekly(r, "a", now.Add(time.Hour), "0.5")
	claudeWeekly(r, "b", now.Add(96*time.Hour), "0.2")
	r.pick(pickReq("s1", "a", "b"))
	claudeWeekly(r, "a", now.Add(time.Hour), "0.97")
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "a" {
		t.Fatalf("bound session left an account that still has room: %q", got)
	}
	if got := r.pick(pickReq("s2", "a", "b")).AuthID; got != "b" {
		t.Fatalf("new session picked %q, want b (a has under 5%% left)", got)
	}
	claudeWeekly(r, "b", now.Add(96*time.Hour), "1")
	if got := r.pick(pickReq("s3", "a", "b")).AuthID; got != "a" {
		t.Fatalf("picked %q, want the low account over a used-up one", got)
	}
}

func TestPassedResetBeatsLowHeadroom(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeWeekly(r, "a", now.Add(time.Hour), "1.0")
	claudeWeekly(r, "b", now.Add(96*time.Hour), "0.97")
	now = now.Add(2 * time.Hour)
	// Plugin-routed traffic is the only way a's new window gets confirmed, so it must not wait behind an account about to run out.
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "a" {
		t.Fatalf("new session picked %q, want a (its reset passed, b is nearly used up)", got)
	}
}

func TestModelWeeklyLimitOnlyBlocksThatModel(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeReset(r, "a", now.Add(time.Hour))
	claudeReset(r, "b", now.Add(96*time.Hour))
	r.pick(modelReq("s1", "claude-opus-5", "a", "b"))
	rejected(r, "a", "claude-opus-5", "seven_day_opus", now.Add(2*time.Hour))
	if got := r.pick(modelReq("s1", "claude-opus-5", "a", "b")).AuthID; got != "b" {
		t.Fatalf("opus session stayed on an account out of opus: %q", got)
	}
	if got := r.pick(modelReq("s2", "claude-sonnet-5", "a", "b")).AuthID; got != "a" {
		t.Fatalf("sonnet session picked %q, want a (only opus is used up)", got)
	}
	if got := r.pick(modelReq("s3", "claude-opus-5", "a", "b")).AuthID; got != "b" {
		t.Fatalf("new opus session picked %q", got)
	}
	// After the reset, a successful opus request on a is the confirmation.
	now = now.Add(3 * time.Hour)
	if s, _ := r.standing("a", "claude-opus-5", true, now); s != awaiting {
		t.Fatalf("opus mark lifted without evidence: %v", s)
	}
	r.observe(pluginapi.UsageRecord{AuthID: "a", Model: "claude-opus-5"})
	if s, _ := r.standing("a", "claude-opus-5", true, now); s != ready {
		t.Fatalf("successful opus request did not clear the mark: %v", s)
	}
}

func authFailure(r *router, id string, status int) {
	r.observe(pluginapi.UsageRecord{AuthID: id, Failed: true, Failure: pluginapi.UsageFailure{StatusCode: status}})
}

func TestRejectedLoginMovesBoundSessionNow(t *testing.T) {
	for _, status := range []int{401, 403} {
		now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
		r := testRouter(t, &now)
		claudeReset(r, "a", now.Add(time.Hour))
		claudeReset(r, "b", now.Add(2*time.Hour))
		r.pick(pickReq("s1", "a", "b"))
		authFailure(r, "a", status)
		// CLIProxyAPI drops a revoked login from the candidates; the session must not wait out rebind_after.
		r.pick(pickReq("s1", "b"))
		if got := r.st.Bindings[keyOf(pickReq("s1"))].AuthID; got != "b" {
			t.Fatalf("%d kept the binding on %q", status, got)
		}
		r.observe(pluginapi.UsageRecord{AuthID: "a"})
		if s, _ := r.standing("a", "", true, now); s != ready {
			t.Fatalf("a success did not clear the %d: %v", status, s)
		}
	}
}

func TestRefreshable401KeepsTheSession(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeReset(r, "a", now.Add(time.Hour))
	claudeReset(r, "b", now.Add(2*time.Hour))
	r.pick(pickReq("s1", "a", "b"))
	// An expired token: CLIProxyAPI refreshes it and keeps offering a, and the retry's success arrives later.
	authFailure(r, "a", 401)
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "a" {
		t.Fatalf("token refresh moved the session to %q", got)
	}
	if got := r.pick(pickReq("s2", "a", "b")).AuthID; got != "b" {
		t.Fatalf("new session picked %q while a's 401 is unresolved", got)
	}
}

func TestRejectedLoginIsALastResortAfterCPACooldown(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	authFailure(r, "a", 403)
	if s, _ := r.standing("a", "", true, now); s != unusable {
		t.Fatalf("fresh 403: %v", s)
	}
	// Plugin-routed traffic never reaches an unusable account, so without a time limit nothing would ever clear the mark.
	now = now.Add(31 * time.Minute)
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "b" {
		t.Fatalf("picked %q, want b while a is unconfirmed", got)
	}
	if got := r.pick(pickReq("s2", "a")).AuthID; got != "a" {
		t.Fatalf("picked %q, want a as the last resort", got)
	}
}

func TestAccountWithoutWeeklyHeadersStopsWinningNewSessions(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeReset(r, "b", now.Add(96*time.Hour))
	r.observe(pluginapi.UsageRecord{AuthID: "a"}) // e.g. an API key: answers, never reports a weekly window
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "b" {
		t.Fatalf("picked %q, want b (a will never report a reset)", got)
	}
}

func TestEveryAccountUnusableLeavesItToCPA(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeWeekly(r, "a", now.Add(time.Hour), "1")
	if resp := r.pick(pickReq("s1", "a")); resp.Handled {
		t.Fatalf("handled with only a used-up account: %+v", resp)
	}
	if _, ok := r.st.Bindings[keyOf(pickReq("s1"))]; ok {
		t.Fatal("bound a session to nothing")
	}
}

func TestConcurrentPicksObservesAndSaves(t *testing.T) {
	now := time.Now()
	r := testRouter(t, &now)
	r.now = time.Now
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(3)
		go func() { defer wg.Done(); r.pick(pickReq("s"+strconv.Itoa(i), "a", "b")) }()
		go func() { defer wg.Done(); claudeReset(r, "a", time.Now().Add(time.Hour)) }()
		go func() { defer wg.Done(); _ = r.save() }()
	}
	wg.Wait()
}
