package nextserver

import (
	"context"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/The-NeXT-Project/NeXT-Server/adapter"
	"github.com/The-NeXT-Project/NeXT-Server/common/abuse"
	"github.com/The-NeXT-Project/NeXT-Server/common/limit"
	"github.com/The-NeXT-Project/NeXT-Server/constant"

	E "github.com/sagernet/sing/common/exceptions"
)

var errIPLimitCooldown = E.New("ip limit cooldown")

// userState is the accounting for one panel user. Connections hold on to the
// state they started with, so traffic is never counted against a fresh object.
type userState struct {
	id int
	// ctx is canceled when the panel stops serving the user, which closes
	// their open connections.
	ctx    context.Context
	cancel context.CancelFunc
	// scope ends the user's open connections when canceled: with ctx, or
	// when they are banned, after which a fresh scope takes the new ones.
	scope atomic.Pointer[connectionScope]
	abuse abuse.Tracker

	upload   atomic.Int64
	download atomic.Int64
	// Pre-built single-element counter slices reused on every connection.
	uploadCounters   []*atomic.Int64
	downloadCounters []*atomic.Int64
	// limiter is nil when neither the user nor the node has a speed limit.
	limiter    atomic.Pointer[limit.Limiter]
	speedLimit atomic.Uint64
	ipLimit    atomic.Int64

	access    sync.Mutex
	ips       map[netip.Addr]*ipActivity
	cooldowns map[netip.Addr]time.Time
	seq       uint64
	active    int
}

type connectionScope struct {
	ctx    context.Context
	cancel context.CancelFunc
}

type ipActivity struct {
	active    int
	firstSeen time.Time
	lastSeen  time.Time
	seq       uint64
	scope     *connectionScope
}

func newUserState(ctx context.Context, id int) *userState {
	state := &userState{id: id}
	state.uploadCounters = []*atomic.Int64{&state.upload}
	state.downloadCounters = []*atomic.Int64{&state.download}
	state.ctx, state.cancel = context.WithCancel(ctx)
	state.closeConnections()
	return state
}

// connectionContext is canceled when the user's open connections must close.
func (u *userState) connectionContext() context.Context {
	return u.scope.Load().ctx
}

// closeConnections closes the user's open connections and keeps serving them.
func (u *userState) closeConnections() {
	ctx, cancel := context.WithCancel(u.ctx)
	if previous := u.scope.Swap(&connectionScope{ctx: ctx, cancel: cancel}); previous != nil {
		previous.cancel()
	}
}

func (u *userState) setSpeedLimit(bytesPerSecond uint64) {
	if u.speedLimit.Load() == bytesPerSecond {
		return
	}
	u.speedLimit.Store(bytesPerSecond)
	current := u.limiter.Load()
	switch {
	case bytesPerSecond == 0:
		u.limiter.Store(nil)
	case current == nil:
		u.limiter.Store(limit.New(bytesPerSecond))
	default:
		current.SetLimit(bytesPerSecond)
	}
}

func (u *userState) setIPLimit(limit int) {
	if limit < 0 {
		limit = 0
	}
	u.ipLimit.Store(int64(limit))
	if limit > 0 {
		u.access.Lock()
		u.evictToCapacityLocked(limit, time.Now(), constant.DefaultIPLimitCooldown)
		u.access.Unlock()
	}
}

func newIPActivity(parent context.Context, now time.Time, seq uint64) *ipActivity {
	ctx, cancel := context.WithCancel(parent)
	return &ipActivity{
		firstSeen: now,
		lastSeen:  now,
		seq:       seq,
		scope:     &connectionScope{ctx: ctx, cancel: cancel},
	}
}

func isMoreEvictable(candidate, current *ipActivity) bool {
	if (candidate.active == 0) != (current.active == 0) {
		return candidate.active == 0
	}
	if candidate.active == 0 {
		if !candidate.lastSeen.Equal(current.lastSeen) {
			return candidate.lastSeen.Before(current.lastSeen)
		}
		return candidate.seq < current.seq
	}
	if !candidate.firstSeen.Equal(current.firstSeen) {
		return candidate.firstSeen.Before(current.firstSeen)
	}
	if !candidate.lastSeen.Equal(current.lastSeen) {
		return candidate.lastSeen.Before(current.lastSeen)
	}
	return candidate.seq < current.seq
}

func (u *userState) evictToCapacityLocked(maxExisting int, now time.Time, cooldown time.Duration) {
	for len(u.ips) > maxExisting {
		var victimAddr netip.Addr
		var victim *ipActivity
		for addr, activity := range u.ips {
			if victim == nil || isMoreEvictable(activity, victim) {
				victimAddr = addr
				victim = activity
			}
		}
		if victim == nil {
			break
		}
		delete(u.ips, victimAddr)
		if victim.scope != nil {
			victim.scope.cancel()
		}
		if victim.active > 0 && cooldown > 0 {
			if u.cooldowns == nil {
				u.cooldowns = make(map[netip.Addr]time.Time, 1)
			}
			u.cooldowns[victimAddr] = now.Add(cooldown)
		}
	}
}

// open records a connection from address, enforces single-node IP limits by
// evicting the oldest IP when needed, and returns the IP scope context and
// completion callback.
func (u *userState) open(address netip.Addr) (context.Context, func(), error) {
	return u.openAt(address, time.Now())
}

func (u *userState) openAt(address netip.Addr, now time.Time) (context.Context, func(), error) {
	address = address.Unmap()
	if !address.IsValid() {
		u.access.Lock()
		u.active++
		u.access.Unlock()
		var closed atomic.Bool
		return u.ctx, func() {
			if closed.Swap(true) {
				return
			}
			u.access.Lock()
			u.active--
			u.access.Unlock()
		}, nil
	}

	u.access.Lock()
	if until, blocked := u.cooldowns[address]; blocked {
		if now.Before(until) {
			u.access.Unlock()
			return nil, nil, errIPLimitCooldown
		}
		delete(u.cooldowns, address)
	}

	activity := u.ips[address]
	limit := int(u.ipLimit.Load())
	if activity == nil {
		if limit > 0 {
			u.evictToCapacityLocked(limit-1, now, constant.DefaultIPLimitCooldown)
		}
		u.seq++
		activity = newIPActivity(u.ctx, now, u.seq)
		if u.ips == nil {
			u.ips = make(map[netip.Addr]*ipActivity, 1)
		}
		u.ips[address] = activity
	} else if limit > 0 {
		u.evictToCapacityLocked(limit, now, constant.DefaultIPLimitCooldown)
	}

	activity.active++
	activity.lastSeen = now
	u.active++
	ipCtx := activity.scope.ctx
	u.access.Unlock()

	var closed atomic.Bool
	return ipCtx, func() {
		if closed.Swap(true) {
			return
		}
		u.access.Lock()
		activity.active--
		activity.lastSeen = time.Now()
		u.active--
		u.access.Unlock()
	}, nil
}

// kickIP closes active connections for address and blocks reconnects for cooldown.
func (u *userState) kickIP(address netip.Addr, cooldown time.Duration) {
	u.kickIPAt(address, time.Now(), cooldown)
}

func (u *userState) kickIPAt(address netip.Addr, now time.Time, cooldown time.Duration) {
	address = address.Unmap()
	if !address.IsValid() {
		return
	}
	u.access.Lock()
	if activity := u.ips[address]; activity != nil {
		delete(u.ips, address)
		if activity.scope != nil {
			activity.scope.cancel()
		}
	}
	if cooldown > 0 {
		if u.cooldowns == nil {
			u.cooldowns = make(map[netip.Addr]time.Time, 1)
		}
		u.cooldowns[address] = now.Add(cooldown)
	}
	u.access.Unlock()
}

// onlineIPs returns the addresses connected now or since `since`, and forgets
// the others.
func (u *userState) onlineIPs(since time.Time) []netip.Addr {
	now := time.Now()
	u.access.Lock()
	defer u.access.Unlock()
	for addr, until := range u.cooldowns {
		if !now.Before(until) {
			delete(u.cooldowns, addr)
		}
	}
	if len(u.ips) == 0 {
		return nil
	}
	var addresses []netip.Addr
	for address, activity := range u.ips {
		if activity.active > 0 || !activity.lastSeen.Before(since) {
			addresses = append(addresses, address)
		} else {
			if activity.scope != nil {
				activity.scope.cancel()
			}
			delete(u.ips, address)
		}
	}
	return addresses
}

func (u *userState) activeConnections() int {
	u.access.Lock()
	defer u.access.Unlock()
	return u.active
}

// userTable maps panel user IDs to their state. Lookups are lock-free; updates
// come only from the pull loop.
type userTable struct {
	ctx     context.Context
	current atomic.Pointer[map[int]*userState]

	access sync.Mutex
	// retired users are no longer served but may still have unreported
	// traffic from connections that were closing.
	retired []*userState
}

func newUserTable(ctx context.Context) *userTable {
	table := &userTable{ctx: ctx}
	empty := make(map[int]*userState)
	table.current.Store(&empty)
	return table
}

func (t *userTable) get(id int) *userState {
	return (*t.current.Load())[id]
}

func (t *userTable) all() map[int]*userState {
	return *t.current.Load()
}

// update applies a new user list. Users that stay keep their counters, and
// users that leave have their connections closed.
func (t *userTable) update(users []adapter.User, nodeSpeedLimit uint64) {
	previous := *t.current.Load()
	next := make(map[int]*userState, len(users))
	for _, user := range users {
		state := previous[user.ID]
		if state == nil {
			state = newUserState(t.ctx, user.ID)
		}
		state.setSpeedLimit(effectiveLimit(nodeSpeedLimit, user.SpeedLimit))
		state.setIPLimit(user.IPLimit)
		next[user.ID] = state
	}
	t.current.Store(&next)
	t.access.Lock()
	for id, state := range previous {
		if _, kept := next[id]; !kept {
			state.cancel()
			t.retired = append(t.retired, state)
		}
	}
	t.access.Unlock()
}

// collectTraffic resets and returns every user's counters, then drops
// retired users that have nothing left to report.
func (t *userTable) collectTraffic() []adapter.UserTraffic {
	var items []adapter.UserTraffic
	collect := func(state *userState) bool {
		upload, download := state.upload.Swap(0), state.download.Swap(0)
		if upload == 0 && download == 0 {
			return false
		}
		items = append(items, adapter.UserTraffic{UserID: state.id, Upload: upload, Download: download})
		return true
	}
	for _, state := range t.all() {
		collect(state)
	}
	t.access.Lock()
	retired := t.retired[:0]
	for _, state := range t.retired {
		if collect(state) || state.activeConnections() > 0 {
			retired = append(retired, state)
		}
	}
	clear(t.retired[len(retired):])
	t.retired = retired
	t.access.Unlock()
	return items
}

// restoreTraffic puts back counts the panel refused, so they are reported in
// a later batch. Only a refused batch comes back: one that failed without an
// answer may have been recorded, and is sent again as it is (reportOutbox).
func (t *userTable) restoreTraffic(items []adapter.UserTraffic) {
	for _, item := range items {
		state := t.get(item.UserID)
		if state == nil {
			t.access.Lock()
			for _, retired := range t.retired {
				if retired.id == item.UserID {
					state = retired
					break
				}
			}
			if state == nil {
				// Already dropped from the retired list; keep it reportable.
				state = newUserState(t.ctx, item.UserID)
				state.cancel()
				t.retired = append(t.retired, state)
			}
			t.access.Unlock()
		}
		state.upload.Add(item.Upload)
		state.download.Add(item.Download)
	}
}

// effectiveLimit applies the stricter of two limits, where 0 means unlimited.
func effectiveLimit(a, b uint64) uint64 {
	if a == 0 || (b != 0 && b < a) {
		return b
	}
	return a
}
