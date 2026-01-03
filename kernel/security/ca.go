package security

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"
)

// CA 表示内部证书颁发机构
type CA struct {
	CertPath string
	KeyPath  string
	Cert     *x509.Certificate
	Key      *rsa.PrivateKey
}

// NewCA 创建或加载一个 CA
func NewCA(certPath, keyPath string) (*CA, error) {
	ca := &CA{
		CertPath: certPath,
		KeyPath:  keyPath,
	}

	// 尝试加载现有证书
	if _, err := os.Stat(certPath); err == nil {
		if err := ca.load(); err != nil {
			return nil, fmt.Errorf("failed to load CA: %w", err)
		}
		return ca, nil
	}

	// 生成新的 CA 证书
	if err := ca.generate(); err != nil {
		return nil, fmt.Errorf("failed to generate CA: %w", err)
	}

	return ca, nil
}

// generate 生成新的 CA 根证书
func (ca *CA) generate() error {
	// 生成 RSA 私钥
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// 创建 CA 证书模板
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization:  []string{"Trusted Data Space"},
			Country:       []string{"CN"},
			Province:      []string{"Beijing"},
			Locality:      []string{"Beijing"},
			CommonName:    "Trusted Data Space Internal CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), // 10 年有效期
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            2,
	}

	// 自签名
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	// 解析证书
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("failed to parse certificate: %w", err)
	}

	ca.Cert = cert
	ca.Key = privateKey

	// 保存证书
	if err := ca.save(); err != nil {
		return fmt.Errorf("failed to save CA: %w", err)
	}

	return nil
}

// load 从文件加载 CA 证书和私钥
func (ca *CA) load() error {
	// 读取证书
	certPEM, err := os.ReadFile(ca.CertPath)
	if err != nil {
		return fmt.Errorf("failed to read cert file: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("failed to decode PEM block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse certificate: %w", err)
	}

	// 读取私钥
	keyPEM, err := os.ReadFile(ca.KeyPath)
	if err != nil {
		return fmt.Errorf("failed to read key file: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return fmt.Errorf("failed to decode PEM block for key")
	}

	// 尝试解析PKCS8格式（OpenSSL默认格式）
	var key *rsa.PrivateKey
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		// 如果PKCS8失败，尝试PKCS1格式
		key, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		if err != nil {
			return fmt.Errorf("failed to parse private key (tried PKCS8 and PKCS1): %w", err)
		}
	} else {
		// 转换为RSA私钥
		var ok bool
		key, ok = parsedKey.(*rsa.PrivateKey)
		if !ok {
			return fmt.Errorf("private key is not RSA format")
		}
	}

	ca.Cert = cert
	ca.Key = key

	return nil
}

// save 保存 CA 证书和私钥到文件
func (ca *CA) save() error {
	// 保存证书
	certFile, err := os.Create(ca.CertPath)
	if err != nil {
		return fmt.Errorf("failed to create cert file: %w", err)
	}
	defer certFile.Close()

	if err := pem.Encode(certFile, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: ca.Cert.Raw,
	}); err != nil {
		return fmt.Errorf("failed to write cert: %w", err)
	}

	// 保存私钥
	keyFile, err := os.Create(ca.KeyPath)
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}
	defer keyFile.Close()

	if err := pem.Encode(keyFile, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(ca.Key),
	}); err != nil {
		return fmt.Errorf("failed to write key: %w", err)
	}

	return nil
}

// IssueConnectorCert 为连接器签发证书
func (ca *CA) IssueConnectorCert(connectorID string, dnsNames []string, ipAddrs []string) (certPEM, keyPEM []byte, err error) {
	// 生成连接器私钥
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	// 创建证书模板
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Trusted Data Space"},
			CommonName:   connectorID,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(1, 0, 0), // 1 年有效期
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
	}

	// 使用 CA 签名
	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.Cert, &privateKey.PublicKey, ca.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	// PEM 编码证书
	certPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	// PEM 编码私钥
	keyPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	return certPEM, keyPEM, nil
}

// VerifyConnectorCert 验证连接器证书是否由此 CA 签发
func (ca *CA) VerifyConnectorCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	// 创建证书池
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)

	opts := x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	if _, err := cert.Verify(opts); err != nil {
		return nil, fmt.Errorf("certificate verification failed: %w", err)
	}

	return cert, nil
}

