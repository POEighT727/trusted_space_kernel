package security

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
)

// SignData 使用RSA私钥对数据进行签名
func SignData(data []byte, privateKey *rsa.PrivateKey) (string, error) {
	// 计算数据的SHA256哈希
	hash := sha256.Sum256(data)

	// 使用RSA-PSS签名
	signature, err := rsa.SignPSS(rand.Reader, privateKey, crypto.SHA256, hash[:], nil)
	if err != nil {
		return "", fmt.Errorf("failed to sign data: %w", err)
	}

	// 返回base64编码的签名
	return base64.StdEncoding.EncodeToString(signature), nil
}

// VerifySignature 使用RSA公钥验证签名
func VerifySignature(data []byte, signature string, publicKey *rsa.PublicKey) error {
	// 解码base64签名
	sigBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("failed to decode signature: %w", err)
	}

	// 计算数据的SHA256哈希
	hash := sha256.Sum256(data)

	// 验证RSA-PSS签名
	err = rsa.VerifyPSS(publicKey, crypto.SHA256, hash[:], sigBytes, nil)
	if err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}

	return nil
}

// GenerateEvidenceSignature 生成存证记录的签名（使用内核私钥）
func GenerateEvidenceSignature(connectorID, eventType, channelID, dataHash string, timestamp int64) (string, error) {
	// 注意：这里我们使用内核的私钥来签名evidence，而不是连接器的私钥
	// 这样可以证明evidence是由内核生成的

	// 构建要签名的数据
	data := fmt.Sprintf("%s|%s|%s|%s|%d", connectorID, eventType, channelID, dataHash, timestamp)

	// 尝试加载内核的私钥（用于evidence签名）
	// 这里我们使用CA的私钥，因为内核应该有CA的私钥
	caKeyPath := "certs/ca.key"  // 假设CA私钥文件路径
	privateKey, err := LoadRSAPrivateKey(caKeyPath)
	if err != nil {
		// 如果找不到CA私钥，返回空签名（不影响主要功能）
		return "", fmt.Errorf("failed to load private key for connector %s: %w", connectorID, err)
	}

	// 对数据进行签名
	return SignData([]byte(data), privateKey)
}

// LoadRSAPrivateKey 从PEM文件加载RSA私钥
func LoadRSAPrivateKey(keyPath string) (*rsa.PrivateKey, error) {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key file: %w", err)
	}

	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// 尝试解析PKCS8格式
		key, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("failed to parse private key (PKCS1: %v, PKCS8: %v)", err, err2)
		}

		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private key is not RSA")
		}
		privateKey = rsaKey
	}

	return privateKey, nil
}

// LoadRSAPublicKey 从PEM文件加载RSA公钥
func LoadRSAPublicKey(keyPath string) (*rsa.PublicKey, error) {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read public key file: %w", err)
	}

	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	pubKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key: %w", err)
	}

	rsaPubKey, ok := pubKey.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not RSA")
	}

	return rsaPubKey, nil
}

// ExtractPublicKeyFromCert 从X509证书中提取RSA公钥
func ExtractPublicKeyFromCert(certPath string) (*rsa.PublicKey, error) {
	certData, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read certificate file: %w", err)
	}

	block, _ := pem.Decode(certData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	rsaPubKey, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("certificate public key is not RSA")
	}

	return rsaPubKey, nil
}


// VerifyEvidenceSignature 验证存证记录的签名
func VerifyEvidenceSignature(connectorID, eventType, channelID, dataHash, signature string, timestamp int64) error {
	// 构造原始签名数据
	data := fmt.Sprintf("%s|%s|%s|%s|%d", connectorID, eventType, channelID, dataHash, timestamp)

	// 从证书中提取公钥进行验证
	certPath := fmt.Sprintf("certs/%s.crt", connectorID)
	publicKey, err := ExtractPublicKeyFromCert(certPath)
	if err != nil {
		return fmt.Errorf("failed to load public key for connector %s: %w", connectorID, err)
	}

	return VerifySignature([]byte(data), signature, publicKey)
}
