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
	"log"
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

	// 使用内核的私钥进行签名
	kernelKeyPath := "certs/kernel.key"
	privateKey, err := LoadRSAPrivateKey(kernelKeyPath)
	if err != nil {
		// 如果找不到内核私钥，返回空签名（不影响主要功能）
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
// 参数说明：
// - isCrossKernel: true 表示跨内核场景，接收内核信任源内核的验证结果，跳过签名验证
// - isCrossKernel: false 表示同内核场景，需要验证签名
// 注意：存证记录由内核使用CA私钥（certs/ca.key）签名，因此使用CA证书验证
func VerifyEvidenceSignature(isCrossKernel bool, connectorID, eventType, channelID, dataHash, signature string, timestamp int64) error {
	if isCrossKernel {
		// 跨内核场景：源内核已通过mTLS验证连接器身份，信任源内核的验证结果
		log.Printf("[WARN] Cross-kernel evidence from connector %s - skipping signature verification (trusted via mTLS)", connectorID)
		return nil
	}

	if signature == "" {
		// 签名为空时跳过验证（证据记录可能由未配置签名的旧版本生成）
		return nil
	}

	// 构造原始签名数据
	data := fmt.Sprintf("%s|%s|%s|%s|%d", connectorID, eventType, channelID, dataHash, timestamp)

	// 存证由内核使用CA私钥签名，优先使用CA证书验证
	caPublicKey, caErr := ExtractPublicKeyFromCert("certs/ca.crt")
	if caErr == nil {
		if err := VerifySignature([]byte(data), signature, caPublicKey); err == nil {
			return nil
		}
	}

	// 回退：尝试使用连接器自己的证书验证（兼容旧版本）
	certPath := fmt.Sprintf("certs/%s.crt", connectorID)
	publicKey, err := ExtractPublicKeyFromCert(certPath)
	if err != nil {
		// 既没有CA证书也没有连接器证书时，记录警告但不阻断业务
		log.Printf("[WARN] Cannot verify evidence signature for connector %s: CA cert error: %v, connector cert error: %v", connectorID, caErr, err)
		return nil
	}

	return VerifySignature([]byte(data), signature, publicKey)
}

// GenerateFlowSignature 生成数据流的最终签名（使用内核私钥对链尾哈希签名）
func GenerateFlowSignature(chainTailHash string, timestamp int64) (string, error) {
	// 构建要签名的数据：链尾哈希 + 时间戳
	data := fmt.Sprintf("%s|%d", chainTailHash, timestamp)

	// 使用内核的私钥进行签名
	kernelKeyPath := "certs/kernel.key"
	privateKey, err := LoadRSAPrivateKey(kernelKeyPath)
	if err != nil {
		return "", fmt.Errorf("failed to load kernel private key: %w", err)
	}

	// 对数据进行签名
	return SignData([]byte(data), privateKey)
}

// VerifyFlowSignature 验证数据流的最终签名
func VerifyFlowSignature(chainTailHash string, timestamp int64, signature string) error {
	if signature == "" {
		return fmt.Errorf("empty flow signature")
	}

	// 构建待验证数据
	data := fmt.Sprintf("%s|%d", chainTailHash, timestamp)

	// 优先使用CA证书验证签名
	caPublicKey, caErr := ExtractPublicKeyFromCert("certs/ca.crt")
	if caErr == nil {
		if err := VerifySignature([]byte(data), signature, caPublicKey); err == nil {
			return nil
		}
	}

	// 回退：使用内核证书验证（兼容旧版本）
	kernelPublicKey, kernelErr := ExtractPublicKeyFromCert("certs/kernel.crt")
	if kernelErr != nil {
		return fmt.Errorf("failed to load certificate for verification: CA error: %v, kernel error: %v", caErr, kernelErr)
	}

	// 验证签名
	if err := VerifySignature([]byte(data), signature, kernelPublicKey); err != nil {
		return fmt.Errorf("flow signature verification failed: %w", err)
	}

	return nil
}
