package jobs

import "testing"

// A model download can take long and must hold up nothing else: not
// translations, which have one worker, and not searches and grabs, which
// share two.
func TestModelDownloadHasAPoolOfItsOwn(t *testing.T) {
	p := kindPools["subtitle_model_download"]
	if p != PoolModels || p == PoolTranslate || p == PoolIO {
		t.Fatalf("subtitle_model_download runs in %q, want %q", p, PoolModels)
	}
	if DefaultPools[PoolModels] != 1 {
		t.Fatalf("%d workers for model downloads", DefaultPools[PoolModels])
	}
	found := false
	for _, x := range AllPools {
		found = found || x == PoolModels
	}
	if !found {
		t.Fatal("the pool is never started")
	}
}
