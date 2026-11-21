package n8n

import (
	"regexp"
	"strings"
)

// Rapiwa WhatsApp API (community node n8n-nodes-rapiwa.rapiwa).

var rapiwaNonDigit = regexp.MustCompile(`\D`)

var rapiwaMime = map[string]string{"image": "image/jpeg", "video": "video/mp4", "document": "application/pdf"}

func init() {
	registerREST("n8n-nodes-rapiwa.rapiwa", &restService{
		defOps: map[string]string{"": "sendWhatsAppMessage"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "rapiwaApi", nil)
			if err != nil {
				return nil, err
			}
			return bearerClient(ec.apiBase("rapiwa", "https://app.rapiwa.com/api"), c, "apiKey"), nil
		},
		ops: map[string]*restOp{
			"/verifyWhatsAppNumber": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				a := &cmtyArgs{ec: ec, i: i}
				raw := a.str("number", "")
				if a.err != nil {
					return nil, a.err
				}
				if strings.HasSuffix(raw, "@lid") {
					return restItems(map[string]interface{}{"success": true, "number": raw, "exists": true,
						"message": "Skipped verification — already verified (ends with @lid)", "jid": raw}), nil
				}
				number := rapiwaNonDigit.ReplaceAllString(raw, "")
				v, err := cmtyCall(ec, c, "POST", "/verify-whatsapp", nil, map[string]interface{}{"number": number})
				if err != nil {
					return nil, err
				}
				res := asMap(v)
				d := asMap(res["data"])
				out := map[string]interface{}{"success": toBool(res["success"]), "number": number, "exists": toBool(d["exists"]),
					"message": d["message"], "jid": nil}
				if n := toString(d["number"]); n != "" {
					out["number"] = d["number"]
				}
				if j := toString(d["jid"]); j != "" {
					out["jid"] = d["jid"]
				}
				return restItems(out), nil
			}},
			"/sendWhatsAppMessage": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				a := &cmtyArgs{ec: ec, i: i}
				raw := a.str("number", "")
				number := raw
				if !strings.HasSuffix(raw, "@lid") {
					number = rapiwaNonDigit.ReplaceAllString(raw, "")
				}
				typ := a.str("messageType", "text")
				body := map[string]interface{}{"number": number, "message_type": typ, "productOperation": a.str("productOperation", "welcome")}
				switch typ {
				case "text":
					body["message"] = a.str("message", "Hello from Rapiwa!")
				case "image", "video":
					body["message"] = a.str("message", "Hello from Rapiwa!")
					body["media_url"] = a.str("mediaUrl", "")
					body["mimetype"] = rapiwaMime[typ]
				case "document":
					u := a.str("mediaUrl", "")
					name := u[strings.LastIndex(u, "/")+1:]
					if name == "" {
						name = "file.pdf"
					}
					body["media_url"], body["file_name"], body["mimetype"] = u, name, rapiwaMime[typ]
				}
				if a.err != nil {
					return nil, a.err
				}
				v, err := cmtyCall(ec, c, "POST", "/send-message", nil, body)
				if err != nil {
					return nil, err
				}
				if m := asMap(v); m["success"] == false {
					return nil, permanentError{"Rapiwa: " + firstNonEmpty(toString(m["error"]), "Rapiwa API returned success: false")}
				}
				return restItems(v), nil
			}},
		},
	})
}
