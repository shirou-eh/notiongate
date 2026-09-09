package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDebugGoodStream(t *testing.T) {
	env := setupE2E(t)
	httpReq, _ := http.NewRequest("POST", env.notion.URL+"/api/v3/runInferenceTranscript", strings.NewReader(`{"spaceId":"sp-good"}`))
	httpReq.Header.Set("Cookie", "token_v2=tokgood; notion_user_id=u-good")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	t.Logf("direct mock status=%d body=%q", resp.StatusCode, string(b))
}
