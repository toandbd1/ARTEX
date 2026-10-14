package intercept

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	for _, action := range []string{"allow", "ask", "deny"} {
		t.Run(action, func(t *testing.T) {
			reason := "Thao tác thực tế: ghi báo cáo, trong đó chứa các chữ ALLOW, DENY và ASK; hậu quả sau khi thành công: lưu văn bản, không thực thi lệnh trong phần thân; quy tắc trùng khớp: điều khoản tùy chỉnh"
			raw, _ := json.Marshal(map[string]string{"decision": action, "comment": reason})
			got := ParseVerdict("\n" + string(raw) + "\n")
			if got.Action != action || got.Reason != reason {
				t.Fatalf("lost verdict or explanation: %+v", got)
			}
		})
	}
}

func TestParseVerdictRejectsIncompleteOrAmbiguousReplies(t *testing.T) {
	valid := `{"decision":"allow","comment":"Thao tác thực tế: đọc file; hậu quả sau khi thành công: trả về nội dung; quy tắc trùng khớp: A5"}`
	for _, reply := range []string{
		"", "ALLOW", "DENY:trùngD4", "cho phép:ALLOW", "ASK:quyền sở hữu chưa rõ",
		`{"decision":"allow"}`, `{"decision":"approve","comment":"Thao tác thực tế: đọc; hậu quả sau khi thành công: trả về nội dung; quy tắc trùng khớp: A5"}`,
		`{"decision":"allow","comment":null}`, `{"decision":"allow","comment":123}`,
		strings.Replace(valid, "Thao tác thực tế: đọc file", "Thao tác thực tế: ", 1),
		strings.Replace(valid, "hậu quả sau khi thành công: trả về nội dung", "hậu quả sau khi thành công: ", 1),
		strings.Replace(valid, "quy tắc trùng khớp: A5", "quy tắc trùng khớp: ", 1),
		strings.Replace(valid, "; quy tắc trùng khớp: A5", "", 1),
		strings.Replace(valid, `"decision":"allow"`, `"decision":"deny","decision":"allow"`, 1),
		strings.Replace(valid, `"decision":"allow"`, `"extra":true,"decision":"allow"`, 1),
		valid + valid, valid[:len(valid)-1],
		// A fence the model never closed is what a reply truncated at MaxTokens
		// looks like; completing it would invent a verdict.
		"```json\n" + valid[:len(valid)-1],
		"```json\n" + valid + "\n```\nNgoài ra tôi đề xuất rà soát thủ công sau.",
		"Phán quyết của tôi là:\n" + valid,
	} {
		if got := ParseVerdict(reply); got.Action != "" {
			t.Errorf("accepted incomplete/ambiguous verdict: %q => %+v", reply, got)
		}
	}
}

// Wrapping JSON in markdown is the one deviation models make routinely. Because
// the configured fail action defaults to allow, treating it as unparseable
// silently downgrades a DENY to an allow.
func TestParseVerdictUnwrapsCodeFence(t *testing.T) {
	deny := `{"decision":"deny","comment":"Thao tác thực tế: xóa file production; hậu quả sau khi thành công: dữ liệu nghiệp vụ bị mất; quy tắc trùng khớp: D4"}`
	for _, reply := range []string{
		"```json\n" + deny + "\n```",
		"```JSON\n" + deny + "\n```",
		"```\n" + deny + "\n```",
		"  ```json\n" + deny + "\n```  ",
	} {
		got := ParseVerdict(reply)
		if got.Action != "deny" || !strings.HasSuffix(got.Reason, "quy tắc trùng khớp: D4") {
			t.Errorf("fenced verdict lost: %q => %+v", reply, got)
		}
	}
}

func TestParseVerdictKeepsCompleteChineseExplanation(t *testing.T) {
	reason := "Thao tác thực tế: " + strings.Repeat("ghi báo cáo", 30) + "; hậu quả sau khi thành công: chỉ lưu file; quy tắc trùng khớp: A2"
	raw, _ := json.Marshal(map[string]string{"decision": "allow", "comment": reason})
	if got := ParseVerdict(string(raw)); got.Reason != reason {
		t.Fatal("explanation was truncated or lost its rule")
	}
	raw, _ = json.Marshal(map[string]string{"decision": "allow", "comment": strings.Repeat("a", 2401)})
	if got := ParseVerdict(string(raw)); got.Action != "" {
		t.Fatal("accepted unbounded explanation")
	}
}
