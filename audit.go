package nextserver

import (
	"encoding/hex"
	"sync"
	"sync/atomic"

	"github.com/The-NeXT-Project/NeXT-Server/adapter"
)

const hexStackPayloadLimit = 512

var hexBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 4096)
		return &b
	},
}

// auditor matches connections against the panel's detect rules and keeps
// the hits until they are reported.
type auditor struct {
	rules atomic.Pointer[[]adapter.DetectRule]

	access sync.Mutex
	hits   map[adapter.DetectLog]struct{}
}

func newAuditor() *auditor {
	return &auditor{hits: make(map[adapter.DetectLog]struct{})}
}

func (a *auditor) setRules(rules []adapter.DetectRule) {
	a.rules.Store(&rules)
}

func (a *auditor) enabled() bool {
	rules := a.rules.Load()
	return rules != nil && len(*rules) > 0
}

// match returns the first rule that matches the connection. Plain rules see
// the destination host, the sniffed domain and the first payload as text; hex
// rules see the payload hex encoded, as the panel's rule types describe.
func (a *auditor) match(host string, domain string, payload []byte) (adapter.DetectRule, bool) {
	rules := a.rules.Load()
	if rules == nil {
		return adapter.DetectRule{}, false
	}
	var (
		stackHex [hexStackPayloadLimit * 2]byte
		hexBytes []byte
		poolPtr  *[]byte
	)
	defer func() {
		if poolPtr != nil {
			hexBufPool.Put(poolPtr)
		}
	}()
	for _, rule := range *rules {
		switch rule.Type {
		case adapter.DetectRuleTypeHex:
			if len(payload) == 0 {
				continue
			}
			if hexBytes == nil {
				need := hex.EncodedLen(len(payload))
				if len(payload) <= hexStackPayloadLimit {
					hex.Encode(stackHex[:need], payload)
					hexBytes = stackHex[:need]
				} else {
					poolPtr = hexBufPool.Get().(*[]byte)
					if cap(*poolPtr) < need {
						*poolPtr = make([]byte, need)
					} else {
						*poolPtr = (*poolPtr)[:need]
					}
					hex.Encode(*poolPtr, payload)
					hexBytes = *poolPtr
				}
			}
			if rule.Regexp.Match(hexBytes) {
				return rule, true
			}
		case adapter.DetectRuleTypePlain:
			if (host != "" && rule.Regexp.MatchString(host)) ||
				(domain != "" && domain != host && rule.Regexp.MatchString(domain)) ||
				(len(payload) > 0 && rule.Regexp.Match(payload)) {
				return rule, true
			}
		}
	}
	return adapter.DetectRule{}, false
}

func (a *auditor) record(userID int, ruleID int) {
	a.access.Lock()
	a.hits[adapter.DetectLog{UserID: userID, RuleID: ruleID}] = struct{}{}
	a.access.Unlock()
}

func (a *auditor) collect() []adapter.DetectLog {
	a.access.Lock()
	defer a.access.Unlock()
	if len(a.hits) == 0 {
		return nil
	}
	logs := make([]adapter.DetectLog, 0, len(a.hits))
	for hit := range a.hits {
		logs = append(logs, hit)
	}
	clear(a.hits)
	return logs
}

// restore puts back hits the panel refused, so they are reported in a later
// batch.
func (a *auditor) restore(logs []adapter.DetectLog) {
	a.access.Lock()
	for _, log := range logs {
		a.hits[log] = struct{}{}
	}
	a.access.Unlock()
}
