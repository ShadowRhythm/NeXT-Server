package hysteria2

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"

	"github.com/The-NeXT-Project/NeXT-Server/adapter"
	"github.com/The-NeXT-Project/NeXT-Server/proxy/inbound"

	boxAdapter "github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	boxOption "github.com/sagernet/sing-box/option"
	qtls "github.com/sagernet/sing-quic"
	"github.com/sagernet/sing-quic/hysteria2"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var _ adapter.NodeServer = (*Server)(nil)

type Server struct {
	logger    logger.ContextLogger
	router    boxAdapter.ConnectionRouterEx
	listener  *inbound.Listener
	tlsConfig tls.ServerConfig
	service   *hysteria2.Service[int]
}

func New(options inbound.NodeOptions) (*Server, error) {
	if options.Config.TLS == nil {
		return nil, C.ErrTLSRequired
	}
	tlsConfig, err := options.NewTLSServer()
	if err != nil {
		return nil, err
	}
	server := &Server{
		logger:    options.Logger,
		router:    options.Router,
		tlsConfig: tlsConfig,
	}
	masqueradeHandler, err := buildMasqueradeHandler(options.Server.Masquerade)
	if err != nil {
		return nil, err
	}
	serviceOptions := hysteria2.ServiceOptions{
		Context:           options.Context,
		Logger:            options.Logger,
		TLSConfig:         tlsConfig,
		QUICOptions:       qtls.QUICOptions{},
		UDPTimeout:        time.Duration(options.Server.UDPTimeout),
		Handler:           server,
		MasqueradeHandler: masqueradeHandler,
	}
	if serviceOptions.UDPTimeout == 0 {
		serviceOptions.UDPTimeout = C.UDPTimeout
	}
	if password := options.Config.Hysteria2.ObfsPassword; password != "" {
		switch options.Config.Hysteria2.ObfsType {
		case hysteria2.ObfsTypeSalamander:
			serviceOptions.SalamanderPassword = password
		case hysteria2.ObfsTypeGecko:
			serviceOptions.GeckoPassword = password
		default:
			return nil, E.New("unknown hysteria2 obfs type: ", options.Config.Hysteria2.ObfsType)
		}
	}
	server.service, err = hysteria2.NewService[int](serviceOptions)
	if err != nil {
		return nil, err
	}
	server.listener, err = options.NewListener(nil, nil, true)
	if err != nil {
		return nil, err
	}
	return server, nil
}

func (s *Server) Start() error {
	err := s.tlsConfig.Start()
	if err != nil {
		return err
	}
	packetConn, err := s.listener.ListenUDP()
	if err != nil {
		return err
	}
	return s.service.Start(packetConn)
}

func (s *Server) Close() error {
	return common.Close(s.listener, s.tlsConfig, s.service)
}

// UpdateUsers authenticates users by password.
func (s *Server) UpdateUsers(users []adapter.User) error {
	ids := make([]int, 0, len(users))
	passwords := make([]string, 0, len(users))
	for _, user := range users {
		if user.Password == "" {
			continue
		}
		ids = append(ids, user.ID)
		passwords = append(passwords, user.Password)
	}
	s.service.UpdateUsers(ids, passwords)
	return nil
}

func buildMasqueradeHandler(masquerade *boxOption.Hysteria2Masquerade) (http.Handler, error) {
	if masquerade == nil || masquerade.Type == "" {
		return nil, nil
	}
	switch masquerade.Type {
	case C.Hysterai2MasqueradeTypeFile:
		dir := os.ExpandEnv(masquerade.FileOptions.Directory)
		if dir == "" {
			return nil, E.New("masquerade.directory is required")
		}
		if _, err := os.ReadDir(dir); err != nil && !os.IsNotExist(err) {
			return nil, E.Cause(err, "read masquerade directory")
		}
		return http.FileServer(http.Dir(dir)), nil
	case C.Hysterai2MasqueradeTypeProxy:
		masqueradeURL, err := url.Parse(masquerade.ProxyOptions.URL)
		if err != nil {
			return nil, E.Cause(err, "parse masquerade URL")
		}
		if (masqueradeURL.Scheme != "http" && masqueradeURL.Scheme != "https") || masqueradeURL.Host == "" {
			return nil, E.New("masquerade proxy URL must be http or https with host: ", masquerade.ProxyOptions.URL)
		}
		rewriteHost := masquerade.ProxyOptions.RewriteHost
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 10 * time.Second
		return &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(masqueradeURL)
				if !rewriteHost {
					r.Out.Host = r.In.Host
				}
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				w.WriteHeader(http.StatusBadGateway)
			},
		}, nil
	case C.Hysterai2MasqueradeTypeString:
		headers := masquerade.StringOptions.Headers
		statusCode := masquerade.StringOptions.StatusCode
		content := []byte(masquerade.StringOptions.Content)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for key, values := range headers {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			if statusCode != 0 {
				w.WriteHeader(statusCode)
			}
			_, _ = w.Write(content)
		}), nil
	default:
		return nil, E.New("unknown masquerade type: ", masquerade.Type)
	}
}

func (s *Server) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	ctx, metadata, userID := s.newMetadata(ctx, source, destination)
	s.logger.DebugContext(ctx, "[", userID, "] inbound connection to ", metadata.Destination)
	s.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func (s *Server) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	ctx, metadata, userID := s.newMetadata(ctx, source, destination)
	s.logger.DebugContext(ctx, "[", userID, "] inbound packet connection to ", metadata.Destination)
	s.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}

func (s *Server) newMetadata(ctx context.Context, source M.Socksaddr, destination M.Socksaddr) (context.Context, boxAdapter.InboundContext, int) {
	var metadata boxAdapter.InboundContext
	metadata.Inbound = C.TypeHysteria2
	metadata.InboundType = C.TypeHysteria2
	metadata.OriginDestination = s.listener.UDPAddr()
	metadata.Source = source
	metadata.Destination = destination
	userID, _ := auth.UserFromContext[int](ctx)
	ctx = inbound.UserContext(log.ContextWithNewID(ctx), &metadata, userID)
	return ctx, metadata, userID
}
