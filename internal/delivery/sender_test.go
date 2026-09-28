package delivery

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"hookrelay/internal/signing"
	"hookrelay/internal/targetpolicy"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func demoJob() Job {
	return Job{
		ID:        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		AttemptID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		EventID:   "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		EventType: "order.created", Payload: []byte(`{"order_id":"demo-1"}`),
		EndpointID:  "11111111-1111-4111-8111-111111111111",
		EndpointURL: "http://127.0.0.1:18080/hook", SecretRef: "demo/a-v1",
	}
}

func TestSenderSignsExactBodyAndAcceptsVerifiedSuccess(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	now := time.Unix(1789992000, 0)
	sender := NewHTTPSender(map[string][]byte{"demo/a-v1": secret}, targetpolicy.ProfileDemo)
	sender.Now = func() time.Time { return now }
	sender.Client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != string(demoJob().Payload) {
			t.Fatalf("body changed: %s", body)
		}
		if !signing.Verify(secret, now, request.Header.Get("X-HookRelay-Timestamp"),
			request.Header.Get("X-HookRelay-Key-Id"), request.Header.Get("X-HookRelay-Event-Id"),
			request.Header.Get("X-HookRelay-Event-Type"), request.Header.Get("X-HookRelay-Delivery-Id"), request.Header.Get("X-HookRelay-Attempt-Id"),
			body, request.Header.Get("X-HookRelay-Signature")) {
			t.Fatal("request signature did not verify")
		}
		if request.Header.Get("X-HookRelay-Key-Id") != "demo/a-v1" {
			t.Fatal("wrong key ID")
		}
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	result := sender.Send(context.Background(), demoJob())
	if result.Status != "succeeded" || result.HTTPStatus == nil || *result.HTTPStatus != 204 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestSenderNeverConnectsToUnapprovedTarget(t *testing.T) {
	sender := NewHTTPSender(map[string][]byte{"demo/a-v1": []byte(strings.Repeat("s", 32))}, targetpolicy.ProfileDemo)
	sender.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unapproved target was dialed"); return nil, nil })
	job := demoJob()
	job.EndpointURL = "http://127.0.0.1:5432/"
	if result := sender.Send(context.Background(), job); result.FailureClass != "target_not_allowed" {
		t.Fatalf("unexpected result: %+v", result)
	}
	job.EndpointURL = "https://hooks.example.com/hook"
	if result := sender.Send(context.Background(), job); result.FailureClass != "target_not_allowed" {
		t.Fatalf("demo key reused for external target: %+v", result)
	}
	job.SecretRef = "external/example-v1"
	if result := sender.Send(context.Background(), job); result.FailureClass != "secret_unavailable" {
		t.Fatalf("missing external key should fail before network: %+v", result)
	}
}

func TestSenderBoundsResponseAndDoesNotFollowRedirect(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	for _, tc := range []struct {
		name           string
		status         int
		body, location string
		want           string
	}{
		{"large success response", 200, strings.Repeat("x", maxResponseBytes+1), "", "response_too_large"},
		{"redirect", 302, "", "http://127.0.0.1:5432/", "http_302"},
		{"server error", 500, "no", "", "http_500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			sender := NewHTTPSender(map[string][]byte{"demo/a-v1": secret}, targetpolicy.ProfileDemo)
			sender.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{"Location": []string{tc.location}}}, nil
			})
			result := sender.Send(context.Background(), demoJob())
			if result.FailureClass != tc.want || calls != 1 {
				t.Fatalf("result %+v, requests %d", result, calls)
			}
		})
	}
}

func TestSenderTimesOutSlowReceiver(t *testing.T) {
	sender := NewHTTPSender(map[string][]byte{"demo/a-v1": []byte(strings.Repeat("s", 32))}, targetpolicy.ProfileDemo)
	sender.Client.Timeout = 20 * time.Millisecond
	sender.Client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	result := sender.Send(context.Background(), demoJob())
	if result.Status != "failed" || result.FailureClass != "timeout" {
		t.Fatalf("slow receiver result: %+v", result)
	}
}

func TestSenderPreservesRetryAfterAndClassifiesDNS(t *testing.T) {
	sender := NewHTTPSender(map[string][]byte{"demo/a-v1": []byte(strings.Repeat("s", 32))}, targetpolicy.ProfileDemo)
	sender.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader("busy")),
			Header:     http.Header{"Retry-After": []string{"10"}},
		}, nil
	})
	result := sender.Send(context.Background(), demoJob())
	if result.FailureClass != "http_429" || result.RetryAfter != "10" {
		t.Fatalf("rate limit result: %+v", result)
	}
	sender.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &net.DNSError{Err: "temporary DNS failure", Name: "example.test", IsTemporary: true}
	})
	result = sender.Send(context.Background(), demoJob())
	if result.FailureClass != "dns_error" {
		t.Fatalf("DNS result: %+v", result)
	}
	sender.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &tls.CertificateVerificationError{Err: errors.New("invalid certificate")}
	})
	result = sender.Send(context.Background(), demoJob())
	if result.FailureClass != "tls_auth_error" {
		t.Fatalf("TLS authentication result: %+v", result)
	}
}

func TestExternalRotationSignsWithEachSnapshotKey(t *testing.T) {
	oldKey := []byte(strings.Repeat("o", 32))
	newKey := []byte(strings.Repeat("n", 32))
	keys := map[string][]byte{"external-shop-v1": oldKey, "external-shop-v2": newKey}
	sender := NewHTTPSender(keys, targetpolicy.ProfilePublic)
	sender.Now = func() time.Time { return time.Unix(1789992000, 0) }
	seen := map[string]bool{}
	sender.Client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		keyID := request.Header.Get("X-HookRelay-Key-Id")
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !signing.Verify(keys[keyID], sender.Now(), request.Header.Get("X-HookRelay-Timestamp"),
			keyID, request.Header.Get("X-HookRelay-Event-Id"), request.Header.Get("X-HookRelay-Event-Type"),
			request.Header.Get("X-HookRelay-Delivery-Id"), request.Header.Get("X-HookRelay-Attempt-Id"),
			body, request.Header.Get("X-HookRelay-Signature")) {
			t.Fatalf("signature failed for %s", keyID)
		}
		seen[keyID] = true
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	for keyID := range keys {
		job := demoJob()
		job.EndpointURL = "https://hooks.example.com/hook"
		job.SecretRef = keyID
		if result := sender.Send(context.Background(), job); result.Status != "succeeded" {
			t.Fatalf("%s: %+v", keyID, result)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("rotation did not use both keys: %+v", seen)
	}
}
