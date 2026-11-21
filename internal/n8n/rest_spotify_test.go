package n8n

import (
	"strings"
	"testing"
)

func TestSpotifyNode(t *testing.T) {
	var f *fakeAPI
	f = newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer sp-tok" {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "POST" && r.path == "/playlists/PL1/tracks":
			return 201, "application/json", map[string]interface{}{"snapshot_id": "s1"}
		case r.method == "GET" && r.path == "/me/playlists":
			if strings.Contains(r.query, "offset=2") {
				return 200, "application/json", map[string]interface{}{"items": []interface{}{map[string]interface{}{"id": "p3"}, nil}, "next": nil}
			}
			return 200, "application/json", map[string]interface{}{"items": []interface{}{map[string]interface{}{"id": "p1"}, map[string]interface{}{"id": "p2"}},
				"next": f.srv.URL + "/me/playlists?offset=2&limit=2"}
		case r.method == "GET" && r.path == "/search":
			return 200, "application/json", map[string]interface{}{"tracks": map[string]interface{}{"items": []interface{}{map[string]interface{}{"id": "t1"}}, "next": nil}}
		case r.method == "POST" && r.path == "/me/player/next":
			return 204, "application/json", ""
		case r.method == "DELETE" && r.path == "/playlists/PL1/tracks":
			return 200, "application/json", map[string]interface{}{"snapshot_id": "s2"}
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	cred := map[string]map[string]interface{}{"spotifyOAuth2Api": {"oauthTokenData": map[string]interface{}{"access_token": "sp-tok"}}}

	// Default playlist operation is add; the spotify:playlist: prefix is stripped.
	out := restRun(t, f, "spotify", "spotify", map[string]interface{}{"resource": "playlist", "id": "spotify:playlist:PL1", "trackID": "spotify:track:T1",
		"additionalFields": map[string]interface{}{"position": 0}}, cred)
	if out[0]["snapshot_id"] != "s1" {
		t.Fatalf("add: %v", out)
	}
	if q := f.find(t, "POST", "/playlists/PL1/tracks").query; q != "position=0&uris=spotify%3Atrack%3AT1" {
		t.Fatalf("add query %q", q)
	}

	// getUserPlaylists follows next and drops null playlists.
	out = restRun(t, f, "spotify", "spotify", map[string]interface{}{"resource": "playlist", "operation": "getUserPlaylists", "returnAll": true}, cred)
	if len(out) != 3 || out[2]["id"] != "p3" {
		t.Fatalf("getUserPlaylists: %v", out)
	}
	out = restRun(t, f, "spotify", "spotify", map[string]interface{}{"resource": "playlist", "operation": "getUserPlaylists", "limit": 1}, cred)
	if len(out) != 1 {
		t.Fatalf("getUserPlaylists limit: %v", out)
	}

	out = restRun(t, f, "spotify", "spotify", map[string]interface{}{"resource": "track", "operation": "search", "query": "abba", "limit": 5,
		"filters": map[string]interface{}{"market": "US"}}, cred)
	if len(out) != 1 || out[0]["id"] != "t1" {
		t.Fatalf("search: %v", out)
	}
	if q := f.find(t, "GET", "/search").query; q != "limit=5&market=US&q=abba&type=track" {
		t.Fatalf("search query %q", q)
	}

	out = restRun(t, f, "spotify", "spotify", map[string]interface{}{"operation": "nextSong"}, cred)
	if out[0]["success"] != true {
		t.Fatalf("nextSong: %v", out)
	}
	out = restRun(t, f, "spotify", "spotify", map[string]interface{}{"resource": "playlist", "operation": "delete", "id": "PL1", "trackID": "spotify:track:T1"}, cred)
	if out[0]["success"] != true || mustJSON(jsonBody(t, f.find(t, "DELETE", "/playlists/PL1/tracks"))) != `{"tracks":[{"uri":"spotify:track:T1"}]}` {
		t.Fatalf("delete: %v", out)
	}
}
