package n8n

import (
	"net/url"
	"strings"
)

// Evolution API (n8n-nodes-evolution-api and its English twin
// n8n-nodes-evolution-api-en): self-hosted WhatsApp gateway. The server URL
// comes from the credential. Outputs are {success: true, data: <response>}.
// Except send-text and send-image, the package reads parameters of the first
// item and calls the API once per execution; that is mirrored here.
// Not supported: Chatwoot/Typebot/Dify/Flowise integrations and RabbitMQ.

// evoReq describes one call: method, path (instance appended by callers) and body.
type evoReq struct {
	method, path string
	query        url.Values
	body         map[string]interface{}
}

type evoParams struct {
	ec *execCtx
	i  int
}

func (p evoParams) str(name, def string) string {
	v, _ := p.ec.param(p.i, name, def)
	if v == nil {
		return def
	}
	return rlValue(v)
}

func (p evoParams) val(name string, def interface{}) interface{} {
	v, _ := p.ec.param(p.i, name, def)
	if v == nil {
		return def
	}
	return v
}

func (p evoParams) inst() string { return url.PathEscape(p.str("instanceName", "")) }

// jid appends @s.whatsapp.net when the number has no domain.
func evoJID(s string, anyDomain bool) string {
	if anyDomain && strings.Contains(s, "@") || strings.Contains(s, "@s.whatsapp.net") || strings.Contains(s, "@g.us") {
		return s
	}
	return s + "@s.whatsapp.net"
}

func evoCSV(s string) []interface{} {
	var out []interface{}
	for _, x := range strings.Split(s, ",") {
		out = append(out, strings.TrimSpace(x))
	}
	return out
}

// evoMessageOptions applies options_message (delay, quoted, mentions, link preview).
func evoMessageOptions(p evoParams, body map[string]interface{}, mentions, linkPreview bool) {
	o := asMap(p.val("options_message", map[string]interface{}{}))
	if d := o["delay"]; d != nil && d != "" && d != 0.0 {
		body["delay"] = d
	}
	if linkPreview {
		if v, ok := o["linkPreview"]; ok {
			body["linkPreview"] = v
		}
	}
	if id := asMap(asMap(o["quoted"])["messageQuoted"])["messageId"]; id != nil && id != "" {
		body["quoted"] = map[string]interface{}{"key": map[string]interface{}{"id": id}}
	}
	if !mentions {
		return
	}
	if ms := asMap(asMap(o["mentions"])["mentionsSettings"]); len(ms) > 0 {
		if toBool(ms["mentionsEveryOne"]) {
			body["mentionsEveryOne"] = true
		} else if m := toString(ms["mentioned"]); m != "" {
			var l []interface{}
			for _, n := range evoCSV(m) {
				l = append(l, evoJID(toString(n), false))
			}
			body["mentioned"] = l
		}
	}
}

func evoMedia(kind, mime, name string) func(p evoParams) (*evoReq, error) {
	return func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"number": p.str("remoteJid", ""), "mediatype": kind, "media": p.str("media", ""),
			"mimetype": firstNonEmpty(p.str("mimetype", mime), mime), "caption": p.str("caption", ""), "fileName": firstNonEmpty(p.str("fileName", name), name)}
		evoMessageOptions(p, body, true, false)
		return &evoReq{"POST", "/message/sendMedia/" + p.inst(), nil, body}, nil
	}
}

var evoOps = map[string]func(p evoParams) (*evoReq, error){
	// messages
	"messages-api/send-text": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"number": p.str("remoteJid", ""), "text": p.str("messageText", "")}
		evoMessageOptions(p, body, true, true)
		return &evoReq{"POST", "/message/sendText/" + p.inst(), nil, body}, nil
	},
	"messages-api/send-image":    evoMedia("image", "image/jpeg", "image.jpg"),
	"messages-api/send-video":    evoMedia("video", "video/mp4", "video.mp4"),
	"messages-api/send-document": evoMedia("document", "application/pdf", "document.pdf"),
	"messages-api/send-audio": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"number": p.str("remoteJid", ""), "audio": p.str("media", ""),
			"mimetype": firstNonEmpty(p.str("mimetype", ""), "audio/mp3"), "fileName": firstNonEmpty(p.str("fileName", ""), "audio.mp3")}
		evoMessageOptions(p, body, true, false)
		return &evoReq{"POST", "/message/sendWhatsAppAudio/" + p.inst(), nil, body}, nil
	},
	"messages-api/send-poll": func(p evoParams) (*evoReq, error) {
		var values []interface{}
		for _, o := range asList(p.val("options_display.metadataValues", nil)) {
			values = append(values, asMap(o)["optionValue"])
		}
		if len(values) == 0 {
			return nil, permanentError{p.ec.node.Name + ": the poll needs at least one option"}
		}
		body := map[string]interface{}{"number": p.str("remoteJid", ""), "name": p.str("caption", ""), "selectableCount": 1, "values": values}
		evoMessageOptions(p, body, false, false)
		return &evoReq{"POST", "/message/sendPoll/" + p.inst(), nil, body}, nil
	},
	"messages-api/send-contact": func(p evoParams) (*evoReq, error) {
		var contacts []interface{}
		for _, x := range asList(p.val("contacts.contactValues", nil)) {
			c := asMap(x)
			m := map[string]interface{}{"fullName": c["fullName"], "wuid": c["wuid"], "phoneNumber": c["phoneNumber"]}
			for _, k := range []string{"organization", "email", "url"} {
				if !restEmpty(c[k]) {
					m[k] = c[k]
				}
			}
			contacts = append(contacts, m)
		}
		if len(contacts) == 0 {
			return nil, permanentError{p.ec.node.Name + ": at least one contact is required"}
		}
		return &evoReq{"POST", "/message/sendContact/" + p.inst(), nil, map[string]interface{}{"number": p.str("remoteJid", ""), "contact": contacts}}, nil
	},
	"messages-api/send-list": func(p evoParams) (*evoReq, error) {
		o := asMap(p.val("options_message", map[string]interface{}{}))
		var sections []interface{}
		for _, x := range asList(p.val("sections.sectionValues", nil)) {
			s := asMap(x)
			var rows []interface{}
			for _, r := range asList(asMap(s["rows"])["rowValues"]) {
				rm := asMap(r)
				rows = append(rows, map[string]interface{}{"title": rm["title"], "description": toString(pick(rm, "description")),
					"rowId": firstNonEmpty(toString(pick(rm, "rowId")), toString(s["title"])+"_"+toString(rm["title"]))})
			}
			sections = append(sections, map[string]interface{}{"title": s["title"], "rows": rows})
		}
		if len(sections) == 0 {
			return nil, permanentError{p.ec.node.Name + ": at least one section is required"}
		}
		body := map[string]interface{}{"number": p.str("remoteJid", ""), "title": p.str("title", ""), "description": p.str("description", ""),
			"buttonText": p.str("buttonText", "Ver Opções"), "footerText": toString(pick(o, "footer")), "sections": sections}
		evoMessageOptions(p, body, true, false)
		return &evoReq{"POST", "/message/sendList/" + p.inst(), nil, body}, nil
	},
	"messages-api/send-buttons": func(p evoParams) (*evoReq, error) {
		var buttons []interface{}
		for _, x := range asList(p.val("buttons.buttonValues", nil)) {
			b := asMap(x)
			m := map[string]interface{}{"type": b["type"], "displayText": b["displayText"]}
			switch b["type"] {
			case "reply":
				m["id"] = b["id"]
			case "copy":
				m["copyCode"] = b["copyCode"]
			case "url":
				m["url"] = b["url"]
			case "call":
				m["phoneNumber"] = b["phoneNumber"]
			}
			buttons = append(buttons, m)
		}
		if len(buttons) == 0 || len(buttons) > 3 {
			return nil, permanentError{p.ec.node.Name + ": between 1 and 3 buttons are required"}
		}
		body := map[string]interface{}{"number": p.str("remoteJid", ""), "title": p.str("title", ""), "description": p.str("description", ""), "buttons": buttons}
		if f := p.str("footer", ""); f != "" {
			body["footer"] = f
		}
		evoMessageOptions(p, body, true, false)
		return &evoReq{"POST", "/message/sendButtons/" + p.inst(), nil, body}, nil
	},
	"messages-api/send-pix": func(p evoParams) (*evoReq, error) {
		kt := p.str("keyType", "email")
		switch kt {
		case "cpf", "cnpj", "email", "phone", "random":
		default:
			return nil, permanentError{p.ec.node.Name + ": PIX key type must be cpf, cnpj, email, phone or random"}
		}
		btn := map[string]interface{}{"type": "pix", "currency": "BRL", "name": p.str("name", ""), "keyType": kt, "key": p.str("key", "")}
		return &evoReq{"POST", "/message/sendButtons/" + p.inst(), nil, map[string]interface{}{"number": p.str("remoteJid", ""), "buttons": []interface{}{btn}}}, nil
	},
	"messages-api/send-reaction": func(p evoParams) (*evoReq, error) {
		r := p.str("reaction", "👍")
		if r == "" {
			return nil, permanentError{p.ec.node.Name + ": an emoji is required for the reaction"}
		}
		key := map[string]interface{}{"remoteJid": p.str("remoteJid", ""), "fromMe": toBool(p.val("fromMe", true)), "id": p.str("messageId", "")}
		return &evoReq{"POST", "/message/sendReaction/" + p.inst(), nil, map[string]interface{}{"key": key, "reaction": r}}, nil
	},
	"messages-api/send-stories": func(p evoParams) (*evoReq, error) {
		typ, content := p.str("type", "text"), p.str("content", "")
		if typ != "text" && !strings.HasPrefix(content, "http") && !strings.HasPrefix(content, "data:") {
			return nil, permanentError{p.ec.node.Name + ": the content must be a URL or base64 data"}
		}
		all := toBool(p.val("allContacts", false))
		body := map[string]interface{}{"type": typ, "content": content, "backgroundColor": p.str("backgroundColor", "#000000"), "font": p.val("font", 1), "allContacts": all}
		if c := p.str("caption", ""); c != "" && (typ == "image" || typ == "video") {
			body["caption"] = c
		}
		if l := p.str("statusJidList", ""); !all && l != "" {
			var jids []interface{}
			for _, n := range evoCSV(l) {
				jids = append(jids, evoJID(toString(n), false))
			}
			body["statusJidList"] = jids
		}
		return &evoReq{"POST", "/message/sendStatus/" + p.inst(), nil, body}, nil
	},

	// chat
	"chat-api/check-number": func(p evoParams) (*evoReq, error) {
		return &evoReq{"POST", "/chat/whatsappNumbers/" + p.inst(), nil, map[string]interface{}{"numbers": evoCSV(p.str("numbers", ""))}}, nil
	},
	"chat-api/read-messages": func(p evoParams) (*evoReq, error) {
		m := map[string]interface{}{"remoteJid": p.str("remoteJid", ""), "fromMe": toBool(p.val("fromMe", false)), "id": p.str("messageId", "")}
		return &evoReq{"POST", "/chat/markMessageAsRead/" + p.inst(), nil, map[string]interface{}{"readMessages": []interface{}{m}}}, nil
	},
	"chat-api/manage-archive": func(p evoParams) (*evoReq, error) {
		chat := p.str("chat", "")
		last := map[string]interface{}{"key": map[string]interface{}{"remoteJid": chat, "fromMe": toBool(p.val("fromMe", false)), "id": p.str("messageId", "")}}
		return &evoReq{"POST", "/chat/archiveChat/" + p.inst(), nil, map[string]interface{}{"lastMessage": last, "chat": chat, "archive": p.val("archive", true)}}, nil
	},
	"chat-api/mark-unread": func(p evoParams) (*evoReq, error) {
		chat := p.str("chat", "")
		last := map[string]interface{}{"key": map[string]interface{}{"remoteJid": chat, "fromMe": toBool(p.val("fromMe", false)), "id": p.str("messageId", "")}}
		return &evoReq{"POST", "/chat/markChatUnread/" + p.inst(), nil, map[string]interface{}{"lastMessage": last, "chat": chat}}, nil
	},
	"chat-api/delete-message": func(p evoParams) (*evoReq, error) {
		fromMe := toBool(p.val("fromMe", false))
		body := map[string]interface{}{"id": p.str("messageId", ""), "fromMe": fromMe, "remoteJid": evoJID(p.str("remoteJid", ""), false)}
		if !fromMe {
			body["participant"] = evoJID(p.str("participant", ""), false)
		}
		return &evoReq{"DELETE", "/chat/deleteMessageForEveryone/" + p.inst(), nil, body}, nil
	},
	"chat-api/fetch-profile-picture": func(p evoParams) (*evoReq, error) {
		return &evoReq{"POST", "/chat/fetchProfilePictureUrl/" + p.inst(), nil, map[string]interface{}{"number": p.str("number", "")}}, nil
	},
	"chat-api/get-media-base64": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"message": map[string]interface{}{"key": map[string]interface{}{"id": p.str("messageId", "")}}, "convertToMp4": toBool(p.val("convertToMp4", false))}
		return &evoReq{"POST", "/chat/getBase64FromMediaMessage/" + p.inst(), nil, body}, nil
	},
	"chat-api/update-message": func(p evoParams) (*evoReq, error) {
		jid := p.str("remoteJid", "")
		body := map[string]interface{}{"number": jid, "key": map[string]interface{}{"remoteJid": jid, "fromMe": true, "id": p.str("messageId", "")}, "text": p.str("text", "")}
		return &evoReq{"POST", "/chat/updateMessage/" + p.inst(), nil, body}, nil
	},
	"chat-api/send-presence": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"number": p.str("remoteJid", ""), "presence": p.str("presence", "composing"), "delay": p.val("delay", 1200)}
		return &evoReq{"POST", "/chat/sendPresence/" + p.inst(), nil, body}, nil
	},
	"chat-api/block-contact": func(p evoParams) (*evoReq, error) {
		return &evoReq{"POST", "/chat/updateBlockStatus/" + p.inst(), nil, map[string]interface{}{"number": p.str("remoteJid", ""), "status": p.str("status", "block")}}, nil
	},
	"chat-api/find-contacts": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{}
		if !toBool(p.val("listAll", true)) {
			body["where"] = map[string]interface{}{"id": evoJID(p.str("remoteJid", ""), true)}
		}
		return &evoReq{"POST", "/chat/findContacts/" + p.inst(), nil, body}, nil
	},
	"chat-api/find-messages": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"where": map[string]interface{}{"key": map[string]interface{}{"remoteJid": evoJID(p.str("remoteJid", ""), true)}},
			"page": p.val("page", 1), "offset": p.val("offset", 10)}
		return &evoReq{"POST", "/chat/findMessages/" + p.inst(), nil, body}, nil
	},
	"chat-api/find-status-messages": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"where": map[string]interface{}{"instanceId": p.str("instanceName", ""), "remoteJid": evoJID(p.str("remoteJid", ""), true), "keyId": p.str("messageId", "")},
			"page": p.val("page", 1), "offset": p.val("offset", 10)}
		return &evoReq{"POST", "/chat/findStatusMessage/" + p.inst(), nil, body}, nil
	},
	"chat-api/find-chats": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"where": map[string]interface{}{"remoteJid": evoJID(p.str("remoteJid", ""), true), "id": p.str("messageId", "")},
			"page": p.val("page", 1), "offset": p.val("offset", 10)}
		return &evoReq{"POST", "/chat/findChats/" + p.inst(), nil, body}, nil
	},

	// groups
	"groups-api/create-group": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"subject": p.str("subject", ""), "description": p.str("description", ""), "participants": evoCSV(p.str("participants", ""))}
		return &evoReq{"POST", "/group/create/" + p.inst(), nil, body}, nil
	},
	"groups-api/fetch-groups": func(p evoParams) (*evoReq, error) {
		switch p.str("searchMethod", "inviteCode") {
		case "inviteCode":
			return &evoReq{"GET", "/group/inviteInfo/" + p.inst(), url.Values{"inviteCode": {p.str("inviteCode", "")}}, nil}, nil
		case "groupJid":
			return &evoReq{"GET", "/group/findGroupInfos/" + p.inst(), url.Values{"groupJid": {p.str("groupJid", "")}}, nil}, nil
		case "fetchAll":
			return &evoReq{"GET", "/group/fetchAllGroups/" + p.inst(), url.Values{"getParticipants": {toString(toBool(p.val("getParticipants", false)))}}, nil}, nil
		}
		return nil, permanentError{p.ec.node.Name + ": invalid search method"}
	},
	"groups-api/fetch-invite-code":  evoGroupGet("GET", "/group/inviteCode/"),
	"groups-api/find-participants":  evoGroupGet("GET", "/group/participants/"),
	"groups-api/leave-group":        evoGroupGet("DELETE", "/group/leaveGroup/"),
	"groups-api/revoke-invite-code": evoGroupGet("POST", "/group/revokeInviteCode/"),
	"groups-api/join-group": func(p evoParams) (*evoReq, error) {
		return &evoReq{"GET", "/group/acceptInviteCode/" + p.inst(), url.Values{"inviteCode": {p.str("inviteCode", "")}}, nil}, nil
	},
	"groups-api/send-invite-link": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"groupJid": p.str("groupJid", ""), "description": p.str("description", ""), "numbers": evoCSV(p.str("numbers", ""))}
		return &evoReq{"POST", "/group/sendInvite/" + p.inst(), nil, body}, nil
	},
	"groups-api/toggle-ephemeral":         evoGroupBody("/group/toggleEphemeral/", "expiration", 0),
	"groups-api/update-group-description": evoGroupBody("/group/updateGroupDescription/", "description", ""),
	"groups-api/update-group-name":        evoGroupBody("/group/updateGroupSubject/", "subject", ""),
	"groups-api/update-group-picture":     evoGroupBody("/group/updateGroupPicture/", "image", ""),
	"groups-api/update-settings":          evoGroupBody("/group/updateSetting/", "action", "not_announcement"),
	"groups-api/update-participants": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"action": p.str("action", "add"), "participants": evoCSV(p.str("participants", ""))}
		return &evoReq{"POST", "/group/updateParticipant/" + p.inst(), url.Values{"groupJid": {p.str("groupJid", "")}}, body}, nil
	},

	// instances
	"instances-api/instance-basic": evoCreateInstance,
	"instances-api/instance-connect": func(p evoParams) (*evoReq, error) {
		return &evoReq{"GET", "/instance/connect/" + p.inst(), nil, nil}, nil
	},
	"instances-api/restart-instance": func(p evoParams) (*evoReq, error) {
		return &evoReq{"POST", "/instance/restart/" + p.inst(), nil, nil}, nil
	},
	"instances-api/logout-instance": func(p evoParams) (*evoReq, error) {
		return &evoReq{"DELETE", "/instance/logout/" + p.inst(), nil, nil}, nil
	},
	"instances-api/delete-instance": func(p evoParams) (*evoReq, error) {
		return &evoReq{"DELETE", "/instance/delete/" + p.inst(), nil, nil}, nil
	},
	"instances-api/fetch-instances": func(p evoParams) (*evoReq, error) {
		var q url.Values
		if n := p.str("instanceName", ""); n != "" {
			q = url.Values{"instanceName": {n}}
		}
		return &evoReq{"GET", "/instance/fetchInstances", q, nil}, nil
	},
	"instances-api/set-presence": func(p evoParams) (*evoReq, error) {
		return &evoReq{"POST", "/instance/setPresence/" + p.inst(), nil, map[string]interface{}{"presence": p.str("presence", "available")}}, nil
	},
	"instances-api/instance-settings": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"rejectCall": toBool(p.val("rejectCall", false)), "msgCall": p.str("msgCall", "Não aceitamos ligações telefônicas.")}
		for _, k := range []string{"groupsIgnore", "alwaysOnline", "readMessages", "syncFullHistory", "readStatus"} {
			body[k] = toBool(p.val(k, false))
		}
		return &evoReq{"POST", "/settings/set/" + p.inst(), nil, body}, nil
	},
	"instances-api/set-proxy": func(p evoParams) (*evoReq, error) {
		if p.str("resourceForProxy", "setProxy") == "findProxy" {
			return &evoReq{"GET", "/proxy/find/" + p.inst(), nil, nil}, nil
		}
		body := map[string]interface{}{"enabled": p.val("enabled", true), "host": firstNonEmpty(p.str("proxyHost", ""), "1234"), "port": p.str("proxyPort", "1234"),
			"protocol": p.str("proxyProtocol", ""), "username": p.str("proxyUsername", ""), "password": p.str("proxyPassword", "")}
		return &evoReq{"POST", "/proxy/set/" + p.inst(), nil, body}, nil
	},

	// events and integrations
	"events-api/webhook": func(p evoParams) (*evoReq, error) {
		if p.str("resourceForWebhook", "setWebhook") == "findWebhook" {
			return &evoReq{"GET", "/webhook/find/" + p.inst(), nil, nil}, nil
		}
		ev := p.val("webhookEvents", []interface{}{})
		wh := map[string]interface{}{"enabled": p.val("enabled", true), "url": firstNonEmpty(p.str("webhookUrl", ""), "vazio"),
			"webhookByEvents": toBool(p.val("webhookByEvents", false)), "webhookBase64": toBool(p.val("webhookBase64", false)), "events": asListOrOne(ev)}
		return &evoReq{"POST", "/webhook/set/" + p.inst(), nil, map[string]interface{}{"webhook": wh}}, nil
	},
	"integrations-api/evolution-bot": evoBot,

	// profile
	"profile-api/fetch-profile": func(p evoParams) (*evoReq, error) {
		return &evoReq{"POST", "/chat/fetchProfile/" + p.inst(), nil, map[string]interface{}{"number": evoJID(p.str("remoteJid", ""), true)}}, nil
	},
	"profile-api/fetch-business-profile": func(p evoParams) (*evoReq, error) {
		return &evoReq{"POST", "/chat/fetchBusinessProfile/" + p.inst(), nil, map[string]interface{}{"number": evoJID(p.str("remoteJid", ""), true)}}, nil
	},
	"profile-api/update-profile-name": func(p evoParams) (*evoReq, error) {
		return &evoReq{"POST", "/chat/updateProfileName/" + p.inst(), nil, map[string]interface{}{"name": p.str("name", "")}}, nil
	},
	"profile-api/update-profile-status": func(p evoParams) (*evoReq, error) {
		return &evoReq{"POST", "/chat/updateProfileStatus/" + p.inst(), nil, map[string]interface{}{"status": p.str("status", "")}}, nil
	},
	"profile-api/update-profile-picture": func(p evoParams) (*evoReq, error) {
		return &evoReq{"POST", "/chat/updateProfilePicture/" + p.inst(), nil, map[string]interface{}{"picture": p.str("picture", "")}}, nil
	},
	"profile-api/remove-profile-picture": func(p evoParams) (*evoReq, error) {
		return &evoReq{"DELETE", "/chat/removeProfilePicture/" + p.inst(), nil, nil}, nil
	},
	"profile-api/fetch-privacy-settings": func(p evoParams) (*evoReq, error) {
		return &evoReq{"GET", "/chat/fetchPrivacySettings/" + p.inst(), nil, nil}, nil
	},
	"profile-api/update-privacy-settings": func(p evoParams) (*evoReq, error) {
		body := map[string]interface{}{"readreceipts": p.str("readreceipts", "all"), "profile": p.str("profile", "all"), "status": p.str("status", "contacts"),
			"online": p.str("online", "all"), "last": p.str("last", "contacts"), "groupadd": p.str("groupadd", "all")}
		return &evoReq{"POST", "/chat/updatePrivacySettings/" + p.inst(), nil, body}, nil
	},
}

func evoGroupGet(method, path string) func(p evoParams) (*evoReq, error) {
	return func(p evoParams) (*evoReq, error) {
		return &evoReq{method, path + p.inst(), url.Values{"groupJid": {p.str("groupJid", "")}}, nil}, nil
	}
}

func evoGroupBody(path, field string, def interface{}) func(p evoParams) (*evoReq, error) {
	return func(p evoParams) (*evoReq, error) {
		return &evoReq{"POST", path + p.inst(), url.Values{"groupJid": {p.str("groupJid", "")}}, map[string]interface{}{field: p.val(field, def)}}, nil
	}
}

func evoCreateInstance(p evoParams) (*evoReq, error) {
	body := map[string]interface{}{"instanceName": p.str("instanceName", ""), "integration": "WHATSAPP-BAILEYS"}
	if t := p.str("token", ""); t != "" {
		body["token"] = t
	}
	if n := p.str("number", ""); n != "" {
		body["number"] = n
	}
	opt := func(path string) map[string]interface{} { return asMap(p.val("options_Create_instance."+path, nil)) }
	for k, v := range opt("instanceSettings.settings") {
		body[k] = v
	}
	if ps := opt("proxy.proxySettings"); len(ps) > 0 {
		port := "1234"
		if !restEmpty(ps["proxyPort"]) {
			port = toString(ps["proxyPort"])
		}
		body["proxyHost"], body["proxyPort"], body["proxyProtocol"] = toString(pick(ps, "proxyHost")), port, toString(pick(ps, "proxyProtocol"))
		body["proxyUsername"], body["proxyPassword"] = toString(pick(ps, "proxyUsername")), toString(pick(ps, "proxyPassword"))
	}
	if ws := opt("webhook.webhookSettings"); len(ws) > 0 {
		ev := ws["webhookEvents"]
		if ev == nil {
			ev = []interface{}{}
		}
		body["webhook"] = map[string]interface{}{"url": toString(pick(ws, "webhookUrl")), "byEvents": toBool(ws["webhookByEvents"]), "base64": toBool(ws["webhookBase64"]), "events": ev}
	}
	if rs := opt("rabbitmq.rabbitmqSettings"); len(rs) > 0 {
		ev := rs["rabbitmqEvents"]
		if ev == nil {
			ev = []interface{}{}
		}
		body["rabbitmq"] = map[string]interface{}{"enabled": toBool(rs["rabbitmqEnabled"]), "events": ev}
	}
	if cs := opt("chatwoot.chatwootSettings"); len(cs) > 0 {
		body["chatwootAccountId"], body["chatwootToken"], body["chatwootUrl"] = toString(pick(cs, "chatwootAccountId")), toString(pick(cs, "chatwootToken")), toString(pick(cs, "chatwootUrl"))
		body["chatwootSignMsg"] = toBool(cs["chatwootSignMsg"])
	}
	return &evoReq{"POST", "/instance/create", nil, body}, nil
}

func evoBot(p evoParams) (*evoReq, error) {
	inst, id := p.inst(), url.PathEscape(p.str("evolutionBotId", ""))
	settings := func() map[string]interface{} {
		b := map[string]interface{}{"enabled": true, "apiUrl": p.str("apiUrl", ""), "triggerType": p.str("triggerType", "keyword"),
			"triggerOperator": p.str("triggerOperator", "contains"), "triggerValue": p.str("triggerValue", ""), "expire": p.val("expire", 0),
			"keywordFinish": p.str("keywordFinish", "#sair"), "delayMessage": p.val("delayMessage", 1000), "unknownMessage": p.str("unknownMessage", "Mensagem não reconhecida"),
			"listeningFromMe": toBool(p.val("listeningFromMe", false)), "stopBotFromMe": toBool(p.val("stopBotFromMe", false)), "keepOpen": toBool(p.val("keepOpen", false)),
			"debounceTime": p.val("debounceTime", 0), "ignoreJids": p.val("ignoreJids", []interface{}{})}
		if k := p.str("apiKeyBot", ""); k != "" {
			b["apiKey"] = k
		}
		if restEmpty(b["ignoreJids"]) {
			b["ignoreJids"] = []interface{}{}
		}
		return b
	}
	switch p.str("resourceForEvolutionBot", "createEvolutionBot") {
	case "createEvolutionBot":
		return &evoReq{"POST", "/evolutionBot/create/" + inst, nil, settings()}, nil
	case "findEvolutionBot":
		if id != "" {
			return &evoReq{"GET", "/evolutionBot/fetch/" + id + "/" + inst, nil, nil}, nil
		}
		return &evoReq{"GET", "/evolutionBot/find/" + inst, nil, nil}, nil
	case "updateEvolutionBot":
		return &evoReq{"PUT", "/evolutionBot/update/" + id + "/" + inst, nil, settings()}, nil
	case "deleteEvolutionBot":
		return &evoReq{"DELETE", "/evolutionBot/delete/" + id + "/" + inst, nil, nil}, nil
	case "fetchSessionsEvolutionBot":
		return &evoReq{"GET", "/evolutionBot/fetchSessions/" + id + "/" + inst, nil, nil}, nil
	case "changeStatusEvolutionBot":
		return &evoReq{"POST", "/evolutionBot/changeStatus/" + inst, nil, map[string]interface{}{"remoteJid": p.str("remoteJid", ""), "status": p.str("status", "opened")}}, nil
	}
	return nil, permanentError{p.ec.node.Name + ": unknown Evolution Bot operation"}
}

// evoPerItem lists the operations the package runs for every input item.
var evoPerItem = map[string]bool{"messages-api/send-text": true, "messages-api/send-image": true}

func init() {
	ops := map[string]*restOp{}
	for k, build := range evoOps {
		key, build := k, build
		ops[key] = &restOp{run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
			if !evoPerItem[key] && i != 0 {
				return nil, nil
			}
			r, err := build(evoParams{ec, i})
			if err != nil {
				return nil, err
			}
			req := &restRequest{method: r.method, path: r.path, query: r.query, body: r.body}
			if req.query == nil {
				req.query = url.Values{}
			}
			v, err := c.do(ec, req)
			if err != nil {
				return nil, err
			}
			return []Item{{JSON: map[string]interface{}{"success": true, "data": v}}}, nil
		}}
	}
	s := &restService{
		defResource: "instances-api",
		defOps: map[string]string{"instances-api": "instance-basic", "messages-api": "send-text", "events-api": "webhook", "integrations-api": "chatwoot",
			"groups-api": "create-group", "chat-api": "check-number", "profile-api": "fetch-profile"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("evolutionApi")
			if err != nil {
				return nil, err
			}
			base := ec.apiBase("evolutionApi", strings.TrimRight(credString(c, "server-url"), "/"))
			return &restClient{base: base, headers: map[string]string{"apikey": credString(c, "apikey")}}, nil
		},
		ops: ops,
	}
	registerREST("n8n-nodes-evolution-api.evolutionApi", s)
	registerREST("n8n-nodes-evolution-api-en.evolutionApi", s)
}
