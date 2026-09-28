package targetpolicy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

var ErrUnsafeTarget = errors.New("target is not an allowed public HTTPS destination")

var blockedIPv4 = parsePrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.168.0.0/16", "192.88.99.0/24", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
)

var blockedIPv6 = parsePrefixes(
	"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
)

var publicIPv6 = netip.MustParsePrefix("2000::/3")

func parsePrefixes(values ...string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}

type Target struct {
	Host string
	Port string
	Demo bool
}

type Profile string

const (
	ProfilePublic Profile = "public"
	ProfileDemo   Profile = "demo"
)

func ParseForProfile(raw string, profile Profile) (Target, error) {
	target, err := Parse(raw)
	if err != nil || target.Demo && profile != ProfileDemo {
		return Target{}, ErrUnsafeTarget
	}
	return target, nil
}

// Parse rejects ambiguous URL syntax before storage and again before sending.
// The two exact demo URLs are the only plain HTTP exception.
func Parse(raw string) (Target, error) {
	if raw == "http://127.0.0.1:18080/hook" || raw == "http://127.0.0.1:18081/hook" {
		parsed, _ := url.Parse(raw)
		return Target{Host: parsed.Hostname(), Port: parsed.Port(), Demo: true}, nil
	}
	if len(raw) == 0 || len(raw) > 1024 || strings.ContainsAny(raw, "\\\r\n\t #") {
		return Target{}, ErrUnsafeTarget
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.RawQuery != "" || parsed.ForceQuery {
		return Target{}, ErrUnsafeTarget
	}
	host := parsed.Hostname()
	if host == "" || strings.Contains(host, "%") {
		return Target{}, ErrUnsafeTarget
	}
	port := parsed.Port()
	if port != "" && port != "443" {
		return Target{}, ErrUnsafeTarget
	}
	if port == "" {
		port = "443"
	}
	if address, err := netip.ParseAddr(host); err == nil {
		if !IsPublic(address) {
			return Target{}, ErrUnsafeTarget
		}
	} else if !validDNSName(host) {
		return Target{}, ErrUnsafeTarget
	}
	return Target{Host: host, Port: port}, nil
}

func validDNSName(host string) bool {
	if len(host) > 253 || !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func IsPublic(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	if address.Is4() {
		for _, prefix := range blockedIPv4 {
			if prefix.Contains(address) {
				return false
			}
		}
		return true
	}
	if !publicIPv6.Contains(address) {
		return false
	}
	for _, prefix := range blockedIPv6 {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type Dialer struct {
	Resolver  Resolver
	Dial      func(ctx context.Context, network, address string) (net.Conn, error)
	AllowDemo bool
	// ComposeDemo maps only the two exact demo loopback addresses to fixed
	// service names. It must be enabled only for the isolated local demo stack.
	ComposeDemo bool
}

// DialContext validates every A/AAAA answer, then connects to a checked
// numeric IP. The HTTP transport keeps the original hostname for TLS checks.
func (d Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrUnsafeTarget
	}
	if d.AllowDemo && host == "127.0.0.1" && (port == "18080" || port == "18081") {
		if d.ComposeDemo {
			if port == "18080" {
				return d.dial(ctx, network, "demo-a:18080")
			}
			return d.dial(ctx, network, "demo-b:18081")
		}
		return d.dial(ctx, network, address)
	}
	if port != "443" {
		return nil, ErrUnsafeTarget
	}
	if parsed, err := netip.ParseAddr(host); err == nil {
		if !IsPublic(parsed) {
			return nil, ErrUnsafeTarget
		}
		return d.dial(ctx, network, net.JoinHostPort(parsed.Unmap().String(), port))
	}
	if !validDNSName(host) {
		return nil, ErrUnsafeTarget
	}
	resolver := d.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve target: %w", err)
	}
	if len(addresses) == 0 {
		return nil, ErrUnsafeTarget
	}
	for _, resolved := range addresses {
		address, ok := netip.AddrFromSlice(resolved.IP)
		if !ok || resolved.Zone != "" || !IsPublic(address) {
			return nil, ErrUnsafeTarget
		}
	}
	chosen, _ := netip.AddrFromSlice(addresses[0].IP)
	return d.dial(ctx, network, net.JoinHostPort(chosen.Unmap().String(), port))
}

func (d Dialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if d.Dial != nil {
		return d.Dial(ctx, network, address)
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}
