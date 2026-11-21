package n8n

import (
	"testing"
)

func TestEvolutionAPINode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		return 201, "application/json", map[string]interface{}{"key": map[string]interface{}{"id": "MSG1"}, "path": r.path}
	})
	key := headerSpy(f, "apikey")
	creds := map[string]map[string]interface{}{"evolutionApi": {"server-url": "https://evo.example.com/", "apikey": "evo-key"}}
	const typ = "n8n-nodes-evolution-api.evolutionApi"

	out := communityRun(t, f, "evolutionApi", typ, 1, map[string]interface{}{"resource": "messages-api", "instanceName": "main", "remoteJid": "5511999999999",
		"messageText": "Olá", "options_message": map[string]interface{}{"delay": 1000, "quoted": map[string]interface{}{"messageQuoted": map[string]interface{}{"messageId": "Q1"}},
			"mentions": map[string]interface{}{"mentionsSettings": map[string]interface{}{"mentioned": "5511, 5522@s.whatsapp.net"}}}}, creds, "", "")
	if out[0].JSON["success"] != true || asMap(out[0].JSON["data"])["path"] != "/message/sendText/main" || key("/message/sendText/main") != "evo-key" {
		t.Fatalf("send-text: %+v", out)
	}
	b := jsonBody(t, f.find(t, "POST", "/message/sendText/main"))
	if b["number"] != "5511999999999" || b["text"] != "Olá" || b["delay"] != 1000.0 || asMap(asMap(b["quoted"])["key"])["id"] != "Q1" ||
		len(asList(b["mentioned"])) != 2 || asList(b["mentioned"])[0] != "5511@s.whatsapp.net" {
		t.Fatalf("send-text body: %v", b)
	}

	communityRun(t, f, "evolutionApi", typ, 1, map[string]interface{}{"resource": "messages-api", "operation": "send-image", "instanceName": "main", "remoteJid": "55",
		"media": "https://img/x.png"}, creds, "", "")
	if b := jsonBody(t, f.find(t, "POST", "/message/sendMedia/main")); b["mediatype"] != "image" || b["mimetype"] != "image/jpeg" || b["fileName"] != "image.jpg" || b["caption"] != "" {
		t.Fatalf("send-image body: %v", b)
	}

	communityRun(t, f, "evolutionApi", typ, 1, map[string]interface{}{"resource": "chat-api", "operation": "get-media-base64", "instanceName": "main", "messageId": "M9", "convertToMp4": true}, creds, "", "")
	if b := jsonBody(t, f.find(t, "POST", "/chat/getBase64FromMediaMessage/main")); asMap(asMap(b["message"])["key"])["id"] != "M9" || b["convertToMp4"] != true {
		t.Fatalf("media base64 body: %v", b)
	}

	communityRun(t, f, "evolutionApi", typ, 1, map[string]interface{}{"resource": "groups-api", "operation": "fetch-groups", "instanceName": "main", "searchMethod": "fetchAll"}, creds, "", "")
	if q := f.find(t, "GET", "/group/fetchAllGroups/main").query; q != "getParticipants=false" {
		t.Fatalf("fetch groups query %q", q)
	}

	// Default resource/operation: create instance.
	communityRun(t, f, "evolutionApi", "n8n-nodes-evolution-api-en.evolutionApi", 1, map[string]interface{}{"instanceName": "new1"}, creds, "", "")
	if b := jsonBody(t, f.find(t, "POST", "/instance/create")); b["instanceName"] != "new1" || b["integration"] != "WHATSAPP-BAILEYS" {
		t.Fatalf("create instance body: %v", b)
	}

	communityRun(t, f, "evolutionApi", typ, 1, map[string]interface{}{"resource": "integrations-api", "operation": "evolution-bot", "instanceName": "main",
		"resourceForEvolutionBot": "changeStatusEvolutionBot", "remoteJid": "55@s.whatsapp.net", "status": "closed"}, creds, "", "")
	if b := jsonBody(t, f.find(t, "POST", "/evolutionBot/changeStatus/main")); b["status"] != "closed" {
		t.Fatalf("bot body: %v", b)
	}

	if b := importBlocked(t, typ, 1, map[string]interface{}{"resource": "integrations-api", "operation": "typebot"}); len(b) == 0 {
		t.Fatal("typebot should be blocked")
	}
}
