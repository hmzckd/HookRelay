package targetpolicy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestURLPolicy(t *testing.T) {
	for _, allowed := range []string{
		"http://127.0.0.1:18080/hook",
		"http://127.0.0.1:18081/hook",
		"https://hooks.example.com/path",
		"https://hooks.example.com:443/path",
		"https://8.8.8.8/hook",
		"https://[2606:4700:4700::1111]/hook",
	} {
		if _, err := Parse(allowed); err != nil {
			t.Errorf("safe URL %q rejected: %v", allowed, err)
		}
	}
	for _, denied := range []string{
		"http://127.0.0.1:18080/hook?bypass=1",
		"http://example.com/hook", "https://localhost/hook",
		"https://127.0.0.1/hook", "https://10.0.0.1/hook",
		"https://169.254.169.254/latest/meta-data/",
		"https://[::1]/hook", "https://[::ffff:127.0.0.1]/hook",
		"https://[fc00::1]/hook", "https://[fe80::1]/hook",
		"https://example.com:5432/hook", "https://example.com:8443/hook",
		"https://user:pass@example.com/hook", "https://example.com/#fragment",
		"https://example.com/hook?key=secret",
		"https://example.com\\@evil.com/hook", "https://example.com./hook",
		"https://example.com/hook\nHost: 127.0.0.1", "file:///etc/passwd",
	} {
		if _, err := Parse(denied); !errors.Is(err, ErrUnsafeTarget) {
			t.Errorf("unsafe URL %q accepted: %v", denied, err)
		}
	}
}

func TestAddressPolicyRejectsLocalSpecialAndTunnels(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.0", "10.1.2.3", "100.64.0.1", "127.0.0.1",
		"169.254.169.254", "172.16.0.1", "192.0.0.1", "192.168.0.1",
		"198.18.0.1", "198.51.100.1", "203.0.113.1",
		"::1", "::ffff:127.0.0.1", "::ffff:169.254.169.254",
		"fc00::1", "fe80::1", "2001:db8::1", "2002:c0a8:0101::1",
	} {
		if IsPublic(netip.MustParseAddr(raw)) {
			t.Errorf("special address %s accepted", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		if !IsPublic(netip.MustParseAddr(raw)) {
			t.Errorf("public address %s rejected", raw)
		}
	}
}

type fakeResolver struct{ answers []net.IPAddr }

func (r *fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.answers, nil
}

func TestDialPinsPublicDNSAnswerAndRejectsMixedOrReboundAnswer(t *testing.T) {
	resolver := &fakeResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	var dialed []string
	dialer := Dialer{Resolver: resolver, Dial: func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		local, remote := net.Pipe()
		remote.Close()
		return local, nil
	}}
	conn, err := dialer.DialContext(context.Background(), "tcp", "hooks.example.com:443")
	if err != nil || len(dialed) != 1 || dialed[0] != "8.8.8.8:443" {
		t.Fatalf("pinned dial: %v %v", dialed, err)
	}
	conn.Close()
	resolver.answers = []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("10.0.0.1")}}
	if _, err := dialer.DialContext(context.Background(), "tcp", "hooks.example.com:443"); !errors.Is(err, ErrUnsafeTarget) || len(dialed) != 1 {
		t.Fatalf("mixed DNS answer dialed: %v %v", dialed, err)
	}
	resolver.answers = []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}
	if _, err := dialer.DialContext(context.Background(), "tcp", "hooks.example.com:443"); !errors.Is(err, ErrUnsafeTarget) || len(dialed) != 1 {
		t.Fatalf("rebound DNS answer dialed: %v %v", dialed, err)
	}
	resolver.answers = []net.IPAddr{{IP: net.ParseIP("2606:4700:4700::1111")}}
	conn, err = dialer.DialContext(context.Background(), "tcp", "hooks.example.com:443")
	if err != nil || len(dialed) != 2 || !strings.Contains(dialed[1], "[2606:4700:4700::1111]:443") {
		t.Fatalf("IPv6 dial: %v %v", dialed, err)
	}
	conn.Close()
}

func TestDialRejectsForbiddenPortsAndDirectPrivateAddresses(t *testing.T) {
	dialer := Dialer{Dial: func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("forbidden target was dialed")
		return nil, nil
	}}
	for _, address := range []string{"127.0.0.1:443", "10.0.0.1:443", "169.254.169.254:443", "[::1]:443", "[::ffff:127.0.0.1]:443", "example.com:5432"} {
		if _, err := dialer.DialContext(context.Background(), "tcp", address); !errors.Is(err, ErrUnsafeTarget) {
			t.Errorf("unsafe dial %q returned %v", address, err)
		}
	}
}

func TestDemoExceptionRequiresExplicitProfile(t *testing.T) {
	if _, err := ParseForProfile("http://127.0.0.1:18080/hook", ProfilePublic); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("public profile accepted demo URL: %v", err)
	}
	if _, err := ParseForProfile("http://127.0.0.1:18080/hook", ProfileDemo); err != nil {
		t.Fatalf("demo profile rejected its exact URL: %v", err)
	}
	dialer := Dialer{Dial: func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("default dialer connected to local demo")
		return nil, nil
	}}
	if _, err := dialer.DialContext(context.Background(), "tcp", "127.0.0.1:18080"); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("default dialer accepted demo address: %v", err)
	}
}

func TestComposeDemoMapsOnlyExactDemoAddresses(t *testing.T) {
	var dialed []string
	dialer := Dialer{AllowDemo: true, ComposeDemo: true, Dial: func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		local, remote := net.Pipe()
		remote.Close()
		return local, nil
	}}
	for _, address := range []string{"127.0.0.1:18080", "127.0.0.1:18081"} {
		conn, err := dialer.DialContext(context.Background(), "tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
	if len(dialed) != 2 || dialed[0] != "demo-a:18080" || dialed[1] != "demo-b:18081" {
		t.Fatalf("unexpected demo routing: %v", dialed)
	}
	for _, address := range []string{"127.0.0.1:18082", "127.0.0.2:18080", "demo-a:18080"} {
		if _, err := dialer.DialContext(context.Background(), "tcp", address); !errors.Is(err, ErrUnsafeTarget) {
			t.Fatalf("unlisted demo address %q accepted: %v", address, err)
		}
	}
	if len(dialed) != 2 {
		t.Fatalf("unlisted address was dialed: %v", dialed)
	}
}
