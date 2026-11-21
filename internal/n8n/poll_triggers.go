package n8n

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Polling triggers that check a service for changes since the last poll:
// Notion, Google Calendar and Airtable. Like n8n, the first poll only
// records the current time.

// timeCursor is the stored state of a time-based polling trigger.
type timeCursor struct {
	Last string   `json:"last"`
	Dups []string `json:"dups,omitempty"`
}

func (p *pollCursor) timeState() (timeCursor, bool) {
	var c timeCursor
	if !p.seen || p.prev == "" {
		return c, false
	}
	if err := json.Unmarshal([]byte(p.prev), &c); err != nil || c.Last == "" {
		// A plain timestamp from an older cursor format.
		c = timeCursor{Last: p.prev}
	}
	return c, true
}

func (p *pollCursor) commitTime(c timeCursor, out []Item) ([][]Item, error) {
	b, _ := json.Marshal(c)
	return p.commit(string(b), out)
}

// ---------------------------------------------------------------- Notion

func notionTriggerNode(ec *execCtx) ([][]Item, error) {
	credType := "notionApi"
	if a, _ := ec.str(0, "authentication", "apiKey"); a == "oAuth2" {
		credType = "notionOAuth2Api"
	}
	c, err := ec.credential(credType)
	if err != nil {
		return nil, err
	}
	h := map[string]string{"Authorization": "Bearer " + firstNonEmpty(credString(c, "apiKey"), accessToken(c)), "Notion-Version": "2022-06-28"}
	db, err := ec.rlString(0, "databaseId")
	if err != nil {
		return nil, err
	}
	if db == "" {
		db, _ = ec.rlString(0, "dataSourceId")
	}
	if db == "" {
		return nil, permanentError{"Notion Trigger: no database selected"}
	}
	event, _ := ec.str(0, "event", "pageAddedToDatabase")
	simple, _ := ec.boolean(0, "simple", true)
	sortProp := "last_edited_time"
	if event == "pageAddedToDatabase" {
		sortProp = "created_time"
	}
	pc, err := ec.openPoll()
	if err != nil {
		return nil, err
	}
	defer pc.close()
	now := time.Now().UTC().Truncate(time.Minute) // Notion timestamps have minute precision
	state, seen := pc.timeState()
	if !seen {
		return pc.commitTime(timeCursor{Last: now.Format(time.RFC3339)}, nil)
	}
	last, _ := time.Parse(time.RFC3339, state.Last)
	dups := map[string]bool{}
	for _, id := range state.Dups {
		dups[id] = true
	}
	base := ec.apiBase("notion", "https://api.notion.com") + "/v1/databases/" + url.PathEscape(notionID(db)) + "/query"
	body := map[string]interface{}{
		"page_size": 100,
		"sorts":     []interface{}{map[string]interface{}{"timestamp": sortProp, "direction": "descending"}},
		"filter":    map[string]interface{}{"timestamp": sortProp, sortProp: map[string]interface{}{"on_or_after": last.Format(time.RFC3339)}},
	}
	var records []map[string]interface{}
	for page := 0; page < 100; page++ {
		res, _, err := ec.apiCall("POST", base, h, body, "")
		if err != nil {
			return nil, err
		}
		for _, r := range asList(res["results"]) {
			records = append(records, asMap(r))
		}
		if !toBool(res["has_more"]) || toString(res["next_cursor"]) == "" {
			break
		}
		body["start_cursor"] = res["next_cursor"]
	}
	next := timeCursor{Last: now.Format(time.RFC3339)}
	var out []Item
	var newest time.Time
	for _, r := range records {
		if dups[toString(r["id"])] {
			continue
		}
		t, _ := timeOf(r[sortProp])
		if t.After(newest) {
			newest, next.Dups = t, nil
		}
		if t.Equal(newest) {
			next.Dups = append(next.Dups, toString(r["id"]))
		}
		if simple {
			out = append(out, Item{JSON: simplifyNotionPage(r)})
		} else {
			out = append(out, Item{JSON: r})
		}
	}
	return pc.commitTime(next, out)
}

// ---------------------------------------------------------------- Google Calendar

func googleCalendarTriggerNode(ec *execCtx) ([][]Item, error) {
	h, err := ec.googleAuth("calendar", "googleCalendarOAuth2Api")
	if err != nil {
		return nil, err
	}
	cal, err := ec.rlString(0, "calendarId")
	if err != nil {
		return nil, err
	}
	triggerOn, _ := ec.str(0, "triggerOn", "")
	if triggerOn == "" {
		return nil, permanentError{"Google Calendar Trigger: please select an event"}
	}
	if cal == "" {
		return nil, permanentError{"Google Calendar Trigger: please select a calendar"}
	}
	pc, err := ec.openPoll()
	if err != nil {
		return nil, err
	}
	defer pc.close()
	end := time.Now().UTC().Truncate(time.Second)
	state, seen := pc.timeState()
	if !seen {
		return pc.commitTime(timeCursor{Last: end.Format(time.RFC3339)}, nil)
	}
	start, _ := time.Parse(time.RFC3339, state.Last)
	qs := url.Values{"showDeleted": {"false"}}
	if m, _ := ec.str(0, "options.matchTerm", ""); m != "" {
		qs.Set("q", m)
	}
	switch triggerOn {
	case "eventCreated", "eventUpdated", "eventCancelled":
		qs.Set("updatedMin", start.Format(time.RFC3339))
		qs.Set("orderBy", "updated")
		if triggerOn == "eventCancelled" {
			qs.Set("showDeleted", "true")
		}
	case "eventStarted", "eventEnded":
		qs.Set("singleEvents", "true")
		qs.Set("timeMin", start.Add(-time.Second).Format(time.RFC3339))
		qs.Set("timeMax", end.Add(time.Second).Format(time.RFC3339))
		qs.Set("orderBy", "startTime")
	}
	base := ec.apiBase("calendar", "https://www.googleapis.com") + "/calendar/v3/calendars/" + url.PathEscape(cal)
	var events []map[string]interface{}
	for page := 0; page < 100; page++ {
		res, _, err := ec.apiCall("GET", base+"/events?"+qs.Encode(), h, nil, "")
		if err != nil {
			return nil, err
		}
		for _, e := range asList(res["items"]) {
			events = append(events, asMap(e))
		}
		tok := toString(res["nextPageToken"])
		if tok == "" {
			break
		}
		qs.Set("pageToken", tok)
	}
	var tz *time.Location
	eventTime := func(d map[string]interface{}) (time.Time, bool) {
		if s := toString(d["dateTime"]); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			return t, err == nil
		}
		s := toString(d["date"])
		if s == "" {
			return time.Time{}, false
		}
		loc := time.UTC
		if z := toString(d["timeZone"]); z != "" {
			if l, err := time.LoadLocation(z); err == nil {
				loc = l
			}
		} else if tz != nil {
			loc = tz
		}
		t, err := time.ParseInLocation("2006-01-02", s, loc)
		return t, err == nil
	}
	if triggerOn == "eventStarted" || triggerOn == "eventEnded" {
		for _, e := range events {
			field := map[bool]string{true: "start", false: "end"}[triggerOn == "eventStarted"]
			if d := asMap(e[field]); toString(d["date"]) != "" && toString(d["timeZone"]) == "" {
				res, _, err := ec.apiCall("GET", base, h, nil, "")
				if err != nil {
					return nil, err
				}
				if l, err := time.LoadLocation(toString(res["timeZone"])); err == nil {
					tz = l
				}
				break
			}
		}
	}
	trunc := func(v interface{}) string {
		t, _ := timeOf(v)
		return t.UTC().Format("2006-01-02T15:04:05")
	}
	var out []Item
	for _, e := range events {
		keep := false
		switch triggerOn {
		case "eventCreated":
			t, ok := timeOf(e["created"])
			keep = ok && t.After(start) && t.Before(end)
		case "eventUpdated", "eventCancelled":
			keep = trunc(e["created"]) != trunc(e["updated"])
			if triggerOn == "eventCancelled" {
				keep = keep && e["status"] == "cancelled"
			}
		case "eventStarted", "eventEnded":
			field := "start"
			if triggerOn == "eventEnded" {
				field = "end"
			}
			t, ok := eventTime(asMap(e[field]))
			keep = ok && !t.Before(start) && !t.After(end)
		}
		if keep {
			out = append(out, Item{JSON: e})
		}
	}
	return pc.commitTime(timeCursor{Last: end.Format(time.RFC3339)}, out)
}

// ---------------------------------------------------------------- Airtable

var (
	airtableBaseRe  = regexp.MustCompile(`app[A-Za-z0-9]{14}`)
	airtableTableRe = regexp.MustCompile(`tbl[A-Za-z0-9]{14}`)
)

func airtableTriggerNode(ec *execCtx) ([][]Item, error) {
	credType, _ := ec.str(0, "authentication", "airtableApi")
	c, err := ec.credential(credType)
	if err != nil {
		return nil, err
	}
	h := map[string]string{"Authorization": "Bearer " + firstNonEmpty(accessToken(c), credString(c, "apiKey"))}
	bv, _ := ec.param(0, "baseId", "")
	tv, _ := ec.param(0, "tableId", "")
	baseID, table := rlValue(bv), rlValue(tv)
	if m := airtableBaseRe.FindString(baseID); m != "" {
		baseID = m
	}
	if m := airtableTableRe.FindString(table); m != "" {
		table = m
	}
	field, _ := ec.str(0, "triggerField", "")
	if baseID == "" || table == "" || field == "" {
		return nil, permanentError{"Airtable Trigger: base, table and trigger field are required"}
	}
	pc, err := ec.openPoll()
	if err != nil {
		return nil, err
	}
	defer pc.close()
	end := time.Now().UTC().Truncate(time.Second)
	state, seen := pc.timeState()
	if !seen {
		return pc.commitTime(timeCursor{Last: end.Format(time.RFC3339)}, nil)
	}
	start, _ := time.Parse(time.RFC3339, state.Last)
	af := fieldsOf(ec, 0, "additionalFields")
	qs := url.Values{}
	if v := toString(af["viewId"]); v != "" {
		qs.Set("view", v)
	}
	for _, f := range splitList(toString(af["fields"])) {
		qs.Add("fields[]", f)
	}
	formula := `IS_AFTER({` + field + `}, DATETIME_PARSE("` + start.Format(time.RFC3339) + `", "YYYY-MM-DD HH:mm:ss"))`
	if f := toString(af["formula"]); f != "" {
		formula = "AND(" + formula + ", " + f + ")"
	}
	qs.Set("filterByFormula", formula)
	u := ec.apiBase("airtable", "https://api.airtable.com/v0") + "/" + url.PathEscape(baseID) + "/" + url.PathEscape(table)
	var out []Item
	for page := 0; page < 1000; page++ {
		res, _, err := ec.apiCall("GET", u+"?"+qs.Encode(), h, nil, "")
		if err != nil {
			return nil, err
		}
		for _, r := range asList(res["records"]) {
			out = append(out, Item{JSON: asMap(r)})
		}
		off := toString(res["offset"])
		if off == "" {
			break
		}
		qs.Set("offset", off)
	}
	if dl, _ := ec.boolean(0, "downloadAttachments", false); dl && len(out) > 0 {
		names, _ := ec.str(0, "downloadFieldNames", "")
		for k := range out {
			fields := asMap(out[k].JSON["fields"])
			for _, name := range splitList(names) {
				for n, a := range asList(fields[name]) {
					am := asMap(a)
					data, mt, err := ec.downloadWith(toString(am["url"]), nil)
					if err != nil {
						return nil, err
					}
					if out[k].Binary == nil {
						out[k].Binary = map[string]interface{}{}
					}
					out[k].Binary[name+"_"+strconv.Itoa(n)] = binaryData(data, firstNonEmpty(toString(am["type"]), strings.Split(mt, ";")[0]), toString(am["filename"]))
				}
			}
		}
	}
	return pc.commitTime(timeCursor{Last: end.Format(time.RFC3339)}, out)
}
