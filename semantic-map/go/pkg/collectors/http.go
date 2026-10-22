package collectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/DiyazY/di-agent/pkg/spec"
	"github.com/DiyazY/di-agent/pkg/types"
)

// maxHTTPBody bounds one response body, the same 4 MiB an exec reply line gets. A
// sidecar that sends more is faulted rather than allowed to grow the agent's
// memory without limit.
const maxHTTPBody = 4 << 20

// HTTPCollector reaches a sidecar plugin speaking wire protocol v1 as POST requests
// to one URL, and presents it as a CollectorContract.
//
// There is no connection to lose, so a fault is only remembered: after any
// failure the next Collect sends hello before collect, which is what a restarted
// sidecar needs to hear. Spacing the retries is the supervisor's backoff.
type HTTPCollector struct {
	env     Env
	name    string
	sp      spec.HTTPSpec
	timeout time.Duration
	config  map[string]any
	// client has no client-level timeout; each request is bounded by the context
	// deadline of the Collect or Start that makes it.
	client *http.Client

	// mu serialises Start, Collect and Close, which own seq and needHello.
	mu        sync.Mutex
	needHello bool
	closed    bool
	seq       uint64

	availMu   sync.Mutex
	available []types.MetricType
}

// NewHTTPCollector prepares the sidecar entry name; nothing is sent until Start. A
// non-positive timeout takes the default, min(interval/2, 5s).
func NewHTTPCollector(env Env, name string, sp spec.HTTPSpec, timeout time.Duration, config map[string]any) *HTTPCollector {
	if config == nil {
		config = map[string]any{}
	}
	return &HTTPCollector{env: env, name: name, sp: sp, timeout: resolveTimeout(env, timeout),
		config: config, client: &http.Client{}, needHello: true}
}

// Start sends hello. An error means the sidecar was unreachable or refused it; the
// daemon refuses to start on it.
func (h *HTTPCollector) Start() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("collector closed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()
	return h.hello(ctx)
}

// Collect sends one collect request and returns the stamped samples. After an
// earlier fault it sends hello first; a failing hello is this tick's fault. The
// hello and the collect share one deadline of the configured timeout, because the
// supervisor waits only a short grace beyond it.
func (h *HTTPCollector) Collect() ([]*types.MetricSample, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errors.New("collector closed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()
	if h.needHello {
		if err := h.hello(ctx); err != nil {
			return nil, err
		}
	}
	h.seq++
	seq := h.seq
	body, err := h.post(ctx, CollectRequest{Op: "collect", Seq: seq, NowUnix: time.Now().Unix()})
	if err != nil {
		return nil, h.fault(err)
	}
	var r CollectReply
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, h.fault(fmt.Errorf("collect reply is not JSON: %w", err))
	}
	if err := checkCollect(&r, seq); err != nil {
		return nil, h.fault(err)
	}
	samples, avail := stampReply(h.name, h.env.NodeID, &r)
	h.availMu.Lock()
	h.available = avail
	h.availMu.Unlock()
	return samples, nil
}

// SourceID is the entry name, so it is stable across reconnects.
func (h *HTTPCollector) SourceID() string { return h.name }

// AvailableMetrics is the last successful reply's available list, or the types
// that reply carried.
func (h *HTTPCollector) AvailableMetrics() []types.MetricType {
	h.availMu.Lock()
	defer h.availMu.Unlock()
	return append([]types.MetricType(nil), h.available...)
}

// Close has nothing to release: every request opens and ends within its call.
func (h *HTTPCollector) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.client.CloseIdleConnections()
	return nil
}

// hello performs the handshake and clears needHello on success. Called with mu held.
func (h *HTTPCollector) hello(ctx context.Context) error {
	req := Hello{Op: "hello", Protocol: ProtocolVersion, Name: h.name, NodeID: h.env.NodeID,
		IntervalS: int(h.env.Interval / time.Second), Config: h.config}
	body, err := h.post(ctx, req)
	if err != nil {
		return h.fault(fmt.Errorf("hello: %w", err))
	}
	var r HelloReply
	if err := json.Unmarshal(body, &r); err != nil {
		return h.fault(fmt.Errorf("hello reply is not JSON: %w", err))
	}
	if err := checkHello(r); err != nil {
		return h.fault(err)
	}
	h.needHello = false
	return nil
}

// fault records that the next contact must say hello again, and returns err.
func (h *HTTPCollector) fault(err error) error {
	h.needHello = true
	return err
}

// post sends one request and returns the response body: a non-2xx status, a
// transport error, a timeout or a body over maxHTTPBody is an error.
func (h *HTTPCollector) post(ctx context.Context, req any) ([]byte, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, h.sp.URL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("sidecar answered %s", resp.Status)
	}
	// One byte past the cap tells an oversized body from one that is exactly full.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody+1))
	if err != nil {
		return nil, fmt.Errorf("read reply: %w", err)
	}
	if len(body) > maxHTTPBody {
		return nil, fmt.Errorf("reply exceeds %d MiB", maxHTTPBody>>20)
	}
	return body, nil
}
