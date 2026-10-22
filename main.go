// Command expiring-quota-first is a CLIProxyAPI scheduler plugin; README.md says what it does and how it picks an account.
// This file is the plugin ABI boundary: C entry points, the method dispatch, config handling and the save loop. router.go holds the picking.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

// version is the release tag without its v, set at build time with -ldflags; CLIProxyAPI shows it as the installed version.
var version = "0.0.0-dev"

var (
	mu      sync.Mutex
	current *router
	stopper chan struct{}
	stopped chan struct{} // closed once saveLoop has made its final save
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr, response.len = nil, 0
	}
	var raw []byte
	if request != nil && requestLen > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	out, err := handle(C.GoString(method), raw)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, out)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { stop() }

func handle(method string, raw []byte) (out []byte, err error) {
	// A panic here would take CLIProxyAPI down with it; an unhandled pick falls back to CLIProxyAPI's own routing.
	defer func() {
		if p := recover(); p != nil {
			log.Printf("expiring-quota-first: panic in %s: %v", method, p)
			if method == pluginabi.MethodPluginRegister || method == pluginabi.MethodPluginReconfigure {
				out, err = nil, fmt.Errorf("expiring-quota-first %s: %v", method, p)
				return
			}
			out, err = okEnvelope(pluginapi.SchedulerPickResponse{})
		}
	}()
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configure(raw); err != nil {
			return nil, err
		}
		return okEnvelope(registration())
	case pluginabi.MethodPluginShutdown:
		stop()
		return okEnvelope(struct{}{})
	case pluginabi.MethodPluginQuiesce:
		// CLIProxyAPI sends this before it loads a replacement copy, which reads the state file; this copy keeps answering from memory until it is retired, but writes no more.
		stop()
		return okEnvelope(struct{}{})
	case pluginabi.MethodSchedulerPick:
		var req pluginapi.SchedulerPickRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if r := active(); r != nil {
			return okEnvelope(r.pick(req))
		}
		return okEnvelope(pluginapi.SchedulerPickResponse{})
	case pluginabi.MethodUsageHandle:
		var rec pluginapi.UsageRecord
		if err := json.Unmarshal(raw, &rec); err == nil {
			if r := active(); r != nil {
				r.observe(rec)
			}
		}
		return okEnvelope(struct{}{})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var req struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
	}
	cfg := defaultConfig()
	if len(req.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			return fmt.Errorf("expiring-quota-first config: %w", err)
		}
	}
	if cfg.Mode != "shadow" && cfg.Mode != "active" {
		return fmt.Errorf("expiring-quota-first config: mode must be shadow or active, got %q", cfg.Mode)
	}
	if cfg.TTL <= 0 || cfg.RebindAfter <= 0 {
		return fmt.Errorf("expiring-quota-first config: ttl and rebind_after must be positive")
	}
	if cfg.MinRemaining < 0 || cfg.MinRemaining > 100 {
		return fmt.Errorf("expiring-quota-first config: min_remaining must be a percentage, got %v", cfg.MinRemaining)
	}
	stop()
	mu.Lock()
	defer mu.Unlock()
	current = newRouter(cfg)
	stopper, stopped = make(chan struct{}), make(chan struct{})
	go saveLoop(current, stopper, stopped)
	return nil
}

func saveLoop(r *router, done, finished chan struct{}) {
	defer close(finished)
	defer func() { // this goroutine is outside handle's recover; a panic here would kill CLIProxyAPI
		if p := recover(); p != nil {
			log.Printf("expiring-quota-first: panic while saving, state is no longer persisted: %v", p)
		}
	}()
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	var lastErr string
	save := func() {
		err := r.save()
		if msg := fmt.Sprint(err); err != nil && msg != lastErr {
			log.Printf("expiring-quota-first: cannot save state to %s: %v", r.cfg.StateFile, err)
			lastErr = msg
		} else if err == nil {
			lastErr = ""
		}
	}
	for {
		select {
		case <-tick.C:
			save()
		case <-done:
			save()
			return
		}
	}
}

// stop waits for the final save, so a router started next reads what this one last knew; the lock is not held meanwhile, so picks keep flowing.
func stop() {
	mu.Lock()
	done, finished := stopper, stopped
	stopper, stopped = nil, nil
	mu.Unlock()
	if done != nil {
		close(done)
		<-finished
	}
}

func active() *router {
	mu.Lock()
	defer mu.Unlock()
	return current
}

func registration() any {
	d := defaultConfig()
	return map[string]any{
		"schema_version": pluginabi.SchemaVersion,
		"metadata": pluginapi.Metadata{
			Name:             "expiring-quota-first",
			Version:          version,
			Author:           "stavarengo",
			GitHubRepository: "https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"shadow", "active"}, Description: "shadow only records decisions; active routes."},
				{Name: "ttl", Type: pluginapi.ConfigFieldTypeString, Description: "Idle time before a session forgets its account (default " + shortDuration(d.TTL) + ")."},
				{Name: "rebind_after", Type: pluginapi.ConfigFieldTypeString, Description: "How long a bound account may be unavailable before the session moves (default " + shortDuration(d.RebindAfter) + ")."},
				{Name: "min_remaining", Type: pluginapi.ConfigFieldTypeNumber, Description: "Weekly percent left below which new sessions avoid an account while another is ready (default " + strconv.FormatFloat(d.MinRemaining, 'f', -1, 64) + ")."},
				{Name: "state_file", Type: pluginapi.ConfigFieldTypeString, Description: "Where bindings, what was learned about each account and the recent picks are kept (default " + d.StateFile + ")."},
			},
		},
		"capabilities": map[string]bool{"scheduler": true, "usage_plugin": true},
	}
}

// shortDuration writes 168h, not 168h0m0s, so a default reads the way config.yaml spells it.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	response.ptr = C.CBytes(raw)
	response.len = C.size_t(len(raw))
}
