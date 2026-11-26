package nextserver

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/The-NeXT-Project/NeXT-Server/adapter"
	"github.com/The-NeXT-Project/NeXT-Server/api"
	"github.com/The-NeXT-Project/NeXT-Server/api/serverv1"
	"github.com/The-NeXT-Project/NeXT-Server/common/frontproxy"
	"github.com/The-NeXT-Project/NeXT-Server/common/reporter"
	"github.com/The-NeXT-Project/NeXT-Server/constant"
	"github.com/The-NeXT-Project/NeXT-Server/option"
	"github.com/The-NeXT-Project/NeXT-Server/proxy"
	"github.com/The-NeXT-Project/NeXT-Server/proxy/inbound"
	"github.com/The-NeXT-Project/NeXT-Server/update"

	boxAdapter "github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/route"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// Server runs one panel node: it keeps a node server in sync with the panel
// and routes, accounts and reports everything its users send.
type Server struct {
	ctx      context.Context
	cancel   context.CancelFunc
	logger   log.ContextLogger
	reporter reporter.Reporter
	options  option.ServerOptions
	api      adapter.APIClient

	dialer        N.Dialer
	resolveDomain bool
	connections   *route.ConnectionManager
	users         *userTable
	// local is nil with allow_private_destinations.
	local   *localAddresses
	auditor *auditor
	abuse   *abuseGuard

	pullInterval time.Duration
	pushInterval time.Duration
	loops        sync.WaitGroup
	onRestart    func()

	// Owned by the pull loop once started.
	nodeConfig    *adapter.NodeConfig
	runningConfig *adapter.NodeConfig
	nodeServer    adapter.NodeServer
	userList      []adapter.User

	// Owned by the push loop once started, and by Close after it.
	lastPush   time.Time
	traffic    *reportOutbox[adapter.UserTraffic]
	detectLogs *reportOutbox[adapter.DetectLog]
}

func NewServer(ctx context.Context, logger log.ContextLogger, errorReporter reporter.Reporter, options option.ServerOptions) (*Server, error) {
	apiClient, err := api.NewClient(options)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	connections := route.NewConnectionManager(ctx, connectionLogger{logger})
	// The default dialer registers its connections here, so Close can end them.
	ctx = service.ContextWith[boxAdapter.ConnectionManager](ctx, connections)
	defaultDialer, err := dialer.NewDefault(ctx, common.PtrValueOrDefault(options.Dialer))
	if err != nil {
		cancel()
		return nil, E.Cause(err, "create dialer")
	}
	server := &Server{
		ctx:           ctx,
		cancel:        cancel,
		logger:        logger,
		reporter:      errorReporter,
		options:       options,
		api:           apiClient,
		dialer:        defaultDialer,
		resolveDomain: true,
		connections:   connections,
		users:         newUserTable(ctx),
		auditor:       newAuditor(),
		pullInterval:  time.Duration(options.PullInterval),
		pushInterval:  time.Duration(options.PushInterval),
	}
	server.abuse, err = newAbuseGuard(options.Abuse, logger, server.auditor)
	if err != nil {
		cancel()
		return nil, err
	}
	if options.FrontProxy != nil && options.FrontProxy.Type != "" {
		server.dialer, err = frontproxy.New(defaultDialer, *options.FrontProxy)
		if err != nil {
			cancel()
			return nil, E.Cause(err, "create front proxy")
		}
		server.resolveDomain = options.FrontProxy.ResolveDomain
	}
	if !options.AllowPrivateDestinations {
		server.local = &localAddresses{}
		server.dialer = &privateFilterDialer{Dialer: server.dialer, passDomains: !server.resolveDomain, local: server.local}
	}
	server.traffic = &reportOutbox[adapter.UserTraffic]{
		name:    "traffic",
		logger:  logger,
		collect: server.users.collectTraffic,
		restore: server.users.restoreTraffic,
		send:    apiClient.ReportTraffic,
	}
	server.detectLogs = &reportOutbox[adapter.DetectLog]{
		name:    "detect log",
		logger:  logger,
		collect: server.auditor.collect,
		restore: server.auditor.restore,
		send:    apiClient.ReportDetectLogs,
	}
	if server.pullInterval <= 0 {
		server.pullInterval = constant.DefaultPullInterval
	}
	if server.pushInterval <= 0 {
		server.pushInterval = constant.DefaultPushInterval
	}
	return server, nil
}

// Start fetches the node from the panel and serves it. A panel that cannot be
// reached here is fatal, so a misconfigured node fails loudly.
func (s *Server) Start() error {
	var (
		config    *adapter.NodeConfig
		configErr error
		users     []adapter.User
		usersErr  error
		rules     []adapter.DetectRule
		rulesErr  error
		wg        sync.WaitGroup
	)
	wg.Add(5)
	go func() {
		defer wg.Done()
		config, configErr = s.api.FetchNodeConfig(s.ctx)
	}()
	go func() {
		defer wg.Done()
		users, usersErr = s.api.FetchUsers(s.ctx)
	}()
	go func() {
		defer wg.Done()
		rules, rulesErr = s.api.FetchDetectRules(s.ctx)
	}()
	go func() {
		defer wg.Done()
		s.refreshLocalAddresses()
	}()
	go func() {
		defer wg.Done()
		s.abuse.refreshSignatures(s.ctx)
	}()
	wg.Wait()

	if configErr != nil {
		return E.Cause(configErr, "fetch node config")
	}
	s.nodeConfig = config
	if errors.Is(usersErr, serverv1.ErrNodeUnavailable) {
		s.logger.Warn("panel serves no users: ", usersErr)
		users = []adapter.User{}
	} else if usersErr != nil {
		return E.Cause(usersErr, "fetch users")
	}
	s.logError("fetch detect rules", rulesErr)
	if rules != nil {
		s.auditor.setRules(rules)
	}
	s.applyUsers(users)
	err := s.syncNodeServer()
	if err != nil {
		return err
	}
	s.lastPush = time.Now()
	s.loops.Add(2)
	go s.loop(s.pullInterval, s.pullOnce)
	go s.loop(s.pushInterval, s.pushOnce)
	go func() {
		hb, err := s.api.Heartbeat(s.ctx, 0)
		if err != nil && !E.IsClosedOrCanceled(err) {
			s.logger.Error("heartbeat: ", err)
		}
		if hb.Upgrade {
			s.triggerUpgrade()
		} else if hb.Restart {
			s.triggerRestart()
		}
	}()
	return nil
}

// Close stops the node, ends open connections and reports their last traffic
// and detect logs, after any batch still waiting to be sent again. What does
// not get through here is lost.
func (s *Server) Close() error {
	s.cancel()
	s.loops.Wait()
	var err error
	if s.nodeServer != nil {
		err = s.nodeServer.Close()
	}
	s.connections.CloseAll()
	ctx, cancel := context.WithTimeout(context.Background(), constant.FinalPushTimeout)
	defer cancel()
	var (
		trafficErr   error
		detectLogErr error
		flushGroup   sync.WaitGroup
	)
	flushGroup.Add(2)
	go func() {
		defer flushGroup.Done()
		if reportErr := s.traffic.flush(ctx); reportErr != nil {
			trafficErr = E.Cause(reportErr, "report final traffic")
		}
	}()
	go func() {
		defer flushGroup.Done()
		if reportErr := s.detectLogs.flush(ctx); reportErr != nil {
			detectLogErr = E.Cause(reportErr, "report final detect logs")
		}
	}()
	flushGroup.Wait()
	return E.Errors(err, trafficErr, detectLogErr)
}

func (s *Server) loop(interval time.Duration, run func()) {
	defer s.loops.Done()
	defer s.reporter.Recover()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (s *Server) logError(message string, err error) {
	if err != nil && !E.IsClosedOrCanceled(err) {
		s.logger.Error(message, ": ", err)
	}
}

func (s *Server) pullOnce() {
	var (
		config    *adapter.NodeConfig
		configErr error
		users     []adapter.User
		usersErr  error
		rules     []adapter.DetectRule
		rulesErr  error
		wg        sync.WaitGroup
	)
	wg.Add(5)
	go func() {
		defer wg.Done()
		config, configErr = s.api.FetchNodeConfig(s.ctx)
	}()
	go func() {
		defer wg.Done()
		users, usersErr = s.api.FetchUsers(s.ctx)
	}()
	go func() {
		defer wg.Done()
		rules, rulesErr = s.api.FetchDetectRules(s.ctx)
	}()
	go func() {
		defer wg.Done()
		s.refreshLocalAddresses()
	}()
	go func() {
		defer wg.Done()
		s.abuse.refreshSignatures(s.ctx)
	}()
	wg.Wait()

	s.logError("fetch node config", configErr)
	if config != nil {
		s.nodeConfig = config
	}
	s.logError("fetch detect rules", rulesErr)
	if rules != nil {
		s.auditor.setRules(rules)
	}
	if errors.Is(usersErr, serverv1.ErrNodeUnavailable) {
		if len(s.userList) > 0 {
			s.logger.Warn("panel stopped serving users: ", usersErr)
		}
		users = []adapter.User{}
	} else {
		s.logError("fetch users", usersErr)
	}

	if s.nodeServer == nil || !sameListener(s.nodeConfig, s.runningConfig) {
		if users != nil {
			s.users.update(users, s.nodeConfig.SpeedLimit)
			s.userList = users
		}
		s.logError("update node server", s.syncNodeServer())
		return
	}
	s.logError("update node server", s.syncNodeServer())
	if users != nil {
		s.applyUsers(users)
	}
}

// refreshLocalAddresses keeps the last addresses when reading them fails,
// which leaves at least the private networks refused.
func (s *Server) refreshLocalAddresses() {
	if s.local != nil {
		s.logError("refresh local addresses", s.local.refresh())
	}
}

// syncNodeServer brings the running node server in line with the panel. The
// listener is only rebuilt when something it was built from changed; a failed
// rebuild is retried on the next pull.
func (s *Server) syncNodeServer() error {
	config := s.nodeConfig
	if s.runningConfig != nil && config.SpeedLimit != s.runningConfig.SpeedLimit {
		s.users.update(s.userList, config.SpeedLimit)
	}
	if s.nodeServer != nil && sameListener(config, s.runningConfig) {
		s.runningConfig = config
		return nil
	}
	if s.nodeServer != nil {
		err := s.nodeServer.Close()
		s.logError("close node server", err)
		s.nodeServer = nil
		s.runningConfig = nil
	}
	if config.TLS != nil && config.TLS.Insecure {
		s.logger.Warn("no certificate configured, serving a self-signed one; clients must set allow_insecure")
	}
	nodeServer, err := proxy.New(inbound.NodeOptions{
		Context: s.ctx,
		Logger:  connectionLogger{s.logger},
		Router:  s,
		Config:  config,
		Server:  s.options,
	})
	if err != nil {
		return E.Cause(err, "create ", config.Type, " server")
	}
	err = nodeServer.UpdateUsers(s.userList)
	if err != nil {
		nodeServer.Close()
		return E.Cause(err, "update users")
	}
	err = nodeServer.Start()
	if err != nil {
		nodeServer.Close()
		return E.Cause(err, "start ", config.Type, " server")
	}
	s.nodeServer = nodeServer
	s.runningConfig = config
	s.logger.Info(config.Type, " server started on port ", config.ListenPort, " with ", len(s.userList), " users")
	return nil
}

// sameListener compares configs except the speed limit, which applies
// without a restart.
func sameListener(a, b *adapter.NodeConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	left, right := *a, *b
	left.SpeedLimit, right.SpeedLimit = 0, 0
	return reflect.DeepEqual(left, right)
}

func (s *Server) applyUsers(users []adapter.User) {
	// The table first: a removed user must stop routing before the node
	// server stops authenticating them, and a new one must be routable as
	// soon as it authenticates.
	s.users.update(users, s.nodeConfig.SpeedLimit)
	s.userList = users
	if s.nodeServer == nil {
		return
	}
	err := s.nodeServer.UpdateUsers(users)
	if err != nil {
		s.logError("update users", err)
		return
	}
	s.logger.Info("serving ", len(users), " users")
}

func (s *Server) pushOnce() {
	since := s.lastPush
	s.lastPush = time.Now()

	var online []adapter.UserOnline
	onlineUsers := 0
	for id, user := range s.users.all() {
		addresses := user.onlineIPs(since)
		if len(addresses) == 0 {
			continue
		}
		onlineUsers++
		for _, address := range addresses {
			online = append(online, adapter.UserOnline{UserID: id, IP: address.String()})
		}
	}
	hb, heartbeatErr := s.api.Heartbeat(s.ctx, onlineUsers)
	s.logError("heartbeat", heartbeatErr)
	if hb.Upgrade {
		if s.triggerUpgrade() {
			return
		}
	} else if hb.Restart {
		s.triggerRestart()
		return
	}

	var reportGroup sync.WaitGroup
	reportGroup.Add(2)
	go func() {
		defer reportGroup.Done()
		s.logError("report traffic", s.traffic.flush(s.ctx))
	}()
	go func() {
		defer reportGroup.Done()
		s.logError("report detect logs", s.detectLogs.flush(s.ctx))
	}()
	if len(online) > 0 {
		reportGroup.Add(1)
		go func() {
			defer reportGroup.Done()
			s.logError("report online users", s.api.ReportOnline(s.ctx, online))
		}()
	}
	reportGroup.Wait()
}

// triggerUpgrade downloads the latest release and replaces the running binary,
// then requests a graceful shutdown so the process manager (systemd) restarts
// it with the new binary after flushing final reports.
func (s *Server) triggerUpgrade() bool {
	s.logger.Info("upgrade requested by panel, downloading latest release...")
	if err := update.Do(s.ctx, "ShadowRhythm/NeXT-Server"); err != nil {
		if errors.Is(err, update.ErrInProgress) {
			s.logger.Info("upgrade already in progress, skipping duplicate request")
			return false
		}
		s.logger.Error("upgrade failed: ", err)
		return false
	}
	s.logger.Info("upgrade complete, restarting gracefully...")
	s.requestRestart()
	return true
}

// triggerRestart requests a graceful shutdown so systemd restarts the process
// after flushing final reports.
func (s *Server) triggerRestart() {
	s.logger.Info("restart requested by panel, restarting gracefully...")
	s.requestRestart()
}

func (s *Server) requestRestart() {
	if s.onRestart != nil {
		s.onRestart()
		return
	}
	os.Exit(1)
}
