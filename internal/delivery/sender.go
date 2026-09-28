package delivery

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hookrelay/internal/endpoints"
	"hookrelay/internal/ratelimit"
	"hookrelay/internal/signing"
	"hookrelay/internal/targetpolicy"
)

const maxResponseBytes = 4 * 1024

type HTTPSender struct {
	Client  *http.Client
	Secrets map[string][]byte
	Now     func() time.Time
	Profile targetpolicy.Profile
	Rate    *ratelimit.Limiter
}

func NewHTTPSender(secrets map[string][]byte, profile targetpolicy.Profile) HTTPSender {
	return NewHTTPSenderForNetwork(secrets, profile, false)
}

// NewHTTPSenderForNetwork keeps the persisted demo URL unchanged while routing
// its exact loopback address to the matching receiver service in Compose.
func NewHTTPSenderForNetwork(secrets map[string][]byte, profile targetpolicy.Profile, composeDemo bool) HTTPSender {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	guard := targetpolicy.Dialer{Dial: dialer.DialContext, AllowDemo: profile == targetpolicy.ProfileDemo, ComposeDemo: composeDemo}
	return HTTPSender{
		Client: &http.Client{
			Timeout: 4 * time.Second,
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           guard.DialContext,
				ResponseHeaderTimeout: 3 * time.Second,
				DisableKeepAlives:     true,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Secrets: secrets,
		Now:     time.Now,
		Profile: profile,
		Rate:    ratelimit.New(5, 2),
	}
}

func (s HTTPSender) Send(ctx context.Context, job Job) Result {
	failed := func(class string) Result { return Result{Status: "failed", FailureClass: class} }
	target, err := targetpolicy.ParseForProfile(job.EndpointURL, s.Profile)
	if err != nil {
		return failed("target_not_allowed")
	}
	if target.Demo {
		secretRef, _ := endpoints.DemoSecretRef(job.EndpointURL)
		if job.SecretRef != secretRef {
			return failed("target_not_allowed")
		}
	} else if strings.HasPrefix(job.SecretRef, "demo/") {
		return failed("target_not_allowed")
	}
	secret := s.Secrets[job.SecretRef]
	if len(secret) < 32 {
		return failed("secret_unavailable")
	}
	if s.Rate != nil {
		if err := s.Rate.Wait(ctx, job.EndpointID); err != nil {
			return failed("cancelled")
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, job.EndpointURL, bytes.NewReader(job.Payload))
	if err != nil {
		return failed("invalid_request")
	}
	timestamp := s.Now().Unix()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-HookRelay-Event-Id", job.EventID)
	request.Header.Set("X-HookRelay-Event-Type", job.EventType)
	request.Header.Set("X-HookRelay-Delivery-Id", job.ID)
	request.Header.Set("X-HookRelay-Attempt-Id", job.AttemptID)
	request.Header.Set("X-HookRelay-Key-Id", job.SecretRef)
	request.Header.Set("X-HookRelay-Timestamp", strconv.FormatInt(timestamp, 10))
	request.Header.Set("X-HookRelay-Signature", signing.Sign(secret, timestamp, job.SecretRef, job.EventID, job.EventType, job.ID, job.AttemptID, job.Payload))
	response, err := s.Client.Do(request)
	if err != nil {
		if errors.Is(err, targetpolicy.ErrUnsafeTarget) {
			return failed("target_not_allowed")
		}
		if errors.Is(err, context.Canceled) {
			return failed("cancelled")
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() || errors.Is(err, context.DeadlineExceeded) {
			return failed("timeout")
		}
		var certificateErr *tls.CertificateVerificationError
		if errors.As(err, &certificateErr) {
			return failed("tls_auth_error")
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return failed("dns_error")
		}
		return failed("transport_error")
	}
	defer response.Body.Close()
	status := response.StatusCode
	result := Result{Status: "failed", HTTPStatus: &status, RetryAfter: response.Header.Get("Retry-After")}
	n, err := io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		result.FailureClass = "response_read_error"
		return result
	}
	if n > maxResponseBytes {
		result.FailureClass = "response_too_large"
		return result
	}
	if status < 200 || status >= 300 {
		result.FailureClass = fmt.Sprintf("http_%d", status)
		return result
	}
	result.Status = "succeeded"
	return result
}
