package nextserver

import (
	"bytes"
	"regexp"
	"testing"

	"github.com/The-NeXT-Project/NeXT-Server/adapter"
)

func TestAuditorMatchHexStackAndPool(t *testing.T) {
	a := newAuditor()
	a.setRules([]adapter.DetectRule{
		{ID: 1, Type: adapter.DetectRuleTypeHex, Regexp: regexp.MustCompile(`^160301`)},
		{ID: 2, Type: adapter.DetectRuleTypeHex, Regexp: regexp.MustCompile(`deadbeef$`)},
		{ID: 3, Type: adapter.DetectRuleTypePlain, Regexp: regexp.MustCompile(`blocked\.example`)},
	})

	// Empty payload skipped for hex, matches plain host.
	if rule, hit := a.match("blocked.example", "", nil); !hit || rule.ID != 3 {
		t.Fatalf("expected plain rule 3 hit, got hit=%v rule=%+v", hit, rule)
	}

	// Small payload (<= 512 bytes, stack path).
	if rule, hit := a.match("ok.example", "", []byte{0x16, 0x03, 0x01, 0x00}); !hit || rule.ID != 1 {
		t.Fatalf("expected hex rule 1 hit on small payload, got hit=%v rule=%+v", hit, rule)
	}

	// Large payload (> 512 bytes, pooled path).
	large := append(bytes.Repeat([]byte{0xaa}, 700), 0xde, 0xad, 0xbe, 0xef)
	if rule, hit := a.match("ok.example", "", large); !hit || rule.ID != 2 {
		t.Fatalf("expected hex rule 2 hit on large payload, got hit=%v rule=%+v", hit, rule)
	}

	// Miss path on large payload.
	miss := bytes.Repeat([]byte{0x11}, 700)
	if rule, hit := a.match("ok.example", "ok.example", miss); hit {
		t.Fatalf("expected miss, got hit rule=%+v", rule)
	}
}
