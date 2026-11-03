package popnow

import (
	"encoding/json"
	"testing"
)

func TestEmbeddingOutputWithoutMedia(t *testing.T) {
	var out TaskOutput
	if err := json.Unmarshal([]byte(`{"embeddings":{"data":[{"index":0,"embedding":[0.25,-0.5]}],"inputTokens":7}}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.Embeddings == nil || out.Embeddings.InputTokens != 7 || len(out.Embeddings.Data) != 1 || out.Embeddings.Data[0].Embedding[1] != -0.5 || len(out.Assets) != 0 {
		t.Fatalf("vector output lost: %+v", out)
	}
}
