package podcasts

import (
	"encoding/json"
	"testing"
)

func episodeFromJSON(t *testing.T, js string) Episode {
	var e Episode
	if err := json.Unmarshal([]byte(js), &e); err != nil {
		t.Fatal(err)
	}
	return e
}
