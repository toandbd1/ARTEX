package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestValidSkillName(t *testing.T) {
	ok := []string{"web-recon", "a", "nuclei2", "\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd", "\xe7\xab\xaf\xe5\x8f\xa3\xe6\x89\xab\xe6\x8f\x8f-x", "\xe6\x97\xa5\xe6\x9c\xac\xe8\xaa\x9e\xe3\x82\xb9\xe3\x82\xad\xe3\x83\xab"}
	bad := []string{
		"", "Web-Recon", "-lead", "trail-", "dou--ble", "has space", "\xe4\xb8\xad\xe6\x96\x87 \xe6\x8a\x80\xe8\x83\xbd",
		"dot.name", "a/b", `a\b`, "..", ".", "\xe4\xb8\xad\xe6\x96\x87/\xe6\x8a\x80\xe8\x83\xbd", "sk\x00ill", "\xe4\xb8\xad\xe2\x80\xae\xe6\x96\x87",
		strings.Repeat("a", 65), "1abc", "\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd!",
	}
	for _, n := range ok {
		if !validSkillName(n) {
			t.Errorf("validSkillName(%q) = false, want true", n)
		}
	}
	for _, n := range bad {
		if validSkillName(n) {
			t.Errorf("validSkillName(%q) = true, want false", n)
		}
	}
}

func TestSkillRelPath(t *testing.T) {
	ok := map[string]string{
		"SKILL.md":           "SKILL.md",
		"scripts/run.py":     "scripts/run.py",
		"\xe5\x8f\x82\xe8\x80\x83/\xe4\xb8\xad\xe6\x96\x87\xe8\xaf\xb4\xe6\x98\x8e.md":         "\xe5\x8f\x82\xe8\x80\x83/\xe4\xb8\xad\xe6\x96\x87\xe8\xaf\xb4\xe6\x98\x8e.md",
		"references/a b.txt": "references/a b.txt",
		"./SKILL.md":         "SKILL.md",
		"assets/\xe5\x9b\xbe\xe7\x89\x87-1_v2.png": "assets/\xe5\x9b\xbe\xe7\x89\x87-1_v2.png",
	}
	for in, want := range ok {
		got, msg := skillRelPath(in)
		if msg != "" || got != want {
			t.Errorf("skillRelPath(%q) = (%q, %q), want (%q, \"\")", in, got, msg, want)
		}
	}
	bad := []string{
		"", "../etc/passwd", "a/../../b", "/abs/path", "a//b", `..\..\x`,
		"%2e%2e/x", "a\x00b", "\xe4\xb8\xad\xe6\x96\x87 \xe5\x90\x8d.md", "\xe4\xb8\xad\xe2\x80\xae\xe6\x96\x87.md", "a#b.md", "a?b.md",
		"a:b.md", string([]byte{0xd6, 0xd0}) + ".md", // byte GBK trần: UTF-8 không hợp lệ
		strings.Repeat("a", maxSkillPathLen+1),
	}
	for _, in := range bad {
		if got, msg := skillRelPath(in); msg == "" {
			t.Errorf("skillRelPath(%q) = (%q, \"\"), want rejection", in, got)
		}
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// zipFile describes one entry for buildZip.
type zipFile struct {
	name    string
	body    string
	method  uint16
	nonUTF8 bool // write the name bytes as-is (gói GBK)
}

func buildZip(t *testing.T, files ...zipFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.RegisterCompressor(zipMethodZstd, zstd.ZipCompressor())
	// Deflate64 không có encoder Go thuần; ở đây ghi nguyên văn, chỉ để gắn cờ method 9 cho entry ——
	// điều cần assert là "thông báo gì khi phương thức không được hỗ trợ", không thực sự giải nén nó.
	zw.RegisterCompressor(zipMethodDeflate64, func(w io.Writer) (io.WriteCloser, error) {
		return nopWriteCloser{w}, nil
	})
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: f.method, NonUTF8: f.nonUTF8}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("CreateHeader(%q): %v", f.name, err)
		}
		if _, err := io.WriteString(w, f.body); err != nil {
			t.Fatalf("write %q: %v", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// uploadZip posts raw zip bytes to fsUploadSkill and returns the response.
func uploadZip(t *testing.T, skillDir string, filename string, data []byte) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/skills/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	(&Server{skillDir: skillDir}).fsUploadSkill(rr, req)

	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr, out
}

// Các fixture tên/file CJK trong file này được viết dưới dạng escape \x.. (byte UTF-8)
// để mã nguồn không chứa ký tự Hán; giá trị runtime giữ nguyên như bản gốc.
const zhSkillMD = "---\nname: \xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd\ndescription: \xe6\xb5\x8b\xe8\xaf\x95\n---\n\xe6\xad\xa3\xe6\x96\x87\n"

// A zstd-compressed archive (phương thức nén tùy chọn của WinZip) used to blow up with
// "zip: unsupported compression"; it now installs like any Deflate archive.
func TestUploadSkillZstdAndChineseNames(t *testing.T) {
	dir := t.TempDir()
	data := buildZip(t,
		zipFile{name: "\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd/SKILL.md", body: zhSkillMD, method: zipMethodZstd},
		zipFile{name: "\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd/\xe5\x8f\x82\xe8\x80\x83/\xe8\xaf\xb4\xe6\x98\x8e \xe6\x96\x87\xe6\xa1\xa3.md", body: "\xe5\x8f\x82\xe8\x80\x83", method: zipMethodZstd},
		zipFile{name: "\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd/scripts/run.py", body: "print(1)", method: zip.Deflate},
	)
	rr, out := uploadZip(t, dir, "\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd.zip", data)
	if rr.Code != 201 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body)
	}
	if out["name"] != "\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd" {
		t.Fatalf("name = %v, want \xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd", out["name"])
	}
	for _, rel := range []string{"SKILL.md", "\xe5\x8f\x82\xe8\x80\x83/\xe8\xaf\xb4\xe6\x98\x8e \xe6\x96\x87\xe6\xa1\xa3.md", "scripts/run.py"} {
		if _, err := os.Stat(filepath.Join(dir, "\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd", rel)); err != nil {
			t.Errorf("missing extracted file %q: %v", rel, err)
		}
	}
}

// GBK-named entries (7-Zip / Windows Explorer on Chinese Windows) must be decoded rather
// than rejected as invalid UTF-8 paths.
func TestUploadSkillGBKNames(t *testing.T) {
	gbk := func(s string) string {
		b, err := simplifiedchinese.GBK.NewEncoder().String(s)
		if err != nil {
			t.Fatalf("gbk encode %q: %v", s, err)
		}
		return b
	}
	dir := t.TempDir()
	data := buildZip(t,
		zipFile{name: gbk("\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd/SKILL.md"), body: zhSkillMD, method: zip.Deflate, nonUTF8: true},
		zipFile{name: gbk("\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd/\xe5\x8f\x82\xe8\x80\x83\xe8\xb5\x84\xe6\x96\x99.md"), body: "\xe5\x86\x85\xe5\xae\xb9", method: zip.Deflate, nonUTF8: true},
	)
	rr, out := uploadZip(t, dir, "skill.zip", data)
	if rr.Code != 201 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body)
	}
	if out["name"] != "\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd" {
		t.Fatalf("name = %v, want \xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd", out["name"])
	}
	if _, err := os.Stat(filepath.Join(dir, "\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd", "\xe5\x8f\x82\xe8\x80\x83\xe8\xb5\x84\xe6\x96\x99.md")); err != nil {
		t.Errorf("GBK-named entry not extracted: %v", err)
	}
}

// An archive we genuinely cannot decode should name the method in the localized
// (Vietnamese) message instead of surfacing "zip: unsupported compression algorithm".
func TestUploadSkillUnsupportedMethod(t *testing.T) {
	data := buildZip(t,
		zipFile{name: "demo/SKILL.md", body: "---\nname: demo\n---\n", method: zipMethodDeflate64},
	)
	rr, out := uploadZip(t, t.TempDir(), "demo.zip", data)
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body)
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "Deflate64") || !strings.Contains(msg, "không được hỗ trợ") {
		t.Fatalf("error = %q, want a Vietnamese message naming Deflate64", msg)
	}
}

func TestUploadSkillEncrypted(t *testing.T) {
	data := buildZip(t, zipFile{name: "demo/SKILL.md", body: "---\nname: demo\n---\n", method: zip.Deflate})
	// flip the "encrypted" general-purpose flag bit in the local file header
	// (offset 6) and in the central directory copy (offset 8).
	local := bytes.Index(data, []byte("PK\x03\x04"))
	central := bytes.Index(data, []byte("PK\x01\x02"))
	if local < 0 || central < 0 {
		t.Fatal("could not locate zip headers")
	}
	data[local+6] |= 1
	data[central+8] |= 1

	rr, out := uploadZip(t, t.TempDir(), "demo.zip", data)
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "đã bị mã hóa") {
		t.Fatalf("error = %q, want gợi ý mã hóa", msg)
	}
}

// Zip-slip must still be refused now that the path check accepts Unicode.
func TestUploadSkillRejectsTraversal(t *testing.T) {
	data := buildZip(t,
		zipFile{name: "demo/SKILL.md", body: "---\nname: demo\n---\n", method: zip.Deflate},
		zipFile{name: "demo/../../evil.sh", body: "rm -rf /", method: zip.Deflate},
	)
	dir := t.TempDir()
	rr, out := uploadZip(t, dir, "demo.zip", data)
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "đường dẫn không hợp lệ") {
		t.Fatalf("error = %q, want đường dẫn không hợp lệ", msg)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("upload left files behind: %v", entries)
	}
}

func TestSkillNameFromFrontmatterQuoted(t *testing.T) {
	got := skillNameFromFrontmatter([]byte("---\nname: \"\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd\"\ndescription: x\n---\n"))
	if got != "\xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd" {
		t.Fatalf("name = %q, want \xe4\xb8\xad\xe6\x96\x87\xe6\x8a\x80\xe8\x83\xbd", got)
	}
}
