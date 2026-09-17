package compute

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const maxTranscriptBytes = 64 << 10

type ManagedEndpointRegistration struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Port           int    `json:"port"`
	CredentialName string `json:"credentialName,omitempty"`
	SkillName      string `json:"skillName"`
	StartScript    string `json:"startScript"`
	StopScript     string `json:"stopScript"`
	LivePath       string `json:"livePath"`
}

func ApprovedManagedEndpointHash(registration ManagedEndpointRegistration) string {
	raw, _ := json.Marshal(registration)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type StopResult struct {
	Transcript string
	Duration   time.Duration
}

type StartResult struct {
	Transcript string
	Duration   time.Duration
}

// RunApprovedStart executes exactly the approved lifecycle bytes and then
// performs one bounded readiness loop against the registered loopback route.
// Failure is terminal for the stored endpoint until the user explicitly stops
// or re-registers it; callers must not wrap this in an unbounded retry loop.
func RunApprovedStart(ctx context.Context, registration ManagedEndpointRegistration, approvedHash string, timeout time.Duration) (StartResult, error) {
	if strings.TrimSpace(registration.Name) == "" || strings.TrimSpace(registration.StartScript) == "" || strings.TrimSpace(registration.LivePath) == "" {
		return StartResult{}, errors.New("managed endpoint start contract is incomplete")
	}
	if strings.ContainsRune(registration.StartScript, '\x00') || !strings.HasPrefix(registration.LivePath, "/") {
		return StartResult{}, errors.New("managed endpoint start contract is invalid")
	}
	if ApprovedManagedEndpointHash(registration) != strings.TrimPrefix(strings.TrimSpace(approvedHash), "sha256:") {
		return StartResult{}, errors.New("stored scripts no longer match the approved hash")
	}
	parsed, err := url.Parse(registration.URL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
		return StartResult{}, errors.New("managed endpoint start requires a literal loopback HTTP URL")
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	var transcript cappedBuffer
	command := exec.CommandContext(runCtx, "/bin/sh", "-c", registration.StartScript)
	command.Env = managedEndpointStopEnvironment(os.Environ())
	command.Stdout, command.Stderr = &transcript, &transcript
	if err := command.Run(); err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return StartResult{Transcript: transcript.String(), Duration: time.Since(started)}, fmt.Errorf("start script timed out after %s", timeout)
		}
		return StartResult{Transcript: transcript.String(), Duration: time.Since(started)}, fmt.Errorf("start script failed: %w", err)
	}
	probeURL := parsed.Scheme + "://" + parsed.Host + registration.LivePath
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 3 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	delay := 100 * time.Millisecond
	for {
		request, _ := http.NewRequestWithContext(runCtx, http.MethodGet, probeURL, nil)
		response, probeErr := client.Do(request)
		if probeErr == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return StartResult{Transcript: transcript.String(), Duration: time.Since(started)}, nil
			}
		}
		select {
		case <-runCtx.Done():
			return StartResult{Transcript: transcript.String(), Duration: time.Since(started)}, errors.New("managed endpoint readiness deadline expired")
		case <-time.After(delay):
			if delay < 2*time.Second {
				delay *= 2
				if delay > 2*time.Second {
					delay = 2 * time.Second
				}
			}
		}
	}
}

// RunApprovedStop executes only bytes covered by the stored approval hash.
// The caller owns durable state transitions before and after this function.
func RunApprovedStop(ctx context.Context, registration ManagedEndpointRegistration, approvedHash string, timeout time.Duration) (StopResult, error) {
	if strings.TrimSpace(registration.Name) == "" || strings.TrimSpace(registration.StopScript) == "" {
		return StopResult{}, errors.New("managed endpoint stop script is empty")
	}
	if strings.ContainsRune(registration.StopScript, '\x00') {
		return StopResult{}, errors.New("managed endpoint stop script contains NUL")
	}
	if ApprovedManagedEndpointHash(registration) != strings.TrimPrefix(strings.TrimSpace(approvedHash), "sha256:") {
		return StopResult{}, errors.New("stored scripts no longer match the approved hash")
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	command := exec.CommandContext(ctx, "/bin/sh", "-c", registration.StopScript)
	command.Env = managedEndpointStopEnvironment(os.Environ())
	var output cappedBuffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	result := StopResult{Transcript: output.String(), Duration: time.Since(started)}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result, fmt.Errorf("stop script timed out after %s", timeout)
	}
	if err != nil {
		return result, fmt.Errorf("stop script failed: %w", err)
	}
	return result, nil
}

func managedEndpointStopEnvironment(environment []string) []string {
	result := append([]string(nil), environment...)
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		index := -1
		value := ""
		for candidate, entry := range result {
			if strings.HasPrefix(entry, key+"=") {
				index = candidate
				value = strings.TrimPrefix(entry, key+"=")
				break
			}
		}
		seen := map[string]struct{}{}
		values := make([]string, 0, 8)
		for _, entry := range append(strings.Split(value, ","), "localhost", "127.0.0.1", "127.0.0.0/8", "::1") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			normalized := strings.ToLower(entry)
			if _, duplicate := seen[normalized]; duplicate {
				continue
			}
			seen[normalized] = struct{}{}
			values = append(values, entry)
		}
		assignment := key + "=" + strings.Join(values, ",")
		if index >= 0 {
			result[index] = assignment
		} else {
			result = append(result, assignment)
		}
	}
	return result
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := maxTranscriptBytes - b.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.Buffer.Write(value)
	}
	return original, nil
}
