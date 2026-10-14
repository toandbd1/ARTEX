package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// Các API CRUD thủ công tổng quan cho "quản lý mục tiêu". Ghi cùng một nhóm node goal với công cụ
// set_goals phía agent, nhưng điểm vào là con người thêm/xóa/sửa trực tiếp trên UI; sau khi thêm/sửa
// tái sử dụng logic "hồi sinh tác vụ" (admitTask resume: trạng thái kết thúc → running, gỡ tạm dừng,
// xếp hàng khi cần), xóa thì không hồi sinh (theo quyết định sản phẩm). Mọi handler thay đổi đều đi qua
// beginTaskOperation/decInflight, tránh race với việc xóa tác vụ (nhất quán với CRUD ý đồ).

// listGoals trả về toàn bộ mục tiêu của tác vụ này (đã tách text/vulnclass/state), để render thẻ quản lý mục tiêu.
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	goals, err := t.Store.ListByKind(db.KindGoal, 10000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"goals": goalDTOs(goals)})
}

// addGoal: con người thêm thủ công một mục tiêu: ghi vào DB (gắn dưới spawns gốc của tác vụ) → ghi một
// bản "đã thêm mục tiêu" để kích hoạt đánh thức planner → hồi sinh tác vụ, để người lập kế hoạch đánh giá
// lại theo mục tiêu mới xem đã đạt chưa.
func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "Tác vụ đang bị xóa, không thể thêm mục tiêu")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "Nội dung mục tiêu không được để trống")
		return
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(body.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	id, err := t.Store.AddGoal(payload, "human")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if of, _ := t.Store.OriginFactID(); of > 0 && id > 0 {
		_ = t.Store.Link(of, db.RelSpawns, id) // goal descends from the task root (origin fact)
	}
	t.NotifyGoal([]string{text}) // ghi "người dùng đã thêm mục tiêu: …" để kích hoạt và đánh thức planner
	s.reviveTask(t)              // kéo tác vụ đã hoàn thành/tạm dừng về trạng thái chạy tiếp
	node, _ := t.Store.GetNode(id)
	if node == nil {
		writeErr(w, 500, "Đọc lại sau khi ghi mục tiêu thất bại")
		return
	}
	writeJSON(w, 200, goalDTO(node))
}

// editGoal: con người sửa thủ công nội dung một mục tiêu (và vulnclass): sửa DB → ghi "người dùng đã sửa
// mục tiêu từ old thành new" để kích hoạt đánh thức planner → hồi sinh tác vụ, để người lập kế hoạch điều
// chỉnh hướng đi theo mục tiêu mới.
func (s *Server) editGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "Tác vụ đang bị xóa, không thể sửa mục tiêu")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "Nội dung mục tiêu không được để trống")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "Mục tiêu không tồn tại")
		return
	}
	oldText := goalDTO(node).Text
	if err := t.Store.UpdateGoalPayload(gid, text, strings.TrimSpace(body.VulnClass)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalEdited(oldText, text) // ghi "người dùng đã sửa mục tiêu từ old thành new" để kích hoạt và đánh thức planner
	s.reviveTask(t)                   // giống khi thêm mới: hồi sinh tác vụ để đánh giá lại theo mục tiêu mới
	updated, _ := t.Store.GetNode(gid)
	if updated == nil {
		writeErr(w, 500, "Đọc lại sau khi cập nhật mục tiêu thất bại")
		return
	}
	writeJSON(w, 200, goalDTO(updated))
}

// deleteGoal: con người xóa thủ công một mục tiêu (xóa cứng, xóa cascade cạnh/anchor): xóa khỏi DB → ghi
// "người dùng đã xóa mục tiêu X" để kích hoạt đánh thức planner đánh giá lại các mục tiêu còn lại.
// Theo quyết định sản phẩm, xóa thì 【không】hồi sinh tác vụ.
func (s *Server) deleteGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "Tác vụ đang bị xóa, không thể xóa mục tiêu")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "Mục tiêu không tồn tại")
		return
	}
	text := goalDTO(node).Text
	if err := t.Store.DeleteGoal(gid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalDeleted(text) // ghi "người dùng đã xóa mục tiêu: …" để kích hoạt và đánh thức planner (không hồi sinh tác vụ)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
