package util

import (
	"math"
	"testing"
)

const epsilon = 1e-9

func TestCosineSimilarity_IdenticalVectors(t *testing.T) {
	v := []float32{1.0, 2.0, 3.0}
	got := CosineSimilarity(v, v)
	if math.Abs(got-1.0) > epsilon {
		t.Fatalf("CosineSimilarity(v, v) = %f, want 1.0", got)
	}
}

func TestCosineSimilarity_OrthogonalVectors(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	got := CosineSimilarity(a, b)
	if math.Abs(got) > epsilon {
		t.Fatalf("CosineSimilarity(orthogonal) = %f, want 0.0", got)
	}
}

func TestCosineSimilarity_OppositeVectors(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{-1, -2, -3}
	got := CosineSimilarity(a, b)
	if math.Abs(got-(-1.0)) > epsilon {
		t.Fatalf("CosineSimilarity(opposite) = %f, want -1.0", got)
	}
}

func TestCosineSimilarity_ZeroVectors(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	got := CosineSimilarity(a, b)
	if math.Abs(got) > epsilon {
		t.Fatalf("CosineSimilarity(zero, non-zero) = %f, want 0.0", got)
	}

	got = CosineSimilarity(b, a)
	if math.Abs(got) > epsilon {
		t.Fatalf("CosineSimilarity(non-zero, zero) = %f, want 0.0", got)
	}

	got = CosineSimilarity(a, a)
	if math.Abs(got) > epsilon {
		t.Fatalf("CosineSimilarity(zero, zero) = %f, want 0.0", got)
	}
}

func TestCosineSimilarity_Symmetric(t *testing.T) {
	a := []float32{1.0, 2.0, 3.0}
	b := []float32{4.0, 5.0, 6.0}
	ab := CosineSimilarity(a, b)
	ba := CosineSimilarity(b, a)
	if math.Abs(ab-ba) > epsilon {
		t.Fatalf("CosineSimilarity(a,b)=%f, CosineSimilarity(b,a)=%f; should be symmetric", ab, ba)
	}
}

func TestCosineSimilarity_DifferentLengths(t *testing.T) {
	a := []float32{1, 2}
	b := []float32{1, 2, 3}
	got := CosineSimilarity(a, b)
	if got != 0 {
		t.Fatalf("CosineSimilarity with different lengths should return 0, got %f", got)
	}
}

func TestCosineSimilarity_Empty(t *testing.T) {
	got := CosineSimilarity([]float32{}, []float32{})
	if got != 0 {
		t.Fatalf("CosineSimilarity empty slices should return 0, got %f", got)
	}
}
