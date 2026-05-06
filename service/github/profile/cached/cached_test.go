package cached_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmj0121/imagelet/service/github/profile"
	"github.com/cmj0121/imagelet/service/github/profile/cached"
)

// stubUser is a controllable inner UserProvider. Returns the preset
// (profile, err) and increments calls atomically. setOK / setErr flip
// behavior between calls.
type stubUser struct {
	mu    sync.Mutex
	p     profile.Profile
	err   error
	delay time.Duration
	calls atomic.Int32
}

func (s *stubUser) User(_ context.Context, login string) (profile.Profile, error) {
	s.calls.Add(1)
	s.mu.Lock()
	d, p, err := s.delay, s.p, s.err
	s.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
	if p.Login == "" && err == nil {
		p.Login = login
	}
	return p, err
}

func (s *stubUser) setOK(p profile.Profile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p = p
	s.err = nil
}

func (s *stubUser) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p = profile.Profile{}
	s.err = err
}

// stubRepo mirrors stubUser for the repo path.
type stubRepo struct {
	mu    sync.Mutex
	r     profile.Repo
	err   error
	calls atomic.Int32
}

func (s *stubRepo) Repo(_ context.Context, owner, name string) (profile.Repo, error) {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.r
	if r.FullName == "" && s.err == nil {
		r.FullName = owner + "/" + name
	}
	return r, s.err
}

func (s *stubRepo) setOK(r profile.Repo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.r = r
	s.err = nil
}

func (s *stubRepo) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.r = profile.Repo{}
	s.err = err
}

// ---------------------------------------------------------------------
// UserCached
// ---------------------------------------------------------------------

func TestCached_HitMiss(t *testing.T) {
	s := &stubUser{}
	s.setOK(profile.Profile{Login: "octocat", Followers: 10})
	c := cached.NewUserWithTTL(s, time.Hour, time.Hour, time.Hour)

	for i := 0; i < 3; i++ {
		p, err := c.User(context.Background(), "octocat")
		if err != nil {
			t.Fatalf("User #%d: %v", i, err)
		}
		if p.Followers != 10 {
			t.Errorf("Followers = %d, want 10", p.Followers)
		}
	}
	if got := s.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1 (cache hit)", got)
	}
}

func TestCached_TTLExpiry(t *testing.T) {
	s := &stubUser{}
	s.setOK(profile.Profile{Login: "octocat"})
	c := cached.NewUserWithTTL(s, 10*time.Millisecond, time.Hour, time.Hour)

	if _, err := c.User(context.Background(), "octocat"); err != nil {
		t.Fatalf("first User: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := c.User(context.Background(), "octocat"); err != nil {
		t.Fatalf("second User: %v", err)
	}
	if got := s.calls.Load(); got != 2 {
		t.Errorf("inner calls = %d, want 2 (cache miss after TTL)", got)
	}
}

func TestCached_Singleflight(t *testing.T) {
	s := &stubUser{delay: 50 * time.Millisecond}
	s.setOK(profile.Profile{Login: "octocat"})
	c := cached.NewUserWithTTL(s, time.Hour, time.Hour, time.Hour)

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, _ = c.User(context.Background(), "octocat")
		}()
	}
	close(start)
	wg.Wait()

	if got := s.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1 (singleflight collapse)", got)
	}
}

func TestCached_NegativeNotFound(t *testing.T) {
	s := &stubUser{}
	s.setErr(profile.ErrNotFound)
	c := cached.NewUserWithTTL(s, time.Hour, time.Hour, time.Hour)

	if _, err := c.User(context.Background(), "ghost"); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("first User err = %v, want ErrNotFound", err)
	}
	if _, err := c.User(context.Background(), "ghost"); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("second User err = %v, want ErrNotFound (cached)", err)
	}
	if got := s.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1 (cached not-found)", got)
	}
}

func TestCached_NegativeNotFoundExpiry(t *testing.T) {
	s := &stubUser{}
	s.setErr(profile.ErrNotFound)
	// Tight notFoundTTL so the expiry path runs in test time.
	c := cached.NewUserWithTTL(s, time.Hour, time.Hour, 10*time.Millisecond)

	if _, err := c.User(context.Background(), "ghost"); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("first User: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := c.User(context.Background(), "ghost"); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("second User: %v", err)
	}
	if got := s.calls.Load(); got != 2 {
		t.Errorf("inner calls = %d, want 2 (notFoundTTL expired)", got)
	}
}

func TestCached_TransientFail(t *testing.T) {
	s := &stubUser{}
	s.setErr(profile.ErrUnavailable)
	c := cached.NewUserWithTTL(s, time.Hour, 50*time.Millisecond, time.Hour)

	if _, err := c.User(context.Background(), "octocat"); !errors.Is(err, profile.ErrUnavailable) {
		t.Fatalf("first User: %v", err)
	}
	if _, err := c.User(context.Background(), "octocat"); !errors.Is(err, profile.ErrUnavailable) {
		t.Fatalf("second User (within failureTTL): %v", err)
	}
	if got := s.calls.Load(); got != 1 {
		t.Errorf("inner calls within failureTTL = %d, want 1", got)
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := c.User(context.Background(), "octocat"); !errors.Is(err, profile.ErrUnavailable) {
		t.Fatalf("third User (post failureTTL): %v", err)
	}
	if got := s.calls.Load(); got != 2 {
		t.Errorf("inner calls post failureTTL = %d, want 2", got)
	}
}

func TestCached_StaleOnTransient(t *testing.T) {
	s := &stubUser{}
	s.setOK(profile.Profile{Login: "octocat", Followers: 10})
	c := cached.NewUserWithTTL(s, 10*time.Millisecond, time.Hour, time.Hour)

	p, err := c.User(context.Background(), "octocat")
	if err != nil {
		t.Fatalf("seed User: %v", err)
	}
	if p.Followers != 10 {
		t.Fatalf("seed Followers = %d, want 10", p.Followers)
	}

	// Flip stub to a transient error and exceed successTTL.
	s.setErr(profile.ErrUnavailable)
	time.Sleep(20 * time.Millisecond)

	// Stale-serve: cached value + the refresh err.
	p, err = c.User(context.Background(), "octocat")
	if err == nil {
		t.Fatalf("expected refresh err on stale path, got nil")
	}
	if !errors.Is(err, profile.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if p.Login != "octocat" {
		t.Errorf("stale Login = %q, want octocat (cached)", p.Login)
	}
	if p.Followers != 10 {
		t.Errorf("stale Followers = %d, want 10 (cached)", p.Followers)
	}
}

// TestCached_StaleNotFoundEvictsValue pins that ErrNotFound on refresh
// is treated as "the entity is gone" — the stale cached profile is
// dropped instead of being served alongside the not-found err. This
// matches the design's "stale wins over transient" but NOT over
// permanent-feeling outcomes.
func TestCached_StaleNotFoundEvictsValue(t *testing.T) {
	s := &stubUser{}
	s.setOK(profile.Profile{Login: "octocat", Followers: 10})
	c := cached.NewUserWithTTL(s, 10*time.Millisecond, time.Hour, time.Hour)

	if _, err := c.User(context.Background(), "octocat"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s.setErr(profile.ErrNotFound)
	time.Sleep(20 * time.Millisecond)

	p, err := c.User(context.Background(), "octocat")
	if !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if p.Login != "" {
		t.Errorf("Login = %q, want empty (stale value dropped on 404)", p.Login)
	}
}

func TestCached_LogHourlyStopsOnContextCancel(t *testing.T) {
	s := &stubUser{}
	s.setOK(profile.Profile{Login: "x"})
	c := cached.NewUser(s)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.LogHourly(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("LogHourly did not return within 50ms of context cancel")
	}
}

// ---------------------------------------------------------------------
// RepoCached — mirror coverage of the major paths
// ---------------------------------------------------------------------

func TestCached_RepoHitMiss(t *testing.T) {
	s := &stubRepo{}
	s.setOK(profile.Repo{FullName: "octocat/Hello-World", Stars: 42})
	c := cached.NewRepoWithTTL(s, time.Hour, time.Hour, time.Hour)

	for i := 0; i < 3; i++ {
		r, err := c.Repo(context.Background(), "octocat", "Hello-World")
		if err != nil {
			t.Fatalf("Repo #%d: %v", i, err)
		}
		if r.Stars != 42 {
			t.Errorf("Stars = %d, want 42", r.Stars)
		}
	}
	if got := s.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1", got)
	}
}

func TestCached_RepoNotFoundCached(t *testing.T) {
	s := &stubRepo{}
	s.setErr(profile.ErrNotFound)
	c := cached.NewRepoWithTTL(s, time.Hour, time.Hour, time.Hour)

	for i := 0; i < 3; i++ {
		_, err := c.Repo(context.Background(), "x", "y")
		if !errors.Is(err, profile.ErrNotFound) {
			t.Fatalf("Repo #%d err = %v, want ErrNotFound", i, err)
		}
	}
	if got := s.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1", got)
	}
}

func TestCached_RepoLogHourlyStopsOnContextCancel(t *testing.T) {
	s := &stubRepo{}
	s.setOK(profile.Repo{FullName: "x/y"})
	c := cached.NewRepo(s)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.LogHourly(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("RepoCached.LogHourly did not return within 50ms of context cancel")
	}
}
