package n8n

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/models"
)

func TestArchiveCryptoPDFNodes(t *testing.T) {
	h := newHarness(t, nil)
	pdf := base64.StdEncoding.EncodeToString(minimalPDF("Hello PDF"))
	txt := base64.StdEncoding.EncodeToString([]byte("hello archive"))
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		nd("Seed", "set", 3.4, map[string]interface{}{"assignments": map[string]interface{}{"assignments": []interface{}{
			assign("pdf", "string", pdf), assign("txt", "string", txt), assign("id", "string", "r1")}}}),
		nd("PDF bin", "convertToFile", 1.1, map[string]interface{}{"operation": "toBinary", "sourceProperty": "pdf"}),
		nd("Read PDF", "readPDF", 1, nil),
		nd("Txt bin", "convertToFile", 1.1, map[string]interface{}{"operation": "toBinary", "sourceProperty": "txt", "options": map[string]interface{}{"fileName": "a.txt"}}),
		nd("Zip", "compression", 1.1, map[string]interface{}{"operation": "compress", "fileName": "out.zip"}),
		nd("Unzip", "compression", 1.1, nil),
		nd("Gzip", "compression", 1.1, map[string]interface{}{"operation": "compress", "outputFormat": "gzip", "outputPrefix": "gz"}),
		nd("Gunzip", "compression", 1.1, map[string]interface{}{"binaryPropertyName": "gz0"}),
		nd("MD5", "crypto", 1, map[string]interface{}{"value": "abc", "dataPropertyName": "md5"}),
		nd("HMAC", "crypto", 1, map[string]interface{}{"action": "hmac", "type": "SHA256", "value": "abc", "secret": "key", "dataPropertyName": "sig"}),
		nd("UUID", "crypto", 1, map[string]interface{}{"action": "generate", "dataPropertyName": "uuid"}),
		nd("Save", "executionData", 1, map[string]interface{}{"dataToSave": map[string]interface{}{"values": []interface{}{map[string]interface{}{"key": "requestId", "value": "={{ $json.id }}"}}}}),
	}, "Start->Seed", "Seed->PDF bin", "PDF bin->Read PDF", "Seed->Txt bin", "Txt bin->Zip", "Zip->Unzip", "Txt bin->Gzip", "Gzip->Gunzip",
		"Seed->MD5", "MD5->HMAC", "HMAC->UUID", "UUID->Save")
	_, steps := h.run(t, data, "")

	if got := itemsOf(t, steps["Read PDF"], 0); len(got) != 1 || !strings.Contains(toString(got[0]["text"]), "Hello PDF") || got[0]["numpages"] != 1.0 {
		t.Fatalf("read pdf: %v", got)
	}
	for _, name := range []string{"Unzip", "Gunzip"} {
		out := decodeOutput(steps[name].Output)[0]
		if len(out) != 1 {
			t.Fatalf("%s: %d items", name, len(out))
		}
		b, _, fname, err := binaryOf(out[0], "file_0")
		if err != nil || string(b) != "hello archive" || fname != "a.txt" {
			t.Fatalf("%s: %q %q %v (binary %v)", name, b, fname, err, out[0].Binary)
		}
	}
	got := itemsOf(t, steps["Save"], 0)
	if len(got) != 1 || got[0]["md5"] != "900150983cd24fb0d6963f7d28e17f72" ||
		got[0]["sig"] != "9c196e32dc0175f86f4b1cb89289d6619de6bee699e4c378e68309ed97a1a6ab" ||
		len(toString(got[0]["uuid"])) != 36 || got[0]["id"] != "r1" {
		t.Fatalf("crypto/executionData: %v", got)
	}
}

func TestDecompressLimits(t *testing.T) {
	defer func(b int64, n int) { maxDecompressedBytes, maxArchiveEntries = b, n }(maxDecompressedBytes, maxArchiveEntries)
	maxDecompressedBytes, maxArchiveEntries = 1000, 3

	mkZip := func(files int, size int) Item {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for i := 0; i < files; i++ {
			w, _ := zw.Create(strings.Repeat("f", i+1) + ".txt")
			_, _ = w.Write(make([]byte, size))
		}
		_ = zw.Close()
		return Item{JSON: map[string]interface{}{}, Binary: map[string]interface{}{"data": binaryData(buf.Bytes(), "application/zip", "a.zip")}}
	}
	ec := &execCtx{node: &models.Node{}, params: map[string]interface{}{}}
	if _, err := decompressItem(ec, 0, mkZip(1, 5000), []string{"data"}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("zip bomb not stopped: %v", err)
	}
	if _, err := decompressItem(ec, 0, mkZip(5, 1), []string{"data"}); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("entry limit not enforced: %v", err)
	}
	if _, err := decompressItem(ec, 0, mkZip(2, 400), []string{"data"}); err != nil {
		t.Fatalf("archive within limits: %v", err)
	}
}

func TestEvaluationNodesOutsideEvaluation(t *testing.T) {
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		nd("Eval Trigger", "evaluationTrigger", 4.7, map[string]interface{}{"source": "dataTable"}),
		nd("Seed", "set", 3.4, map[string]interface{}{"mode": "raw", "jsonOutput": `{"q":"hi"}`}),
		nd("Evaluating?", "evaluation", 4.8, map[string]interface{}{"operation": "checkIfEvaluating"}),
		nd("Metrics", "evaluation", 4.8, map[string]interface{}{"operation": "setMetrics"}),
		nd("Normal", "noOp", 1, nil),
	}, "Start->Seed", "Eval Trigger->Seed", "Seed->Evaluating?", "Evaluating?:0->Metrics", "Evaluating?:1->Normal")
	_, rep, _ := Import(data, ImportOptions{})
	if !rep.Runnable {
		t.Fatalf("not runnable: %+v", rep.Blocking)
	}
	_, steps := newHarness(t, nil).run(t, data, "")
	if got := itemsOf(t, steps["Normal"], 0); len(got) != 1 || got[0]["q"] != "hi" {
		t.Fatalf("normal: %v", got)
	}
	if s := steps["Metrics"]; s != nil && s.Status == "COMPLETED" && len(itemsOf(t, s, 0)) > 0 {
		t.Fatal("evaluation branch ran")
	}
}
