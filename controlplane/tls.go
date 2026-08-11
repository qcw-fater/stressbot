// Package controlplane 提供 Admin-Agent 内部控制面的共享安全能力。
package controlplane

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// TLSConfig 使用本端证书和独立的对端 CA 建立角色分离的双向 TLS。
type TLSConfig struct {
	CertFile   string `json:"certFile"`
	KeyFile    string `json:"keyFile"`
	PeerCAFile string `json:"peerCaFile"`
}

// Enabled 表示配置中是否声明了 TLS。全部为空时用于兼容迁移期 HTTP。
func (c TLSConfig) Enabled() bool {
	return c.CertFile != "" || c.KeyFile != "" || c.PeerCAFile != ""
}

// Server 构造要求并校验客户端证书的 TLS 1.3 配置。
func (c TLSConfig) Server() (*tls.Config, error) {
	certificate, peerPool, err := c.load()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    peerPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}

// Client 构造携带本端证书并校验服务端证书的 TLS 1.3 配置。
func (c TLSConfig) Client() (*tls.Config, error) {
	certificate, peerPool, err := c.load()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      peerPool,
	}, nil
}

func (c TLSConfig) load() (tls.Certificate, *x509.CertPool, error) {
	if c.CertFile == "" {
		return tls.Certificate{}, nil, fmt.Errorf("控制面 TLS 缺少 certFile")
	}
	if c.KeyFile == "" {
		return tls.Certificate{}, nil, fmt.Errorf("控制面 TLS 缺少 keyFile")
	}
	if c.PeerCAFile == "" {
		return tls.Certificate{}, nil, fmt.Errorf("控制面 TLS 缺少 peerCaFile")
	}

	certificate, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("加载控制面证书失败: %w", err)
	}
	peerPEM, err := os.ReadFile(c.PeerCAFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("读取控制面对端 CA 失败: %w", err)
	}
	peerPool := x509.NewCertPool()
	if !peerPool.AppendCertsFromPEM(peerPEM) {
		return tls.Certificate{}, nil, fmt.Errorf("控制面对端 CA 不包含有效证书")
	}
	return certificate, peerPool, nil
}
