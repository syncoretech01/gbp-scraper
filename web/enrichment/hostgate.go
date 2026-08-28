package enrichment

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/idna"
)

// HostGate bounds how hard a pool of enrichment workers may hit one host.
//
// The unit of politeness is the host, not the task: a workspace routinely
// holds many businesses that share a site (the live workspace has 28
// businesses whose website is instagram.com), and a worker pool without a gate
// would fetch that host once per business, in parallel, from one IP.
//
// Acquire blocks until the caller may issue a request, and returns the release
// function that hands the slot back. A nil HostGate is not usable; callers
// hold the interface so a crawl without one simply skips the gate.
type HostGate interface {
	Acquire(ctx context.Context, host string) (func(), error)
}

// HostGateConfig bounds one gate. Zero values disable the corresponding rule,
// so an unconfigured gate behaves exactly like no gate at all.
type HostGateConfig struct {
	// MaxConcurrentPerHost is how many requests may be in flight against a
	// single host at once.
	MaxConcurrentPerHost int
	// MinInterval is the minimum spacing between the starts of two requests to
	// the same host.
	MinInterval time.Duration
	// MaxHosts bounds the gate's own memory. When more hosts than this have
	// been seen, idle entries are dropped; a dropped host simply starts its
	// spacing again, which is safe because it has no request in flight.
	MaxHosts int
}

const (
	// defaultHostGateHosts bounds the tracked host table. A local run visits
	// hundreds of hosts, not hundreds of thousands.
	defaultHostGateHosts = 4096
)

type hostSlot struct {
	permits   chan struct{}
	lastStart time.Time
	inFlight  int
}

type hostGate struct {
	config HostGateConfig
	mutex  sync.Mutex
	hosts  map[string]*hostSlot
	// sleep is the wait used for per-host spacing. Tests replace it so a
	// politeness delay never makes the suite slow or flaky.
	sleep func(ctx context.Context, delay time.Duration) error
	// now is the clock used for spacing decisions.
	now func() time.Time
}

// NewHostGate returns a gate that limits concurrency and request spacing per
// host. It is safe for concurrent use by a whole worker pool.
func NewHostGate(config HostGateConfig) HostGate {
	if config.MaxConcurrentPerHost < 1 {
		config.MaxConcurrentPerHost = 1
	}
	if config.MinInterval < 0 {
		config.MinInterval = 0
	}
	if config.MaxHosts < 1 {
		config.MaxHosts = defaultHostGateHosts
	}

	return &hostGate{
		config: config,
		hosts:  make(map[string]*hostSlot),
		sleep:  sleepContext,
		now:    time.Now,
	}
}

// Acquire blocks until one request to host may start.
func (gate *hostGate) Acquire(ctx context.Context, host string) (func(), error) {
	host = NormalizeGateHost(host)

	slot := gate.slotFor(host)

	select {
	case slot.permits <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	release := func() {
		gate.mutex.Lock()
		slot.inFlight--
		gate.mutex.Unlock()

		<-slot.permits
	}

	for {
		gate.mutex.Lock()
		wait := time.Duration(0)
		if gate.config.MinInterval > 0 && !slot.lastStart.IsZero() {
			elapsed := gate.now().Sub(slot.lastStart)
			if elapsed < gate.config.MinInterval {
				wait = gate.config.MinInterval - elapsed
			}
		}
		if wait == 0 {
			slot.lastStart = gate.now()
			slot.inFlight++
			gate.mutex.Unlock()

			return release, nil
		}
		gate.mutex.Unlock()

		if err := gate.sleep(ctx, wait); err != nil {
			<-slot.permits

			return nil, err
		}
	}
}

func (gate *hostGate) slotFor(host string) *hostSlot {
	gate.mutex.Lock()
	defer gate.mutex.Unlock()

	if slot, found := gate.hosts[host]; found {
		return slot
	}

	if len(gate.hosts) >= gate.config.MaxHosts {
		gate.evictIdleLocked()
	}

	slot := &hostSlot{permits: make(chan struct{}, gate.config.MaxConcurrentPerHost)}
	gate.hosts[host] = slot

	return slot
}

// evictIdleLocked drops hosts with nothing in flight. The caller holds the
// mutex. Dropping an idle host loses only its spacing memory.
func (gate *hostGate) evictIdleLocked() {
	for host, slot := range gate.hosts {
		if slot.inFlight == 0 {
			delete(gate.hosts, host)
		}
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NormalizeGateHost reduces a URL or host string to the key the gate limits
// on: the lower-case ASCII host without a leading "www." and without a port.
// Two businesses whose sites differ only by those parts share one budget.
func NormalizeGateHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if strings.Contains(value, "//") {
		if parsed, err := url.Parse(value); err == nil && parsed.Hostname() != "" {
			value = parsed.Hostname()
		}
	}

	if index := strings.LastIndexByte(value, ':'); index > 0 && !strings.Contains(value[index:], "]") {
		value = value[:index]
	}

	value = strings.TrimSuffix(strings.ToLower(value), ".")
	if ascii, err := idna.Lookup.ToASCII(value); err == nil {
		value = strings.ToLower(ascii)
	}

	return strings.TrimPrefix(value, "www.")
}

// SiteKey reduces an entry URL to the identity the domain-audit cache reuses
// on: the normalized host plus the exact path and query.
//
// The unit is deliberately the page, not the bare domain. A local workspace
// routinely holds many businesses whose "website" is a per-business page on a
// shared host — instagram.com/shopA, instagram.com/shopB, a directory's
// /business-name pages — and reusing one business's audit for another's page
// would attribute one shop's contacts to a different shop. Two businesses
// share evidence only when they point at the same page.
//
// The fragment is dropped because it never reaches the server, and a bare
// trailing slash is normalized because "/" and "" are the same resource.
func SiteKey(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}

	if !strings.Contains(rawURL, "//") {
		rawURL = "https://" + rawURL
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	host := NormalizeGateHost(parsed.Host)
	if host == "" {
		return ""
	}

	path := strings.TrimSuffix(parsed.EscapedPath(), "/")
	key := host + path

	if parsed.RawQuery != "" {
		key += "?" + parsed.RawQuery
	}

	return key
}
