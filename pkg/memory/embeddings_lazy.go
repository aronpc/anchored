package memory

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// DefaultEmbedIdleUnload is how long a loaded model may sit unused before
// LazyONNXEmbedder releases it.
const DefaultEmbedIdleUnload = 5 * time.Minute

// LazyONNXEmbedder defers loading the ONNX model until the first Embed and
// releases it again after idleTTL without use. Every AI session spawns its own
// anchored MCP process, and an eager embedder pinned the full model (~600 MB
// RSS) in each one even when the session never embedded anything.
//
// Identity (model, revision, dimensions) comes from the artifacts on disk, so
// generation bookkeeping never forces a load and the vectors produced are
// identical to the eager embedder's.
type LazyONNXEmbedder struct {
	modelName     string
	modelRevision string
	dims          int
	idleTTL       time.Duration
	logger        *slog.Logger
	load          func() (EmbeddingProvider, error)
	now           func() time.Time

	mu       sync.Mutex
	inner    EmbeddingProvider
	lastUsed time.Time
	closed   bool

	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// NewLazyONNXEmbedder prepares the ONNX artifacts (downloading them when
// missing) without loading the model. idleTTL <= 0 keeps the model loaded
// once the first Embed has paid for it.
func NewLazyONNXEmbedder(modelDir string, idleTTL time.Duration, logger *slog.Logger) (*LazyONNXEmbedder, error) {
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "onnx-embedder")

	artifacts, err := prepareONNXArtifacts(modelDir, logger)
	if err != nil {
		return nil, err
	}
	load := func() (EmbeddingProvider, error) {
		return loadONNXEmbedder(artifacts, logger)
	}
	return newLazyEmbedder(artifacts.modelName, artifacts.modelRevision, onnxModelDims, idleTTL, load, logger), nil
}

func newLazyEmbedder(
	modelName, modelRevision string,
	dims int,
	idleTTL time.Duration,
	load func() (EmbeddingProvider, error),
	logger *slog.Logger,
) *LazyONNXEmbedder {
	l := &LazyONNXEmbedder{
		modelName:     modelName,
		modelRevision: modelRevision,
		dims:          dims,
		idleTTL:       idleTTL,
		logger:        logger,
		load:          load,
		now:           time.Now,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
	if idleTTL > 0 {
		go l.reapIdle()
	} else {
		close(l.done)
	}
	return l
}

func (l *LazyONNXEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, fmt.Errorf("onnx: embedder closed")
	}
	if l.inner == nil {
		started := l.now()
		inner, err := l.load()
		if err != nil {
			return nil, fmt.Errorf("onnx: load model: %w", err)
		}
		l.inner = inner
		l.logger.Info("ONNX model loaded on demand", "took", l.now().Sub(started).Round(time.Millisecond))
	}
	l.lastUsed = l.now()
	return l.inner.Embed(ctx, texts)
}

// Loaded reports whether the model is currently resident.
func (l *LazyONNXEmbedder) Loaded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner != nil
}

func (l *LazyONNXEmbedder) reapIdle() {
	defer close(l.done)
	interval := l.idleTTL / 4
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			l.unloadIfIdle()
		}
	}
}

func (l *LazyONNXEmbedder) unloadIfIdle() {
	l.mu.Lock()
	if l.inner == nil || l.now().Sub(l.lastUsed) < l.idleTTL {
		l.mu.Unlock()
		return
	}
	err := l.inner.Close()
	l.inner = nil
	l.mu.Unlock()
	if err != nil {
		l.logger.Warn("ONNX model unload failed", "error", err)
	}
	// The weights live in C memory freed by Close; this returns the Go side
	// (tokenizer tables, tensors' Go slices) to the OS as well.
	debug.FreeOSMemory()
	l.logger.Info("ONNX model unloaded after idle", "idle_ttl", l.idleTTL)
}

func (l *LazyONNXEmbedder) Close() error {
	var err error
	l.closeOnce.Do(func() {
		close(l.stop)
		<-l.done
		l.mu.Lock()
		defer l.mu.Unlock()
		l.closed = true
		if l.inner != nil {
			err = l.inner.Close()
			l.inner = nil
		}
	})
	return err
}

func (l *LazyONNXEmbedder) Dimensions() int       { return l.dims }
func (l *LazyONNXEmbedder) Name() string          { return "onnx" }
func (l *LazyONNXEmbedder) Model() string         { return l.modelName }
func (l *LazyONNXEmbedder) ModelRevision() string { return l.modelRevision }
func (l *LazyONNXEmbedder) Normalization() string { return "l2" }
