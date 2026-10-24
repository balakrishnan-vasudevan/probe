// probe sends the same HTTP request many times and reports how long each
// phase took (DNS, TCP connect, TLS, waiting for the server, download),
// as averages, percentiles, and a histogram.
//
//	go run . -n 200 https://bala-krishnan.com/
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"time"
)

// Sample is one timed request. Phases are separate, not cumulative:
// Total is roughly the sum of the others.
type Sample struct {
	Start    time.Time
	DNS      time.Duration // name lookup
	Connect  time.Duration // TCP handshake
	TLS      time.Duration // TLS handshake
	Wait     time.Duration // request sent → first response byte (server time + 1 round trip)
	Transfer time.Duration // first byte → last byte
	Total    time.Duration
	Reused   bool   // connection came from the keep-alive pool
	Status   int    // HTTP status code
	Cache    string // cf-cache-status header (Cloudflare's cache result)
	Err      error
}

func main() {
	n := flag.Int("n", 100, "number of requests")
	interval := flag.Duration("interval", 200*time.Millisecond, "pause between requests")
	reuse := flag.Bool("reuse", false, "reuse connections (keep-alive); default opens a fresh connection every time")
	timeout := flag.Duration("timeout", 10*time.Second, "per-request timeout")
	csvPath := flag.String("csv", "", "also write every sample to this CSV file")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: probe [flags] URL\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	url := flag.Arg(0)

	client := &http.Client{
		Timeout: *timeout,
		Transport: &http.Transport{
			// With keep-alives off, every request pays for DNS, TCP and TLS
			// again: the "first visit" experience. With them on, only the
			// first request does: the "already browsing the site" experience.
			DisableKeepAlives: !*reuse,
			TLSClientConfig:   &tls.Config{},
		},
	}

	mode := "fresh connection per request"
	if *reuse {
		mode = "reusing connections"
	}
	fmt.Printf("probing %s  ×%d  (%s, %s apart)\n", url, *n, mode, *interval)

	// Only draw the live counter in a real terminal; in a pipe or a CI log
	// the carriage returns would turn into one long line of numbers.
	fi, _ := os.Stdout.Stat()
	interactive := fi != nil && fi.Mode()&os.ModeCharDevice != 0

	samples := make([]Sample, 0, *n)
	for i := 0; i < *n; i++ {
		s := measure(client, url)
		samples = append(samples, s)
		if s.Err != nil {
			fmt.Fprintf(os.Stderr, "\nrequest %d failed: %v\n", i+1, s.Err)
		}
		if interactive {
			fmt.Printf("\r%d/%d", i+1, *n)
		}
		if i < *n-1 {
			time.Sleep(*interval)
		}
	}
	if interactive {
		fmt.Println()
	}
	fmt.Println()

	report(os.Stdout, samples)

	if *csvPath != "" {
		if err := writeCSV(*csvPath, samples); err != nil {
			fmt.Fprintf(os.Stderr, "writing CSV: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nraw samples written to %s\n", *csvPath)
	}
}

// measure makes one GET request and times each phase using httptrace,
// which lets us hook into the moments inside net/http where each phase
// starts and ends.
func measure(client *http.Client, url string) Sample {
	var (
		s                                     Sample
		dnsStart, connStart, tlsStart, wrote  time.Time
		dnsDone, connDone, tlsDone, firstByte time.Time
	)

	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:  func(httptrace.DNSDoneInfo) { dnsDone = time.Now() },
		// Go may race IPv4 and IPv6 connections ("happy eyeballs"), so
		// ConnectStart can fire more than once. Keep the first start and
		// the first successful finish.
		ConnectStart: func(_, _ string) {
			if connStart.IsZero() {
				connStart = time.Now()
			}
		},
		ConnectDone: func(_, _ string, err error) {
			if err == nil && connDone.IsZero() {
				connDone = time.Now()
			}
		},
		TLSHandshakeStart:    func() { tlsStart = time.Now() },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { tlsDone = time.Now() },
		GotConn:              func(info httptrace.GotConnInfo) { s.Reused = info.Reused },
		WroteRequest:         func(httptrace.WroteRequestInfo) { wrote = time.Now() },
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		s.Err = err
		return s
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	s.Start = time.Now()
	resp, err := client.Do(req)
	if err != nil {
		s.Err = err
		return s
	}
	// Read the whole body: the response isn't "done" until the last byte
	// arrives, and an unread body would also stop the connection being reused.
	_, err = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	end := time.Now()
	if err != nil {
		s.Err = err
		return s
	}

	s.Status = resp.StatusCode
	s.Cache = resp.Header.Get("cf-cache-status")
	s.DNS = between(dnsStart, dnsDone)
	s.Connect = between(connStart, connDone)
	s.TLS = between(tlsStart, tlsDone)
	s.Wait = between(wrote, firstByte)
	s.Transfer = between(firstByte, end)
	s.Total = end.Sub(s.Start)
	return s
}

// between returns b-a, or 0 if the phase never happened (for example, no
// DNS or TLS on a reused connection).
func between(a, b time.Time) time.Duration {
	if a.IsZero() || b.IsZero() {
		return 0
	}
	return b.Sub(a)
}
