package serverv1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/The-NeXT-Project/NeXT-Server/adapter"
	"github.com/The-NeXT-Project/NeXT-Server/option"
)

func TestReportsCarryTheReportID(t *testing.T) {
	bodies := make(map[string]map[string]any)
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(content, &body); err != nil {
			t.Error(err)
		}
		bodies[r.URL.Path] = body
		w.WriteHeader(status)
		w.Write([]byte(`{"data": {"accepted": 0, "skipped": 0, "duplicate": true}}`))
	}))
	defer server.Close()
	client, err := NewClient(option.ServerOptions{URL: server.URL, Key: "key"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A duplicate answer is a success: the batch is delivered.
	if err = client.ReportTraffic(ctx, "abc", []adapter.UserTraffic{{UserID: 1, Upload: 2, Download: 3}}); err != nil {
		t.Fatal(err)
	}
	if err = client.ReportDetectLogs(ctx, "def", []adapter.DetectLog{{UserID: 1, RuleID: 4}}); err != nil {
		t.Fatal(err)
	}
	if id := bodies["/api/server/v1/users/traffic"]["report_id"]; id != "abc" {
		t.Error("traffic report_id: ", id)
	}
	if id := bodies["/api/server/v1/users/detect_logs"]["report_id"]; id != "def" {
		t.Error("detect log report_id: ", id)
	}
	// The panel refuses an empty ID, so none is sent.
	client.ReportTraffic(ctx, "", []adapter.UserTraffic{{UserID: 1, Upload: 2}})
	if _, sent := bodies["/api/server/v1/users/traffic"]["report_id"]; sent {
		t.Error("empty report_id sent")
	}

	for _, testCase := range []struct {
		status  int
		refused bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusUnauthorized, true},
		{http.StatusForbidden, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, false},
		{http.StatusBadGateway, false},
		{http.StatusGatewayTimeout, false},
	} {
		status = testCase.status
		err = client.ReportTraffic(ctx, "abc", []adapter.UserTraffic{{UserID: 1, Upload: 2}})
		if err == nil || adapter.IsRefused(err) != testCase.refused {
			t.Errorf("HTTP %d: refused = %v (%v)", testCase.status, adapter.IsRefused(err), err)
		}
	}

	// Without an answer the outcome is open.
	server.Close()
	if err = client.ReportTraffic(ctx, "abc", nil); err == nil || adapter.IsRefused(err) {
		t.Error("a failed connection counts as a refusal: ", err)
	}
}

// The panel stores rule types as its form offers them: 1 for plaintext, 0
// for hexadecimal.
func TestDetectRuleTypes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data": [
			{"id": 1, "name": "plain", "text": "", "regex": "torrent", "type": 1},
			{"id": 2, "name": "hex0", "text": "", "regex": "^1603", "type": 0},
			{"id": 3, "name": "hex2", "text": "", "regex": "^beef", "type": 2},
			{"id": 4, "name": "unknown", "text": "", "regex": "x", "type": 7},
			{"id": 5, "name": "broken", "text": "", "regex": "(", "type": 1}
		]}`))
	}))
	defer server.Close()
	client, err := NewClient(option.ServerOptions{URL: server.URL, Key: "key"})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := client.FetchDetectRules(context.Background())
	if err == nil {
		t.Error("the unknown type and the broken regex were not reported")
	}
	if len(rules) != 3 || rules[0].Type != adapter.DetectRuleTypePlain || rules[1].Type != adapter.DetectRuleTypeHex || rules[2].Type != adapter.DetectRuleTypeHex {
		t.Fatalf("rules: %+v", rules)
	}
}

func TestFetchUsersIncludesIPLimitAndHeartbeatParsesKickIPs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server/v1/users":
			w.Write([]byte(`{"data": [
				{"id": 1, "node_speedlimit": 10, "node_iplimit": 2, "uuid": "u1"},
				{"id": 2, "node_speedlimit": "0", "node_iplimit": "3", "uuid": "u2"},
				{"id": 3, "node_speedlimit": 0, "node_iplimit": -1, "uuid": "u3"}
			]}`))
		case "/api/server/v1/heartbeat":
			w.Write([]byte(`{"data": "ok", "upgrade": false, "restart": false, "kick_ips": [{"user_id": 1, "ip": "1.2.3.4"}, {"user_id": 0, "ip": "9.9.9.9"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewClient(option.ServerOptions{URL: server.URL, Key: "key"})
	if err != nil {
		t.Fatal(err)
	}

	users, err := client.FetchUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 3 || users[0].IPLimit != 2 || users[1].IPLimit != 3 || users[2].IPLimit != 0 {
		t.Fatalf("unexpected users ip limits: %+v", users)
	}

	hb, err := client.Heartbeat(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hb.KickIPs) != 1 || hb.KickIPs[0].UserID != 1 || hb.KickIPs[0].IP != "1.2.3.4" {
		t.Fatalf("unexpected heartbeat kick_ips: %+v", hb.KickIPs)
	}
}
