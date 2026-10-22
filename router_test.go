package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// Shaped like what CLIProxyAPI v8.0.33 sends for a plain Claude route: Provider empty, the route's provider in Providers.
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
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeReset(r, "a", now.Add(96*time.Hour))
	claudeReset(r, "b", now.Add(20*time.Hour))

	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "b" {
		t.Fatalf("new session picked %q, want b (resets soonest)", got)
	}
	// b's weekly window rolls over and its next reset is a week out: s1 must stay on b, a new session goes to a.
	claudeReset(r, "b", now.Add(168*time.Hour))
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "b" {
		t.Fatalf("existing session moved to %q", got)
	}
	if got := r.pick(pickReq("s2", "a", "b")).AuthID; got != "a" {
		t.Fatalf("new session picked %q, want a", got)
	}
}

func TestUnknownAccountsGetASessionFirst(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeReset(r, "a", now.Add(time.Hour))
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "b" {
		t.Fatalf("picked %q, want b (no reset time known yet)", got)
	}
}

func TestFailoverThenRebind(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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

// codexWeekly sends the weekly window in the primary slot, the shape seen since September 2026.
func codexWeekly(r *router, id string, at time.Time, usedPercent string, failed bool) {
	rec := pluginapi.UsageRecord{AuthID: id, Failed: failed, ResponseHeaders: map[string][]string{
		"X-Codex-Primary-Window-Minutes": {"10080"},
		"X-Codex-Primary-Reset-At":       {strconv.FormatInt(at.Unix(), 10)},
		"X-Codex-Primary-Used-Percent":   {usedPercent},
	}}
	if failed {
		rec.Failure = pluginapi.UsageFailure{StatusCode: 429}
	}
	r.observe(rec)
}

func TestUsedUpMarkOutlivesAResetThatJumps(t *testing.T) {
	// openai/codex#23190: a week at 5% resetting in six days, then a 429 at 100% whose reset is four days earlier; responses during the incident carry either value.
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	codexWeekly(r, "a", now.Add(6*24*time.Hour), "5", false)
	codexWeekly(r, "a", now.Add(2*24*time.Hour), "100", true)
	if s, _ := r.standing("a", "", true, now); s != unusable {
		t.Fatalf("429 at 100%% left a %v", s)
	}
	codexWeekly(r, "a", now.Add(6*24*time.Hour), "5", true)
	if s, _ := r.standing("a", "", true, now); s != unusable {
		t.Fatalf("a failed response with another reset lifted the mark: %v", s)
	}
	codexWeekly(r, "a", now.Add(2*24*time.Hour), "98", false)
	if s, _ := r.standing("a", "", true, now); s != unusable {
		t.Fatalf("a late success from the used-up window lifted the mark: %v", s)
	}
	codexWeekly(r, "a", now.Add(6*24*time.Hour), "5", false)
	if s, _ := r.standing("a", "", true, now); s != ready {
		t.Fatalf("a served request from another window did not lift the mark: %v", s)
	}
}

func TestNoSessionHeaderLeavesItToCLIProxyAPI(t *testing.T) {
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
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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

func TestHeaderFallbackRejectsWhatCLIProxyAPIRejects(t *testing.T) {
	now := time.Now()
	r := testRouter(t, &now)
	for name, sid := range map[string]string{"oversized": strings.Repeat("x", 257), "control character": "s1\nabc"} {
		if r.pick(pickReq(sid, "a")).Handled {
			t.Fatalf("bound a session to an %s id CLIProxyAPI would have dropped", name)
		}
	}
	if !r.pick(pickReq("  "+strings.Repeat("x", 256)+"  ", "a")).Handled {
		t.Fatal("rejected an id of 256 bytes with surrounding spaces, which CLIProxyAPI accepts")
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
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	rejected(r, "a", "claude-opus-5", "five_hour", now.Add(time.Hour))
	rejected(r, "a", "claude-opus-5", "seven_day_overage_included", now.Add(time.Hour))
	if s, why := r.standing("a", "claude-opus-5", true, now); s != ready {
		t.Fatalf("non-weekly rejection marked a: %v %s", s, why)
	}
}

// scopedWeekly sends the headers Claude adds on a request to a model with its own weekly bucket, as captured for Fable: the shared 7d window has room, 7d_oi is the scoped one.
func scopedWeekly(r *router, id, model, status, used string, until time.Time, failed bool) {
	rec := pluginapi.UsageRecord{AuthID: id, Model: model, Failed: failed, ResponseHeaders: map[string][]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":             {strconv.FormatInt(until.Add(72*time.Hour).Unix(), 10)},
		"Anthropic-Ratelimit-Unified-7d-Utilization":       {"0.65"},
		"Anthropic-Ratelimit-Unified-7d_oi-Status":         {status},
		"Anthropic-Ratelimit-Unified-7d_oi-Utilization":    {used},
		"Anthropic-Ratelimit-Unified-7d_oi-Reset":          {strconv.FormatInt(until.Unix(), 10)},
		"Anthropic-Ratelimit-Unified-Representative-Claim": {"seven_day_overage_included"},
	}}
	if failed {
		rec.Failure = pluginapi.UsageFailure{StatusCode: 429}
		rec.ResponseHeaders["Anthropic-Ratelimit-Unified-Status"] = []string{"rejected"}
	}
	r.observe(rec)
}

func TestScopedWeeklyBucketOnlyBlocksThatModel(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	scopedWeekly(r, "a", "claude-fable-5-1", "allowed", "0.8", now.Add(2*time.Hour), false)
	if s, why := r.standing("a", "claude-fable-5-1", true, now); s != ready {
		t.Fatalf("scoped bucket with room marked a: %v %s", s, why)
	}
	scopedWeekly(r, "a", "claude-fable-5-1", "rejected", "1.0", now.Add(2*time.Hour), true)
	if s, why := r.standing("a", "claude-fable-5-1", true, now); s != unusable || why != "fable weekly limit used up" {
		t.Fatalf("used-up scoped bucket left a %v %q", s, why)
	}
	if s, _ := r.standing("a", "claude-opus-5", true, now); s != ready {
		t.Fatalf("scoped bucket blocked another model: %v", s)
	}
	if s, _ := r.standing("a", "", true, now); s != ready {
		t.Fatalf("scoped bucket blocked a request with no model: %v", s)
	}
	now = now.Add(3 * time.Hour)
	if s, _ := r.standing("a", "claude-fable-5-1", true, now); s != awaiting {
		t.Fatalf("scoped mark lifted without evidence: %v", s)
	}
	scopedWeekly(r, "a", "claude-fable-5-1", "allowed", "0.1", now.Add(168*time.Hour), false)
	if s, _ := r.standing("a", "claude-fable-5-1", true, now); s != ready {
		t.Fatalf("successful request after the reset did not clear the mark: %v", s)
	}
}

func TestModelFamily(t *testing.T) {
	for model, want := range map[string]string{"claude-fable-5-1": "fable", "Claude-Opus-5": "opus", "claude-haiku-4-5-20251001": "haiku", "gpt-5.5-codex": "", "": ""} {
		if got := modelFamily(model); got != want {
			t.Errorf("modelFamily(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestLowHeadroomAvoidedForNewSessionsOnly(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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
		now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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

func TestAnOlderRequestsLateSuccessDoesNotClearARejection(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	r.observe(pluginapi.UsageRecord{AuthID: "a", Failed: true, Failure: pluginapi.UsageFailure{StatusCode: 403}, RequestedAt: now.Add(-2 * time.Second), Latency: time.Second})
	// A request dispatched a minute before the rejection finishes after it: the login was still good back then, not now.
	r.observe(pluginapi.UsageRecord{AuthID: "a", RequestedAt: now.Add(-time.Minute), Latency: 70 * time.Second})
	if s, _ := r.standing("a", "", true, now); s != unusable {
		t.Fatalf("an older request's success cleared the rejection: %v", s)
	}
	r.observe(pluginapi.UsageRecord{AuthID: "a", RequestedAt: now.Add(time.Second), Latency: time.Second})
	if s, _ := r.standing("a", "", true, now); s != ready {
		t.Fatalf("a later request's success did not clear the rejection: %v", s)
	}
}

func TestCloudflareChallengeIsNotARejectedLogin(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	r.observe(pluginapi.UsageRecord{AuthID: "a", Failed: true, Failure: pluginapi.UsageFailure{StatusCode: 403, Body: `<html><head><title>Just a moment...</title><script src="/cdn-cgi/challenge-platform/h/b/orchestrate/chl_page/v1"></script>`}})
	if s, why := r.standing("a", "", true, now); s != ready {
		t.Fatalf("challenge page marked a: %v %s", s, why)
	}
	r.observe(pluginapi.UsageRecord{AuthID: "b", Failed: true, Failure: pluginapi.UsageFailure{StatusCode: 403}, ResponseHeaders: map[string][]string{"Cf-Mitigated": {"challenge"}}})
	if s, why := r.standing("b", "", true, now); s != ready {
		t.Fatalf("challenge header marked b: %v %s", s, why)
	}
	r.observe(pluginapi.UsageRecord{AuthID: "c", Failed: true, Failure: pluginapi.UsageFailure{StatusCode: 403, Body: `{"error":{"type":"permission_error"}}`}})
	if s, _ := r.standing("c", "", true, now); s != unusable {
		t.Fatalf("a real 403 left c %v", s)
	}
}

func TestRefreshable401KeepsTheSession(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeReset(r, "a", now.Add(time.Hour))
	claudeReset(r, "b", now.Add(2*time.Hour))
	r.pick(pickReq("s1", "a", "b"))
	// a's login has expired, so CLIProxyAPI refreshes it and keeps offering a, and the retry's success arrives later.
	authFailure(r, "a", 401)
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "a" {
		t.Fatalf("token refresh moved the session to %q", got)
	}
	if got := r.pick(pickReq("s2", "a", "b")).AuthID; got != "b" {
		t.Fatalf("new session picked %q while a's 401 is unresolved", got)
	}
}

func TestRejectedLoginIsALastResortOnceTheMarkAges(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
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
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeReset(r, "b", now.Add(96*time.Hour))
	r.observe(pluginapi.UsageRecord{AuthID: "a"}) // e.g. an API key: answers, never reports a weekly window
	if got := r.pick(pickReq("s1", "a", "b")).AuthID; got != "b" {
		t.Fatalf("picked %q, want b (a will never report a reset)", got)
	}
}

func TestEveryAccountUnusableLeavesItToCLIProxyAPI(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	r := testRouter(t, &now)
	claudeWeekly(r, "a", now.Add(time.Hour), "1")
	if resp := r.pick(pickReq("s1", "a")); resp.Handled {
		t.Fatalf("handled with only a used-up account: %+v", resp)
	}
	if _, ok := r.st.Bindings[keyOf(pickReq("s1"))]; ok {
		t.Fatal("bound a session to nothing")
	}
}

func TestStateTimesAreUTC(t *testing.T) {
	cfg := defaultConfig()
	cfg.Mode = "active"
	cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	r := newRouter(cfg)
	r.pick(pickReq("s1", "a"))
	if loc := r.st.Recent[0].At.Location(); loc != time.UTC {
		t.Fatalf("decision time recorded in %v, want UTC", loc)
	}
	if loc := r.st.Bindings[keyOf(pickReq("s1"))].LastSeen.Location(); loc != time.UTC {
		t.Fatalf("binding time recorded in %v, want UTC", loc)
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
