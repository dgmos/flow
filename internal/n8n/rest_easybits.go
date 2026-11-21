package n8n

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
)

// easybits Extractor (@easybits/n8n-nodes-extractor): sends every input
// file as a data URL to the credential's pipeline in one call.

func init() {
	registerREST("@easybits/n8n-nodes-extractor.easybitsExtractor", &restService{
		defResource: "document",
		defOps:      map[string]string{"document": "extract"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("easybitsExtractorApi")
			if err != nil {
				return nil, err
			}
			base := ec.apiBase("easybits", "https://extractor.easybits.tech") + "/api/pipelines/" + credString(c, "pipelineId")
			return &restClient{base: base, headers: map[string]string{"Authorization": "Bearer " + credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{"document/extract": {run: easybitsExtract}},
	})
}

var easybitsMimes = map[string]bool{"image/jpeg": true, "image/jpg": true, "image/png": true, "application/pdf": true}

// easybitsExtract runs once for all items (on the first one).
func easybitsExtract(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	if i != 0 {
		return nil, nil
	}
	in, _ := ec.str(0, "inputType", "binaryFiles")
	var files []interface{}
	for k, it := range ec.items() {
		if in == "binaryFiles" || in == "auto" {
			props := make([]string, 0, len(it.Binary))
			for p := range it.Binary {
				props = append(props, p)
			}
			sort.Strings(props)
			for _, prop := range props {
				data, mt, _, err := binaryOf(it, prop)
				if err != nil {
					return nil, err
				}
				if !easybitsMimes[mt] {
					return nil, permanentError{fmt.Sprintf("%s: unsupported file type %q for binary property %q. Only JPEG, PNG, or PDF files are allowed.", ec.node.Name, mt, prop)}
				}
				files = append(files, "data:"+mt+";base64,"+base64.StdEncoding.EncodeToString(data))
			}
		}
		if in == "dataUrls" || in == "auto" {
			field, _ := ec.str(k, "dataUrlField", "dataUrl")
			v, _ := getPath(it.JSON, field, true)
			if v == nil {
				continue
			}
			for _, x := range asListOrOne(v) {
				s, ok := x.(string)
				if !ok || !strings.HasPrefix(s, "data:") {
					return nil, permanentError{fmt.Sprintf("%s: invalid Data URL in field %q", ec.node.Name, field)}
				}
				files = append(files, s)
			}
		}
	}
	if len(files) == 0 {
		return nil, nil
	}
	v, err := c.do(ec, &restRequest{method: "POST", body: map[string]interface{}{"files": files}})
	if err != nil {
		return nil, err
	}
	return restItems(v), nil
}
