package memory

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	util "github.com/jholhewres/anchored/pkg/util"
)

type EmbeddingCache struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewEmbeddingCache(db *sql.DB, logger *slog.Logger) *EmbeddingCache {
	logger = util.DefaultLogger(logger)
	return &EmbeddingCache{db: db, logger: logger}
}

func (c *EmbeddingCache) Get(ctx context.Context, text, model string) ([]float32, bool) {
	key := util.ContentHash(text)

	var data []byte
	err := c.db.QueryRowContext(ctx,
		"SELECT embedding FROM embedding_cache WHERE text_hash = ? AND model = ?",
		key, model,
	).Scan(&data)
	if err != nil {
		return nil, false
	}

	var qe QuantizedEmbedding
	if err := qe.UnmarshalBinary(data); err != nil {
		c.logger.Warn("failed to unmarshal cached embedding", "error", err)
		return nil, false
	}

	vec := qe.Dequantize()
	return vec, true
}

func (c *EmbeddingCache) Put(ctx context.Context, text, model string, vec []float32, quantize bool) error {
	key := util.ContentHash(text)

	var data []byte
	if quantize {
		qe := QuantizeFloat32(vec)
		bin, err := qe.MarshalBinary()
		if err != nil {
			return fmt.Errorf("quantize embedding: %w", err)
		}
		data = bin
	} else {
		data = Float32sToBlob(vec)
	}

	_, err := c.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO embedding_cache (text_hash, model, embedding) VALUES (?, ?, ?)`,
		key, model, data,
	)
	return err
}

func (c *EmbeddingCache) MigrateFromLegacy(currentModel string) int64 {
	var count int64
	err := c.db.QueryRow(
		"SELECT COUNT(*) FROM embedding_cache WHERE model = ?",
		legacyModelName,
	).Scan(&count)
	if err != nil || count == 0 {
		return 0
	}

	res, err := c.db.Exec(
		"DELETE FROM embedding_cache WHERE model = ?",
		legacyModelName,
	)
	if err != nil {
		c.logger.Warn("failed to migrate legacy embedding cache", "error", err)
		return 0
	}
	deleted, _ := res.RowsAffected()
	c.logger.Info("Model updated. Re-generating embeddings in background...",
		"deleted_cache_entries", deleted,
		"old_model", legacyModelName,
		"new_model", currentModel,
	)
	return deleted
}
