package n8n

import (
	"strings"
)

// ElevenLabs: the official package (@elevenlabs/n8n-nodes-elevenlabs) and
// the older community package (n8n-nodes-elevenlabs). Both use the
// elevenLabsApi credential (xi-api-key header).

func init() {
	client := func(ec *execCtx, i int) (*restClient, error) {
		c, err := ec.credential("elevenLabsApi")
		if err != nil {
			return nil, err
		}
		return &restClient{base: ec.apiBase("elevenLabs", "https://api.elevenlabs.io/v1"), headers: map[string]string{"xi-api-key": credString(c, "xiApiKey")}}, nil
	}
	simplify := func(keys ...string) func(ec *execCtx, i int, v interface{}) (interface{}, error) {
		return func(ec *execCtx, i int, v interface{}) (interface{}, error) {
			if s, _ := ec.boolean(i, "simplify", false); !s {
				return v, nil
			}
			pickKeys := func(x interface{}) interface{} {
				m, out := asMap(x), map[string]interface{}{}
				for _, k := range keys {
					out[k] = m[k]
				}
				return out
			}
			if l, ok := v.([]interface{}); ok {
				out := make([]interface{}, len(l))
				for k, x := range l {
					out[k] = pickKeys(x)
				}
				return out, nil
			}
			return pickKeys(v), nil
		}
	}
	registerREST("@elevenlabs/n8n-nodes-elevenlabs.elevenLabs", &restService{
		defResource: "voice",
		defOps:      map[string]string{"voice": "get", "speech": "textToSpeech"},
		client:      client,
		ops: map[string]*restOp{
			"voice/get":    {path: "/voices/{voice}", post: simplify("voice_id", "name", "category", "labels", "preview_url")},
			"voice/getAll": {path: "/voices", list: true, listKey: "voices", sizeParam: "page_size", post: simplify("voice_id", "name", "category", "labels", "preview_url")},
			"voice/delete": {method: "DELETE", path: "/voices/{voice}"},
			"voice/createClone": {run: func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
				return elevenClone(ec, i, it, c, "audioFiles", "")
			}},
			"speech/textToSpeech":   {run: elevenTTS},
			"speech/speechToText":   {run: elevenSTT},
			"speech/speechToSpeech": {run: elevenSTS},
		},
	})

	registerREST("n8n-nodes-elevenlabs.elevenLabs", &restService{
		defResource: "speech",
		defOps: map[string]string{"speech": "text-to-speech", "voice": "list-voices", "history": "get-history",
			"user": "get-user-info"},
		client: client,
		ops: map[string]*restOp{
			"speech/text-to-speech":   {run: elevenLegacyTTS},
			"speech/speech-to-speech": {run: elevenLegacySTS},
			"voice/list-voices": {path: "/voices", out: "voices", post: func(ec *execCtx, i int, v interface{}) (interface{}, error) {
				premade, _ := ec.boolean(i, "includePremade", false)
				simple, _ := ec.boolean(i, "simplify", true)
				var out []interface{}
				for _, x := range asList(v) {
					m := asMap(x)
					if !premade && m["category"] == "premade" {
						continue
					}
					if simple {
						l := asMap(m["labels"])
						m = map[string]interface{}{"voice_id": m["voice_id"], "name": m["name"], "accent": l["accent"], "age": l["age"],
							"gender": l["gender"], "use_case": l["use case"], "high_quality_base_model_ids": m["high_quality_base_model_ids"]}
					}
					out = append(out, m)
				}
				return out, nil
			}},
			"voice/get-voice-details": {path: "/voices/{voice_id}", query: "with_settings:with_settings:bool"},
			"voice/delete-voice":      {method: "DELETE", path: "/voices/{voice_id}"},
			"voice/create-voice": {method: "POST", path: "/voice-generation/create-voice", body: "name:voice_name generated_voice_id",
				build: func(ec *execCtx, i int, r *restRequest) error {
					desc, _ := ec.str(i, "additionalFields.description", "")
					r.body["voice_description"] = firstNonEmpty(desc, "Generated with n8n")
					labels := map[string]interface{}{}
					if s, _ := ec.str(i, "additionalFields.labels", ""); s != "" {
						_ = jsonUnmarshal([]byte(s), &labels)
					}
					r.body["labels"] = labels
					return nil
				}},
			"voice/clone-voice": {run: func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
				return elevenClone(ec, i, it, c, "", "Generated with n8n")
			}},
			"history/get-history": {path: "/history", out: "history", query: "page_size last_history_item_id voice_id:voice_id:rl"},
			"history/get-history-audio": {run: func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
				path, err := ec.restPath(i, "/history/{history_item_id}/audio")
				if err != nil {
					return nil, err
				}
				return elevenAudio(ec, i, c, &restRequest{method: "GET", path: path}, elevenLegacyName(ec, i))
			}},
			"history/delete-history-item": {method: "DELETE", path: "/history/{history_item_id}"},
			"user/get-user-info":          {path: "/user"},
			"user/get-user-subscription":  {path: "/user/subscription"},
		},
	})
}

// elevenAudio sends r and returns the audio as binary data with the
// response headers as JSON. name is {property, fileName}.
func elevenAudio(ec *execCtx, i int, c *restClient, r *restRequest, name [2]string) ([]Item, error) {
	if r.query == nil {
		r.query = map[string][]string{}
	}
	r.headers = map[string]string{"Accept": "audio/mpeg"}
	res, err := cnRaw(ec, c, r)
	if err != nil {
		return nil, err
	}
	return []Item{cnBinaryItem(cnHeaders(res.header), name[0], res.body, "audio/mp3", name[1])}, nil
}

func elevenTTS(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	path, err := ec.restPath(i, "/text-to-speech/{voice}")
	if err != nil {
		return nil, err
	}
	r := &restRequest{method: "POST", path: path, query: map[string][]string{}, body: map[string]interface{}{}}
	if err := ec.restFields(i, "text @additionalOptions model:model_id:rl", r.body); err != nil {
		return nil, err
	}
	opts := fieldsOf(ec, i, "additionalOptions")
	if f := toString(opts["outputFormat"]); f != "" {
		r.query.Set("output_format", f)
	}
	if m := toString(r.body["model_id"]); strings.Contains(m, "turbo_v2") || strings.Contains(m, "flash_v2") {
		if l := toString(opts["languageCode"]); l != "" {
			r.body["language_code"] = l
		}
	}
	if vs := toString(opts["voiceSettings"]); strings.TrimSpace(vs) != "" {
		var x interface{}
		if err := jsonUnmarshal([]byte(vs), &x); err != nil {
			return nil, permanentError{ec.node.Name + ": Voice Settings is not valid JSON"}
		}
		r.body["voice_settings"] = x
	}
	return elevenAudio(ec, i, c, r, [2]string{"data", "audio.textToSpeech.mp3"})
}

func elevenSTT(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	prop, _ := ec.str(i, "file", "data")
	data, mt, name, err := binaryOf(it, prop)
	if err != nil {
		return nil, err
	}
	opts := fieldsOf(ec, i, "additionalOptions")
	f := newCNForm()
	f.file("file", name, mt, data)
	f.field("model_id", firstNonEmpty(rlValue(opts["model"]), "scribe_v1"))
	if v := toString(opts["languageCode"]); v != "" {
		f.field("language_code", v)
	}
	if v := opts["numberOfSpeakers"]; !restEmpty(v) {
		f.field("num_speakers", v)
	}
	if v, ok := opts["diarize"]; ok {
		f.field("diarize", toBool(v))
	}
	v, err := c.do(ec, f.request("POST", "/speech-to-text"))
	if err != nil {
		return nil, err
	}
	return restItems(v), nil
}

func elevenSTS(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	path, err := ec.restPath(i, "/speech-to-speech/{voice}")
	if err != nil {
		return nil, err
	}
	prop, _ := ec.str(i, "file", "data")
	data, mt, name, err := binaryOf(it, prop)
	if err != nil {
		return nil, err
	}
	opts := fieldsOf(ec, i, "additionalOptions")
	f := newCNForm()
	f.file("audio", name, mt, data)
	f.field("model_id", firstNonEmpty(rlValue(opts["model"]), "eleven_english_sts_v2"))
	f.field("voice_settings", firstNonEmpty(toString(opts["voiceSettings"]), "{}"))
	r := f.request("POST", path)
	if v := toString(opts["outputFormat"]); v != "" {
		r.query.Set("output_format", v)
	}
	return elevenAudio(ec, i, c, r, [2]string{"data", "audio.speechToSpeech.mp3"})
}

// elevenClone adds a voice from audio samples (POST /voices/add); n8n
// returns the full response.
func elevenClone(ec *execCtx, i int, it Item, c *restClient, fileParam, defDesc string) ([]Item, error) {
	prop := "data"
	if fileParam != "" {
		prop, _ = ec.str(i, fileParam, "data")
	}
	data, mt, fname, err := binaryOf(it, prop)
	if err != nil {
		return nil, err
	}
	name, _ := ec.str(i, "name", "")
	desc, _ := ec.str(i, "additionalFields.description", defDesc)
	labels, _ := ec.str(i, "additionalFields.labels", "{}")
	f := newCNForm()
	f.field("name", name)
	f.field("description", desc)
	f.field("labels", firstNonEmpty(labels, "{}"))
	f.file("files", fname, mt, data)
	res, err := cnRaw(ec, c, f.request("POST", "/voices/add"))
	if err != nil {
		return nil, err
	}
	out := cnFullResponse(res)
	if fileParam == "" {
		// The legacy node adds name and parsed labels to the response.
		out["name"] = name
		var l interface{} = map[string]interface{}{}
		_ = jsonUnmarshal([]byte(firstNonEmpty(labels, "{}")), &l)
		out["labels"] = l
	}
	return []Item{{JSON: out}}, nil
}

func elevenLegacyName(ec *execCtx, i int) [2]string {
	prop, _ := ec.str(i, "additionalFields.binary_name", "data")
	name, _ := ec.str(i, "additionalFields.file_name", "voice")
	return [2]string{firstNonEmpty(prop, "data"), firstNonEmpty(name, "voice")}
}

func elevenLegacyQuery(ec *execCtx, i int, r *restRequest) error {
	q := map[string]interface{}{}
	if err := ec.restFields(i, "@additionalFields optimize_streaming_latency output_format", q); err != nil {
		return err
	}
	for k, v := range q {
		r.query.Set(k, toString(v))
	}
	return nil
}

func elevenLegacyTTS(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	path, err := ec.restPath(i, "/text-to-speech/{voice_id}")
	if err != nil {
		return nil, err
	}
	af := fieldsOf(ec, i, "additionalFields")
	num := func(k string, def float64) interface{} {
		if f, ok := toFloat(af[k]); ok {
			return f
		}
		return def
	}
	text, _ := ec.str(i, "text", "")
	body := map[string]interface{}{"text": text}
	// Some templates carry voice_settings / model_id as top-level JSON.
	if vs, _ := ec.param(i, "voice_settings", nil); !restEmpty(vs) {
		if s, ok := vs.(string); ok {
			var x interface{}
			if jsonUnmarshal([]byte(s), &x) == nil {
				vs = x
			}
		}
		body["voice_settings"] = vs
	} else {
		boost := true
		if v, ok := af["use_speaker_boost"]; ok {
			boost = toBool(v)
		}
		body["voice_settings"] = map[string]interface{}{"stability": num("stability", 0.5), "similarity_boost": num("similarity_boost", 1),
			"style": num("style", 0), "use_speaker_boost": boost}
	}
	model := toString(af["model_id"])
	if m, _ := ec.str(i, "model_id", ""); m != "" {
		model = m
	}
	if toBool(af["stitching"]) {
		if s, ok := toFloat(af["seed"]); ok && s != 0 {
			body["seed"] = s
		}
		if model != "" {
			body["model_id"] = model
		}
		items := ec.items()
		if i > 0 && i-1 < len(items) {
			body["previous_text"] = items[i-1].JSON["text"]
		}
		if i < len(items)-1 {
			body["next_text"] = items[i+1].JSON["text"]
		}
	} else if model != "" {
		body["model_id"] = model
	}
	r := &restRequest{method: "POST", path: path, query: map[string][]string{}, body: body}
	if err := elevenLegacyQuery(ec, i, r); err != nil {
		return nil, err
	}
	return elevenAudio(ec, i, c, r, elevenLegacyName(ec, i))
}

func elevenLegacySTS(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	path, err := ec.restPath(i, "/speech-to-speech/{voice_id}")
	if err != nil {
		return nil, err
	}
	data, mt, name, err := binaryOf(it, "data")
	if err != nil {
		return nil, err
	}
	f := newCNForm()
	f.file("audio", name, mt, data)
	r := f.request("POST", path)
	if err := elevenLegacyQuery(ec, i, r); err != nil {
		return nil, err
	}
	return elevenAudio(ec, i, c, r, elevenLegacyName(ec, i))
}
