package security

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// MTLSConfig mTLS 配置
type MTLSConfig struct {
	RootCACertPath  string // external root CA certificate (used for client certificate verification)
	IntermediateCAPath string // kernel intermediate CA certificate (sent to peers during TLS handshake)
	ServerCertPath  string
	ServerKeyPath   string
}

// NewServerTLSConfig 创建服务端 TLS 配置（要求客户端证书）
func NewServerTLSConfig(config *MTLSConfig) (*tls.Config, error) {
	caCertPool := x509.NewCertPool()

	// 添加根 CA 证书
	caCert, err := os.ReadFile(config.RootCACertPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read root CA cert: %w", err)
	}
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to append root CA cert")
	}

	// 添加中间 CA 证书（connector 证书由中间 CA 签发，需要在服务端信任链中）
	if config.IntermediateCAPath != "" {
		intermediateCert, err := os.ReadFile(config.IntermediateCAPath)
		if err == nil {
			if !caCertPool.AppendCertsFromPEM(intermediateCert) {
				return nil, fmt.Errorf("failed to append intermediate CA cert")
			}
		}
	}

	// 加载服务端证书
	serverCert, err := tls.LoadX509KeyPair(config.ServerCertPath, config.ServerKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load server cert: %w", err)
	}

	tlsConfig := &tls.Config{
		ClientAuth:   tls.RequireAndVerifyClientCert, // 强制 mTLS
		ClientCAs:    caCertPool,
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS13, // 使用 TLS 1.3
		NextProtos:   []string{"h2"},
	}

	return tlsConfig, nil
}

// NewClientTLSConfig creates a client TLS config.
func NewClientTLSConfig(caCertPath, clientCertPath, clientKeyPath, serverName string) (*tls.Config, error) {
	// 加载 CA 证书
	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA cert: %w", err)
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to append CA cert")
	}

	// 加载客户端证书
	clientCert, err := tls.LoadX509KeyPair(clientCertPath, clientKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load client cert: %w", err)
	}

	tlsConfig := &tls.Config{
		RootCAs:      caCertPool,
		Certificates: []tls.Certificate{clientCert},
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2"},
	}

	return tlsConfig, nil
}

// NewServerTransportCredentials creates a server gRPC transport credential.
func NewServerTransportCredentials(config *MTLSConfig) (credentials.TransportCredentials, error) {
	tlsConfig, err := NewServerTLSConfig(config)
	if err != nil {
		return nil, err
	}

	return credentials.NewTLS(tlsConfig), nil
}

// NewClientTransportCredentials 创建客户端 gRPC 传输凭证
func NewClientTransportCredentials(caCertPath, clientCertPath, clientKeyPath, serverName string) (credentials.TransportCredentials, error) {
	tlsConfig, err := NewClientTLSConfig(caCertPath, clientCertPath, clientKeyPath, serverName)
	if err != nil {
		return nil, err
	}

	return credentials.NewTLS(tlsConfig), nil
}

// NewBootstrapServerTransportCredentials 创建引导服务端 gRPC 传输凭证（不需要客户端证书）
// 用于首次注册，允许无证书连接
func NewBootstrapServerTransportCredentials(config *MTLSConfig) (credentials.TransportCredentials, error) {
	// 加载服务端证书
	serverCert, err := tls.LoadX509KeyPair(config.ServerCertPath, config.ServerKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load server cert: %w", err)
	}

	tlsConfig := &tls.Config{
		ClientAuth:   tls.NoClientCert, // 不需要客户端证书
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2"},
	}

	return credentials.NewTLS(tlsConfig), nil
}

// NewDynamicServerTransportCredentials creates a server TransportCredentials that
// rebuilds the client CA pool from disk before every TLS handshake. This is required
// for inter-kernel mTLS because peer intermediate CA certs are exchanged dynamically
// and are not available at startup. Without dynamic rebuilding, the first handshake
// would fail and the connection would deadlock.
func NewDynamicServerTransportCredentials(
	serverCertPath, serverKeyPath string,
	rootCAPath, intermediateCAPath, peerCertsDir string,
	peerCertGetter func(peerKernelID string) []byte,
) (credentials.TransportCredentials, error) {
	return NewDynamicTLSCredentials(
		serverCertPath, serverKeyPath,
		rootCAPath, intermediateCAPath, peerCertsDir,
		peerCertGetter,
	)
}

// DynamicTLSCredentials is a gRPC server TransportCredentials that rebuilds
// the client CA pool from disk on every TLS handshake. This is required for
// kernel-to-kernel mTLS because the peer intermediate CA cert is not available
// at startup — it is only exchanged during the first handshake. Without dynamic
// rebuilding the first handshake would fail, the cert would never be saved, and
// the connection would deadlock.
type DynamicTLSCredentials struct {
	// Server certificate (loaded once at construction)
	serverCert tls.Certificate
	// Absolute paths
	rootCAPath         string
	intermediateCAPath string // this kernel's intermediate CA cert (sent by peers)
	peerCertsDir      string // directory containing peer-*.crt files
	peerCertGetter    func(peerKernelID string) []byte // returns raw PEM for a peer intermediate CA cert
	// TLS 1.3 only
	minVersion uint16
}

// NewDynamicTLSCredentials creates a DynamicTLSCredentials.
// peerCertGetter(peerKernelID) returns the raw PEM bytes of that peer's
// intermediate CA cert so the CA pool can be rebuilt before verification.
// intermediateCAPath is this kernel's intermediate CA cert, used for
// verifying the client's certificate chain.
func NewDynamicTLSCredentials(
	serverCertPath, serverKeyPath string,
	rootCAPath, intermediateCAPath, peerCertsDir string,
	peerCertGetter func(peerKernelID string) []byte,
) (*DynamicTLSCredentials, error) {
	// Load server certificate and private key.
	cert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load server cert: %w", err)
	}

	// Build the full certificate chain for the TLS handshake.
	// tls.LoadX509KeyPair only stores the leaf cert in Certificate[0] as DER.
	// We need to append the intermediate CA as a DER-encoded DER entry so the
	// server sends the complete chain (leaf -> intermediate) to the client.
	if intermediateCAPath != "" {
		icPEM, err := os.ReadFile(intermediateCAPath)
		if err == nil {
			// Parse the PEM to get DER-encoded bytes.
			block, _ := pem.Decode(icPEM)
			if block != nil && block.Type == "CERTIFICATE" {
				cert.Certificate = append(cert.Certificate, block.Bytes)
			}
		}
	}

	return &DynamicTLSCredentials{
		serverCert:          cert,
		rootCAPath:          rootCAPath,
		intermediateCAPath:  intermediateCAPath,
		peerCertsDir:        peerCertsDir,
		peerCertGetter:      peerCertGetter,
		minVersion:          tls.VersionTLS13,
	}, nil
}

// Clone implements credentials.TransportCredentials. Clone must be safe for
// concurrent use — it returns the same underlying struct since we rebuild
// state on every handshake anyway.
func (d *DynamicTLSCredentials) Clone() credentials.TransportCredentials {
	return d
}

// OverrideServerName implements credentials.TransportCredentials (deprecated but required).
func (d *DynamicTLSCredentials) OverrideServerName(string) error {
	return nil
}

// Info implements credentials.TransportCredentials.
func (d *DynamicTLSCredentials) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{
		SecurityProtocol: "tls",
		SecurityVersion:  "1.3",
	}
}

// ServerHandshake performs a TLS 1.3 server-side handshake, rebuilding the
// client CA pool from disk before verifying the peer's certificate chain.
func (d *DynamicTLSCredentials) ServerHandshake(rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	// Build the CA pool fresh from disk.
	caCertPool := x509.NewCertPool()
	rootCert, err := os.ReadFile(d.rootCAPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read root CA cert: %w", err)
	}
	if !caCertPool.AppendCertsFromPEM(rootCert) {
		return nil, nil, fmt.Errorf("failed to append root CA cert")
	}

	// Load this kernel's intermediate CA so we can verify client certificates
	// signed by this CA (e.g., when a peer's connector authenticates).
	// Peers send us their intermediate CA during RegisterKernelCA so we can
	// verify their connector/client certs.
	if d.intermediateCAPath != "" {
		if ic, err := os.ReadFile(d.intermediateCAPath); err == nil {
			caCertPool.AppendCertsFromPEM(ic)
		}
	}

	// Also load all peer-*.crt files found on disk.
	peerCertsLoaded := 0
	if d.peerCertsDir != "" {
		entries, err := os.ReadDir(d.peerCertsDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				if !strings.HasPrefix(entry.Name(), "peer-") || !strings.HasSuffix(entry.Name(), ".crt") {
					continue
				}
				path := filepath.Join(d.peerCertsDir, entry.Name())
				if pem, err := os.ReadFile(path); err == nil {
					if caCertPool.AppendCertsFromPEM(pem) {
						peerCertsLoaded++
					}
				}
			}
		}
		// Also call the getter for each known peer so we include newly
		// received-but-not-yet-persisted certs.
		if d.peerCertGetter != nil {
			if pem := d.peerCertGetter(""); len(pem) > 0 {
				caCertPool.AppendCertsFromPEM(pem)
			}
		}
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{d.serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caCertPool,
		MinVersion:   d.minVersion,
		NextProtos:   []string{"h2"},
		ServerName:   "", // no SNI in this deployment
	}

	conn := tls.Server(rawConn, tlsConfig)
	if err := conn.Handshake(); err != nil {
		conn.Close()
		return nil, nil, err
	}

	state := conn.ConnectionState()
	return conn, credentials.TLSInfo{State: state}, nil
}

// ClientHandshake implements the client side (not needed for server-only use).
func (d *DynamicTLSCredentials) ClientHandshake(ctx context.Context, authority string, rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, fmt.Errorf("ClientHandshake not implemented on server DynamicTLSCredentials")
}

// BuildPeerCertPool rebuilds a fresh x509.CertPool containing the root CA
// and all peer intermediate CA certificates from disk. Used by clients that
// need to connect to remote kernels (the caller passes its own peer certs).
func BuildPeerCertPool(rootCAPath string, peerCertsDir string) *x509.CertPool {
	pool := x509.NewCertPool()
	if pem, err := os.ReadFile(rootCAPath); err == nil {
		pool.AppendCertsFromPEM(pem)
	}
	if peerCertsDir != "" {
		if entries, err := os.ReadDir(peerCertsDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				if !strings.HasPrefix(entry.Name(), "peer-") || !strings.HasSuffix(entry.Name(), ".crt") {
					continue
				}
				path := filepath.Join(peerCertsDir, entry.Name())
				if pem, err := os.ReadFile(path); err == nil {
					pool.AppendCertsFromPEM(pem)
				}
			}
		}
	}
	return pool
}

// ExtractConnectorIDFromContext 从 gRPC context 中提取连接器 ID（从证书 CN）
func ExtractConnectorIDFromContext(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("no peer information in context")
	}

	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", fmt.Errorf("no TLS info in peer")
	}

	if len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return "", fmt.Errorf("no verified certificate chains")
	}

	cert := tlsInfo.State.VerifiedChains[0][0]
	connectorID := cert.Subject.CommonName

	if connectorID == "" {
		return "", fmt.Errorf("empty connector ID in certificate")
	}

	return connectorID, nil
}

// VerifyConnectorID 验证请求中的 connector_id 是否与证书中的 CN 匹配
func VerifyConnectorID(ctx context.Context, requestedID string) error {
	certID, err := ExtractConnectorIDFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to extract connector ID from cert: %w", err)
	}

	if certID != requestedID {
		return fmt.Errorf("connector ID mismatch: cert=%s, request=%s", certID, requestedID)
	}

	return nil
}

