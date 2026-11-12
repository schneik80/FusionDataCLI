package api

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"
)

// The Manufacturing Data Model API meters query points per minute, per
// user and application. A request that would overdraw the minute is
// refused whole with HTTP 429 and
//
//	"Query point value per minute quota exceeded with point value of N
//	 and remaining quota M. Please try again later."
//
// There is no Retry-After header and the quota frees up gradually, so
// asking again within a second or two — what the flakiness retry does —
// only fails again. Everything in this file exists so that a throttled
// request waits and then succeeds instead of surfacing an error:
//
//   - a pause shared by every request: once one request is throttled,
//     none is sent until the pause is over;
//   - a meter of the points this process spent in the last minute;
//   - a price for every request before it is sent (cost.go): one that
//     would take the meter past PointBudget waits until enough of the
//     minute's spending has aged out, instead of being sent into a 429;
//   - a lower budget for background work (assembly/part tags), so that
//     what the user is waiting on gets the remaining points.
//
// The meter counts this process only. Another session on the same
// account spends the same quota unseen, so a 429 can still arrive; the
// shared pause is what handles it.

const (
	// throttlePauseStep is the pause after a first throttled response;
	// each further one in a row adds a step, up to throttlePauseMax.
	throttlePauseStep = 5 * time.Second
	throttlePauseMax  = 20 * time.Second

	// PointBudget is the most points per minute this process spends.
	// The quota behaves like roughly 10,000 points a minute; the margin
	// covers a request priced too low on its first use.
	PointBudget = 9000

	// BackgroundPointBudget is the most points per minute this process
	// spends before background requests wait. It leaves the rest for
	// navigation and for anything else using the same account.
	BackgroundPointBudget = 5000
)

// ErrThrottled is returned (wrapped) when a request was still being
// throttled when its context ran out.
var ErrThrottled = errors.New("APS rate limit reached: the per-minute query quota is used up; wait a minute and try again")

type spend struct {
	at     time.Time
	points int
}

type quotaState struct {
	mu          sync.Mutex
	window      []*spend  // spends and reservations in the last minute
	pausedUntil time.Time // no request is sent before this
	heldUntil   time.Time // a foreground request is waiting for points until this
	streak      int       // throttled responses in a row
	throttles   int       // throttled responses, total

	// Test seams.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
	step  time.Duration
}

var quota = &quotaState{now: time.Now, sleep: sleepCtx, step: throttlePauseStep}

type backgroundKey struct{}

// Background marks ctx as carrying work the user is not waiting on.
// Such requests are held while the points spent in the last minute
// exceed BackgroundPointBudget.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

// spent records points outside any reservation.
func (s *quotaState) spent(points int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if points > 0 {
		s.window = append(s.window, &spend{s.now(), points})
	}
}

// settle replaces a reservation's estimate with what the request was
// charged: the API's number, or nothing when it reported none (a
// throttled or failed request is not charged). A response that reports
// points was not throttled, so the streak ends.
func (s *quotaState) settle(r *spend, points int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.points = max(points, 0)
	if points > 0 {
		s.streak = 0
	}
}

// lastMinute returns the points spent in the last 60 seconds and, when
// that is non-zero, how long until the oldest spend drops out.
func (s *quotaState) lastMinute() (points int, untilOldestExpires time.Duration) {
	cut := s.now().Add(-time.Minute)
	i := 0
	for i < len(s.window) && !s.window[i].at.After(cut) {
		i++
	}
	s.window = s.window[i:]
	for _, sp := range s.window {
		points += sp.points
	}
	if len(s.window) > 0 {
		untilOldestExpires = s.window[0].at.Sub(cut)
	}
	return points, untilOldestExpires
}

// throttled records a throttled response and sets the shared pause.
func (s *quotaState) throttled(retryAfter time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streak++
	s.throttles++
	pause := retryAfter
	if pause <= 0 {
		pause = min(time.Duration(s.streak)*s.step, throttlePauseMax)
	}
	if until := s.now().Add(pause); until.After(s.pausedUntil) {
		s.pausedUntil = until
	}
	dbgLog("THROTTLED pause=%s streak=%d spent_last_minute=%d", pause, s.streak, sumPoints(s.window))
}

func sumPoints(w []*spend) (n int) {
	for _, sp := range w {
		n += sp.points
	}
	return n
}

// freeIn returns how long until the last minute's spending has dropped
// to at most target. The caller holds mu and has just called lastMinute.
func (s *quotaState) freeIn(points, target int) time.Duration {
	cut := s.now().Add(-time.Minute)
	for _, sp := range s.window {
		points -= sp.points
		if points <= target {
			return sp.at.Sub(cut)
		}
	}
	return 0
}

// admit blocks until a request priced at cost may be sent — past any
// throttle pause, and with room for it under the budget (PointBudget,
// or BackgroundPointBudget for background work) — and reserves its
// points so that requests admitted meanwhile see them. The caller must
// settle the reservation with what the request was charged.
//
// A request that costs more than the whole budget goes when nothing
// else has been spent in the last minute: holding it would hold it for
// ever.
func (s *quotaState) admit(ctx context.Context, cost int) (*spend, error) {
	background := ctx.Value(backgroundKey{}) != nil
	budget := PointBudget
	if background {
		budget = BackgroundPointBudget
	}
	for {
		s.mu.Lock()
		d := s.pausedUntil.Sub(s.now())
		if d <= 0 {
			if points, _ := s.lastMinute(); points > 0 && points+cost > budget {
				d = s.freeIn(points, max(budget-cost, 0))
				if until := s.now().Add(d); !background && until.After(s.heldUntil) {
					s.heldUntil = until
				}
				dbgLog("HOLD cost=%d spent_last_minute=%d budget=%d wait=%s", cost, points, budget, d)
			}
		}
		if d <= 0 {
			r := &spend{s.now(), cost}
			s.window = append(s.window, r)
			s.mu.Unlock()
			return r, nil
		}
		s.mu.Unlock()
		if err := s.sleep(ctx, d); err != nil {
			return nil, err
		}
	}
}

// Quota reports the points this process spent in the last minute, how
// long requests are still being held — paused after a throttled
// response, or waiting for points under the budget — (zero when they
// are not), and how many throttled responses there have been.
func Quota() (pointsLastMinute int, pausedFor time.Duration, throttles int) {
	quota.mu.Lock()
	defer quota.mu.Unlock()
	pointsLastMinute, _ = quota.lastMinute()
	if d := quota.pausedUntil.Sub(quota.now()); d > 0 {
		pausedFor = d
	}
	if d := quota.heldUntil.Sub(quota.now()); d > pausedFor {
		pausedFor = d
	}
	return pointsLastMinute, pausedFor, quota.throttles
}

// isQuotaBody recognises the quota refusal by its text, in case it ever
// arrives with a status other than 429.
func isQuotaBody(raw []byte) bool {
	return bytes.Contains(raw, []byte("per minute quota exceeded"))
}
