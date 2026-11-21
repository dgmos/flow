package n8n

import (
	"net/http"
	"net/url"
	"strings"
)

// Spotify (n8n-nodes-base.spotify), Web API v1.

func init() {
	ok := func(method, path string) *restOp { return &restOp{method: method, path: path, success: true} }
	list := func(path, key string, query string) *restOp {
		return &restOp{path: path, list: true, listKey: key, sizeParam: "limit", query: query, pager: spotifyNext(strings.Split(key, ".")[0])}
	}
	registerREST("spotify", &restService{
		defResource: "player",
		defOps: map[string]string{"player": "addSongToQueue", "album": "get", "artist": "get", "playlist": "add", "track": "get",
			"library": "getLikedTracks", "myData": "getFollowingArtists"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("spotifyOAuth2Api")
			if err != nil {
				return nil, err
			}
			return bearerClient(ec.apiBase("spotify", "https://api.spotify.com/v1"), c), nil
		},
		ops: map[string]*restOp{
			"player/pause":            ok("PUT", "/me/player/pause"),
			"player/nextSong":         ok("POST", "/me/player/next"),
			"player/previousSong":     ok("POST", "/me/player/previous"),
			"player/resume":           ok("PUT", "/me/player/play"),
			"player/startMusic":       {method: "PUT", path: "/me/player/play", body: "id:context_uri", success: true},
			"player/addSongToQueue":   {method: "POST", path: "/me/player/queue", query: "id:uri", success: true},
			"player/volume":           {method: "PUT", path: "/me/player/volume", query: "volumePercent:volume_percent", success: true},
			"player/currentlyPlaying": {path: "/me/player/currently-playing"},
			"player/recentlyPlayed":   list("/me/player/recently-played", "items", ""),

			"album/get":            {path: "/albums/{id}", build: spotifyID("album")},
			"album/getNewReleases": list("/browse/new-releases", "albums.items", "@filters *"),
			"album/getTracks":      withBuild(list("/albums/{id}/tracks", "items", ""), spotifyID("album")),
			"album/search":         list("/search", "albums.items", "query:q =type:album @filters *"),

			"artist/get":               {path: "/artists/{id}", build: spotifyID("artist")},
			"artist/getAlbums":         withBuild(list("/artists/{id}/albums", "items", ""), spotifyID("artist")),
			"artist/getRelatedArtists": {path: "/artists/{id}/related-artists", out: "artists", build: spotifyID("artist")},
			"artist/getTopTracks":      {path: "/artists/{id}/top-tracks", query: "country", out: "tracks", build: spotifyID("artist")},
			"artist/search":            list("/search", "artists.items", "query:q =limit:50 =type:artist @filters *"),

			"playlist/add":       {method: "POST", path: "/playlists/{id}/tracks", query: "trackID:uris @additionalFields position", build: spotifyID("playlist")},
			"playlist/delete":    {method: "DELETE", path: "/playlists/{id}/tracks", success: true, build: spotifyID("playlist")},
			"playlist/get":       {path: "/playlists/{id}", build: spotifyID("playlist")},
			"playlist/getTracks": withBuild(list("/playlists/{id}/tracks", "items", ""), spotifyID("playlist")),
			"playlist/getUserPlaylists": {path: "/me/playlists", list: true, listKey: "items", sizeParam: "limit", pager: spotifyNext("items"),
				post: func(_ *execCtx, _ int, v interface{}) (interface{}, error) {
					out := []interface{}{}
					for _, x := range asList(v) {
						if x != nil {
							out = append(out, x)
						}
					}
					return out, nil
				}},
			"playlist/create": {method: "POST", path: "/me/playlists", body: "name @additionalFields *"},
			"playlist/search": list("/search", "playlists.items", "query:q =type:playlist =limit:50 @filters *"),

			"track/get":              {path: "/tracks/{id}", build: spotifyID("track")},
			"track/getAudioFeatures": {path: "/audio-features/{id}", build: spotifyID("track")},
			"track/search":           list("/search", "tracks.items", "query:q =type:track =limit:50 @filters *"),

			"library/getLikedTracks":     list("/me/tracks", "items", ""),
			"myData/getFollowingArtists": list("/me/following", "artists.items", "=type:artist"),
		},
	})
}

func withBuild(op *restOp, b func(ec *execCtx, i int, r *restRequest) error) *restOp {
	op.build = b
	return op
}

// spotifyID puts the id, without its spotify:<kind>: prefix, in the path
// ({id} in the op's path) and builds the playlist/delete body.
func spotifyID(kind string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		v, _ := ec.param(i, "id", "")
		id := rlValue(v)
		r.path = strings.Replace(r.path, "/"+url.PathEscape(id), "/"+url.PathEscape(strings.TrimPrefix(id, "spotify:"+kind+":")), 1)
		if kind == "playlist" && r.method == "DELETE" {
			t, _ := ec.str(i, "trackID", "")
			r.body["tracks"] = []interface{}{map[string]interface{}{"uri": t}}
		}
		return nil
	}
}

// spotifyNext follows the next URL (top level or in the list's parent);
// searches stop at offset 1000 as n8n does.
func spotifyNext(parent string) restPager {
	return func(p *restPage, res interface{}, _ http.Header) bool {
		m := asMap(res)
		next := toString(m["next"])
		if next == "" || next == "<nil>" {
			next = toString(asMap(m[parent])["next"])
		}
		if next == "" || next == "<nil>" || (strings.Contains(next, "/search") && strings.Contains(next, "offset=1000")) {
			return false
		}
		p.url = next
		return true
	}
}
