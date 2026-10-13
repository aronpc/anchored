package memory

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeInnerEmbedder struct {
	mu     sync.Mutex
	closed bool
	embeds int
}

func (f *fakeInnerEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.embeds++
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}
func (f *fakeInnerEmbedder) Dimensions() int { return 3 }
func (f *fakeInnerEmbedder) Name() string    { return "fake" }
func (f *fakeInnerEmbedder) Model() string   { return "fake" }
func (f *fakeInnerEmbedder) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

type lazyHarness struct {
	mu     sync.Mutex
	loads  []*fakeInnerEmbedder
	clock  time.Time
	failOn int
}

func (h *lazyHarness) load() (EmbeddingProvider, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failOn > 0 && len(h.loads)+1 == h.failOn {
		h.failOn = 0
		return nil, errors.New("boom")
	}
	f := &fakeInnerEmbedder{}
	h.loads = append(h.loads, f)
	return f, nil
}

func (h *lazyHarness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *lazyHarness) advance(d time.Duration) {
	h.mu.Lock()
	h.clock = h.clock.Add(d)
	h.mu.Unlock()
}

func (h *lazyHarness) loadCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.loads)
}

func newTestLazy(t *testing.T, h *lazyHarness, ttl time.Duration) *LazyONNXEmbedder {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// ttl 0 here keeps the background reaper off; tests drive unloadIfIdle
	// directly with a controlled clock.
	l := newLazyEmbedder("m", "rev", 3, 0, h.load, logger)
	l.idleTTL = ttl
	l.now = h.now
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestLazyEmbedderDoesNotLoadUntilFirstEmbed(t *testing.T) {
	h := &lazyHarness{clock: time.Unix(0, 0)}
	l := newTestLazy(t, h, time.Minute)

	if got := l.Model(); got != "m" {
		t.Fatalf("Model() = %q, want m", got)
	}
	if got := l.ModelRevision(); got != "rev" {
		t.Fatalf("ModelRevision() = %q, want rev", got)
	}
	if l.Dimensions() != 3 || l.Normalization() != "l2" || l.Name() != "onnx" {
		t.Fatalf("identity accessors changed")
	}
	if h.loadCount() != 0 || l.Loaded() {
		t.Fatalf("identity accessors must not load the model")
	}

	if _, err := l.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Embed(context.Background(), []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if h.loadCount() != 1 {
		t.Fatalf("loads = %d, want 1", h.loadCount())
	}
}

func TestLazyEmbedderUnloadsAfterIdleAndReloads(t *testing.T) {
	h := &lazyHarness{clock: time.Unix(0, 0)}
	l := newTestLazy(t, h, time.Minute)

	if _, err := l.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	h.advance(30 * time.Second)
	l.unloadIfIdle()
	if !l.Loaded() {
		t.Fatalf("unloaded before idle TTL elapsed")
	}

	h.advance(31 * time.Second)
	l.unloadIfIdle()
	if l.Loaded() {
		t.Fatalf("still loaded after idle TTL")
	}
	if !h.loads[0].closed {
		t.Fatalf("inner embedder was not closed on unload")
	}

	if _, err := l.Embed(context.Background(), []string{"c"}); err != nil {
		t.Fatal(err)
	}
	if h.loadCount() != 2 {
		t.Fatalf("loads = %d, want 2 after reload", h.loadCount())
	}
}

func TestLazyEmbedderLoadFailureIsRetried(t *testing.T) {
	h := &lazyHarness{clock: time.Unix(0, 0), failOn: 1}
	l := newTestLazy(t, h, time.Minute)

	if _, err := l.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatalf("expected load error")
	}
	if _, err := l.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("second embed should load: %v", err)
	}
}

func TestLazyEmbedderCloseReleasesAndRejects(t *testing.T) {
	h := &lazyHarness{clock: time.Unix(0, 0)}
	l := newTestLazy(t, h, time.Minute)

	if _, err := l.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if !h.loads[0].closed {
		t.Fatalf("Close did not release the model")
	}
	if _, err := l.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatalf("Embed after Close should fail")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestLazyEmbedderReaperRunsInBackground(t *testing.T) {
	h := &lazyHarness{clock: time.Unix(0, 0)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	l := newLazyEmbedder("m", "rev", 3, 40*time.Millisecond, h.load, logger)
	defer l.Close()

	if _, err := l.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for l.Loaded() {
		if time.Now().After(deadline) {
			t.Fatalf("reaper never unloaded the idle model")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEmbedIdleUnload(t *testing.T) {
	cases := map[int]time.Duration{0: DefaultEmbedIdleUnload, -1: 0, 10: 10 * time.Minute}
	for in, want := range cases {
		if got := embedIdleUnload(in); got != want {
			t.Errorf("embedIdleUnload(%d) = %v, want %v", in, got, want)
		}
	}
}
