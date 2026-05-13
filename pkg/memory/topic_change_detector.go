package memory

import (
	"context"
	"sync"

	util "github.com/jholhewres/anchored/pkg/util"
)

type TopicChangeDetector struct {
	mu sync.RWMutex

	embedder       EmbeddingProvider
	entityDetector *EntityDetector
	lastQuery      string
	lastEmbedding  []float32
	lastEntities   map[string]bool

	entityOverlapThresh float32 // Jaccard >= this → same topic (default 0.3)
	cosineThresh        float32 // cosine >= this → same topic (default 0.5)
}

func NewTopicChangeDetector(embedder EmbeddingProvider, entityDetector *EntityDetector) *TopicChangeDetector {
	return &TopicChangeDetector{
		embedder:            embedder,
		entityDetector:      entityDetector,
		entityOverlapThresh: 0.3,
		cosineThresh:        0.5,
	}
}

func (d *TopicChangeDetector) Check(ctx context.Context, currentQuery string) (changed bool, err error) {
	d.mu.RLock()
	lastQuery := d.lastQuery
	lastEmbedding := d.lastEmbedding
	lastEntities := d.lastEntities
	d.mu.RUnlock()

	if lastQuery == "" {
		d.updateState(currentQuery, nil, nil)
		return false, nil
	}

	currentEntities := d.extractEntities(currentQuery)

	overlap := entityOverlapRatio(lastEntities, currentEntities)
	if overlap >= d.entityOverlapThresh {
		d.updateState(currentQuery, currentEntities, lastEmbedding)
		return false, nil
	}

	if d.embedder != nil {
		vecs, err := d.embedder.Embed(ctx, []string{currentQuery})
		if err != nil {
			d.updateState(currentQuery, currentEntities, nil)
			return true, nil
		}
		if len(vecs) > 0 && len(vecs[0]) > 0 && len(lastEmbedding) > 0 {
			sim := util.CosineSimilarity(vecs[0], lastEmbedding)
			if sim >= float64(d.cosineThresh) {
				d.updateState(currentQuery, currentEntities, vecs[0])
				return false, nil
			}
			d.updateState(currentQuery, currentEntities, vecs[0])
			return true, nil
		}
	}

	d.updateState(currentQuery, currentEntities, nil)
	return true, nil
}

func (d *TopicChangeDetector) Reset() {
	d.mu.Lock()
	d.lastQuery = ""
	d.lastEmbedding = nil
	d.lastEntities = nil
	d.mu.Unlock()
}

func (d *TopicChangeDetector) extractEntities(query string) map[string]bool {
	if d.entityDetector == nil {
		return nil
	}
	entities := d.entityDetector.Detect(query)
	if len(entities) == 0 {
		return nil
	}
	m := make(map[string]bool, len(entities))
	for _, e := range entities {
		m[normalizeEntity(e)] = true
	}
	return m
}

func (d *TopicChangeDetector) updateState(query string, entities map[string]bool, embedding []float32) {
	d.mu.Lock()
	d.lastQuery = query
	d.lastEntities = entities
	if embedding != nil {
		cp := make([]float32, len(embedding))
		copy(cp, embedding)
		d.lastEmbedding = cp
	}
	d.mu.Unlock()
}

func entityOverlapRatio(a, b map[string]bool) float32 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for k := range a {
		if b[k] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 1.0
	}
	return float32(intersection) / float32(union)
}

