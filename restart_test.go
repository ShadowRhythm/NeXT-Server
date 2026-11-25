package nextserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-NeXT-Project/NeXT-Server/option"

	boxOption "github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func TestGracefulRestartFlushesTrafficAndConcurrentPush(t *testing.T) {
	var (
		mu             sync.Mutex
		reportedUpload int64
		reportedOnline int
		reportedLogs   int
		wantRestart    atomic.Bool
	)
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/server/v1/info":
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"sort":            11,
					"node_speedlimit": 0,
					"server":          "127.0.0.1",
					"custom_config": map[string]any{
						"offset_port_node": 0,
					},
				},
			})
		case "GET /api/server/v1/users":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": 7, "uuid": "11111111-1111-1111-1111-111111111111"},
				},
			})
		case "PUT /api/server/v1/heartbeat":
			json.NewEncoder(w).Encode(map[string]any{
				"upgrade": false,
				"restart": wantRestart.Load(),
			})
		case "POST /api/server/v1/users/traffic":
			var body struct {
				Traffic []struct {
					UserID int   `json:"user_id"`
					U      int64 `json:"u"`
				} `json:"traffic"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			for _, item := range body.Traffic {
				if item.UserID == 7 {
					reportedUpload += item.U
				}
			}
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accepted": len(body.Traffic)}})
		case "POST /api/server/v1/users/online":
			mu.Lock()
			reportedOnline++
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accepted": 1}})
		case "POST /api/server/v1/users/detect_logs":
			mu.Lock()
			reportedLogs++
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accepted": 1}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer panel.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instance, err := New(ctx, option.Options{
		Log: &boxOption.LogOptions{Disabled: true},
		Servers: []option.ServerOptions{{
			URL:          panel.URL,
			Key:          "key",
			PullInterval: badoption.Duration(time.Hour),
			PushInterval: badoption.Duration(time.Hour),
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := instance.logFactory.Start(); err != nil {
		t.Fatal(err)
	}
	srv := instance.servers[0]
	srv.nodeConfig, err = srv.api.FetchNodeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	users, err := srv.api.FetchUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv.applyUsers(users)

	// Normal pushOnce exercises concurrent reporting of traffic, online IPs, and detect logs.
	u := srv.users.get(7)
	done := u.open(netip.MustParseAddr("::ffff:203.0.113.10"))
	u.upload.Add(1024)
	srv.auditor.record(7, 11)
	srv.pushOnce()
	done()

	// Add more traffic & detect logs right before panel requests a restart.
	u.upload.Add(4096)
	srv.auditor.record(7, 12)
	wantRestart.Store(true)
	srv.pushOnce()

	select {
	case <-instance.RestartRequested():
	case <-time.After(time.Second):
		t.Fatal("expected RestartRequested to fire")
	}

	cancel()
	if err := instance.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if reportedUpload != 5120 {
		t.Fatalf("reportedUpload = %d, want 5120", reportedUpload)
	}
	if reportedOnline != 1 {
		t.Fatalf("reportedOnline = %d, want 1", reportedOnline)
	}
	if reportedLogs != 2 {
		t.Fatalf("reportedLogs = %d, want 2", reportedLogs)
	}
}
