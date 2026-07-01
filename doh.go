package douyinim

// Optional DNS-over-HTTPS resolver. Useful when the host's system DNS can't
// resolve Douyin CDN hostnames (e.g. behind a VPN that only tunnels traffic,
// not DNS). Enable with WithDoH(...). Resolution goes over HTTPS:443, so it
// works wherever plain UDP:53 to Chinese resolvers is blocked.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"
)

// DefaultDoHEndpoint is AliDNS's DoH JSON endpoint (returns China CDN IPs).
const DefaultDoHEndpoint = "https://223.5.5.5/resolve"

// dohResolver resolves A records over DNS-over-HTTPS, with a small TTL cache.
type dohResolver struct {
	endpoint string
	client   *http.Client

	mu    sync.Mutex
	cache map[string]dohEntry
}

type dohEntry struct {
	ips     []string
	expires time.Time
}

func newDoHResolver(endpoint string) *dohResolver {
	if endpoint == "" {
		endpoint = DefaultDoHEndpoint
	}
	return &dohResolver{
		endpoint: endpoint,
		// A bare http.Client (system DNS) is fine here: DoH endpoints are IP
		// literals (223.5.5.5 / 1.1.1.1), so no recursive DNS is needed.
		client: &http.Client{Timeout: 10 * time.Second},
		cache:  map[string]dohEntry{},
	}
}

// resolve returns A-record IPs for host, caching by the record TTL.
func (r *dohResolver) resolve(ctx context.Context, host string) ([]string, error) {
	r.mu.Lock()
	if e, ok := r.cache[host]; ok && time.Now().Before(e.expires) {
		ips := e.ips
		r.mu.Unlock()
		return ips, nil
	}
	r.mu.Unlock()

	url := fmt.Sprintf("%s?name=%s&type=A", r.endpoint, host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Answer []struct {
			Type int    `json:"type"`
			TTL  int    `json:"TTL"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var ips []string
	minTTL := 300
	for _, a := range out.Answer {
		if a.Type == 1 && net.ParseIP(a.Data) != nil { // type 1 = A record
			ips = append(ips, a.Data)
			if a.TTL > 0 && a.TTL < minTTL {
				minTTL = a.TTL
			}
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("DoH: no A records for %s", host)
	}
	r.mu.Lock()
	r.cache[host] = dohEntry{ips: ips, expires: time.Now().Add(time.Duration(minTTL) * time.Second)}
	r.mu.Unlock()
	return ips, nil
}

// dialContext is a net.Dialer-compatible dialer that resolves the host via DoH
// and connects to one of the returned IPs.
func (r *dohResolver) dialContext(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return base.DialContext(ctx, network, addr)
		}
		// IP literal: dial directly.
		if net.ParseIP(host) != nil {
			return base.DialContext(ctx, network, addr)
		}
		ips, err := r.resolve(ctx, host)
		if err != nil {
			// fall back to system DNS
			return base.DialContext(ctx, network, addr)
		}
		// try IPs in random order
		rand.Shuffle(len(ips), func(i, j int) { ips[i], ips[j] = ips[j], ips[i] })
		var lastErr error
		for _, ip := range ips {
			conn, err := base.DialContext(ctx, network, net.JoinHostPort(ip, port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// WithDoH makes the client resolve hostnames via DNS-over-HTTPS instead of the
// system resolver. Pass "" to use the default AliDNS endpoint, or a custom DoH
// JSON endpoint (e.g. "https://1.1.1.1/dns-query"). Use this when your network
// tunnels traffic but not DNS and Douyin CDN hosts won't resolve.
func WithDoH(endpoint string) Option {
	return func(c *Client) {
		resolver := newDoHResolver(endpoint)
		base := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		transport := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           resolver.dialContext(base),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: time.Second,
		}
		c.http = &http.Client{Timeout: c.http.Timeout, Transport: transport}
	}
}
