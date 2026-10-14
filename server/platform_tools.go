package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// Công cụ thao tác nền tảng (dành cho Auto agent tích hợp sẵn): tạo/sửa skill, công cụ tùy biến,
// MCP. Đều là công cụ host, seed vào bảng tools, mặc định gắn với auto, được tiêm qua hostTools.
// Tái dùng logic db/hệ thống tệp hiện có.

func (s *Server) platformTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolCreateSkill(),
		s.toolUpdateSkillFile(),
		s.toolCreateCustomTool(),
		s.toolUpdateCustomTool(),
		s.toolCreateMCP(),
		s.toolUpdateMCP(),
		s.toolDeleteAssetsByHost(),
	}
}

// platformToolKeys are the tool keys the Auto agent gets bound by default.
var platformToolKeys = []string{
	"create_skill", "update_skill_file",
	"create_custom_tool", "update_custom_tool",
	"create_mcp", "update_mcp",
	"delete_assets_by_host",
}

// ---- assets ----

// toolDeleteAssetsByHost hard-deletes every asset tied to one host (exact match).
// Platform-level (not a per-task tool): operates on the global, cross-task asset store.
func (s *Server) toolDeleteAssetsByHost() actool.CoreTool {
	return wrTool("delete_assets_by_host",
		"Xóa tài sản chính xác theo host: xóa tên miền/tên miền con của host đó, cùng các dịch vụ (service), interface (endpoint) thuộc về nó.\n"+
			"host khớp hoàn toàn (chữ thường, đã bỏ khoảng trắng), không phải khớp mờ/wildcard.\n"+
			"Truyền tên miền gốc (vd example.com) sẽ xóa kèm các tên miền con của nó cùng dịch vụ/interface; truyền tên miền con (vd a.example.com) hoặc IP chỉ xóa bản thân host đó cùng dịch vụ/interface của nó.\n"+
			"⚠️ Xóa cứng, tác động tới kho tài sản toàn cục (chia sẻ xuyên tác vụ), không thể hoàn tác.",
		objSchema(map[string]any{
			"host": strParam("host cần xóa: tên miền/tên miền con/IP. Khớp hoàn toàn, vd example.com hoặc a.example.com hoặc 1.2.3.4"),
		}, "host"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			as := s.assetStore()
			if as == nil {
				return actool.Errorf("kho tài sản chưa được khởi tạo"), nil
			}
			var a struct {
				Host string `json:"host"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Host) == "" {
				return actool.Errorf("host không được để trống"), nil
			}
			counts, err := as.DeleteByHost(a.Host)
			if err != nil {
				return actool.Errorf("xóa thất bại: " + err.Error()), nil
			}
			var total int64
			for _, n := range counts {
				total += n
			}
			return jsonResult(map[string]any{
				"host":            a.Host,
				"deleted":         total,
				"deleted_by_type": counts,
			})
		})
}

// ---- skills ----

func (s *Server) toolCreateSkill() actool.CoreTool {
	return wrTool("create_skill",
		"Tạo một skill mới (viết SKILL.md, theo chuẩn agentskills.io). name gồm chữ thường/số/dấu gạch nối.",
		objSchema(map[string]any{
			"name":         strParam("tên skill (bắt đầu bằng chữ thường, gồm chữ/số/dấu gạch nối)"),
			"description":  strParam("mô tả skill (bắt buộc, nói rõ nó làm gì/khi nào dùng)"),
			"instructions": strParam("nội dung Markdown chính (tùy chọn)"),
		}, "name", "description"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Name, Description, Instructions string }
			_ = json.Unmarshal(in, &a)
			if !validSkillName(a.Name) {
				return actool.Errorf("tên skill không hợp lệ (bắt đầu bằng chữ thường, chỉ gồm chữ/số/dấu gạch nối, ≤64)"), nil
			}
			if strings.TrimSpace(a.Description) == "" {
				return actool.Errorf("description bắt buộc"), nil
			}
			path := filepath.Join(s.skillDir, a.Name)
			if _, err := os.Stat(path); err == nil {
				return actool.Errorf("skill đã tồn tại: " + a.Name), nil
			}
			if err := os.MkdirAll(path, 0o755); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var b strings.Builder
			b.WriteString("---\n")
			fmt.Fprintf(&b, "name: %s\n", a.Name)
			fmt.Fprintf(&b, "description: %s\n", a.Description)
			b.WriteString("---\n")
			if strings.TrimSpace(a.Instructions) != "" {
				b.WriteString(a.Instructions)
			} else {
				fmt.Fprintf(&b, "## %s\n\n1. \n2. \n3. \n", a.Name)
			}
			if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(b.String()), 0o644); err != nil {
				_ = os.RemoveAll(path)
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("skill created: " + a.Name), nil
		})
}

func (s *Server) toolUpdateSkillFile() actool.CoreTool {
	return wrTool("update_skill_file",
		"Ghi/ghi đè một tệp trong skill nào đó (mặc định SKILL.md). Dùng để sửa nội dung kỹ năng hoặc thêm script/tham chiếu.",
		objSchema(map[string]any{
			"name":    strParam("tên skill"),
			"file":    strParam("đường dẫn tương đối (tùy chọn, mặc định SKILL.md, vd scripts/run.py)"),
			"content": strParam("nội dung tệp đầy đủ"),
		}, "name", "content"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Name, File, Content string }
			_ = json.Unmarshal(in, &a)
			if !validSkillName(a.Name) {
				return actool.Errorf("tên skill không hợp lệ"), nil
			}
			skillPath := filepath.Join(s.skillDir, a.Name)
			if _, err := os.Stat(skillPath); os.IsNotExist(err) {
				return actool.Errorf("skill không tồn tại: " + a.Name), nil
			}
			rel := strings.TrimSpace(a.File)
			if rel == "" {
				rel = "SKILL.md"
			}
			clean, msg := skillRelPath(rel)
			if msg != "" {
				return actool.Errorf("đường dẫn không hợp lệ: " + msg), nil
			}
			full := filepath.Join(skillPath, clean)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if err := os.WriteFile(full, []byte(a.Content), 0o644); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("skill file written: " + a.Name + "/" + clean), nil
		})
}

// ---- custom tools ----

type customToolToolInput struct {
	Key         string          `json:"key"`
	Description string          `json:"description"`
	Kind        string          `json:"kind"`
	Exec        json.RawMessage `json:"exec"`
	Schema      json.RawMessage `json:"schema"`
	Agents      []string        `json:"agents"`
	Deferred    bool            `json:"deferred"`
	Enabled     *bool           `json:"enabled"`
}

func customToolSchema(keyDesc string) map[string]any {
	return objSchema(map[string]any{
		"key":         strParam(keyDesc),
		"description": strParam("mô tả gửi cho mô hình"),
		"kind":        strParam("shell | command | script(chỉ Python) | http. shell=khai báo môi trường bash (chỉ thông báo cho mô hình rằng công cụ có thể gọi trực tiếp trong bash, không cần exec/schema); ba loại còn lại cần cung cấp exec"),
		"exec":        map[string]any{"type": "object", "description": "đặc tả thực thi (loại shell không cần): command→{command}; script→{code}; http→{method,url,headers,body,proxy,use_recording_proxy}"},
		"schema":      map[string]any{"type": "object", "description": "JSON-Schema tham số (shell/command/script có thể để trống; http bắt buộc và phải chứa properties)"},
		"agents":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "agent key được gắn (tùy chọn)"},
		"deferred":    map[string]any{"type": "boolean", "description": "có hoãn lại không (loại shell không hiệu lực; chỉ bật cho các công cụ ít dùng thuộc command/script/http)"},
		"enabled":     map[string]any{"type": "boolean", "description": "có bật không (mặc định true)"},
	}, "key", "kind")
}

func toDBTool(a customToolToolInput) *db.Tool {
	enabled := true
	if a.Enabled != nil {
		enabled = *a.Enabled
	}
	return &db.Tool{
		Key: a.Key, Description: a.Description, Schema: a.Schema, Agents: a.Agents,
		Enabled: enabled, Kind: a.Kind, Exec: a.Exec, Deferred: a.Deferred,
	}
}

func (s *Server) toolCreateCustomTool() actool.CoreTool {
	return wrTool("create_custom_tool", "【QUAN TRỌNG】Khi cài đặt một số công cụ mà nền tảng chưa có, hãy gọi công cụ này để đưa công cụ đã cài vào nền tảng, giúp nền tảng có thể gọi được! Tạo một công cụ tùy biến (shell/command/script/http). shell=khai báo môi trường bash, chỉ cần key+description+agents, không cần exec/schema.",
		customToolSchema("key công cụ (bắt đầu bằng chữ thường, gồm chữ/số/dấu gạch dưới)"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a customToolToolInput
			_ = json.Unmarshal(in, &a)
			a.Key = strings.TrimSpace(a.Key)
			if !reToolKey.MatchString(a.Key) {
				return actool.Errorf("key phải bắt đầu bằng chữ thường, chỉ chứa chữ thường/số/dấu gạch dưới"), nil
			}
			if a.Kind != "shell" && a.Kind != "command" && a.Kind != "script" && a.Kind != "http" {
				return actool.Errorf("kind phải là shell / command / script / http"), nil
			}
			if a.Kind == "http" && !hasSchemaProps(a.Schema) {
				return actool.Errorf("công cụ http phải cung cấp JSON Schema tham số (không được để trống)"), nil
			}
			if exist, _ := s.m.pg.GetTool(a.Key); exist != nil {
				return actool.Errorf("key này đã tồn tại: " + a.Key), nil
			}
			if err := s.m.pg.CreateCustomTool(toDBTool(a)); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("custom tool created: " + a.Key), nil
		})
}

func (s *Server) toolUpdateCustomTool() actool.CoreTool {
	return wrTool("update_custom_tool", "Sửa một công cụ tùy biến đã có (theo key).",
		customToolSchema("key của công cụ tùy biến cần sửa"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a customToolToolInput
			_ = json.Unmarshal(in, &a)
			existing, _ := s.m.pg.GetTool(a.Key)
			if existing == nil || existing.System {
				return actool.Errorf("chỉ có thể sửa công cụ tùy biến: " + a.Key), nil
			}
			if a.Kind != "shell" && a.Kind != "command" && a.Kind != "script" && a.Kind != "http" {
				return actool.Errorf("kind phải là shell / command / script / http"), nil
			}
			if a.Kind == "http" && !hasSchemaProps(a.Schema) {
				return actool.Errorf("công cụ http phải cung cấp JSON Schema tham số (không được để trống)"), nil
			}
			if err := s.m.pg.UpdateCustomTool(toDBTool(a)); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("custom tool updated: " + a.Key), nil
		})
}

// ---- MCP ----

type mcpToolInput struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Transport string          `json:"transport"`
	Command   string          `json:"command"`
	Args      json.RawMessage `json:"args"`
	Env       json.RawMessage `json:"env"`
	URL       string          `json:"url"`
	Enabled   *bool           `json:"enabled"`
	Insecure  *bool           `json:"insecure"`
}

func mcpSchema(withID bool) map[string]any {
	props := map[string]any{
		"name":      strParam("tên máy chủ MCP"),
		"transport": strParam("stdio | http / sse"),
		"command":   strParam("lệnh khởi động cho stdio (vd npx)"),
		"args":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "mảng tham số lệnh"},
		"env":       map[string]any{"type": "object", "description": "biến môi trường {KEY:VALUE}"},
		"url":       strParam("URL của http/sse"),
		"enabled":   map[string]any{"type": "boolean", "description": "có bật không (mặc định true)"},
		"insecure":  map[string]any{"type": "boolean", "description": "http: bỏ qua kiểm chứng chứng chỉ TLS (đặt true khi dùng chứng chỉ tự ký, mặc định false)"},
	}
	required := []string{"name", "transport"}
	if withID {
		props["id"] = map[string]any{"type": "integer", "description": "id máy chủ MCP cần sửa"}
		required = []string{"id", "name", "transport"}
	}
	return objSchema(props, required...)
}

func (a mcpToolInput) toDB() *db.MCPServer {
	enabled := true
	if a.Enabled != nil {
		enabled = *a.Enabled
	}
	insecure := false
	if a.Insecure != nil {
		insecure = *a.Insecure
	}
	return &db.MCPServer{
		ID: a.ID, Name: a.Name, Transport: a.Transport, Command: a.Command,
		Args: a.Args, Env: a.Env, URL: a.URL, Enabled: enabled, Insecure: insecure,
	}
}

func (s *Server) toolCreateMCP() actool.CoreTool {
	return wrTool("create_mcp", "Tạo một máy chủ MCP (stdio/http/sse). Sau khi tạo, các công cụ của nó cần được cấp quyền theo khả năng hiển thị cho từng agent.",
		mcpSchema(false),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a mcpToolInput
			_ = json.Unmarshal(in, &a)
			a.ID = 0
			if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Transport) == "" {
				return actool.Errorf("name / transport bắt buộc"), nil
			}
			id, err := s.m.pg.SaveMCP(a.toDB())
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("mcp created: id=%d name=%s", id, a.Name)), nil
		})
}

func (s *Server) toolUpdateMCP() actool.CoreTool {
	return wrTool("update_mcp", "Sửa một máy chủ MCP đã có (theo id).",
		mcpSchema(true),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a mcpToolInput
			_ = json.Unmarshal(in, &a)
			if a.ID == 0 {
				return actool.Errorf("id bắt buộc"), nil
			}
			if _, err := s.m.pg.SaveMCP(a.toDB()); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("mcp updated: id=%d", a.ID)), nil
		})
}
