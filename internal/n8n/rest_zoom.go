package n8n

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Zoom (n8n-nodes-base.zoom), API v2: the meeting resource.

const zoomSettings = "settings.cnMeeting:settings.cn_meeting settings.inMeeting:settings.in_meeting settings.joinBeforeHost:settings.join_before_host " +
	"settings.muteUponEntry:settings.mute_upon_entry settings.watermark:settings.watermark settings.audio:settings.audio settings.alternativeHosts:settings.alternative_hosts " +
	"settings.participantVideo:settings.participant_video settings.hostVideo:settings.host_video settings.autoRecording:settings.auto_recording " +
	"settings.registrationType:settings.registration_type"

func init() {
	registerREST("zoom", &restService{
		defResource: "meeting",
		defOps:      map[string]string{"meeting": "create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "zoomApi", map[string]string{"accessToken": "zoomApi", "oAuth2": "zoomOAuth2Api"})
			if err != nil {
				return nil, err
			}
			restDefaultLimit(ec, 30)
			return bearerClient(ec.apiBase("zoom", "https://api.zoom.us/v2"), c, "accessToken"), nil
		},
		ops: map[string]*restOp{
			"meeting/create": {method: "POST", path: "/users/me/meetings", body: "topic @additionalFields type duration scheduleFor:schedule_for timeZone:timezone password agenda " + zoomSettings,
				build: zoomStartTime("additionalFields", true)},
			"meeting/update": {method: "PATCH", path: "/meetings/{meetingId}", success: true,
				body:  "@updateFields topic type duration scheduleFor:schedule_for timeZone:timezone password agenda " + zoomSettings,
				build: zoomStartTime("updateFields", false)},
			"meeting/get":    {path: "/meetings/{meetingId}", query: "@additionalFields showPreviousOccurrences:show_previous_occurrences occurrenceId:occurrence_id"},
			"meeting/delete": {method: "DELETE", path: "/meetings/{meetingId}", success: true, query: "@additionalFields scheduleForReminder:schedule_for_reminder occurrenceId:occurrence_id"},
			"meeting/getAll": {path: "/users/me/meetings", list: true, listKey: "meetings", pager: zoomPager, query: "@filters type",
				build: func(ec *execCtx, i int, r *restRequest) error {
					if all, _ := ec.boolean(i, "returnAll", false); all {
						r.query.Set("page_number", "0")
					} else {
						n, _ := ec.num(i, "limit", 30)
						r.query.Set("page_size", strconv.Itoa(int(n)))
					}
					return nil
				}},
		},
	})
}

// zoomPager increments page_number until page_count == page_number.
func zoomPager(p *restPage, res interface{}, _ http.Header) bool {
	if p.query.Get("page_number") == "" {
		return false
	}
	m := asMap(res)
	if toString(m["page_count"]) == toString(m["page_number"]) {
		return false
	}
	p.query.Set("page_number", strconv.Itoa(p.n+1))
	return true
}

// zoomStartTime formats startTime like n8n: local wall time when a time
// zone is given, else an ISO time with the workflow zone's offset (create);
// update sends it unchanged.
func zoomStartTime(coll string, create bool) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		f := fieldsOf(ec, i, coll)
		s := toString(f["startTime"])
		if s == "" || s == "<nil>" {
			return nil
		}
		if !create {
			r.body["start_time"] = f["startTime"]
			return nil
		}
		loc, err := loadZone(ec.opts.timezone(ec.data.wf))
		if err != nil {
			loc = time.UTC
		}
		t, ok := zoomParse(s, loc)
		if !ok {
			r.body["start_time"] = s
			return nil
		}
		if tz := toString(f["timeZone"]); tz != "" && tz != "<nil>" {
			r.body["start_time"] = t.Format("2006-01-02T15:04:05")
		} else {
			r.body["start_time"] = t.In(loc).Format("2006-01-02T15:04:05Z07:00")
		}
		return nil
	}
}

func zoomParse(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(l, s); err == nil {
			return t.In(loc), true
		}
	}
	for _, l := range []string{"2006-01-02T15:04:05.000", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, true
		}
	}
	return timeOf(s)
}
