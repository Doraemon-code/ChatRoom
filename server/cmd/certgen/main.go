// certgen creates a short-lived development certificate in the edge container's
// tmpfs. Production deployments should mount a certificate issued by a trusted
// internal CA instead.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"math/big"
	"net"
	"os"
	"strings"
	"time"
)

func main() {
	hostsArg := flag.String("hosts", "chat.local,localhost,127.0.0.1", "comma-separated certificate names and IPs")
	certPath := flag.String("cert", "/run/chat-tls/tls.crt", "certificate output path")
	keyPath := flag.String("key", "/run/chat-tls/tls.key", "private key output path")
	flag.Parse()

	hosts := splitHosts(*hostsArg)
	if len(hosts) == 0 {
		panic("at least one certificate host is required")
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	must(err)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(7 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	must(err)
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	must(err)
	must(os.WriteFile(*certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0600))
	must(os.WriteFile(*keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0600))
}

func splitHosts(value string) []string {
	var hosts []string
	for _, raw := range strings.Split(value, ",") {
		if host := strings.TrimSpace(raw); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
