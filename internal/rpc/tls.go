package rpc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// GetCertFingerprint returns the sha256 fingerprint of a PEM-encoded x509 certificate.
// The return values corresponds with the fingerprint return value of GenCertificate.
func GetCertFingerprint(cert []byte) string {
	certHash := sha256.Sum256(cert)
	return hex.EncodeToString(certHash[:])
}

// GenCertificate generates a new TLS certificate or loads it from disk.
// The fingerprint text file will be regenerated if it's missing.
// The cert will be regenerated if it and/or the private key are invalid or missing.
func GenCertificate(dir string) (tls.Certificate, string /* fingerprint */, error) {
	dir = filepath.Join(dir, "tls")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return tls.Certificate{}, "", err
	}

	var (
		certFile        = filepath.Join(dir, "cert.pem")
		keyFile         = filepath.Join(dir, "cert-private-key.pem")
		fingerprintFile = filepath.Join(dir, "cert-fingerprint.txt")
	)

	certObj, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err == nil {
		certObj.Leaf, err = x509.ParseCertificate(certObj.Certificate[0])
		if err != nil {
			return certObj, "", err
		}

		if fingerprint, err := os.ReadFile(fingerprintFile); err == nil {
			return certObj, string(fingerprint), nil
		}

		fingerprint := GetCertFingerprint(certObj.Leaf.Raw)
		if err := os.WriteFile(fingerprintFile, []byte(fingerprint), 0644); err != nil {
			return certObj, "", fmt.Errorf("writing fingerprint: %w", err)
		}
		return certObj, fingerprint, nil
	}

	cert, key, err := genCert()
	if err != nil {
		return certObj, "", err
	}

	if err := os.WriteFile(certFile, cert, 0644); err != nil {
		return certObj, "", fmt.Errorf("writing cert: %w", err)
	}
	if err := os.WriteFile(keyFile, key, 0644); err != nil {
		return certObj, "", fmt.Errorf("writing key: %w", err)
	}

	certObj, err = tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return certObj, "", err
	}

	certObj.Leaf, err = x509.ParseCertificate(certObj.Certificate[0])
	if err != nil {
		return certObj, "", err
	}

	fingerprint := GetCertFingerprint(certObj.Leaf.Raw)
	if err := os.WriteFile(fingerprintFile, []byte(fingerprint), 0644); err != nil {
		return certObj, "", fmt.Errorf("writing fingerprint: %w", err)
	}

	return certObj, fingerprint, err
}

func genCert() ([]byte, []byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "recompose"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour * 24 * 3650),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}

	certPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPem := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return certPem, keyPem, nil
}
