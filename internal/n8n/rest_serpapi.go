package n8n

import (
	"net/url"
)

// SerpApi Official (community node n8n-nodes-serpapi.serpApi). The operation
// is the SerpApi engine; every other parameter (and each field of Additional
// Fields) is a query parameter of the same name.

var serpAPIEngines = []string{"amazon", "baidu", "bing_images", "bing", "duckduckgo", "ebay", "facebook_profile", "google_ai_mode", "google_ai_overview",
	"google_autocomplete", "google_finance", "google_flights", "google_hotels", "google_images", "google_images_light", "google_immersive_product",
	"google_jobs", "google_lens", "google_light", "google_local", "google_maps", "google_maps_directions", "google_maps_reviews", "google_news",
	"google_news_light", "google_play_games", "google_play_product", "google_play", "google_product", "google_scholar", "google", "google_shopping",
	"google_travel_explore", "google_trends", "google_trends_trending_now", "google_videos", "youtube"}

// serpAPISkip are node parameters that are not query parameters.
var serpAPISkip = map[string]bool{"resource": true, "operation": true, "requestOptions": true, "additionalFields": true,
	"descriptionType": true, "toolDescription": true}

func init() {
	ops := map[string]*restOp{}
	for _, e := range serpAPIEngines {
		ops["search/"+e] = &restOp{path: "/search.json", build: serpAPIQuery(e)}
	}
	registerREST("n8n-nodes-serpapi.serpApi", &restService{
		defResource: "search",
		defOps:      map[string]string{"search": "google"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "serpApi", nil)
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("serpApi", "https://serpapi.com"), query: url.Values{"api_key": {credString(c, "apiKey")}}}, nil
		},
		ops: ops,
	})
}

func serpAPIQuery(engine string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		r.query.Set("engine", engine)
		for k := range ec.params {
			if serpAPISkip[k] {
				continue
			}
			v, err := ec.param(i, k, nil)
			if err != nil {
				return err
			}
			if !restEmpty(v) {
				r.query.Set(k, toString(rlValue(v)))
			}
		}
		for k, v := range fieldsOf(ec, i, "additionalFields") {
			if !restEmpty(v) {
				r.query.Set(k, toString(v))
			}
		}
		return nil
	}
}
