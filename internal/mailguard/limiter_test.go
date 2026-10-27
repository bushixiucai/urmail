package mailguard_test

import (
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/mailguard"
)

// fakeClock is a monotonic test clock. It is safe for the concurrency test.
type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func newLimiter(t *testing.T, cfg mailguard.Config) (*mailguard.Limiter, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	cfg.Now = clock.Now
	limiter, err := mailguard.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return limiter, clock
}

func smallConfig() mailguard.Config {
	return mailguard.Config{
		MaxFailures: 3,
		Window:      time.Minute,
		BanDuration: 5 * time.Minute,
		MaxSources:  16,
	}
}

func TestLimiterThresholdAndExpiry(t *testing.T) {
	limiter, clock := newLimiter(t, smallConfig())
	source := netip.MustParseAddr("203.0.113.50")

	for i := 0; i < 2; i++ {
		if limiter.Banned(mailguard.ScopeSMTPAuth, source) {
			t.Fatalf("banned after %d failures, want 3", i)
		}
		limiter.Failure(mailguard.ScopeSMTPAuth, source)
	}
	status := limiter.Status(mailguard.ScopeSMTPAuth, source)
	if status.Banned || status.Failures != 2 {
		t.Fatalf("status before threshold = %+v", status)
	}

	// The threshold failure bans immediately and reports the full duration.
	limiter.Failure(mailguard.ScopeSMTPAuth, source)
	if !limiter.Banned(mailguard.ScopeSMTPAuth, source) {
		t.Fatal("source not banned at threshold")
	}
	if got := limiter.RetryAfter(mailguard.ScopeSMTPAuth, source); got != 5*time.Minute {
		t.Fatalf("RetryAfter = %v, want 5m", got)
	}

	// A failure while banned neither extends the ban nor is counted.
	clock.Advance(4 * time.Minute)
	limiter.Failure(mailguard.ScopeSMTPAuth, source)
	if got := limiter.RetryAfter(mailguard.ScopeSMTPAuth, source); got != time.Minute {
		t.Fatalf("RetryAfter after failure while banned = %v, want 1m", got)
	}

	// The ban expires on its own and the budget starts from zero.
	clock.Advance(time.Minute)
	if limiter.Banned(mailguard.ScopeSMTPAuth, source) {
		t.Fatal("ban did not expire")
	}
	status = limiter.Status(mailguard.ScopeSMTPAuth, source)
	if status.Failures != 0 || status.RetryAfter != 0 {
		t.Fatalf("status after ban expiry = %+v", status)
	}
}

func TestLimiterWindowExpiry(t *testing.T) {
	limiter, clock := newLimiter(t, smallConfig())
	source := netip.MustParseAddr("2001:db8::50")

	limiter.Failure(mailguard.ScopeIMAPAuth, source)
	clock.Advance(30 * time.Second)
	limiter.Failure(mailguard.ScopeIMAPAuth, source)
	// The first failure leaves the window before the threshold is reached.
	clock.Advance(45 * time.Second)
	if limiter.Status(mailguard.ScopeIMAPAuth, source).Failures != 1 {
		t.Fatalf("stale failures were not pruned: %+v", limiter.Status(mailguard.ScopeIMAPAuth, source))
	}
	limiter.Failure(mailguard.ScopeIMAPAuth, source)
	if limiter.Banned(mailguard.ScopeIMAPAuth, source) {
		t.Fatal("banned even though failures were spread across the window")
	}
}

func TestLimiterSuccessResets(t *testing.T) {
	limiter, _ := newLimiter(t, smallConfig())
	source := netip.MustParseAddr("203.0.113.51")

	limiter.Failure(mailguard.ScopePOP3Auth, source)
	limiter.Failure(mailguard.ScopePOP3Auth, source)
	limiter.Success(mailguard.ScopePOP3Auth, source)
	if got := limiter.Status(mailguard.ScopePOP3Auth, source); got.Failures != 0 || got.Banned {
		t.Fatalf("success did not reset state: %+v", got)
	}
	if got := limiter.TrackedSources(); got != 0 {
		t.Fatalf("tracked sources after success = %d, want 0", got)
	}

	// A successful login also lifts a ban (a correct password proves the
	// account is not under guessing from this source any more).
	for i := 0; i < 3; i++ {
		limiter.Failure(mailguard.ScopePOP3Auth, source)
	}
	if !limiter.Banned(mailguard.ScopePOP3Auth, source) {
		t.Fatal("expected ban")
	}
	limiter.Success(mailguard.ScopePOP3Auth, source)
	if limiter.Banned(mailguard.ScopePOP3Auth, source) {
		t.Fatal("success did not lift the ban")
	}
}

func TestLimiterScopesAreIndependent(t *testing.T) {
	limiter, _ := newLimiter(t, smallConfig())
	source := netip.MustParseAddr("203.0.113.52")

	for i := 0; i < 3; i++ {
		limiter.Failure(mailguard.ScopeIMAPAuth, source)
	}
	if !limiter.Banned(mailguard.ScopeIMAPAuth, source) {
		t.Fatal("IMAP scope not banned")
	}
	for _, scope := range []mailguard.Scope{
		mailguard.ScopeSMTPAuth,
		mailguard.ScopePOP3Auth,
		mailguard.ScopeWebLogin,
	} {
		if limiter.Banned(scope, source) {
			t.Fatalf("scope %s was banned by IMAP failures", scope)
		}
	}
}

// TestLimiterSharedAddressRecovery models an office/carrier NAT where one
// compromised account trips the limit for every user behind the address, and
// an operator restores access with Unban.
func TestLimiterSharedAddressRecovery(t *testing.T) {
	limiter, _ := newLimiter(t, smallConfig())
	shared := netip.MustParseAddr("198.51.100.60")

	for i := 0; i < 3; i++ {
		limiter.Failure(mailguard.ScopeWebLogin, shared)
	}
	if !limiter.Banned(mailguard.ScopeWebLogin, shared) {
		t.Fatal("shared address not banned")
	}

	// Operator recovery: the ban disappears for both the bad and the good
	// account behind the same address, and a legit login succeeds.
	limiter.Unban(mailguard.ScopeWebLogin, shared)
	if limiter.Banned(mailguard.ScopeWebLogin, shared) {
		t.Fatal("Unban did not restore access")
	}
	if got := limiter.Status(mailguard.ScopeWebLogin, shared); got.Failures != 0 {
		t.Fatalf("Unban left failure state: %+v", got)
	}
	limiter.Success(mailguard.ScopeWebLogin, shared)
	if limiter.Banned(mailguard.ScopeWebLogin, shared) {
		t.Fatal("legitimate account still blocked after recovery")
	}
}

func TestLimiterBoundedMemory(t *testing.T) {
	cfg := smallConfig()
	cfg.MaxSources = 8
	limiter, clock := newLimiter(t, cfg)

	for i := 0; i < 100; i++ {
		// A distributed spray of unique sources must not grow state without
		// bound; addresses cycle through both families.
		source := netip.AddrFrom4([4]byte{198, 51, byte(100 + i/250), byte(i % 250)})
		if i%2 == 1 {
			source = netip.MustParseAddr("2001:db8::1").Next()
		}
		limiter.Failure(mailguard.ScopeSMTPAuth, source)
		if got := limiter.TrackedSources(); got > cfg.MaxSources {
			t.Fatalf("tracked sources = %d, want <= %d", got, cfg.MaxSources)
		}
	}

	// After every window and ban has elapsed, new sources still fit and old
	// state is not resurrected.
	clock.Advance(24 * time.Hour)
	for i := 0; i < 50; i++ {
		source := netip.AddrFrom4([4]byte{192, 0, 2, byte(i)})
		limiter.Failure(mailguard.ScopeSMTPAuth, source)
	}
	if got := limiter.TrackedSources(); got > cfg.MaxSources {
		t.Fatalf("tracked sources = %d, want <= %d", got, cfg.MaxSources)
	}
}

// TestLimiterEvictionKeepsBan is the shared-address protection detail: an
// active ban at the front of the LRU list must survive a spray of new
// sources, because the ban is what protects the service.
func TestLimiterEvictionKeepsFreshBan(t *testing.T) {
	cfg := smallConfig()
	cfg.MaxSources = 4
	limiter, _ := newLimiter(t, cfg)

	banned := netip.MustParseAddr("203.0.113.70")
	for i := 0; i < cfg.MaxFailures; i++ {
		limiter.Failure(mailguard.ScopeSMTPAuth, banned)
	}
	if !limiter.Banned(mailguard.ScopeSMTPAuth, banned) {
		t.Fatal("expected ban")
	}

	// Only MaxSources-1 new sources fit without eviction; the banned source
	// was touched last, so it is evicted only after every older entry.
	for i := 0; i < cfg.MaxSources*3; i++ {
		limiter.Failure(mailguard.ScopeSMTPAuth, netip.AddrFrom4([4]byte{192, 0, 2, byte(i + 1)}))
	}
	if !limiter.Banned(mailguard.ScopeSMTPAuth, banned) {
		t.Fatal("eviction dropped an active ban before older entries")
	}
}

func TestLimiterConcurrentUse(t *testing.T) {
	cfg := mailguard.Config{
		MaxFailures: 50,
		Window:      time.Hour,
		BanDuration: time.Hour,
		MaxSources:  32,
	}
	limiter, err := mailguard.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sources := []netip.Addr{
		netip.MustParseAddr("203.0.113.80"),
		netip.MustParseAddr("2001:db8::80"),
		netip.MustParseAddr("198.51.100.80"),
	}

	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			scope := []mailguard.Scope{
				mailguard.ScopeSMTPAuth,
				mailguard.ScopeIMAPAuth,
				mailguard.ScopePOP3Auth,
				mailguard.ScopeWebLogin,
			}[worker%4]
			for i := 0; i < 200; i++ {
				source := sources[(worker+i)%len(sources)]
				limiter.Failure(scope, source)
				_ = limiter.Banned(scope, source)
				_ = limiter.RetryAfter(scope, source)
				_ = limiter.Status(scope, source)
				if i%7 == 0 {
					limiter.Success(scope, source)
				}
			}
		}(worker)
	}
	wg.Wait()

	if got := limiter.TrackedSources(); got > cfg.MaxSources {
		t.Fatalf("tracked sources = %d, want <= %d", got, cfg.MaxSources)
	}
	// Every source received far more than 50 failures without a matching
	// success for the IMAP worker, so the limiter must be in a consistent,
	// queryable state rather than panicking or losing the map.
	for _, source := range sources {
		_ = limiter.Status(mailguard.ScopeIMAPAuth, source)
	}
}

func TestLimiterInvalidAddressIsIgnored(t *testing.T) {
	limiter, _ := newLimiter(t, smallConfig())
	invalid := netip.Addr{}

	limiter.Failure(mailguard.ScopeSMTPAuth, invalid)
	limiter.Success(mailguard.ScopeSMTPAuth, invalid)
	limiter.Unban(mailguard.ScopeSMTPAuth, invalid)

	if limiter.Banned(mailguard.ScopeSMTPAuth, invalid) {
		t.Fatal("invalid address reported as banned")
	}
	if got := limiter.TrackedSources(); got != 0 {
		t.Fatalf("tracked sources = %d, want 0", got)
	}
	if got := limiter.Status(mailguard.ScopeSMTPAuth, invalid); got.Source.IsValid() {
		t.Fatalf("invalid address produced a source: %+v", got)
	}
}

func TestLimiterIPv4MappedIPv6SharesOneBucket(t *testing.T) {
	limiter, _ := newLimiter(t, smallConfig())
	plain := netip.MustParseAddr("203.0.113.90")
	mapped := netip.MustParseAddr("::ffff:203.0.113.90")

	limiter.Failure(mailguard.ScopeSMTPAuth, plain)
	limiter.Failure(mailguard.ScopeSMTPAuth, mapped)
	limiter.Failure(mailguard.ScopeSMTPAuth, plain)

	if !limiter.Banned(mailguard.ScopeSMTPAuth, mapped) {
		t.Fatal("IPv4-mapped form did not share the IPv4 bucket")
	}
	if got := limiter.TrackedSources(); got != 1 {
		t.Fatalf("tracked sources = %d, want 1", got)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	valid := mailguard.DefaultConfig()
	tests := []struct {
		name string
		cfg  mailguard.Config
	}{
		{name: "zero_failures", cfg: func() mailguard.Config { c := valid; c.MaxFailures = 0; return c }()},
		{name: "zero_window", cfg: func() mailguard.Config { c := valid; c.Window = 0; return c }()},
		{name: "zero_ban", cfg: func() mailguard.Config { c := valid; c.BanDuration = 0; return c }()},
		{name: "negative_ban", cfg: func() mailguard.Config { c := valid; c.BanDuration = -time.Minute; return c }()},
		{name: "zero_sources", cfg: func() mailguard.Config { c := valid; c.MaxSources = 0; return c }()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			limiter, err := mailguard.New(tc.cfg)
			if !errors.Is(err, mailguard.ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}
			if limiter != nil {
				t.Fatal("invalid config returned a limiter")
			}
		})
	}
}

// TestDefaultConfigsAreBounded guards the documented policy: every boundary
// has a finite threshold, a finite window, and a non-permanent ban.
func TestDefaultConfigsAreBounded(t *testing.T) {
	for _, scope := range []mailguard.Scope{
		mailguard.ScopeSMTPAuth,
		mailguard.ScopeIMAPAuth,
		mailguard.ScopePOP3Auth,
		mailguard.ScopeWebLogin,
	} {
		cfg := mailguard.DefaultConfigFor(scope)
		if cfg.MaxFailures <= 0 || cfg.MaxFailures > 20 {
			t.Fatalf("%s: MaxFailures = %d", scope, cfg.MaxFailures)
		}
		if cfg.Window <= 0 || cfg.Window > time.Hour {
			t.Fatalf("%s: Window = %v", scope, cfg.Window)
		}
		if cfg.BanDuration <= 0 || cfg.BanDuration > time.Hour {
			t.Fatalf("%s: BanDuration = %v (bans must not be permanent)", scope, cfg.BanDuration)
		}
		if cfg.MaxSources <= 0 || cfg.MaxSources > 100_000 {
			t.Fatalf("%s: MaxSources = %d", scope, cfg.MaxSources)
		}
	}
}
