package n8n

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/dgmos/flow/internal/engine"
)

type document struct {
	PageContent string                 `json:"pageContent"`
	Metadata    map[string]interface{} `json:"metadata"`
}

// ---------------------------------------------------------------- embeddings

type embedder interface {
	embed(ctx context.Context, texts []string) ([][]float64, error)
}

type openAIEmbedder struct {
	ec      *execCtx
	baseURL string
	headers map[string]string
	model   string
	dims    int
	batch   int
}

func checkEmbeddings(vecs [][]float64, n int) error {
	if len(vecs) != n {
		return fmt.Errorf("embeddings API returned %d vectors for %d texts", len(vecs), n)
	}
	for _, v := range vecs {
		if len(v) == 0 {
			return fmt.Errorf("embeddings API returned an empty vector")
		}
	}
	return nil
}

func (e *openAIEmbedder) embed(ctx context.Context, texts []string) ([][]float64, error) {
	var out [][]float64
	batch := e.batch
	if batch <= 0 {
		batch = 512
	}
	for start := 0; start < len(texts); start += batch {
		end := start + batch
		if end > len(texts) {
			end = len(texts)
		}
		body := map[string]interface{}{"model": e.model, "input": texts[start:end]}
		if e.dims > 0 {
			body["dimensions"] = e.dims
		}
		res, _, err := e.ec.apiCall("POST", e.baseURL+"/embeddings", e.headers, body, "")
		if err != nil {
			return nil, err
		}
		data := asList(res["data"])
		sort.SliceStable(data, func(a, b int) bool {
			am, _ := data[a].(map[string]interface{})
			bm, _ := data[b].(map[string]interface{})
			return toFloatDefault(am["index"], 0) < toFloatDefault(bm["index"], 0)
		})
		for _, d := range data {
			dm, _ := d.(map[string]interface{})
			out = append(out, floats(dm["embedding"]))
		}
	}
	return out, checkEmbeddings(out, len(texts))
}

type geminiEmbedder struct {
	ec      *execCtx
	baseURL string
	apiKey  string
	model   string
}

func (e *geminiEmbedder) embed(ctx context.Context, texts []string) ([][]float64, error) {
	var out [][]float64
	model := e.model
	if !strings.HasPrefix(model, "models/") {
		model = "models/" + model
	}
	for start := 0; start < len(texts); start += 100 {
		end := start + 100
		if end > len(texts) {
			end = len(texts)
		}
		var reqs []interface{}
		for _, t := range texts[start:end] {
			reqs = append(reqs, map[string]interface{}{"model": model, "content": map[string]interface{}{"parts": []interface{}{map[string]interface{}{"text": t}}}})
		}
		res, _, err := e.ec.apiCall("POST", e.baseURL+"/v1beta/"+model+":batchEmbedContents", map[string]string{"x-goog-api-key": e.apiKey}, map[string]interface{}{"requests": reqs}, "")
		if err != nil {
			return nil, err
		}
		for _, emb := range asList(res["embeddings"]) {
			em, _ := emb.(map[string]interface{})
			out = append(out, floats(em["values"]))
		}
	}
	return out, checkEmbeddings(out, len(texts))
}

func floats(v interface{}) []float64 {
	l := asList(v)
	out := make([]float64, len(l))
	for i, x := range l {
		out[i], _ = toFloat(x)
	}
	return out
}

var openAIEmbeddingTypes = map[string]struct{ cred, base, model string }{
	"embeddingsOpenAi":       {"openAiApi", "https://api.openai.com/v1", "text-embedding-3-small"},
	"embeddingsMistralCloud": {"mistralCloudApi", "https://api.mistral.ai/v1", "mistral-embed"},
	"embeddingsOllama":       {"ollamaApi", "http://localhost:11434/v1", "nomic-embed-text"},
}

func (ec *execCtx) buildEmbedder(cfg map[string]interface{}, i int) (embedder, error) {
	subs := aiSubNodes(cfg, "ai_embedding")
	if len(subs) == 0 {
		return nil, permanentError{"no embeddings model connected"}
	}
	s := subs[0]
	p, err := ec.resolveSub(s, i)
	if err != nil {
		return nil, err
	}
	opts, _ := p["options"].(map[string]interface{})
	st := short(s.typ)
	if spec, ok := openAIEmbeddingTypes[st]; ok {
		c, err := ec.subCredential(s, spec.cred)
		if err != nil {
			return nil, err
		}
		model := rlValue(p["model"])
		if model == "" {
			model = spec.model
			if st == "embeddingsOpenAi" && toFloatDefault(s.config[cfgVersion], 1) < 1.1 {
				model = "text-embedding-ada-002"
			}
		}
		base := credBaseURL(c, spec.base)
		if st == "embeddingsOllama" && !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		if u := toString(opts["baseURL"]); u != "" {
			base = strings.TrimRight(u, "/")
		}
		h := map[string]string{}
		if k := credString(c, "apiKey"); k != "" {
			h["Authorization"] = "Bearer " + k
		}
		return &openAIEmbedder{ec: ec, baseURL: base, headers: h, model: model, dims: int(toFloatDefault(opts["dimensions"], 0)), batch: int(toFloatDefault(opts["batchSize"], 512))}, nil
	}
	if st == "embeddingsGoogleGemini" {
		c, err := ec.subCredential(s, "googlePalmApi")
		if err != nil {
			return nil, err
		}
		return &geminiEmbedder{ec: ec, baseURL: credBaseURL(c, "https://generativelanguage.googleapis.com"), apiKey: credString(c, "apiKey"), model: firstNonEmpty(toString(p["modelName"]), "models/text-embedding-004")}, nil
	}
	if st == "embeddingsCohere" {
		c, err := ec.subCredential(s, "cohereApi")
		if err != nil {
			return nil, err
		}
		return &cohereEmbedder{ec: ec, baseURL: credBaseURL(c, "https://api.cohere.com"), apiKey: credString(c, "apiKey"), model: firstNonEmpty(toString(p["modelName"]), "embed-english-v2.0")}, nil
	}
	return nil, permanentError{fmt.Sprintf("embeddings model %s is not supported", s.typ)}
}

// cohereEmbedder calls Cohere's embed API. Texts are embedded as
// documents (input_type search_document), queries included.
type cohereEmbedder struct {
	ec      *execCtx
	baseURL string
	apiKey  string
	model   string
}

func (e *cohereEmbedder) embed(_ context.Context, texts []string) ([][]float64, error) {
	var out [][]float64
	for start := 0; start < len(texts); start += 96 {
		end := min(start+96, len(texts))
		body := map[string]interface{}{"model": e.model, "texts": texts[start:end], "input_type": "search_document", "embedding_types": []string{"float"}}
		res, _, err := e.ec.apiCall("POST", e.baseURL+"/v2/embed", map[string]string{"Authorization": "Bearer " + e.apiKey}, body, "")
		if err != nil {
			return nil, err
		}
		for _, v := range asList(asMap(res["embeddings"])["float"]) {
			out = append(out, floats(v))
		}
	}
	return out, checkEmbeddings(out, len(texts))
}

// ---------------------------------------------------------------- splitting & loading

type splitter struct {
	size, overlap int
	separators    []string
}

func (ec *execCtx) buildSplitter(cfg map[string]interface{}, i int) (*splitter, error) {
	subs := aiSubNodes(cfg, "ai_textSplitter")
	if len(subs) == 0 {
		return nil, nil
	}
	s := subs[0]
	p, err := ec.resolveSub(s, i)
	if err != nil {
		return nil, err
	}
	size := int(toFloatDefault(p["chunkSize"], 1000))
	overlap := int(toFloatDefault(p["chunkOverlap"], 0))
	sep := []string{"\n\n", "\n", " ", ""}
	switch short(s.typ) {
	case "textSplitterTokenSplitter":
		size, overlap = size*4, overlap*4 // ~4 characters per token
	case "textSplitterCharacterTextSplitter":
		sep = []string{firstNonEmpty(toString(p["separator"]), "\n\n"), ""}
	case "textSplitterRecursiveCharacterTextSplitter":
	default:
		return nil, permanentError{fmt.Sprintf("text splitter %s is not supported", s.typ)}
	}
	if overlap >= size {
		overlap = size / 5
	}
	return &splitter{size: size, overlap: overlap, separators: sep}, nil
}

// split implements recursive character splitting with overlap.
func (s *splitter) split(text string) []string {
	if s == nil || len(text) <= s.size {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []string{text}
	}
	var pieces []string
	var rec func(t string, seps []string)
	rec = func(t string, seps []string) {
		if len(t) <= s.size || len(seps) == 0 {
			pieces = append(pieces, t)
			return
		}
		sep := seps[0]
		if sep == "" {
			for start := 0; start < len(t); start += s.size {
				end := start + s.size
				if end > len(t) {
					end = len(t)
				}
				pieces = append(pieces, t[start:end])
			}
			return
		}
		if !strings.Contains(t, sep) {
			rec(t, seps[1:])
			return
		}
		for _, part := range strings.SplitAfter(t, sep) {
			if len(part) > s.size {
				rec(part, seps[1:])
			} else if part != "" {
				pieces = append(pieces, part)
			}
		}
	}
	rec(text, s.separators)
	var chunks []string
	cur := ""
	for _, p := range pieces {
		if len(cur)+len(p) > s.size && cur != "" {
			chunks = append(chunks, strings.TrimSpace(cur))
			tail := ""
			if s.overlap > 0 && len(cur) > s.overlap {
				tail = cur[len(cur)-s.overlap:]
				if j := strings.IndexAny(tail, " \n"); j >= 0 {
					tail = tail[j+1:]
				}
			}
			cur = tail
		}
		cur += p
	}
	if strings.TrimSpace(cur) != "" {
		chunks = append(chunks, strings.TrimSpace(cur))
	}
	return chunks
}

// loadDocuments turns the root node's input items into documents using its
// ai_document loader.
func (ec *execCtx) loadDocuments(cfg map[string]interface{}) ([]document, error) {
	subs := aiSubNodes(cfg, "ai_document")
	if len(subs) == 0 {
		return nil, permanentError{"no document loader connected"}
	}
	s := subs[0]
	var docs []document
	for i, it := range ec.items() {
		p, err := ec.resolveSub(s, i)
		if err != nil {
			return nil, err
		}
		sp, err := ec.buildSplitter(s.config, i)
		if err != nil {
			return nil, err
		}
		if sp == nil && toString(p["textSplittingMode"]) != "custom" && toFloatDefault(s.config[cfgVersion], 1) >= 1.1 {
			sp = &splitter{size: 1000, overlap: 200, separators: []string{"\n\n", "\n", " ", ""}}
		}
		meta := map[string]interface{}{}
		if o, ok := p["options"].(map[string]interface{}); ok {
			if mv, ok := o["metadata"].(map[string]interface{}); ok {
				for _, raw := range asList(mv["metadataValues"]) {
					m, _ := raw.(map[string]interface{})
					meta[toString(m["name"])] = m["value"]
				}
			}
		}
		var texts []string
		dataType := toString(p["dataType"])
		if short(s.typ) == "documentBinaryInputLoader" {
			dataType = "binary"
		}
		if dataType == "binary" {
			key := toString(p["binaryDataKey"])
			for name, raw := range it.Binary {
				if key != "" && toString(p["binaryMode"]) == "specificField" && name != key {
					continue
				}
				bm, _ := raw.(map[string]interface{})
				data, _ := base64.StdEncoding.DecodeString(toString(bm["data"]))
				mt := toString(bm["mimeType"])
				loader := toString(p["loader"])
				switch {
				case loader == "pdfLoader" || loader != "textLoader" && strings.Contains(mt, "pdf"):
					pages, _, err := pdfText(data, 0)
					if err != nil {
						return nil, err
					}
					texts = append(texts, strings.Join(pages, "\n\n"))
				case loader == "docxLoader" || loader == "epubLoader" || strings.Contains(mt, "officedocument"):
					return nil, permanentError{"document loader: " + firstNonEmpty(loader, mt) + " is not supported"}
				default:
					texts = append(texts, string(data))
				}
				meta["source"] = "blob"
				meta["blobType"] = mt
			}
		} else if toString(p["jsonMode"]) == "expressionData" {
			texts = append(texts, toString(p["jsonData"]))
		} else {
			texts = append(texts, mustJSON(it.JSON))
		}
		for _, t := range texts {
			for line, chunk := range sp.split(t) {
				m := cloneJSON(meta)
				m["loc"] = map[string]interface{}{"lines": map[string]interface{}{"from": float64(line + 1)}}
				docs = append(docs, document{PageContent: chunk, Metadata: m})
			}
		}
	}
	return docs, nil
}

// ---------------------------------------------------------------- stores

type scoredDoc struct {
	document
	Score float64
}

type vectorStore interface {
	add(ctx context.Context, docs []document, vecs [][]float64) error
	search(ctx context.Context, vec []float64, k int) ([]scoredDoc, error)
	clear(ctx context.Context) error
}

// in-memory store (vectorStoreInMemory)

type memVec struct {
	doc document
	vec []float64
}

var memStores sync.Map // key -> *memVectorStore

// maxMemVectors bounds each in-memory vector store.
const maxMemVectors = 50000

type memVectorStore struct {
	mu   sync.Mutex
	rows []memVec
}

func (m *memVectorStore) add(_ context.Context, docs []document, vecs [][]float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, d := range docs {
		m.rows = append(m.rows, memVec{d, vecs[i]})
	}
	if over := len(m.rows) - maxMemVectors; over > 0 {
		m.rows = m.rows[over:] // keep the newest documents
	}
	return nil
}

func (m *memVectorStore) search(_ context.Context, vec []float64, k int) ([]scoredDoc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]scoredDoc, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, scoredDoc{r.doc, cosine(vec, r.vec)})
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

func (m *memVectorStore) clear(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = nil
	return nil
}

func cosine(a, b []float64) float64 {
	var dot, na, nb float64
	for i := range a {
		if i >= len(b) {
			break
		}
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Qdrant (LangChain payload layout: {content, metadata})

type qdrantStore struct {
	ec         *execCtx
	base       string
	headers    map[string]string
	collection string
}

func (q *qdrantStore) path(p string) string {
	return q.base + "/collections/" + url.PathEscape(q.collection) + p
}

func (q *qdrantStore) add(ctx context.Context, docs []document, vecs [][]float64) error {
	if len(docs) == 0 {
		return nil
	}
	if _, err := q.ec.doRequest("GET", q.path(""), q.headers, nil, ""); err != nil {
		create := map[string]interface{}{"vectors": map[string]interface{}{"size": len(vecs[0]), "distance": "Cosine"}}
		if _, _, err := q.ec.apiCall("PUT", q.path(""), q.headers, create, ""); err != nil {
			return err
		}
	}
	var points []interface{}
	for i, d := range docs {
		points = append(points, map[string]interface{}{"id": uuid.NewString(), "vector": vecs[i], "payload": map[string]interface{}{"content": d.PageContent, "metadata": d.Metadata}})
	}
	_, _, err := q.ec.apiCall("PUT", q.path("/points?wait=true"), q.headers, map[string]interface{}{"points": points}, "")
	return err
}

func (q *qdrantStore) search(ctx context.Context, vec []float64, k int) ([]scoredDoc, error) {
	res, _, err := q.ec.apiCall("POST", q.path("/points/search"), q.headers, map[string]interface{}{"vector": vec, "limit": k, "with_payload": true}, "")
	if err != nil {
		return nil, err
	}
	var out []scoredDoc
	for _, r := range asList(res["result"]) {
		rm, _ := r.(map[string]interface{})
		pl, _ := rm["payload"].(map[string]interface{})
		meta, _ := pl["metadata"].(map[string]interface{})
		out = append(out, scoredDoc{document{PageContent: toString(pl["content"]), Metadata: meta}, toFloatDefault(rm["score"], 0)})
	}
	return out, nil
}

func (q *qdrantStore) clear(context.Context) error {
	_, err := q.ec.doRequest("DELETE", q.path(""), q.headers, nil, "")
	return err
}

// Pinecone (LangChain layout: metadata.text holds the content)

type pineconeStore struct {
	ec        *execCtx
	host      string
	headers   map[string]string
	namespace string
}

func (p *pineconeStore) add(ctx context.Context, docs []document, vecs [][]float64) error {
	for start := 0; start < len(docs); start += 100 {
		end := start + 100
		if end > len(docs) {
			end = len(docs)
		}
		var vectors []interface{}
		for i := start; i < end; i++ {
			meta := cloneJSON(docs[i].Metadata)
			for k, v := range meta {
				if _, ok := v.(map[string]interface{}); ok {
					meta[k] = mustJSON(v) // Pinecone metadata must be flat
				}
			}
			meta["text"] = docs[i].PageContent
			vectors = append(vectors, map[string]interface{}{"id": uuid.NewString(), "values": vecs[i], "metadata": meta})
		}
		if _, _, err := p.ec.apiCall("POST", p.host+"/vectors/upsert", p.headers, map[string]interface{}{"vectors": vectors, "namespace": p.namespace}, ""); err != nil {
			return err
		}
	}
	return nil
}

func (p *pineconeStore) search(ctx context.Context, vec []float64, k int) ([]scoredDoc, error) {
	res, _, err := p.ec.apiCall("POST", p.host+"/query", p.headers, map[string]interface{}{"vector": vec, "topK": k, "includeMetadata": true, "namespace": p.namespace}, "")
	if err != nil {
		return nil, err
	}
	var out []scoredDoc
	for _, m := range asList(res["matches"]) {
		mm, _ := m.(map[string]interface{})
		meta, _ := mm["metadata"].(map[string]interface{})
		text := toString(meta["text"])
		delete(meta, "text")
		out = append(out, scoredDoc{document{PageContent: text, Metadata: meta}, toFloatDefault(mm["score"], 0)})
	}
	return out, nil
}

func (p *pineconeStore) clear(context.Context) error {
	_, _, err := p.ec.apiCall("POST", p.host+"/vectors/delete", p.headers, map[string]interface{}{"deleteAll": true, "namespace": p.namespace}, "")
	return err
}

// Supabase / pgvector via PostgREST (LangChain layout)

type supabaseStore struct {
	ec        *execCtx
	base      string
	headers   map[string]string
	table     string
	queryName string
}

func (s *supabaseStore) add(ctx context.Context, docs []document, vecs [][]float64) error {
	var rows []interface{}
	for i, d := range docs {
		rows = append(rows, map[string]interface{}{"content": d.PageContent, "metadata": d.Metadata, "embedding": vecs[i]})
	}
	_, err := s.ec.doRequest("POST", s.base+"/rest/v1/"+url.PathEscape(s.table), s.headers, mustBytes(rows), "application/json")
	return err
}

func (s *supabaseStore) search(ctx context.Context, vec []float64, k int) ([]scoredDoc, error) {
	_, raw, err := s.ec.apiCall("POST", s.base+"/rest/v1/rpc/"+url.PathEscape(s.queryName), s.headers, map[string]interface{}{"query_embedding": vec, "match_count": k, "filter": map[string]interface{}{}}, "")
	if err != nil {
		return nil, err
	}
	var rows []map[string]interface{}
	_ = jsonUnmarshal(raw, &rows)
	var out []scoredDoc
	for _, r := range rows {
		meta, _ := r["metadata"].(map[string]interface{})
		out = append(out, scoredDoc{document{PageContent: toString(r["content"]), Metadata: meta}, toFloatDefault(r["similarity"], 0)})
	}
	return out, nil
}

func (s *supabaseStore) clear(context.Context) error {
	_, err := s.ec.doRequest("DELETE", s.base+"/rest/v1/"+url.PathEscape(s.table)+"?id=not.is.null", s.headers, nil, "")
	return err
}

// openStore connects to the vector store described by a node config.
func (ec *execCtx) openStore(typ string, cfg map[string]interface{}, p map[string]interface{}) (vectorStore, error) {
	tmp := subNode{typ: typ, config: cfg}
	switch short(typ) {
	case "vectorStoreInMemory":
		key := rlValue(p["memoryKey"])
		if key == "" {
			key = "vector_store_key"
		}
		// Scoped per namespace so tenants never share documents.
		key = engine.NamespaceFromContext(ec.ctx) + "\x00" + key
		v, _ := memStores.LoadOrStore(key, &memVectorStore{})
		return v.(*memVectorStore), nil
	case "vectorStoreQdrant":
		c, err := ec.subCredential(tmp, "qdrantApi")
		if err != nil {
			return nil, err
		}
		h := map[string]string{}
		if k := credString(c, "apiKey"); k != "" {
			h["api-key"] = k
		}
		return &qdrantStore{ec: ec, base: strings.TrimRight(credString(c, "qdrantUrl", "url"), "/"), headers: h, collection: rlValue(p["qdrantCollection"])}, nil
	case "vectorStorePinecone":
		c, err := ec.subCredential(tmp, "pineconeApi")
		if err != nil {
			return nil, err
		}
		h := map[string]string{"Api-Key": credString(c, "apiKey"), "X-Pinecone-API-Version": "2024-10"}
		index := rlValue(p["pineconeIndex"])
		info, _, err := ec.apiCall("GET", ec.apiBase("pinecone", "https://api.pinecone.io")+"/indexes/"+url.PathEscape(index), h, nil, "")
		if err != nil {
			return nil, err
		}
		host := toString(info["host"])
		if !strings.HasPrefix(host, "http") {
			host = "https://" + host
		}
		opts, _ := p["options"].(map[string]interface{})
		return &pineconeStore{ec: ec, host: host, headers: h, namespace: toString(opts["pineconeNamespace"])}, nil
	case "vectorStoreSupabase":
		c, err := ec.subCredential(tmp, "supabaseApi")
		if err != nil {
			return nil, err
		}
		key := credString(c, "serviceRole", "apiKey")
		opts, _ := p["options"].(map[string]interface{})
		return &supabaseStore{ec: ec, base: credBaseURL(c, ""), headers: map[string]string{"apikey": key, "Authorization": "Bearer " + key},
			table: rlValue(p["tableName"]), queryName: firstNonEmpty(toString(opts["queryName"]), "match_documents")}, nil
	case "vectorStorePGVector":
		c, err := ec.subCredential(tmp, "postgres")
		if err != nil {
			return nil, err
		}
		return ec.openPGVector(c, p)
	case "vectorStoreMongoDBAtlas":
		return ec.openMongoAtlas(tmp, p)
	}
	return nil, permanentError{fmt.Sprintf("vector store %s is not supported", typ)}
}

func docItem(d scoredDoc, withMeta bool) map[string]interface{} {
	doc := map[string]interface{}{"pageContent": d.PageContent}
	if withMeta {
		doc["metadata"] = d.Metadata
	}
	return map[string]interface{}{"document": doc, "score": d.Score}
}

// vectorStoreNode handles insert/load modes as a regular node.
func vectorStoreNode(ec *execCtx) ([][]Item, error) {
	mode, _ := ec.str(0, "mode", "retrieve")
	params := map[string]interface{}{}
	for k := range ec.params {
		v, err := ec.param(0, k, nil)
		if err != nil {
			return nil, err
		}
		params[k] = v
	}
	store, err := ec.openStore(ec.node.Type, ec.node.Config, params)
	if err != nil {
		return nil, err
	}
	emb, err := ec.buildEmbedder(ec.node.Config, 0)
	if err != nil {
		return nil, err
	}
	switch mode {
	case "insert":
		if toBool(params["clearStore"]) || toBool(mapGet(params, "options", "clearNamespace")) {
			if err := store.clear(ec.ctx); err != nil {
				return nil, err
			}
		}
		docs, err := ec.loadDocuments(ec.node.Config)
		if err != nil {
			return nil, err
		}
		texts := make([]string, len(docs))
		for i, d := range docs {
			texts[i] = d.PageContent
		}
		var vecs [][]float64
		if len(texts) > 0 {
			if vecs, err = emb.embed(ec.ctx, texts); err != nil {
				return nil, err
			}
		}
		if err := store.add(ec.ctx, docs, vecs); err != nil {
			return nil, err
		}
		out := make([]Item, len(docs))
		for i, d := range docs {
			out[i] = Item{JSON: map[string]interface{}{"pageContent": d.PageContent, "metadata": d.Metadata}, PairedItem: paired(0)}
		}
		return [][]Item{out}, nil
	case "update":
		up, ok := store.(vectorUpdater)
		if !ok {
			return nil, permanentError{"vector store: update mode is not supported by this store"}
		}
		docs, err := ec.loadDocuments(ec.node.Config)
		if err != nil {
			return nil, err
		}
		if len(docs) != 1 {
			return nil, permanentError{fmt.Sprintf("vector store update: the document loader must produce exactly one document (got %d); disable text splitting", len(docs))}
		}
		id, err := ec.str(0, "id", "")
		if err != nil {
			return nil, err
		}
		if id == "" {
			return nil, permanentError{"vector store update: ID is empty"}
		}
		vecs, err := emb.embed(ec.ctx, []string{docs[0].PageContent})
		if err != nil {
			return nil, err
		}
		if err := up.update(ec.ctx, id, docs[0], vecs[0]); err != nil {
			return nil, err
		}
		return [][]Item{{{JSON: map[string]interface{}{"id": id, "pageContent": docs[0].PageContent, "metadata": docs[0].Metadata}, PairedItem: paired(0)}}}, nil
	case "load":
		var out []Item
		for i := range ec.items() {
			prompt, err := ec.str(i, "prompt", "")
			if err != nil {
				return nil, err
			}
			k, _ := ec.num(i, "topK", 4)
			vecs, err := emb.embed(ec.ctx, []string{prompt})
			if err != nil {
				return nil, err
			}
			docs, err := store.search(ec.ctx, vecs[0], int(k))
			if err == nil {
				docs, err = ec.rerank(ec.node.Config, prompt, docs, i)
			}
			if err != nil {
				return nil, err
			}
			withMeta, _ := ec.boolean(i, "includeDocumentMetadata", true)
			for _, d := range docs {
				out = append(out, Item{JSON: docItem(d, withMeta), PairedItem: paired(i)})
			}
		}
		return [][]Item{out}, nil
	}
	return nil, permanentError{fmt.Sprintf("vector store mode %q is not supported as a step", mode)}
}

func mapGet(m map[string]interface{}, keys ...string) interface{} {
	var cur interface{} = m
	for _, k := range keys {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

// retrieve searches a vector store sub-node (ai_vectorStore of cfg).
func (ec *execCtx) retrieve(cfg map[string]interface{}, query string, k, i int) ([]scoredDoc, error) {
	subs := aiSubNodes(cfg, "ai_vectorStore")
	if len(subs) == 0 {
		return nil, permanentError{"no vector store connected"}
	}
	s := subs[0]
	p, err := ec.resolveSub(s, i)
	if err != nil {
		return nil, err
	}
	return ec.searchStore(s, p, query, k, i)
}

func (ec *execCtx) searchStore(s subNode, p map[string]interface{}, query string, k, i int) ([]scoredDoc, error) {
	store, err := ec.openStore(s.typ, s.config, p)
	if err != nil {
		return nil, err
	}
	emb, err := ec.buildEmbedder(s.config, i)
	if err != nil {
		return nil, err
	}
	vecs, err := emb.embed(ec.ctx, []string{query})
	if err != nil {
		return nil, err
	}
	docs, err := store.search(ec.ctx, vecs[0], k)
	if err != nil {
		return nil, err
	}
	return ec.rerank(s.config, query, docs, i)
}

func docsContext(docs []scoredDoc) string {
	parts := make([]string, len(docs))
	for i, d := range docs {
		parts[i] = d.PageContent
	}
	return strings.Join(parts, "\n\n")
}

// chainRetrievalQaNode answers a question from retrieved documents.
func chainRetrievalQaNode(ec *execCtx) ([][]Item, error) {
	var out []Item
	for i := range ec.items() {
		model, base, err := ec.buildModel(i)
		if err != nil {
			return nil, err
		}
		q, err := ec.promptFor(i, "text")
		if err != nil {
			return nil, err
		}
		rsub := aiSubNodes(ec.node.Config, "ai_retriever")
		if len(rsub) == 0 {
			return nil, permanentError{"Question and Answer Chain: no retriever connected"}
		}
		rp, err := ec.resolveSub(rsub[0], i)
		if err != nil {
			return nil, err
		}
		docs, err := ec.retrieve(rsub[0].config, q, int(toFloatDefault(rp["topK"], 4)), i)
		if err != nil {
			return nil, err
		}
		sys, _ := ec.str(i, "options.systemPromptTemplate", "Use the following pieces of context to answer the user's question.\nIf you don't know the answer, just say that you don't know, don't try to make up an answer.\n----------------\n{context}")
		req := base
		req.Messages = []chatMsg{{Role: "system", Content: strings.ReplaceAll(sys, "{context}", docsContext(docs))}, {Role: "user", Content: q}}
		res, err := model.Chat(ec.ctx, req)
		if err != nil {
			return nil, err
		}
		var resp interface{} = map[string]interface{}{"text": res.Content}
		if ec.version >= 1.5 {
			resp = res.Content
		}
		out = append(out, Item{JSON: map[string]interface{}{"response": resp}, PairedItem: paired(i)})
	}
	return [][]Item{out}, nil
}

// vectorStoreTool builds the "Answer questions with a vector store" tool and
// vector stores used directly as tools (mode retrieve-as-tool).
func (ec *execCtx) vectorStoreTool(s subNode, i int) (aiTool, error) {
	p, err := ec.resolveSub(s, i)
	if err != nil {
		return aiTool{}, err
	}
	schema := objSchema(map[string]interface{}{"input": map[string]interface{}{"type": "string", "description": "The question or search query"}}, []string{"input"})
	k := int(toFloatDefault(p["topK"], 4))
	if short(s.typ) == "toolVectorStore" {
		name := toolName(firstNonEmpty(toString(p["name"]), s.name()))
		desc := toString(p["description"])
		return aiTool{def: toolDef{Name: name, Description: desc, Schema: schema}, run: func(ctx context.Context, args map[string]interface{}) (string, error) {
			q := firstNonEmpty(toString(args["input"]), toString(args["query"]))
			docs, err := ec.retrieve(s.config, q, k, i)
			if err != nil {
				return "", err
			}
			// Answer with the tool's own model when connected.
			if len(aiSubNodes(s.config, "ai_languageModel")) > 0 {
				saved := ec.node
				tmp := *ec.node
				tmp.Config = s.config
				ec.node = &tmp
				model, base, err := ec.buildModel(i)
				ec.node = saved
				if err != nil {
					return "", err
				}
				req := base
				req.Messages = []chatMsg{{Role: "system", Content: "Use the following pieces of context to answer the question. If you don't know the answer, say so.\n----------------\n" + docsContext(docs)}, {Role: "user", Content: q}}
				res, err := model.Chat(ctx, req)
				if err != nil {
					return "", err
				}
				return res.Content, nil
			}
			return docsContext(docs), nil
		}}, nil
	}
	// vector store node in retrieve-as-tool mode
	name := toolName(firstNonEmpty(toString(p["toolName"]), s.name()))
	withMeta := toBool(firstNonNil(p["includeDocumentMetadata"], true))
	return aiTool{def: toolDef{Name: name, Description: toString(p["toolDescription"]), Schema: schema}, run: func(ctx context.Context, args map[string]interface{}) (string, error) {
		q := firstNonEmpty(toString(args["input"]), toString(args["query"]))
		docs, err := ec.searchStore(s, p, q, k, i)
		if err != nil {
			return "", err
		}
		list := make([]interface{}, len(docs))
		for n, d := range docs {
			list[n] = docItem(d, withMeta)["document"]
		}
		return truncate(mustJSON(list), 20000), nil
	}}, nil
}

func firstNonNil(a, b interface{}) interface{} {
	if a != nil {
		return a
	}
	return b
}

var vectorStoreTypes = map[string]bool{"vectorStoreInMemory": true, "vectorStoreQdrant": true, "vectorStorePinecone": true, "vectorStoreSupabase": true,
	"vectorStorePGVector": true, "vectorStoreMongoDBAtlas": true}

func ragSubNodeSupported(typ string) bool {
	s := short(typ)
	if _, ok := openAIEmbeddingTypes[s]; ok {
		return true
	}
	switch s {
	case "embeddingsGoogleGemini", "embeddingsCohere", "documentDefaultDataLoader", "documentBinaryInputLoader",
		"textSplitterRecursiveCharacterTextSplitter", "textSplitterTokenSplitter", "textSplitterCharacterTextSplitter",
		"retrieverVectorStore", "toolVectorStore", "rerankerCohere":
		return true
	}
	return vectorStoreTypes[s]
}

// vectorUpdater is implemented by stores whose documents have IDs; it
// backs the vector store nodes' "update" mode.
type vectorUpdater interface {
	update(ctx context.Context, id string, doc document, vec []float64) error
}

func (s *supabaseStore) update(ctx context.Context, id string, d document, vec []float64) error {
	row := map[string]interface{}{"content": d.PageContent, "metadata": d.Metadata, "embedding": vec}
	_, err := s.ec.doRequest("PATCH", s.base+"/rest/v1/"+url.PathEscape(s.table)+"?id=eq."+url.QueryEscape(id), s.headers, mustBytes(row), "application/json")
	return err
}

func (q *qdrantStore) update(ctx context.Context, id string, d document, vec []float64) error {
	var pid interface{} = id
	if n, err := strconv.ParseUint(id, 10, 64); err == nil {
		pid = n // Qdrant point IDs are unsigned integers or UUIDs
	}
	point := map[string]interface{}{"id": pid, "vector": vec, "payload": map[string]interface{}{"content": d.PageContent, "metadata": d.Metadata}}
	_, _, err := q.ec.apiCall("PUT", q.path("/points?wait=true"), q.headers, map[string]interface{}{"points": []interface{}{point}}, "")
	return err
}

func (p *pineconeStore) update(ctx context.Context, id string, d document, vec []float64) error {
	meta := cloneJSON(d.Metadata)
	for k, v := range meta {
		if _, ok := v.(map[string]interface{}); ok {
			meta[k] = mustJSON(v)
		}
	}
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta["text"] = d.PageContent
	_, _, err := p.ec.apiCall("POST", p.host+"/vectors/upsert", p.headers, map[string]interface{}{"vectors": []interface{}{map[string]interface{}{"id": id, "values": vec, "metadata": meta}}, "namespace": p.namespace}, "")
	return err
}
