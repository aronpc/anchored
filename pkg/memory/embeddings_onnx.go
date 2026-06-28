package memory

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	ort "github.com/yalue/onnxruntime_go"

	util "github.com/jholhewres/anchored/pkg/util"
)

const (
	onnxModelName      = "paraphrase-multilingual-MiniLM-L12-v2"
	legacyModelName    = "all-MiniLM-L6-v2"
	onnxModelDims      = 384
	onnxMaxSeqLen      = 128
	maxEmbedBatchSize = 16
	maxEmbedTextLen   = 2000

	onnxRuntimeVersion = "1.25.1"

	onnxRuntimeURLTemplate = "https://github.com/microsoft/onnxruntime/releases/download/v%s/onnxruntime-%s-%s-%s.tgz"
	onnxModelBaseURL       = "https://huggingface.co/sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2/resolve/main"
	onnxLegacyModelBaseURL = "https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main"
)

type embedRequest struct {
	texts []string
	resp  chan embedResponse
}

type embedResponse struct {
	vecs [][]float32
	err  error
}

type ONNXEmbedder struct {
	session   *ort.DynamicAdvancedSession
	tokenizer Tokenizer
	dims      int
	logger    *slog.Logger
	modelName string

	reqCh chan embedRequest
}

type ONNXPaths struct {
	RuntimeLib    string
	ModelFile     string
	VocabFile     string
	TokenizerFile string
}

func NewONNXEmbedder(modelDir string, logger *slog.Logger) (*ONNXEmbedder, error) {
	logger = util.DefaultLogger(logger)
	logger = logger.With("component", "onnx-embedder")

	paths := resolveONNXPaths(modelDir)

	if err := ensureONNXRuntime(paths, logger); err != nil {
		return nil, fmt.Errorf("onnx: runtime setup: %w", err)
	}
	if err := ensureONNXModel(paths, logger); err != nil {
		return nil, fmt.Errorf("onnx: model setup: %w", err)
	}

	ort.SetSharedLibraryPath(paths.RuntimeLib)
	if !ort.IsInitialized() {
		if err := ort.InitializeEnvironment(); err != nil {
			return nil, fmt.Errorf("onnx: init environment: %w", err)
		}
	}

	var tokenizer Tokenizer
	if fileExists(paths.TokenizerFile) {
		tok, err := NewFastTokenizer(paths.TokenizerFile, onnxMaxSeqLen)
		if err != nil {
			logger.Warn("fast tokenizer failed, falling back to wordpiece", "error", err)
		} else {
			tokenizer = tok
			logger.Info("using fast tokenizer (tokenizer.json)")
		}
	}
	if tokenizer == nil {
		tok, err := NewWordPieceTokenizer(paths.VocabFile, onnxMaxSeqLen)
		if err != nil {
			return nil, fmt.Errorf("onnx: load tokenizer: %w", err)
		}
		tokenizer = tok
		logger.Info("using wordpiece tokenizer (vocab.txt)")
	}

	inputNames := []string{"input_ids", "attention_mask", "token_type_ids"}
	outputNames := []string{"last_hidden_state"}

	var activeModel string
	if strings.Contains(paths.ModelFile, legacyModelName) {
		activeModel = legacyModelName
	} else {
		activeModel = onnxModelName
	}

	sessionOpts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("onnx: create session options: %w", err)
	}
	if err := sessionOpts.SetIntraOpNumThreads(1); err != nil {
		sessionOpts.Destroy()
		return nil, fmt.Errorf("onnx: set intra threads: %w", err)
	}
	if err := sessionOpts.SetInterOpNumThreads(1); err != nil {
		sessionOpts.Destroy()
		return nil, fmt.Errorf("onnx: set inter threads: %w", err)
	}

	session, err := ort.NewDynamicAdvancedSession(
		paths.ModelFile, inputNames, outputNames, sessionOpts,
	)
	sessionOpts.Destroy()
	if err != nil {
		return nil, fmt.Errorf("onnx: create session: %w", err)
	}

	logger.Info("ONNX embedder initialized", "model", activeModel, "dims", onnxModelDims)

	e := &ONNXEmbedder{
		session:   session,
		tokenizer: tokenizer,
		dims:      onnxModelDims,
		logger:    logger,
		modelName: activeModel,
		reqCh:     make(chan embedRequest),
	}

	go e.worker()

	return e, nil
}

func (e *ONNXEmbedder) worker() {
	for req := range e.reqCh {
		vecs, err := e.embedBatch(req.texts)
		req.resp <- embedResponse{vecs: vecs, err: err}
	}
}

func (e *ONNXEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += maxEmbedBatchSize {
		end := i + maxEmbedBatchSize
		if end > len(texts) {
			end = len(texts)
		}

		respCh := make(chan embedResponse, 1)
		select {
		case e.reqCh <- embedRequest{texts: texts[i:end], resp: respCh}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		select {
		case r := <-respCh:
			if r.err != nil {
				return nil, fmt.Errorf("onnx embed batch %d-%d: %w", i, end, r.err)
			}
			results = append(results, r.vecs...)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return results, nil
}

func (e *ONNXEmbedder) embedBatch(texts []string) ([][]float32, error) {
	batchSize := int64(len(texts))
	shape := ort.NewShape(batchSize, int64(onnxMaxSeqLen))
	outputShape := ort.NewShape(batchSize, int64(onnxMaxSeqLen), int64(onnxModelDims))

	inputIDs, err := ort.NewEmptyTensor[int64](shape)
	if err != nil {
		return nil, fmt.Errorf("create input_ids: %w", err)
	}
	defer inputIDs.Destroy()

	attentionMask, err := ort.NewEmptyTensor[int64](shape)
	if err != nil {
		return nil, fmt.Errorf("create attention_mask: %w", err)
	}
	defer attentionMask.Destroy()

	tokenTypeIDs, err := ort.NewEmptyTensor[int64](shape)
	if err != nil {
		return nil, fmt.Errorf("create token_type_ids: %w", err)
	}
	defer tokenTypeIDs.Destroy()

	output, err := ort.NewEmptyTensor[float32](outputShape)
	if err != nil {
		return nil, fmt.Errorf("create output: %w", err)
	}
	defer output.Destroy()

	idsData := inputIDs.GetData()
	maskData := attentionMask.GetData()
	typeData := tokenTypeIDs.GetData()

	masks := make([][]int64, len(texts))
	for i, text := range texts {
		if len(text) > maxEmbedTextLen {
			text = text[:maxEmbedTextLen]
		}
		ids, mask, typeIDs := e.tokenizer.Tokenize(text)
		masks[i] = mask
		base := i * onnxMaxSeqLen
		copy(idsData[base:base+onnxMaxSeqLen], ids)
		copy(maskData[base:base+onnxMaxSeqLen], mask)
		copy(typeData[base:base+onnxMaxSeqLen], typeIDs)
	}

	if err := e.session.Run(
		[]ort.Value{inputIDs, attentionMask, tokenTypeIDs},
		[]ort.Value{output},
	); err != nil {
		return nil, fmt.Errorf("session run: %w", err)
	}

	raw := output.GetData()
	results := make([][]float32, len(texts))
	for i := range texts {
		start := i * onnxMaxSeqLen * e.dims
		end := start + onnxMaxSeqLen*e.dims
		vec := meanPool(raw[start:end], masks[i], onnxMaxSeqLen, e.dims)
		l2Normalize(vec)
		result := make([]float32, len(vec))
		copy(result, vec)
		results[i] = result
	}
	return results, nil
}

func (e *ONNXEmbedder) Dimensions() int { return e.dims }
func (e *ONNXEmbedder) Name() string   { return "onnx" }
func (e *ONNXEmbedder) Model() string  { return e.modelName }

func (e *ONNXEmbedder) Close() error {
	close(e.reqCh)
	return nil
}

func meanPool(raw []float32, mask []int64, seqLen, dims int) []float32 {
	result := make([]float32, dims)
	var count float32
	for i := 0; i < seqLen; i++ {
		if mask[i] == 0 {
			continue
		}
		count++
		offset := i * dims
		for j := 0; j < dims; j++ {
			result[j] += raw[offset+j]
		}
	}
	if count > 0 {
		for j := range result {
			result[j] /= count
		}
	}
	return result
}

func l2Normalize(vec []float32) {
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	norm := float32(math.Sqrt(sum))
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
}

func resolveONNXPaths(modelDir string) *ONNXPaths {
	libDir := filepath.Join(filepath.Dir(modelDir), "lib")

	// Prefer new model directory; fall back to legacy if it already exists.
	modelSubDir := filepath.Join(modelDir, onnxModelName)
	if !fileExists(filepath.Join(modelSubDir, "model.onnx")) {
		legacyDir := filepath.Join(modelDir, legacyModelName)
		if fileExists(filepath.Join(legacyDir, "model.onnx")) {
			modelSubDir = legacyDir
		}
	}

	libName := "libonnxruntime.so"
	if runtime.GOOS == "darwin" {
		libName = "libonnxruntime.dylib"
	}

	return &ONNXPaths{
		RuntimeLib:    filepath.Join(libDir, libName),
		ModelFile:     filepath.Join(modelSubDir, "model.onnx"),
		VocabFile:     filepath.Join(modelSubDir, "vocab.txt"),
		TokenizerFile: filepath.Join(modelSubDir, "tokenizer.json"),
	}
}

func ensureONNXRuntime(paths *ONNXPaths, logger *slog.Logger) error {
	if _, err := os.Stat(paths.RuntimeLib); err == nil {
		return nil
	}

	logger.Info("downloading ONNX Runtime (first run)...", "version", onnxRuntimeVersion)
	if err := os.MkdirAll(filepath.Dir(paths.RuntimeLib), 0o755); err != nil {
		return err
	}

	goos := runtime.GOOS
	goarch := runtime.GOARCH
	if goos == "darwin" {
		goos = "osx" // ONNX Runtime uses "osx" not "darwin" for macOS archives
		goarch = "x64"
		if runtime.GOARCH == "arm64" {
			goarch = "arm64"
		}
	} else {
		goos = "linux"
		goarch = "x64"
	}

	url := fmt.Sprintf(onnxRuntimeURLTemplate, onnxRuntimeVersion, goos, goarch, onnxRuntimeVersion)
	return downloadAndExtractLib(url, paths.RuntimeLib, logger)
}

func ensureONNXModel(paths *ONNXPaths, logger *slog.Logger) error {
	isLegacy := strings.Contains(paths.ModelFile, legacyModelName)
	if isLegacy {
		if fileExists(paths.ModelFile) && (fileExists(paths.VocabFile) || fileExists(paths.TokenizerFile)) {
			return nil
		}
	} else {
		if fileExists(paths.ModelFile) && fileExists(paths.TokenizerFile) {
			return nil
		}
	}

	logger.Info("downloading ONNX model (first run)...", "model", onnxModelName)
	if err := os.MkdirAll(filepath.Dir(paths.ModelFile), 0o755); err != nil {
		return err
	}

	baseURL := onnxModelBaseURL + "/onnx"
	if isLegacy {
		baseURL = onnxLegacyModelBaseURL + "/onnx"
	}

	if !fileExists(paths.ModelFile) {
		modelURL := baseURL + "/model.onnx"
		if err := downloadFileWithProgress(modelURL, paths.ModelFile, logger); err != nil {
			return fmt.Errorf("download model: %w", err)
		}
	}

	if !fileExists(paths.TokenizerFile) {
		tokenizerURL := onnxModelBaseURL + "/tokenizer.json"
		if err := downloadFileWithProgress(tokenizerURL, paths.TokenizerFile, logger); err != nil {
			logger.Warn("tokenizer.json download failed, will use vocab.txt fallback", "error", err)
		}
	}

	if !fileExists(paths.VocabFile) {
		vocabURL := baseURL + "/vocab.txt"
		if err := downloadFileWithProgress(vocabURL, paths.VocabFile, logger); err != nil {
			if !fileExists(paths.TokenizerFile) {
				return fmt.Errorf("download vocab: %w", err)
			}
			logger.Warn("vocab.txt download failed, using tokenizer.json only", "error", err)
		}
	}

	return nil
}

func downloadFile(url, destPath string, logger *slog.Logger) error {
	return downloadFileWithProgress(url, destPath, logger)
}

func downloadFileWithProgress(url, destPath string, logger *slog.Logger) error {
	const maxRetries = 3
	const progressInterval = 10 * 1024 * 1024

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		logger.Info("downloading", "url", url, "dest", filepath.Base(destPath), "attempt", attempt)

		var existingSize int64
		tmpPath := destPath + ".download"
		if info, err := os.Stat(tmpPath); err == nil {
			existingSize = info.Size()
		}

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
			continue
		}
		if existingSize > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
		}

		client := &http.Client{Timeout: 10 * time.Minute}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
			continue
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
			continue
		}

		f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			resp.Body.Close()
			return err
		}
		if existingSize > 0 && resp.StatusCode == http.StatusPartialContent {
			f.Seek(existingSize, io.SeekStart)
		} else {
			f.Truncate(0)
			f.Seek(0, io.SeekStart)
			existingSize = 0
		}

		var totalWritten int64
		nextProgress := progressInterval
		buf := make([]byte, 32*1024)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				written, writeErr := f.Write(buf[:n])
				if writeErr != nil {
					f.Close()
					resp.Body.Close()
					os.Remove(tmpPath)
					lastErr = writeErr
					break
				}
				totalWritten += int64(written)
				if totalWritten+existingSize >= int64(nextProgress) {
					logger.Info("download progress",
						"file", filepath.Base(destPath),
						"bytes", fmt.Sprintf("%d MB", (totalWritten+existingSize)/1024/1024),
					)
					nextProgress += progressInterval
				}
			}
			if readErr == io.EOF {
				f.Close()
				resp.Body.Close()
				return os.Rename(tmpPath, destPath)
			}
			if readErr != nil {
				f.Close()
				resp.Body.Close()
				lastErr = readErr
				break
			}
		}

		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}

	return fmt.Errorf("download failed after %d attempts: %w", maxRetries, lastErr)
}

func downloadAndExtractLib(tgzURL, destPath string, logger *slog.Logger) error {
	tmpTgz := destPath + ".tgz"
	if err := downloadFile(tgzURL, tmpTgz, logger); err != nil {
		return err
	}
	defer os.Remove(tmpTgz)

	return extractLibFromTgz(tmpTgz, destPath)
}

func extractLibFromTgz(tgzPath, destPath string) error {
	f, err := os.Open(tgzPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		name := hdr.Name
		if !strings.Contains(name, "/lib/") {
			continue
		}
		base := filepath.Base(name)
		if !strings.HasPrefix(base, "libonnxruntime.so") && !strings.HasPrefix(base, "libonnxruntime.dylib") {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		tmpPath := destPath + ".extracting"
		out, err := os.Create(tmpPath)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, 200*1024*1024))
		out.Close()
		if err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("extract lib: %w", err)
		}
		if err := os.Chmod(tmpPath, 0o755); err != nil {
			os.Remove(tmpPath)
			return err
		}
		return os.Rename(tmpPath, destPath)
	}

	return fmt.Errorf("libonnxruntime not found in archive")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
