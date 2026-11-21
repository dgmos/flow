package n8n

import (
	"strings"
	"testing"
)

func TestElevenLabsNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch {
		case strings.HasPrefix(r.path, "/text-to-speech/"), strings.HasPrefix(r.path, "/speech-to-speech/"):
			return 200, "audio/mpeg", "MP3DATA"
		case r.path == "/speech-to-text":
			return 200, "application/json", map[string]interface{}{"text": "hello world", "language_code": "en"}
		case r.path == "/voices":
			return 200, "application/json", map[string]interface{}{"voices": []interface{}{
				map[string]interface{}{"voice_id": "v1", "name": "Rachel", "category": "premade", "labels": map[string]interface{}{"accent": "american"}, "extra": 1},
				map[string]interface{}{"voice_id": "v2", "name": "Mine", "category": "cloned", "labels": map[string]interface{}{"use case": "news"}},
			}}
		}
		return 404, "application/json", map[string]interface{}{"detail": "not found"}
	})
	key := headerSpy(f, "xi-api-key")
	creds := map[string]map[string]interface{}{"elevenLabsApi": {"xiApiKey": "xi-1"}}
	const official = "@elevenlabs/n8n-nodes-elevenlabs.elevenLabs"

	out := communityRun(t, f, "elevenLabs", official, 1, map[string]interface{}{"resource": "speech", "text": "Hi",
		"voice":             map[string]interface{}{"__rl": true, "mode": "list", "value": "v1"},
		"additionalOptions": map[string]interface{}{"model": map[string]interface{}{"__rl": true, "mode": "id", "value": "eleven_flash_v2_5"}, "languageCode": "de", "outputFormat": "mp3_22050_32"}}, creds, "", "")
	bin := asMap(out[0].Binary["data"])
	if len(out) != 1 || bin["fileName"] != "audio.textToSpeech.mp3" || bin["mimeType"] != "audio/mp3" || out[0].JSON["content-type"] != "audio/mpeg" {
		t.Fatalf("tts: %+v", out)
	}
	tts := f.find(t, "POST", "/text-to-speech/v1")
	if b := jsonBody(t, tts); b["text"] != "Hi" || b["model_id"] != "eleven_flash_v2_5" || b["language_code"] != "de" || tts.query != "output_format=mp3_22050_32" || key("/text-to-speech/v1") != "xi-1" {
		t.Fatalf("tts request: %+v", tts)
	}

	out = communityRun(t, f, "elevenLabs", official, 1, map[string]interface{}{"resource": "speech", "operation": "speechToText",
		"additionalOptions": map[string]interface{}{"diarize": true}}, creds, "RIFFWAVE", "a.wav")
	if out[0].JSON["text"] != "hello world" {
		t.Fatalf("stt: %+v", out)
	}
	stt := f.find(t, "POST", "/speech-to-text")
	if !strings.HasPrefix(stt.ctype, "multipart/form-data") || !strings.Contains(stt.body, "scribe_v1") || !strings.Contains(stt.body, "RIFFWAVE") || !strings.Contains(stt.body, `name="diarize"`) {
		t.Fatalf("stt request: %+v", stt)
	}

	out = communityRun(t, f, "elevenLabs", official, 1, map[string]interface{}{"operation": "getAll", "limit": 1, "simplify": true}, creds, "", "")
	if len(out) != 1 || out[0].JSON["name"] != "Rachel" || out[0].JSON["extra"] != nil || f.find(t, "GET", "/voices").query != "page_size=1" {
		t.Fatalf("voices: %+v", out)
	}

	// The older community package: default speech/text-to-speech.
	const legacy = "n8n-nodes-elevenlabs.elevenLabs"
	out = communityRun(t, f, "elevenLabs", legacy, 1, map[string]interface{}{"text": "Yo", "model_id": "eleven_turbo_v2",
		"voice_id":         map[string]interface{}{"__rl": true, "mode": "id", "value": "v9"},
		"additionalFields": map[string]interface{}{"binary_name": "audio", "file_name": "out.mp3", "output_format": "mp3_44100_64"}}, creds, "", "")
	if asMap(out[0].Binary["audio"])["fileName"] != "out.mp3" {
		t.Fatalf("legacy tts: %+v", out)
	}
	lt := f.find(t, "POST", "/text-to-speech/v9")
	if b := jsonBody(t, lt); b["model_id"] != "eleven_turbo_v2" || asMap(b["voice_settings"])["stability"] != 0.5 || lt.query != "output_format=mp3_44100_64" {
		t.Fatalf("legacy tts request: %+v", lt)
	}
	out = communityRun(t, f, "elevenLabs", legacy, 1, map[string]interface{}{"resource": "voice"}, creds, "", "")
	if len(out) != 1 || out[0].JSON["use_case"] != "news" {
		t.Fatalf("legacy voices: %+v", out)
	}

	if _, ok := integrationToolBase(official + "Tool"); !ok {
		t.Fatal("tool variant not supported")
	}
	if b := importBlocked(t, legacy, 1, map[string]interface{}{"resource": "voice", "operation": "generate-voice"}); len(b) == 0 {
		t.Fatal("generate-voice should be blocked")
	}
}
