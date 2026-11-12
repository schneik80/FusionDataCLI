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
//   - a budget for background work (assembly/part tags), which is held
//     back while the meter is high so that what the user is waiting on
//     gets the remaining points.

const (
	// throttlePauseStep is the pause after a first throttled response;
	// each further one in a row adds a step, up to throttlePauseMax.
	throttlePauseStep = 5 * time.Second
	throttlePauseMax  = 20 * time.Second

	// BackgroundPointBudget is the most points per minute this process
	// spends before background requests wait. The quota behaves like
	// roughly 10,000 points a minute; this leaves the rest for
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
	window      []spend   // spends in the last minute
	pausedUntil time.Time // no request is sent before this
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

// spent records the points one request cost. A response that reports
// points was not throttled, so the streak ends.
func (s *quotaState) spent(points int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streak = 0
	if points > 0 {
		s.window = append(s.window, spend{s.now(), points})
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

func sumPoints(w []spend) (n int) {
	for _, sp := range w {
		n += sp.points
	}
	return n
}

// wait blocks until a request may be sent: past any throttle pause and,
// for background work, until the meter is under the background budget.
func (s *quotaState) wait(ctx context.Context) error {
	background := ctx.Value(backgroundKey{}) != nil
	for {
		s.mu.Lock()
		d := s.pausedUntil.Sub(s.now())
		if d <= 0 && background {
			if points, until := s.lastMinute(); points >= BackgroundPointBudget {
				d = until
			}
		}
		s.mu.Unlock()
		if d <= 0 {
			return nil
		}
		if err := s.sleep(ctx, d); err != nil {
			return err
		}
	}
}

// Quota reports the points this process spent in the last minute, how
// long requests are still paused after a throttled response (zero when
// they are not), and how many throttled responses there have been.
func Quota() (pointsLastMinute int, pausedFor time.Duration, throttles int) {
	quota.mu.Lock()
	defer quota.mu.Unlock()
	pointsLastMinute, _ = quota.lastMinute()
	if d := quota.pausedUntil.Sub(quota.now()); d > 0 {
		pausedFor = d
	}
	return pointsLastMinute, pausedFor, quota.throttles
}

// isQuotaBody recognises the quota refusal by its text, in case it ever
// arrives with a status other than 429.
func isQuotaBody(raw []byte) bool {
	return bytes.Contains(raw, []byte("per minute quota exceeded"))
}
