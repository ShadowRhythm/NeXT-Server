package constant

import "time"

const (
	DefaultPullInterval = 60 * time.Second
	DefaultPushInterval = 60 * time.Second
	// FinalPushTimeout bounds the traffic report sent while shutting down.
	FinalPushTimeout = 10 * time.Second
	// DefaultIPLimitCooldown blocks a kicked IP from reconnecting briefly to prevent reconnect ping-pong.
	DefaultIPLimitCooldown = 45 * time.Second
)

// Abuse check defaults (option.AbuseOptions).
const (
	DefaultAbuseBanDuration = 10 * time.Minute

	DefaultPortScanWindow             = time.Minute
	DefaultPortScanFailedDestinations = 60
	DefaultPortScanPortsPerHost       = 100

	DefaultFloodWindow                    = 10 * time.Second
	DefaultFloodConnections               = 1000
	DefaultFloodConnectionsPerDestination = 500

	DefaultBruteForceWindow                    = time.Minute
	DefaultBruteForceHosts                     = 30
	DefaultBruteForceConnectionsPerDestination = 120

	// AbuseOutageGrace: a failed dial counts toward a port scan only if a
	// dial on the node got through since this long before it started.
	AbuseOutageGrace = 5 * time.Second

	// SignatureFetchTimeout bounds fetching abuse.signatures.url, which
	// happens in the pull loop.
	SignatureFetchTimeout = 15 * time.Second

	// AbuseNoticeInterval is how often one user's repeated hits of one kind
	// are logged and reported to the panel.
	AbuseNoticeInterval = time.Minute

	// Default Abuse Rule IDs (reported to panel detect logs).
	DefaultP2PRuleID          = 11
	DefaultPortScanRuleID     = 12
	DefaultFloodRuleID        = 13
	DefaultBruteForceRuleID   = 14
	DefaultBlockedPortsRuleID = 15
	DefaultExploitsRuleID     = 16
	DefaultMiningRuleID       = 17
)

var (
	// DefaultBlockedPorts are refused unless configured otherwise: SMTP, which
	// spam goes out on, and MS-RPC and NetBIOS/SMB, which worms spread on and
	// nobody needs across the internet.
	DefaultBlockedPorts = []uint16{25, 135, 137, 138, 139, 445}
	// DefaultLoginPorts are where brute force goes: FTP, SSH, Telnet, MS SQL,
	// Oracle, MySQL, RDP, PostgreSQL, VNC, WinRM, Redis and MongoDB.
	DefaultLoginPorts = []uint16{21, 22, 23, 1433, 1521, 3306, 3389, 5432, 5900, 5985, 5986, 6379, 27017}
)
