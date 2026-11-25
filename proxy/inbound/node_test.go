package inbound

import (
	"bytes"
	"context"
	"net/netip"
	"testing"

	"github.com/The-NeXT-Project/NeXT-Server/adapter"
	"github.com/The-NeXT-Project/NeXT-Server/option"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
)

func TestServerOptionsTCPFastOpenDecodeAndDefault(t *testing.T) {
	testCases := []struct {
		name     string
		raw      string
		wantPtr  *bool
		wantUsed bool
	}{
		{
			name:     "omitted defaults to true",
			raw:      `{"servers":[{"url":"https://panel.example.com","key":"k"}]}`,
			wantPtr:  nil,
			wantUsed: true,
		},
		{
			name:     "explicit false disables",
			raw:      `{"servers":[{"url":"https://panel.example.com","key":"k","tcp_fast_open":false}]}`,
			wantPtr:  common.Ptr(false),
			wantUsed: false,
		},
		{
			name:     "explicit true enables",
			raw:      `{"servers":[{"url":"https://panel.example.com","key":"k","tcp_fast_open":true}]}`,
			wantPtr:  common.Ptr(true),
			wantUsed: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var opts option.Options
			dec := json.NewDecoder(json.NewCommentFilter(bytes.NewReader([]byte(tc.raw))))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&opts); err != nil {
				t.Fatalf("decode failed: %v", err)
			}
			srv := opts.Servers[0]
			if (srv.TCPFastOpen == nil) != (tc.wantPtr == nil) {
				t.Fatalf("TCPFastOpen nil mismatch: got %v, want %v", srv.TCPFastOpen, tc.wantPtr)
			}
			if srv.TCPFastOpen != nil && *srv.TCPFastOpen != *tc.wantPtr {
				t.Fatalf("TCPFastOpen value = %v, want %v", *srv.TCPFastOpen, *tc.wantPtr)
			}
			nodeOpts := NodeOptions{
				Context: context.Background(),
				Logger:  log.NewNOPFactory().Logger(),
				Config:  &adapter.NodeConfig{ListenPort: 10086},
				Server:  srv,
			}
			nodeOpts.Server.Listen = common.Ptr(badoption.Addr(netip.MustParseAddr("127.0.0.1")))
			l, err := nodeOpts.NewListener(nil, nil, false)
			if err != nil {
				t.Fatalf("NewListener failed: %v", err)
			}
			if l.options.Listen.TCPFastOpen != tc.wantUsed {
				t.Fatalf("Listen.TCPFastOpen = %v, want %v", l.options.Listen.TCPFastOpen, tc.wantUsed)
			}
		})
	}
}
