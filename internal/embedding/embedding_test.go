package embedding

import (
	"context"
	"testing"
)

func TestCosineSimilarity_Identical(t *testing.T) {
	v := []float32{1, 0, 0}
	if got := cosineSimilarity(v, v); got != 1.0 {
		t.Errorf("cosineSimilarity(identical) = %v, want 1", got)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	if got := cosineSimilarity(a, b); got != 0.0 {
		t.Errorf("cosineSimilarity(orthogonal) = %v, want 0", got)
	}
}

func TestCosineSimilarity_Opposite(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{-1, 0, 0}
	if got := cosineSimilarity(a, b); got != -1.0 {
		t.Errorf("cosineSimilarity(opposite) = %v, want -1", got)
	}
}

func TestCosineSimilarity_LengthMismatch(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{1, 0, 0}
	if got := cosineSimilarity(a, b); got != 0.0 {
		t.Errorf("cosineSimilarity(length mismatch) = %v, want 0", got)
	}
}

func TestNorm_Zero(t *testing.T) {
	if got := norm([]float32{0, 0}); got != 0 {
		t.Errorf("norm(zeros) = %v, want 0", got)
	}
}

func TestNorm_Unit(t *testing.T) {
	v := []float32{1, 0, 0}
	if got := norm(v); got != 1.0 {
		t.Errorf("norm(1,0,0) = %v, want 1", got)
	}
}

func TestSortByScoreDesc(t *testing.T) {
	list := []scoredQuery{
		{sim: SimilarQuery{Name: "a"}, score: 0.1},
		{sim: SimilarQuery{Name: "b"}, score: 0.9},
		{sim: SimilarQuery{Name: "c"}, score: 0.5},
	}
	sortByScoreDesc(list)
	if list[0].sim.Name != "b" || list[1].sim.Name != "c" || list[2].sim.Name != "a" {
		t.Errorf("sortByScoreDesc: got %v, want b,c,a", list)
	}
}

func TestNewOllamaEmbedder_Defaults(t *testing.T) {
	e := NewOllamaEmbedder("", "")
	if e == nil {
		t.Fatal("NewOllamaEmbedder returned nil")
	}
	// Embed will fail without a real server; we just check we get an error, not panic
	_, err := e.Embed(context.Background(), "hello")
	if err == nil {
		t.Log("Embed succeeded (Ollama may be running); skipping")
	}
}
