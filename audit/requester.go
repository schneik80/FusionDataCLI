package audit

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/schneik80/FusionDataCLI/api"
)

// TokenSource returns a bearer token. With force set it must obtain a
// fresh one (the previous token was just rejected with HTTP 401).
type TokenSource func(ctx context.Context, force bool) (string, error)

// RequestStats counts what the requester did. Every number here is part
// of the report: it is how a reader judges whether throttling or
// failures could have cost data.
type RequestStats struct {
	Requests         int           // HTTP round-trips attempted
	Throttled        int           // HTTP 429 responses
	ThrottleWait     time.Duration // time all workers were held back because of 429s
	PacingWait       time.Duration // time workers spent waiting for points, summed over workers
	ServerErrors     int           // HTTP 5xx / 408 responses
	TransportErrors  int           // no HTTP response at all
	TokenRefreshes   int           // HTTP 401 answered by refreshing the token
	Points           int           // query points spent, as reported by the API
	PointsPerMinute  int           // the per-minute point budget the run ended on
	MinConcurrency   int           // lowest concurrency the throttle control dropped to
	FinalConcurrency int
	GaveUp           int // requests abandoned after every retry
}

// The API meters query points per minute and answers HTTP 429 ("Query
// point value per minute quota exceeded … remaining quota N") once a
// minute's worth is spent. On the test account the quota behaved like
// 10,000 points a minute.
//
// The requester therefore spends points at a steady rate — a token
// bucket refilled at budget/60 points a second — instead of as fast as
// it can. Steady matters: a first version spent a minute's budget in a
// burst and then waited for the minute to pass, and was throttled while
// never exceeding the budget in any 60 seconds of its own clock. The
// server's count evidently carries weight from the previous minute, so
// a burst just after a quiet spell lands on top of the last one.
const (
	// DefaultPointsPerMinute leaves headroom under the observed quota.
	// A steady 8,000 a minute still drew an occasional 429 on the test
	// account; runs that had dropped to 6,800 drew none.
	DefaultPointsPerMinute = 6500
	minPointsPerMinute     = 500
	// burstPoints is the bucket's capacity: one full-size query.
	burstPoints = 1000
	// throttlePause is how long everything stops after a 429 that
	// carries no Retry-After: long enough for most of a minute's count
	// to fall away.
	throttlePause = 30 * time.Second
)

// spend is one request's point reservation.
type spend struct {
	points int
}

// requester sends GraphQL requests for the crawl. It never returns a
// throttled or failed response as if it were an answer: it paces
// itself under the point quota, waits and retries when the API
// throttles anyway, slows every worker down, and only gives up after a
// long, bounded effort — and then says so with an error the caller
// must record.
type requester struct {
	token TokenSource
	query func(ctx context.Context, token, q string, vars map[string]any) (*api.RawResponse, error)
	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time

	maxConcurrency int
	maxAttempts    int           // attempts for server / transport failures
	maxThrottled   int           // additional attempts allowed for 429s
	baseBackoff    time.Duration // first retry delay; doubles each time
	maxBackoff     time.Duration

	mu         sync.Mutex
	cond       *sync.Cond
	limit      int       // current concurrency limit (≤ maxConcurrency)
	inFlight   int       // requests holding a slot
	pauseUntil time.Time // no request starts before this
	okStreak   int       // successes since the limit last changed
	budget     int       // points per minute; the bucket's refill rate ×60
	tokens     float64   // points available to spend now
	refilled   time.Time // when tokens was last brought up to date
	stats      RequestStats
}

func newRequester(token TokenSource, concurrency, pointsPerMinute int) *requester {
	if concurrency < 1 {
		concurrency = 1
	}
	if pointsPerMinute <= 0 {
		pointsPerMinute = DefaultPointsPerMinute
	}
	r := &requester{
		token: token,
		query: api.QueryRaw,
		sleep: func(ctx context.Context, d time.Duration) error {
			if d <= 0 {
				return ctx.Err()
			}
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
		now:            time.Now,
		maxConcurrency: concurrency,
		maxAttempts:    6,
		maxThrottled:   30,
		baseBackoff:    time.Second,
		maxBackoff:     60 * time.Second,
		limit:          concurrency,
		budget:         pointsPerMinute,
		tokens:         burstPoints,
	}
	r.cond = sync.NewCond(&r.mu)
	r.stats.MinConcurrency = concurrency
	return r
}

// growAfter is how many consecutive successes earn back one slot of
// concurrency after a throttle cut it.
const growAfter = 25

// acquire takes a concurrency slot and spends cost points from the
// bucket, first waiting out any pause a 429 imposed on the whole crawl
// and then for the bucket to hold enough. The returned reservation is
// settled by release when the response is in.
func (r *requester) acquire(ctx context.Context, cost int) (*spend, error) {
	for {
		r.mu.Lock()
		for r.inFlight >= r.limit {
			if ctx.Err() != nil {
				r.mu.Unlock()
				return nil, ctx.Err()
			}
			r.cond.Wait()
		}
		now := r.now()
		wait := r.pauseUntil.Sub(now)
		if wait <= 0 {
			rate := float64(r.budget) / 60 // points per second
			if r.refilled.IsZero() {
				r.refilled = now
			}
			r.tokens = min(burstPoints, r.tokens+now.Sub(r.refilled).Seconds()*rate)
			r.refilled = now
			// A query dearer than the bucket can ever hold still has to
			// go out: it waits for a full bucket and overdraws it.
			need := min(float64(cost), burstPoints)
			if r.tokens >= need {
				r.tokens -= float64(cost)
				r.inFlight++
				r.mu.Unlock()
				return &spend{points: cost}, nil
			}
			wait = time.Duration((need - r.tokens) / rate * float64(time.Second))
			if wait < time.Millisecond {
				wait = time.Millisecond
			}
			r.stats.PacingWait += wait
		}
		r.mu.Unlock()
		if err := r.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// release gives the slot back and settles the reservation. points is
// what the request actually cost: the API's figure when it reported
// one, 0 when the request was refused and so spent nothing, or -1 to
// keep the estimate.
func (r *requester) release(s *spend, points int) {
	r.mu.Lock()
	r.inFlight--
	if points >= 0 {
		r.tokens = min(burstPoints, r.tokens+float64(s.points-points))
		s.points = points
	}
	r.mu.Unlock()
	r.cond.Broadcast()
}

// throttled records a 429. Every worker is held for the pause, the
// bucket is emptied, the point budget is cut and concurrency halved.
// wait is the pause the API asked for, 0 if it did not say.
func (r *requester) throttled(wait time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if wait <= 0 {
		wait = throttlePause
	}
	until := now.Add(wait)
	if until.After(r.pauseUntil) {
		// Count only the extension of the pause, so overlapping 429s
		// from parallel workers are not added up twice.
		start := now
		if r.pauseUntil.After(start) {
			start = r.pauseUntil
		}
		r.stats.ThrottleWait += until.Sub(start)
		r.pauseUntil = until
		// The budget was too generous for this account right now. Cut
		// it once per pause, not once per worker that hit the wall.
		// Never cut below the floor, and never "cut" upwards when the
		// caller chose a budget under it.
		r.budget = max(min(minPointsPerMinute, r.budget), r.budget*85/100)
	}
	r.tokens = 0
	r.refilled = until
	r.stats.Throttled++
	if r.limit > 1 {
		r.limit = (r.limit + 1) / 2
	}
	if r.limit < r.stats.MinConcurrency {
		r.stats.MinConcurrency = r.limit
	}
	r.okStreak = 0
}

func (r *requester) succeeded(points int) {
	r.mu.Lock()
	r.stats.Points += points
	r.okStreak++
	grew := false
	if r.okStreak >= growAfter && r.limit < r.maxConcurrency {
		r.limit++
		r.okStreak = 0
		grew = true
	}
	r.mu.Unlock()
	if grew {
		r.cond.Broadcast()
	}
}

func (r *requester) count(f func(*RequestStats)) {
	r.mu.Lock()
	f(&r.stats)
	r.mu.Unlock()
}

// Stats returns a snapshot of the counters.
func (r *requester) Stats() RequestStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.FinalConcurrency = r.limit
	s.PointsPerMinute = r.budget
	return s
}

func (r *requester) backoff(attempt int) time.Duration {
	d := r.baseBackoff
	for i := 1; i < attempt && d < r.maxBackoff; i++ {
		d *= 2
	}
	if d > r.maxBackoff {
		d = r.maxBackoff
	}
	return d
}

// do sends one GraphQL request and returns the HTTP 200 response for
// it. cost is the caller's estimate of the query's point value, used
// to pace the request under the per-minute quota. GraphQL-level errors
// inside the response are the caller's to interpret. Everything else
// is retried here:
//
//   - 429: hold back all workers for Retry-After (30 s when the API
//     does not send one), cut the point budget, halve concurrency, and
//     repeat the request.
//   - 5xx / 408 / no response: growing backoff.
//   - 401: refresh the token once and repeat.
//
// A non-nil error means the request was abandoned; the caller has to
// record what it could not read.
func (r *requester) do(ctx context.Context, q string, vars map[string]any, cost int) (*api.RawResponse, error) {
	var (
		failures  int // server / transport failures so far
		throttles int
		refreshed bool
		force     bool
		lastErr   error
	)
	for {
		token, err := r.token(ctx, force)
		if err != nil {
			return nil, fmt.Errorf("token: %w", err)
		}
		force = false

		res, err := r.acquire(ctx, cost)
		if err != nil {
			return nil, err
		}
		r.count(func(s *RequestStats) { s.Requests++ })
		resp, err := r.query(ctx, token, q, vars)

		var wait time.Duration
		switch {
		case err != nil:
			r.release(res, -1) // unknown whether it was counted; assume so
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			r.count(func(s *RequestStats) { s.TransportErrors++ })
			failures++
			lastErr = err
			wait = r.backoff(failures)

		case resp.Status == http.StatusOK && !throttleError(resp.Errors):
			points := -1
			if resp.Points > 0 {
				points = resp.Points
			}
			r.release(res, points)
			r.succeeded(resp.Points)
			return resp, nil

		case resp.Status == http.StatusTooManyRequests || resp.Status == http.StatusOK:
			// (A 200 only reaches here when its errors say "throttled":
			// a rate limit reported inside the GraphQL envelope must not
			// be mistaken for missing data.)
			r.release(res, 0)
			throttles++
			lastErr = fmt.Errorf("HTTP %d (throttled): %s", resp.Status, snippet(resp.Body))
			r.throttled(resp.RetryAfter)
			if throttles > r.maxThrottled {
				r.count(func(s *RequestStats) { s.GaveUp++ })
				return nil, fmt.Errorf("still throttled after %d attempts: %w", throttles, lastErr)
			}
			// acquire() sleeps out the pause for every worker, this
			// one included.
			continue

		case resp.Status == http.StatusUnauthorized:
			r.release(res, 0)
			if refreshed {
				r.count(func(s *RequestStats) { s.GaveUp++ })
				return nil, fmt.Errorf("HTTP 401 after refreshing the token: %s", snippet(resp.Body))
			}
			refreshed, force = true, true
			r.count(func(s *RequestStats) { s.TokenRefreshes++ })
			continue

		case resp.Status >= 500 || resp.Status == http.StatusRequestTimeout:
			r.release(res, -1)
			r.count(func(s *RequestStats) { s.ServerErrors++ })
			failures++
			lastErr = fmt.Errorf("HTTP %d: %s", resp.Status, snippet(resp.Body))
			wait = r.backoff(failures)

		default:
			r.release(res, 0)
			// Another 4xx. A GraphQL validation error (query over the
			// point budget, say) can arrive as HTTP 400 with a normal
			// errors array; hand that to the caller, who knows how to
			// split the request. Anything else is not retriable.
			if len(resp.Errors) > 0 {
				return resp, nil
			}
			r.count(func(s *RequestStats) { s.GaveUp++ })
			return nil, fmt.Errorf("HTTP %d: %s", resp.Status, snippet(resp.Body))
		}

		if failures >= r.maxAttempts {
			r.count(func(s *RequestStats) { s.GaveUp++ })
			return nil, fmt.Errorf("gave up after %d attempts: %w", failures, lastErr)
		}
		if err := r.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// throttleError reports whether a GraphQL errors array is the API
// saying "slow down" rather than describing the data.
func throttleError(errs []api.RawError) bool {
	for _, e := range errs {
		code := strings.ToUpper(e.Code)
		if strings.Contains(code, "RATE_LIMIT") || strings.Contains(code, "TOO_MANY") ||
			strings.Contains(code, "THROTTL") || strings.Contains(code, "QUOTA") {
			return true
		}
		msg := strings.ToLower(e.Message)
		if strings.Contains(msg, "rate limit") || strings.Contains(msg, "too many requests") ||
			strings.Contains(msg, "quota exceeded") {
			return true
		}
	}
	return false
}

func snippet(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
