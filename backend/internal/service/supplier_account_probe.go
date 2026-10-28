package service

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// SupplierProbeStore reserves a distributed rate limit and opens the owner's credential.
type SupplierProbeStore interface {
	ReserveProbe(context.Context, int64, int64) (string, error)
}
type SupplierProbeResult struct {
	Status string `json:"status"`
}

func (s *SupplierAccountService) Probe(ctx context.Context, user, id int64) (*SupplierProbeResult, error) {
	if err := s.authorize(ctx, user, true); err != nil {
		return nil, err
	}
	if s.concurrency == nil {
		return nil, apperrors.Conflict("probe_unavailable", "Probe unavailable")
	}
	a, err := s.store.Get(ctx, user, id)
	if err != nil {
		return nil, err
	}
	p, ok := s.provider(a.Platform)
	if !ok || p.ProxyID != nil || a.Type != "apikey" || a.CredentialStatus != "configured" {
		return nil, apperrors.Conflict("probe_unavailable", "Probe unavailable")
	}
	slot, err := s.concurrency.AcquireAccountSlot(ctx, id, a.Concurrency)
	if err != nil {
		return nil, err
	}
	if slot == nil || !slot.Acquired {
		return nil, apperrors.Conflict("account_busy", "Account concurrency exhausted")
	}
	defer slot.ReleaseFunc()
	store, ok := s.store.(SupplierProbeStore)
	if !ok {
		return nil, apperrors.Conflict("probe_unavailable", "Probe unavailable")
	}
	secret, err := store.ReserveProbe(ctx, user, id)
	if err != nil {
		return nil, err
	}
	if err = probeOpenAIModels(ctx, p.BaseURL, secret); err != nil {
		return nil, apperrors.Conflict("probe_failed", "Provider health check failed")
	}
	return &SupplierProbeResult{Status: "ok"}, nil
}

// probeOpenAIModels issues only a fixed, non-billable models-list request. Its dialer
// pins a public DNS answer and redirects and environment proxies are disabled.
func probeOpenAIModels(ctx context.Context, baseURL, secret string) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid provider")
	}
	hostname := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/models"
	u.RawPath = ""
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostname}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, requestedPort, e := net.SplitHostPort(address)
		if e != nil || !strings.EqualFold(host, hostname) || requestedPort != port {
			return nil, errors.New("invalid destination")
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, hostname)
		if e != nil || len(ips) == 0 {
			return nil, errors.New("unresolved destination")
		}
		for _, candidate := range ips {
			ip := candidate.IP
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				return nil, errors.New("unsafe destination")
			}
		}
		var last error
		for _, candidate := range ips {
			conn, e := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, fmt.Errorf("connection failed: %w", last)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("provider rejected health check")
	}
	return nil
}
