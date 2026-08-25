package health

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"stackpilot/internal/domain"
)

func TestReadinessChecksImmediatelyAndRequiresConsecutiveSuccesses(t *testing.T) {
	checker := &sequenceChecker{results: []Result{
		{Kind: KindTCP, ErrorCode: CodeTCPRefused, Summary: "not ready"},
		{Kind: KindTCP, Success: true, Summary: "ready one"},
		{Kind: KindTCP, Success: true, Summary: "ready two"},
	}}
	recorder := &memoryRecorder{}
	engine, err := NewEngine(checker, recorder, func(time.Duration, int) time.Duration { return 2 * time.Millisecond })
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	started := time.Now()
	outcome, err := engine.Await(context.Background(), testRequest(2, 100*time.Millisecond))
	if err != nil || !outcome.Ready || outcome.Attempts != 3 || len(recorder.results) != 3 {
		t.Fatalf("Await() = (%#v, %v), records = %d", outcome, err, len(recorder.results))
	}
	if checker.firstDelay >= 5*time.Millisecond || time.Since(started) >= 100*time.Millisecond {
		t.Fatalf("first check was not immediate: %s", checker.firstDelay)
	}
}

func TestReadinessStopsImmediatelyForTerminalProcessFailure(t *testing.T) {
	checker := &sequenceChecker{results: []Result{{Kind: KindProcess, ErrorCode: CodeProcessExited, Summary: "exited"}}}
	recorder := &memoryRecorder{}
	engine, _ := NewEngine(checker, recorder, noJitter)
	request := testRequest(1, time.Second)
	request.Spec.Kind, request.Spec.Identity = KindProcess, testIdentity()
	outcome, err := engine.Await(context.Background(), request)
	if err != nil || outcome.Ready || outcome.Attempts != 1 || outcome.ErrorCode != CodeProcessExited {
		t.Fatalf("Await() = (%#v, %v)", outcome, err)
	}
}

func TestReadinessReturnsStableTimeoutWithoutConcurrentChecks(t *testing.T) {
	checker := &sequenceChecker{delay: 3 * time.Millisecond, results: []Result{{Kind: KindTCP, ErrorCode: CodeTCPRefused, Summary: "refused"}}}
	recorder := &memoryRecorder{}
	engine, _ := NewEngine(checker, recorder, noJitter)
	outcome, err := engine.Await(context.Background(), testRequest(1, 25*time.Millisecond))
	if err != nil || outcome.Ready || outcome.ErrorCode != CodeReadinessTimeout || outcome.Attempts < 2 {
		t.Fatalf("Await() = (%#v, %v)", outcome, err)
	}
	if checker.maximum.Load() != 1 {
		t.Fatalf("maximum concurrent checks = %d, want 1", checker.maximum.Load())
	}
}

func TestReadinessCancellationInterruptsCurrentCheck(t *testing.T) {
	checker := &blockingChecker{started: make(chan struct{})}
	recorder := &memoryRecorder{}
	engine, _ := NewEngine(checker, recorder, noJitter)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := engine.Await(ctx, testRequest(1, time.Second))
		done <- err
	}()
	<-checker.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Await() error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Await() did not stop promptly after cancellation")
	}
	if len(recorder.results) != 0 {
		t.Fatalf("cancelled check persisted %d results", len(recorder.results))
	}
}

func TestReadinessValidatesResolvedInputsAndRequiresRecorder(t *testing.T) {
	checker := &sequenceChecker{results: []Result{{Kind: KindTCP, Success: true, Summary: "ready"}}}
	if _, err := NewEngine(checker, nil, nil); !errors.Is(err, ErrRecorderRequired) {
		t.Fatalf("NewEngine() error = %v", err)
	}
	engine, _ := NewEngine(checker, &memoryRecorder{}, noJitter)
	request := testRequest(1, time.Second)
	request.Spec.CheckTimeout = request.Spec.Interval + time.Millisecond
	if _, err := engine.Await(context.Background(), request); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("Await() error = %v", err)
	}
}

func TestReadinessPersistsComposeChecks(t *testing.T) {
	checker := &sequenceChecker{results: []Result{{Kind: KindCompose, Success: true, Summary: "healthy"}}}
	recorder := &memoryRecorder{}
	engine, _ := NewEngine(checker, recorder, noJitter)
	request := testRequest(1, time.Second)
	request.Spec.Kind, request.Spec.ComposeIdentity = KindCompose, "opaque-project-identity"
	request.Spec.Host, request.Spec.Port = "", 0
	outcome, err := engine.Await(context.Background(), request)
	if err != nil || !outcome.Ready || len(recorder.results) != 1 || recorder.results[0].Kind != KindCompose {
		t.Fatalf("Compose Await() = (%#v, %v), records=%#v", outcome, err, recorder.results)
	}
}

func testRequest(successThreshold int, timeout time.Duration) Request {
	return Request{
		ServiceInstanceID: domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV"),
		Spec: ResolvedSpec{
			Kind: KindTCP, Host: "127.0.0.1", Port: 12345, CheckTimeout: 4 * time.Millisecond,
			Interval: 5 * time.Millisecond, ReadinessTimeout: timeout, SuccessThreshold: successThreshold, FailureThreshold: 1,
		},
	}
}

func noJitter(interval time.Duration, _ int) time.Duration { return interval }

type sequenceChecker struct {
	results    []Result
	delay      time.Duration
	startedAt  time.Time
	firstDelay time.Duration
	index      atomic.Int64
	active     atomic.Int64
	maximum    atomic.Int64
}

func (checker *sequenceChecker) Check(ctx context.Context, _ ResolvedSpec) Result {
	current := checker.active.Add(1)
	defer checker.active.Add(-1)
	for current > checker.maximum.Load() && !checker.maximum.CompareAndSwap(checker.maximum.Load(), current) {
	}
	if checker.startedAt.IsZero() {
		checker.startedAt = time.Now()
		checker.firstDelay = 0
	}
	if checker.delay > 0 {
		select {
		case <-time.After(checker.delay):
		case <-ctx.Done():
			return Result{Kind: KindTCP, ErrorCode: CodeTCPTimeout, Summary: "timed out"}
		}
	}
	index := int(checker.index.Add(1) - 1)
	if index >= len(checker.results) {
		index = len(checker.results) - 1
	}
	result := checker.results[index]
	result.CheckedAt = time.Now().UTC()
	return result
}

type blockingChecker struct {
	started chan struct{}
	once    sync.Once
}

func (checker *blockingChecker) Check(ctx context.Context, _ ResolvedSpec) Result {
	checker.once.Do(func() { close(checker.started) })
	<-ctx.Done()
	return Result{Kind: KindTCP, CheckedAt: time.Now().UTC(), Summary: "cancelled"}
}

type memoryRecorder struct {
	mutex   sync.Mutex
	results []Result
}

func (recorder *memoryRecorder) Record(_ context.Context, _ domain.ServiceInstanceID, result Result) error {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.results = append(recorder.results, result)
	return nil
}
