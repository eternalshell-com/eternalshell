package domains

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	// requestTimeout limits one whole check: connect, TLS, headers and body.
	requestTimeout = 3 * time.Second

	// maxParallel is the number of hosts checked at the same time.
	maxParallel = 8

	// maxBody is how much of the response body is read. Only the headers and the
	// certificate matter; the body is read a little to see that the server answers.
	maxBody = 4 << 10

	userAgent = "EternalShell"

	maxLocation = 120
)

// CheckAll checks the hosts, at most maxParallel at a time, and returns the
// results by host. It only reads: one GET per host.
func CheckAll(ctx context.Context, hosts []string) map[string]Result {
	results := make(map[string]Result, len(hosts))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxParallel) // a full channel makes the next goroutine wait

	for _, h := range hosts {
		wg.Add(1)
		sem <- struct{}{}
		go func(host string) {
			defer wg.Done()
			defer func() { <-sem }()
			r := Check(ctx, host)
			mu.Lock()
			results[host] = r
			mu.Unlock()
		}(h)
	}
	wg.Wait()
	return results
}

// Check sends one GET to https://<host>/ and reports status, redirect target
// and certificate. It connects without verifying the certificate, so even a
// broken one gives a result, and then verifies the chain and the host name
// itself against the system roots.
func Check(ctx context.Context, host string) Result {
	res := Result{Host: host, CheckedAt: time.Now()}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	// The TLS state is kept by the dialer, because the connection can be
	// established even when the HTTP request fails afterwards.
	var state tls.ConnectionState
	var haveState bool

	transport := &http.Transport{
		DisableKeepAlives: true,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &tls.Dialer{
				NetDialer: &net.Dialer{Timeout: requestTimeout},
				Config: &tls.Config{
					ServerName:         host,
					InsecureSkipVerify: true, // verified below, so a bad certificate still gives a result
					NextProtos:         []string{"http/1.1"},
					MinVersion:         tls.VersionTLS12,
				},
			}
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			state, haveState = conn.(*tls.Conn).ConnectionState(), true
			return conn, nil
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
		// A redirect is a result, not something to follow.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/", nil)
	if err != nil {
		res.Err = "error"
		return res
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err == nil {
		res.Status = resp.StatusCode
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			res.Location = cleanText(resp.Header.Get("Location"), maxLocation)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		resp.Body.Close()
	} else {
		res.Err = failureReason(ctx, err)
	}

	if haveState && len(state.PeerCertificates) > 0 {
		res.HasCert = true
		leaf := state.PeerCertificates[0]
		res.NotAfter = leaf.NotAfter
		if err := verifyCert(state.PeerCertificates, host, time.Now()); err != nil {
			res.CertErr = certReason(err)
		}
	}
	return res
}

// failureReason names why the request failed; a deadline of our own context
// counts as a timeout.
func failureReason(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return connReason(err)
}

// verifyCert checks the chain against the system roots and the host name.
func verifyCert(chain []*x509.Certificate, host string, now time.Time) error {
	intermediates := x509.NewCertPool()
	for _, c := range chain[1:] {
		intermediates.AddCert(c)
	}
	_, err := chain[0].Verify(x509.VerifyOptions{
		DNSName:       host,
		Intermediates: intermediates,
		CurrentTime:   now,
	})
	return err
}
