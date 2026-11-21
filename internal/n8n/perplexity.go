package n8n

import (
	"encoding/json"
	"strings"
)

// Perplexity (n8n-nodes-base.perplexity, versions 1–2: chat completions).

// perplexityOptions maps n8n option names to request fields; list options
// are comma-separated in n8n and JSON options are parsed.
var perplexityOptions = map[string]struct {
	field string
	kind  string // "", "list" or "json"
}{
	"disableSearch": {"disable_search", ""}, "enableSearchClassifier": {"enable_search_classifier", ""},
	"frequencyPenalty": {"frequency_penalty", ""}, "imageDomainFilter": {"image_domain_filter", "list"},
	"imageFormatFilter": {"image_format_filter", "list"}, "languagePreference": {"language_preference", ""},
	"lastUpdatedAfter": {"last_updated_after_filter", ""}, "lastUpdatedBefore": {"last_updated_before_filter", ""},
	"maxTokens": {"max_tokens", ""}, "temperature": {"temperature", ""}, "presencePenalty": {"presence_penalty", ""},
	"reasoningEffort": {"reasoning_effort", ""}, "responseFormat": {"response_format", "json"},
	"returnImages": {"return_images", ""}, "returnRelatedQuestions": {"return_related_questions", ""},
	"searchAfterDate": {"search_after_date_filter", ""}, "searchBeforeDate": {"search_before_date_filter", ""},
	"searchDomainFilter": {"search_domain_filter", "list"}, "searchLanguageFilter": {"search_language_filter", "list"},
	"searchMode": {"search_mode", ""}, "searchRecency": {"search_recency_filter", ""}, "stop": {"stop", "list"},
	"topK": {"top_k", ""}, "topP": {"top_p", ""}, "webSearchOptions": {"web_search_options", "json"},
}

func perplexityNode(ec *execCtx) ([][]Item, error) {
	c, err := ec.credential("perplexityApi")
	if err != nil {
		return nil, err
	}
	base := ec.apiBase("perplexity", credBaseURL(c, "https://api.perplexity.ai"))
	headers := map[string]string{"Authorization": "Bearer " + credString(c, "apiKey")}
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		if r, o := ec.resourceOp(i, "chat", "complete"); r != "chat" || o != "complete" {
			return nil, unsupportedOp("Perplexity", r, o)
		}
		model, _ := ec.param(i, "model", "sonar")
		mv, err := ec.param(i, "messages.message", nil)
		if err != nil {
			return nil, err
		}
		var msgs []interface{}
		for _, raw := range asList(mv) {
			m := asMap(raw)
			msgs = append(msgs, map[string]interface{}{"role": firstNonEmpty(toString(m["role"]), "user"), "content": toString(m["content"])})
		}
		body := map[string]interface{}{"model": rlValue(model), "messages": msgs}
		opts := fieldsOf(ec, i, "options")
		for k, v := range opts {
			o, ok := perplexityOptions[k]
			if !ok || v == nil || v == "" {
				continue
			}
			switch o.kind {
			case "list":
				v = splitList(toString(v))
			case "json":
				if s, ok := v.(string); ok {
					var parsed interface{}
					if err := json.Unmarshal([]byte(s), &parsed); err != nil {
						return nil, permanentError{"Perplexity: option " + k + " is not valid JSON"}
					}
					v = parsed
				}
			}
			body[o.field] = v
		}
		out, _, err := ec.apiCall("POST", strings.TrimRight(base, "/")+"/chat/completions", headers, body, "")
		if err != nil {
			return nil, err
		}
		if simplify, _ := ec.boolean(i, "simplify", false); simplify {
			msg := asMap(asMap(firstOf(asList(out["choices"])))["message"])
			return one(map[string]interface{}{"id": out["id"], "created": out["created"], "citations": out["citations"], "message": msg["content"]}), nil
		}
		return one(out), nil
	})
}

func firstOf(l []interface{}) interface{} {
	if len(l) == 0 {
		return nil
	}
	return l[0]
}
