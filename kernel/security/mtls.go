package security

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"context"
)

// MTLSConfig mTLS 配置
type MTLSConfig struct {
	CACertPath     string
	ServerCertPath string
	ServerKeyPath  string
}

// NewServerTLSConfig 创建服务端 TLS 配置（要求客户端证书）
func NewServerTLSConfig(config *MTLSConfig) (*tls.Config, error) {
	// 加载 CA 证书
	caCert, err := os.ReadFile(config.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA cert: %w", err)
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to append CA cert")
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
	}

	return tlsConfig, nil
}

// NewClientTLSConfig 创建客户端 TLS 配置
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
	}

	return tlsConfig, nil
}

// NewServerTransportCredentials 创建服务端 gRPC 传输凭证
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
	}

	return credentials.NewTLS(tlsConfig), nil
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

