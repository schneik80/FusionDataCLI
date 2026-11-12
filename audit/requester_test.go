package audit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schneik80/FusionDataCLI/api"
)

// scripted returns a requester whose API answers come from a list, on a
// fake clock. It records the fake time of each request and how often a
// fresh token was demanded.
func scripted(answers ...*api.RawResponse) (r *requester, times *[]time.Duration, forced *int) {
	var mu sync.Mutex
	clock := time.Unix(1_700_000_000, 0)
	start := clock
	times, forced = new([]time.Duration), new(int)
	r = newRequester(func(_ context.Context, force bool) (string, error) {
		if force {
			*forced++
		}
		return "tok", nil
	}, 2, 1000)
	r.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	r.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		if d > 0 {
			clock = clock.Add(d)
		}
		mu.Unlock()
		return ctx.Err()
	}
	i := 0
	r.query = func(context.Context, string, string, map[string]any) (*api.RawResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		*times = append(*times, clock.Sub(start))
		if i >= len(answers) {
			return &api.RawResponse{Status: 200, Data: []byte(`{}`), Points: 10}, nil
		}
		a := answers[i]
		i++
		if a == nil {
			return nil, errors.New("connection reset")
		}
		return a, nil
	}
	return r, times, forced
}

func ok(points int) *api.RawResponse {
	return &api.RawResponse{Status: 200, Data: []byte(`{}`), Points: points}
}

func TestRequester_HonoursRetryAfter(t *testing.T) {
	r, times, _ := scripted(&api.RawResponse{Status: 429, RetryAfter: 17 * time.Second}, ok(10))
	if _, err := r.do(context.Background(), "q", nil, 10); err != nil {
		t.Fatalf("do: %v", err)
	}
	// After the pause the bucket is empty, so the retry follows as
	// soon as its 10 points have trickled in — under a second later.
	if len(*times) != 2 || (*times)[1] < 17*time.Second || (*times)[1] > 18*time.Second {
		t.Errorf("request times = %v, want the retry just after the 17s the API asked for", *times)
	}
	st := r.Stats()
	if st.Throttled != 1 || st.ThrottleWait != 17*time.Second || st.FinalConcurrency != 1 || st.PointsPerMinute != 850 {
		t.Errorf("stats = %+v, want 1 throttle, 17s wait, concurrency 1, budget cut to 850", st)
	}
}

// Without Retry-After, a 429 stops everything for throttlePause.
func TestRequester_ThrottleWithoutRetryAfter(t *testing.T) {
	r, times, _ := scripted(ok(400), &api.RawResponse{Status: 429}, ok(10))
	ctx := context.Background()
	if _, err := r.do(ctx, "q", nil, 400); err != nil {
		t.Fatal(err)
	}
	if _, err := r.do(ctx, "q", nil, 10); err != nil {
		t.Fatal(err)
	}
	if got := (*times)[2]; got < throttlePause || got > throttlePause+2*time.Second {
		t.Errorf("retry at %s, want just after the %s pause", got, throttlePause)
	}
	if st := r.Stats(); st.ThrottleWait != throttlePause {
		t.Errorf("ThrottleWait = %s", st.ThrottleWait)
	}
}

// Points are spent at a steady rate: a full bucket allows one burst,
// after which a request waits for its points to trickle in at
// budget/60 per second.
func TestRequester_PacesToTheBudget(t *testing.T) {
	ctx := context.Background()

	// Estimated at 400 each, but the API reports 10: the difference is
	// refunded, so nothing waits.
	r, times, _ := scripted()
	for i := 0; i < 5; i++ {
		if _, err := r.do(ctx, "q", nil, 400); err != nil {
			t.Fatal(err)
		}
	}
	for i, at := range *times {
		if at != 0 {
			t.Errorf("request %d at %s: reported costs of 10 leave room, nothing should wait", i, at)
		}
	}

	// Really 400 each against 1000 points a minute: the bucket covers
	// two, the third needs 200 more points at 1000/60 a second = 12s,
	// the fourth another 400 = 24s after that.
	r, times, _ = scripted(ok(400), ok(400), ok(400), ok(400))
	for i := 0; i < 4; i++ {
		if _, err := r.do(ctx, "q", nil, 400); err != nil {
			t.Fatal(err)
		}
	}
	want := []time.Duration{0, 0, 12 * time.Second, 36 * time.Second}
	for i := range want {
		if d := (*times)[i] - want[i]; d < -100*time.Millisecond || d > 100*time.Millisecond {
			t.Fatalf("request times = %v, want about %v", *times, want)
		}
	}
	if w := r.Stats().PacingWait; w < 35*time.Second || w > 37*time.Second {
		t.Errorf("PacingWait = %s, want about 36s", w)
	}
}

func TestRequester_RetriesServerAndNetworkFailures(t *testing.T) {
	r, times, _ := scripted(&api.RawResponse{Status: 503}, nil, &api.RawResponse{Status: 408}, ok(10))
	if _, err := r.do(context.Background(), "q", nil, 10); err != nil {
		t.Fatalf("do: %v", err)
	}
	// Backoff doubles: 1s, 2s, 4s.
	want := []time.Duration{0, time.Second, 3 * time.Second, 7 * time.Second}
	for i := range want {
		if (*times)[i] != want[i] {
			t.Fatalf("request times = %v, want %v", *times, want)
		}
	}
	st := r.Stats()
	if st.ServerErrors != 2 || st.TransportErrors != 1 || st.GaveUp != 0 {
		t.Errorf("stats = %+v", st)
	}
}

func TestRequester_GivesUpLoudly(t *testing.T) {
	bad := &api.RawResponse{Status: 500, Body: []byte("boom")}
	r, times, _ := scripted(bad, bad, bad, bad, bad, bad, bad, bad)
	_, err := r.do(context.Background(), "q", nil, 10)
	if err == nil || !strings.Contains(err.Error(), "gave up after 6 attempts") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want a give-up error carrying the last failure", err)
	}
	if len(*times) != 6 || r.Stats().GaveUp != 1 {
		t.Errorf("attempts = %d, GaveUp = %d", len(*times), r.Stats().GaveUp)
	}
}

func TestRequester_RefreshesTokenOnceOn401(t *testing.T) {
	r, _, forced := scripted(&api.RawResponse{Status: 401}, ok(10))
	if _, err := r.do(context.Background(), "q", nil, 10); err != nil {
		t.Fatalf("do: %v", err)
	}
	if *forced != 1 || r.Stats().TokenRefreshes != 1 {
		t.Errorf("forced refreshes = %d", *forced)
	}

	r, _, _ = scripted(&api.RawResponse{Status: 401}, &api.RawResponse{Status: 401})
	if _, err := r.do(context.Background(), "q", nil, 10); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("second 401: err = %v, want failure", err)
	}
}

// A rate limit reported inside an HTTP 200 must be treated as
// throttling, never returned as if it described the data.
func TestRequester_ThrottleInsideA200(t *testing.T) {
	limited := &api.RawResponse{Status: 200, Errors: []api.RawError{{Message: "Query point value per minute quota exceeded with point value of 892 and remaining quota 88."}}}
	r, times, _ := scripted(limited, ok(10))
	resp, err := r.do(context.Background(), "q", nil, 10)
	if err != nil || len(resp.Errors) != 0 {
		t.Fatalf("resp = %+v, err = %v; want the retried success", resp, err)
	}
	if len(*times) != 2 || r.Stats().Throttled != 1 {
		t.Errorf("attempts = %d, Throttled = %d", len(*times), r.Stats().Throttled)
	}
}

// GraphQL errors that are about the data go back to the caller as-is.
func TestRequester_PassesDataErrorsThrough(t *testing.T) {
	partial := &api.RawResponse{Status: 200, Data: []byte(`{"t0":null}`), Errors: []api.RawError{{Message: "You do not have access to resource.", Code: "FORBIDDEN"}}}
	r, times, _ := scripted(partial)
	resp, err := r.do(context.Background(), "q", nil, 10)
	if err != nil || len(resp.Errors) != 1 || len(*times) != 1 {
		t.Errorf("resp = %+v, err = %v, attempts = %d", resp, err, len(*times))
	}
}

func TestRequester_StopsOnCancel(t *testing.T) {
	r, _, _ := scripted(&api.RawResponse{Status: 500})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.do(ctx, "q", nil, 10); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
