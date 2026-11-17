package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A browser asks for a span from its start in whole milliseconds, which lands a few
// microseconds before the recording. In a gap the engine finds nothing there, so the
// request must start where the recording does.
func TestPlaybackStartsWhereTheRecordingDoes(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list":
			w.Write([]byte(`[{"start":"2026-11-10T07:20:24.759008-08:00","duration":275.73},{"start":"2026-11-10T07:25:00.660035-08:00","duration":41.55}]`))
		case "/get":
			asked = r.URL.RawQuery
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, PathName(1, "main")), 0o755)
	m, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	m.playbackAddr = strings.TrimPrefix(srv.URL, "http://")
	m.placements = map[int64]Placement{1: {Record: dir}}

	get := func(start string, seconds float64) (time.Time, string) {
		t.Helper()
		at, _ := time.Parse(time.RFC3339Nano, start)
		res, err := m.Playback(context.Background(), 1, at, seconds, "fmp4")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		q, _ := url.ParseQuery(asked)
		got, _ := time.Parse(time.RFC3339Nano, q.Get("start"))
		return got, q.Get("duration")
	}
	want, _ := time.Parse(time.RFC3339Nano, "2026-11-10T07:25:00.660035-08:00")
	if got, d := get("2026-11-10T15:25:00.660Z", 41.5); !got.Equal(want) || !strings.HasPrefix(d, "41.49") {
		t.Errorf("start in the gap: asked from %v for %s", got, d)
	}
	mid, _ := time.Parse(time.RFC3339Nano, "2026-11-10T15:25:10Z")
	if got, d := get("2026-11-10T15:25:10Z", 20); !got.Equal(mid) || d != "20" {
		t.Errorf("start inside a recording moved: %v for %s", got, d)
	}
}
