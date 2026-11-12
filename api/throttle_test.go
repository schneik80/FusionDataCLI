package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeQuota swaps the package's quota state for one on a fake clock:
// sleeps return at once and advance the clock, and are recorded.
func fakeQuota(t *testing.T) (q *quotaState, sleeps *[]time.Duration) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	var slept []time.Duration
	q = &quotaState{step: throttlePauseStep}
	q.now = func() time.Time { return now }
	q.sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		slept = append(slept, d)
		now = now.Add(d)
		return nil
	}
	prev := quota
	quota = q
	t.Cleanup(func() { quota = prev })
	return q, &slept
}

const quotaBody = `{"errors":[{"message":"Query point value per minute quota exceeded with point value of 261 and remaining quota 7. Please try again later.","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`

func serve(t *testing.T, h func(n int32, w http.ResponseWriter)) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		h(calls.Add(1), w)
	}))
	t.Cleanup(srv.Close)
	swapEndpoint(t, srv.URL)
	return &calls
}

// TestGqlQuery_ThrottledWaitsAndSucceeds: HTTP 429 is not a failed
// attempt. The request waits out a growing pause — far longer than the
// flakiness backoff, which a quota never recovers within — and then
// succeeds. Four throttled answers would have exhausted three attempts.
func TestGqlQuery_ThrottledWaitsAndSucceeds(t *testing.T) {
	q, sleeps := fakeQuota(t)
	calls := serve(t, func(n int32, w http.ResponseWriter) {
		if n <= 4 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, quotaBody)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"x":1},"extensions":{"pointValue":{"requestedQueryPointValue":261}}}`)
	})

	if _, err := gqlQuery(context.Background(), "tok", "query Q {}", nil); err != nil {
		t.Fatalf("gqlQuery: %v", err)
	}
	if calls.Load() != 5 {
		t.Errorf("calls = %d, want 5", calls.Load())
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second, 20 * time.Second}
	if len(*sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", *sleeps, want)
	}
	for i, d := range want {
		if (*sleeps)[i] != d {
			t.Errorf("sleep %d = %s, want %s", i, (*sleeps)[i], d)
		}
	}
	if points, paused, throttles := Quota(); points != 261 || paused != 0 || throttles != 4 {
		t.Errorf("Quota() = %d points, paused %s, %d throttles; want 261, 0, 4", points, paused, throttles)
	}
	if q.streak != 0 {
		t.Errorf("streak = %d after a success, want 0", q.streak)
	}
}

// TestGqlQuery_ThrottledHonoursRetryAfter: when the API does say how
// long to wait, that is the pause.
func TestGqlQuery_ThrottledHonoursRetryAfter(t *testing.T) {
	_, sleeps := fakeQuota(t)
	serve(t, func(n int32, w http.ResponseWriter) {
		if n == 1 {
			w.Header().Set("Retry-After", "42")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, quotaBody)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"x":1}}`)
	})
	if _, err := gqlQuery(context.Background(), "tok", "query Q {}", nil); err != nil {
		t.Fatalf("gqlQuery: %v", err)
	}
	if len(*sleeps) != 1 || (*sleeps)[0] != 42*time.Second {
		t.Errorf("sleeps = %v, want [42s]", *sleeps)
	}
}

// TestGqlQuery_ThrottledUntilContextEnds: a request still throttled
// when its context runs out reports the rate limit, by name, and not
// "flaky".
func TestGqlQuery_ThrottledUntilContextEnds(t *testing.T) {
	q, _ := fakeQuota(t)
	ctx, cancel := context.WithCancel(context.Background())
	inner := q.sleep
	n := 0
	q.sleep = func(c context.Context, d time.Duration) error {
		if n++; n == 3 {
			cancel()
		}
		return inner(c, d)
	}
	serve(t, func(_ int32, w http.ResponseWriter) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, quotaBody)
	})

	_, err := gqlQuery(ctx, "tok", "query Q {}", nil)
	if !errors.Is(err, ErrThrottled) {
		t.Fatalf("err = %v, want ErrThrottled", err)
	}
	if strings.Contains(err.Error(), "flaky") {
		t.Errorf("a rate limit must not be reported as flakiness: %v", err)
	}
}

// TestGqlQuery_ThrottlePauseIsShared: once one request is throttled,
// another request waits for the same pause instead of being sent into
// the limit.
func TestGqlQuery_ThrottlePauseIsShared(t *testing.T) {
	q, sleeps := fakeQuota(t)
	calls := serve(t, func(_ int32, w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"data":{"x":1}}`)
	})
	q.throttled(0)

	if _, err := gqlQuery(context.Background(), "tok", "query Q {}", nil); err != nil {
		t.Fatalf("gqlQuery: %v", err)
	}
	if len(*sleeps) != 1 || (*sleeps)[0] != throttlePauseStep || calls.Load() != 1 {
		t.Errorf("sleeps = %v calls = %d, want one %s wait and one call", *sleeps, calls.Load(), throttlePauseStep)
	}
}

// TestBackgroundWaitsForBudget: background requests are held while the
// last minute's spend is at the background budget; a foreground request
// is not.
func TestBackgroundWaitsForBudget(t *testing.T) {
	q, sleeps := fakeQuota(t)
	calls := serve(t, func(_ int32, w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"data":{"x":1},"extensions":{"pointValue":{"requestedQueryPointValue":100}}}`)
	})
	q.spent(BackgroundPointBudget)

	if _, err := gqlQuery(context.Background(), "tok", "query Q {}", nil); err != nil {
		t.Fatalf("foreground: %v", err)
	}
	if len(*sleeps) != 0 {
		t.Fatalf("foreground request waited: %v", *sleeps)
	}
	if _, err := gqlQuery(Background(context.Background()), "tok", "query Q {}", nil); err != nil {
		t.Fatalf("background: %v", err)
	}
	// It waits until the big spend is a minute old, then goes.
	if len(*sleeps) != 1 || (*sleeps)[0] != time.Minute || calls.Load() != 2 {
		t.Errorf("sleeps = %v calls = %d, want one 1m wait and two calls", *sleeps, calls.Load())
	}
	// Everything spent before the wait is now a minute old.
	if points, _, _ := Quota(); points != 100 {
		t.Errorf("points in the last minute = %d, want 100 (only the background request)", points)
	}
}

// TestGqlQuery_ClosedProjectFieldKeepsData is the case the retry loop
// used to hide. A document in a closed project fails on fusionWebUrl
// with errorType UNKNOWN at a shallow path — the shape of gateway
// flakiness — while everything else is there. It is a fact about the
// document, not a transient: one request, and the data is used.
func TestGqlQuery_ClosedProjectFieldKeepsData(t *testing.T) {
	fakeQuota(t)
	calls := serve(t, func(_ int32, w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"data":{"item":{"id":"i1","name":"Ejector Pin","fusionWebUrl":null}},"errors":[{"message":"Sorry, the project you are trying to access is now closed by the administrator.","path":["item","fusionWebUrl"],"extensions":{"code":"EXCEPTION_CLOSED_COMMUNITY","errorType":"UNKNOWN"}}]}`)
	})
	raw, err := gqlQuery(context.Background(), "tok", "query Q {}", nil)
	if err != nil {
		t.Fatalf("gqlQuery: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (no retry)", calls.Load())
	}
	if !strings.Contains(string(raw), "Ejector Pin") {
		t.Errorf("data = %s, want the item", raw)
	}
}

// TestGqlQuery_DeterministicRootErrorIsNotRetried: the same code on a
// root field leaves nothing to show, and asking again cannot help.
func TestGqlQuery_DeterministicRootErrorIsNotRetried(t *testing.T) {
	fakeQuota(t)
	calls := serve(t, func(_ int32, w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"data":{"item":null},"errors":[{"message":"You do not have access to resource.","path":["item"],"extensions":{"code":"FORBIDDEN","errorType":"UNKNOWN"}}]}`)
	})
	_, err := gqlQuery(context.Background(), "tok", "query Q {}", nil)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err = %v calls = %d, want an error after 1 call", err, calls.Load())
	}
	for _, want := range []string{"FORBIDDEN", "at item"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "flaky") {
		t.Errorf("a refusal must not be reported as flakiness: %v", err)
	}
}
