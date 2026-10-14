package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Autumn-27/artex/notify"
)

// Tất cả các ca trong file này đều kết nối PostgreSQL thật (bỏ qua khi không có DB). Các SQL này dùng
// FOR UPDATE SKIP LOCKED, make_interval, JSONB, ghép placeholder IN(...) nhiều hàng,
// đều là kiểu viết "biên dịch qua nhưng có thể lỗi lúc chạy", phải chạy thật mới coi là đã kiểm chứng.

func notifyTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// newTestChannel tạo một kênh, tự động xóa khi test kết thúc.
func newTestChannel(t *testing.T, d *DB, kind, mode string, filter string) *NotificationChannel {
	t.Helper()
	if filter == "" {
		filter = `{}`
	}
	ch := &NotificationChannel{
		Name:       "kênh-kiểm-thử-" + t.Name(),
		Kind:       kind,
		Mode:       mode,
		Config:     json.RawMessage(`{"webhook":"https://example.com/hook"}`),
		Filter:     json.RawMessage(filter),
		RatePerMin: 100,
	}
	id, err := d.SaveNotificationChannel(context.Background(), ch)
	if err != nil {
		t.Fatalf("tạo kênh thất bại: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })
	ch.ID = id
	return ch
}

// addTestEvent ghi trực tiếp một sự kiện (không qua finding), dùng để test phân phối và giao.
func addTestEvent(t *testing.T, d *DB, kind string, findingID int64, snap notify.Snapshot) int64 {
	t.Helper()
	snap.Kind = kind
	snap.FindingID = findingID
	id, err := d.AddNotificationEvent(context.Background(), kind, findingID, snap)
	if err != nil {
		t.Fatalf("ghi sự kiện thất bại: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE id=$1`, id) })
	return id
}

func TestNotificationAssetNamesResolvesAndPreservesOrder(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	// Ba loại tài sản mỗi loại có một cách hiển thị riêng: tên miền, IP, URL.
	insertAsset := func(query, value string) int64 {
		t.Helper()
		var id int64
		if err := d.QueryRow(query, value).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	domID := insertAsset(`INSERT INTO assets(type, domain) VALUES('subdomain',$1) RETURNING id`, "a.example.com")
	ipID := insertAsset(`INSERT INTO assets(type, ip) VALUES('ip',$1) RETURNING id`, "10.1.2.3")
	svcID := insertAsset(`INSERT INTO assets(type, url) VALUES('service',$1) RETURNING id`, "https://a.example.com/admin")
	t.Cleanup(func() {
		d.Exec(`DELETE FROM assets WHERE id IN ($1,$2,$3)`, domID, ipID, svcID)
	})

	// Thứ tự truyền vào cố ý đảo lộn, và có chứa một id không tồn tại.
	got, err := d.NotificationAssetNames(ctx, []int64{svcID, 999999999, domID, ipID, svcID})
	if err != nil {
		t.Fatalf("phân giải tên tài sản thất bại: %v", err)
	}
	want := []string{"https://a.example.com/admin", "a.example.com", "10.1.2.3"}
	if len(got) != len(want) {
		t.Fatalf("số lượng tên tài sản không đúng, mong đợi %v nhận được %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("thứ tự/giá trị không đúng, mong đợi %v nhận được %v", want, got)
		}
	}
}

// TestRecordNotificationEventTxUnwindsOnFailure là ca cốt lõi của cơ chế savepoint:
// trong transaction trước làm cho việc ghi notification_events chắc chắn thất bại (tạm thời thêm một ràng buộc luôn false),
// khẳng định ① hàm báo false ② transaction không rơi vào trạng thái aborted, các câu lệnh sau vẫn thực thi được.
//
// Nếu không có savepoint, PostgreSQL sẽ vô hiệu toàn bộ transaction, mọi câu lệnh sau đó đều thất bại với
// "current transaction is aborted" — đó chính là đường sự cố "một vấn đề ở bảng thông báo khiến
// lỗ hổng không lưu được vào DB".
//
// Ở đây cố ý kết thúc bằng **ROLLBACK thay vì COMMIT**: ALTER TABLE trong PG có tính transaction,
// một khi commit, ràng buộc tạm thời đó sẽ nằm vĩnh viễn trong schema, làm hỏng tất cả các ca sau.
// Rollback tự động hủy DDL, không cần dọn tay. Khẳng định chỉ cần "transaction còn sống",
// không cần commit thật.
func TestRecordNotificationEventTxUnwindsOnFailure(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	// Dọn dẹp phòng thủ: nếu các lần chạy trước để lại ràng buộc này, gỡ trước.
	if _, err := d.Exec(`ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS notify_test_never`); err != nil {
		t.Fatal(err)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // hủy ràng buộc tạm thời, xem chú thích hàm

	// NOT VALID: chỉ ràng buộc các hàng ghi sau này, không kiểm tra các sự kiện lịch sử đã có trong DB
	// (nếu không các hàng tồn kho vi phạm sẽ khiến không thêm được ràng buộc).
	if _, err := tx.ExecContext(ctx, `ALTER TABLE notification_events ADD CONSTRAINT notify_test_never CHECK (false) NOT VALID`); err != nil {
		t.Fatalf("thêm ràng buộc tạm thời thất bại: %v", err)
	}
	if RecordNotificationEventTx(ctx, tx, notify.EventFindingCreated, 1, notify.Snapshot{Severity: "high"}) {
		t.Fatal("vẫn báo ghi thành công dưới ràng buộc chắc chắn thất bại")
	}
	// Khẳng định then chốt: transaction vẫn dùng được.
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("transaction đã bị nhiễm bẩn (savepoint không có hiệu lực): %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback thất bại: %v", err)
	}
	// Xác nhận DDL đã bị hủy theo rollback, không để lại mìn cho các ca sau.
	var exists bool
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='notify_test_never')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("ràng buộc tạm thời chưa bị hủy theo rollback, sẽ làm nhiễm bẩn các ca sau")
	}
}

func TestFanOutRoutesEventsByFilter(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	all := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	onlyCritical := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"min_severity":"critical"}`)
	sqlOnly := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"vulnclass_include":["SQL"]}`)

	highSQL := addTestEvent(t, d, notify.EventFindingCreated, 1001, notify.Snapshot{Severity: "high", VulnClass: "SQL Injection"})
	lowXSS := addTestEvent(t, d, notify.EventFindingCreated, 1002, notify.Snapshot{Severity: "low", VulnClass: "XSS"})
	criticalXSS := addTestEvent(t, d, notify.EventFindingCreated, 1003, notify.Snapshot{Severity: "critical", VulnClass: "XSS"})

	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatalf("phân phối thất bại: %v", err)
	}

	cases := []struct {
		name    string
		eventID int64
		channel int64
		want    bool
	}{
		{"kênh nhận tất cả nhận high", highSQL, all.ID, true},
		{"kênh nhận tất cả nhận low", lowXSS, all.ID, true},
		{"kênh chỉ nghiêm trọng bỏ qua high", highSQL, onlyCritical.ID, false},
		{"kênh chỉ nghiêm trọng nhận critical", criticalXSS, onlyCritical.ID, true},
		{"kênh chỉ SQL nhận SQL", highSQL, sqlOnly.ID, true},
		{"kênh chỉ SQL bỏ qua XSS", lowXSS, sqlOnly.ID, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var exists bool
			if err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notification_deliveries WHERE event_id=$1 AND channel_id=$2)`,
				tc.eventID, tc.channel).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists != tc.want {
				t.Fatalf("bản giao có tồn tại không: mong đợi %v nhận được %v", tc.want, exists)
			}
		})
	}

	// Phân phối lại lần nữa không được tạo bản giao trùng (fanned_out idempotent).
	events, deliveries, err := d.FanOutPendingEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if events != 0 || deliveries != 0 {
		t.Fatalf("sự kiện đã phân phối không được xử lý lại, nhận được events=%d deliveries=%d", events, deliveries)
	}
}

// TestFanOutMarksEventsWithNoMatchingChannel bao phủ trường hợp "sự kiện không khớp kênh nào".
// Loại sự kiện này vẫn phải được đánh dấu đã phân phối, nếu không nó sẽ nằm mãi trong tập chờ phân phối, bị quét lại mỗi tick.
func TestFanOutMarksEventsWithNoMatchingChannel(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	pick := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"vulnclass_include":["loại tuyệt đối không khớp"]}`)
	_ = pick

	ev := addTestEvent(t, d, notify.EventFindingCreated, 2001, notify.Snapshot{Severity: "high", VulnClass: "XSS"})
	_, deliveries, err := d.FanOutPendingEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 {
		t.Fatalf("không nên tạo bản giao, nhận được %d", deliveries)
	}
	var fanned bool
	if err := d.QueryRowContext(ctx, `SELECT fanned_out FROM notification_events WHERE id=$1`, ev).Scan(&fanned); err != nil {
		t.Fatal(err)
	}
	if !fanned {
		t.Fatal("sự kiện không khớp kênh cũng phải được đánh dấu đã phân phối, nếu không sẽ bị quét lại vô hạn")
	}
}

func TestClaimRealtimeDeliveriesHonorsLeaseAndMode(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	realtime := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	digest := newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)

	addTestEvent(t, d, notify.EventFindingCreated, 3001, notify.Snapshot{Severity: "high", VulnClass: "XSS"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}

	// Nhận realtime chỉ được lấy bản của kênh realtime, không được động vào kênh digest.
	got, err := d.ClaimRealtimeDeliveries(ctx, realtime.ID, 10, time.Minute)
	if err != nil {
		t.Fatalf("nhận thất bại: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("lẽ ra phải nhận được 1 bản, nhận được %d", len(got))
	}
	if got[0].State != NotifyStateSending || got[0].Attempts != 1 {
		t.Fatalf("sau khi nhận phải là sending và attempts=1, nhận được state=%s attempts=%d", got[0].State, got[0].Attempts)
	}
	// Ngữ cảnh render load kèm phải đầy đủ (cấu hình kênh + snapshot sự kiện + finding id).
	if got[0].Channel == nil || len(got[0].Channel.Config) == 0 {
		t.Fatal("kết quả nhận thiếu cấu hình kênh, render sẽ thất bại")
	}
	if got[0].FindingID != 3001 {
		t.Fatalf("finding id chưa được lấy ra từ sự kiện, nhận được %d", got[0].FindingID)
	}

	// Lease chưa hết hạn, lần nhận thứ hai phải rỗng — đây là bảo đảm cho "cùng một hàng
	// không bị hai dispatcher giao đồng thời".
	again, err := d.ClaimRealtimeDeliveries(ctx, realtime.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("trong thời hạn lease không được nhận lặp, nhận được %d bản", len(again))
	}

	// Bản giao của kênh digest không được bị việc nhận realtime chạm tới.
	left, err := d.ClaimRealtimeDeliveries(ctx, digest.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("nhận realtime không được lấy bản giao của kênh digest, nhận được %d bản", len(left))
	}
}

// TestClaimExpiredLeaseRecovers bao phủ tự phục hồi sau crash: tiến trình chết giữa chừng khi giao sẽ để lại hàng
// sending, sau khi lease hết hạn phải nhận lại được, nếu không bản giao này kẹt vĩnh viễn.
func TestClaimExpiredLeaseRecovers(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 4001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	first, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil || len(first) != 1 {
		t.Fatalf("nhận lần đầu thất bại: %v (%d bản)", err, len(first))
	}
	// Đẩy lease về quá khứ bằng tay, mô phỏng "lease đã hết hạn".
	if _, err := d.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE id=$1`, first[0].ID); err != nil {
		t.Fatal(err)
	}
	second, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("hàng sending hết hạn lease phải nhận lại được, nhận được %d bản", len(second))
	}
	if second[0].Attempts != 2 {
		t.Fatalf("nhận lại phải cộng dồn số lần thử, nhận được %d", second[0].Attempts)
	}
}

func TestClaimSkipsDisabledChannel(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 5001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// Tắt kênh sẽ đánh dấu các bản chờ gửi tồn kho thành skipped cùng lúc.
	if err := d.SetNotificationChannelEnabled(ctx, ch.ID, false); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := d.QueryRow(`SELECT state FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStateSkipped {
		t.Fatalf("bản chờ gửi tồn kho của kênh bị tắt phải được đánh dấu skipped, nhận được %s", state)
	}
	got, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("kênh đã tắt không được phép nhận, nhận được %d bản", len(got))
	}
}

func TestDigestBatchDueAndStableBatchID(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)
	for i := 0; i < 3; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(6000+i), notify.Snapshot{Severity: "high"})
	}
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}

	// Batch vừa tạo, tuổi bằng 0, với chu kỳ 30 phút thì không được đến hạn.
	due, err := d.DigestBatchDue(ctx, ch.ID, 30*time.Minute)
	if err != nil {
		t.Fatalf("phán định batch đến hạn thất bại: %v", err)
	}
	if due {
		t.Fatal("batch vừa tạo không được đến hạn ngay")
	}

	// Đẩy cùng lúc thời gian tạo của ba bản giao về cũ, mô phỏng một batch đã gom đủ chu kỳ.
	if _, err := d.Exec(`UPDATE notification_deliveries SET created_at = now() - interval '40 minutes' WHERE channel_id=$1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	due, err = d.DigestBatchDue(ctx, ch.ID, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Fatal("batch vượt chu kỳ phải được phán là đến hạn")
	}

	batch, err := d.ClaimDigestBatch(ctx, ch.ID, MaxDigestBatchSize, time.Minute)
	if err != nil {
		t.Fatalf("nhận batch tổng hợp thất bại: %v", err)
	}
	if len(batch) != 3 {
		t.Fatalf("tổng hợp phải nhận hết 3 bản một lần, nhận được %d bản", len(batch))
	}
	if batch[0].BatchID == nil {
		t.Fatal("batch tổng hợp phải ghi batch_id, nếu không trong lịch sử không thấy mấy bản này được gửi cùng nhau")
	}
	firstBatchID := *batch[0].BatchID
	for _, dl := range batch {
		if dl.BatchID == nil || *dl.BatchID != firstBatchID {
			t.Fatalf("cùng batch phải chia sẻ batch_id, nhận được %v vs %d", dl.BatchID, firstBatchID)
		}
	}

	// Cho cả batch **toàn bộ** thất bại rồi xếp lại lịch rồi nhận lại, batch_id phải giữ nguyên giá trị (tác dụng của COALESCE):
	// nếu không, một lần thử lại sẽ xóa mất sự thật "batch này được gửi cùng nhau".
	//
	// Phải xếp lại lịch cả batch chứ không chỉ một bản — engine giao xử lý tin nhắn tổng hợp đúng như vậy
	// (một tin nhắn đại diện cả batch, cùng thành cùng bại). Chỉ xếp lại một bản thì phần còn lại vẫn trong thời hạn lease,
	// nhận lại tự nhiên chỉ lấy được đúng bản đó.
	allIDs := make([]int64, 0, len(batch))
	for _, dl := range batch {
		allIDs = append(allIDs, dl.ID)
	}
	if err := d.RescheduleDeliveries(ctx, allIDs, time.Second, "giả lập thất bại"); err != nil {
		t.Fatal(err)
	}
	// Đẩy lease về quá khứ, mô phỏng thời gian backoff đã hết.
	if _, err := d.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE channel_id=$1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := d.ClaimDigestBatch(ctx, ch.ID, MaxDigestBatchSize, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 3 {
		t.Fatalf("nhận lại phải lấy đủ 3 bản, nhận được %d", len(reclaimed))
	}
	if reclaimed[0].BatchID == nil || *reclaimed[0].BatchID != firstBatchID {
		t.Fatalf("sau khi thử lại batch_id phải giữ giá trị gốc %d, nhận được %v", firstBatchID, reclaimed[0].BatchID)
	}
}

func TestDeliveryStateTransitions(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 7001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	got, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil || len(got) != 1 {
		t.Fatalf("nhận thất bại: %v (%d)", err, len(got))
	}
	id := got[0].ID

	if err := d.RescheduleDeliveries(ctx, []int64{id}, time.Second, "chập chờn mạng"); err != nil {
		t.Fatal(err)
	}
	var state, lastErr string
	if err := d.QueryRow(`SELECT state, last_error FROM notification_deliveries WHERE id=$1`, id).Scan(&state, &lastErr); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStatePending || lastErr != "chập chờn mạng" {
		t.Fatalf("sau khi xếp lại lịch phải là pending và ghi nguyên nhân, nhận được state=%s err=%q", state, lastErr)
	}

	if err := d.FailDeliveries(ctx, []int64{id}, "hết lượt thử lại"); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT state FROM notification_deliveries WHERE id=$1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStateFailed {
		t.Fatalf("phải là failed, nhận được %s", state)
	}

	// Gửi lại thủ công phải xóa bộ đếm thử lại và đến hạn ngay, nếu không sẽ thừa kế ngân sách thất bại cũ.
	if err := d.RetryNotificationDelivery(ctx, id); err != nil {
		t.Fatalf("gửi lại thất bại: %v", err)
	}
	var attempts int
	var next time.Time
	if err := d.QueryRow(`SELECT state, attempts, next_attempt_at FROM notification_deliveries WHERE id=$1`, id).Scan(&state, &attempts, &next); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStatePending || attempts != 0 {
		t.Fatalf("sau khi gửi lại phải là pending và attempts=0, nhận được state=%s attempts=%d", state, attempts)
	}
	if next.After(time.Now().Add(time.Second)) {
		t.Fatal("gửi lại phải có thể nhận ngay")
	}

	// Bản giao đã giao tới không được phép gửi lại.
	if err := d.MarkDeliveriesSent(ctx, []int64{id}); err != nil {
		t.Fatal(err)
	}
	if err := d.RetryNotificationDelivery(ctx, id); err == nil {
		t.Fatal("bản giao đã giao tới không nên cho phép gửi lại")
	}
}

func TestListNotificationDeliveriesPagingAndFilter(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	for i := 0; i < 5; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(8000+i), notify.Snapshot{Severity: "high", Name: "kiểm thử phân trang"})
	}
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute); err != nil {
		t.Fatal(err)
	}

	page1, total, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStateSending}, 1, 2)
	if err != nil {
		t.Fatalf("truy vấn thất bại: %v", err)
	}
	if total != 5 {
		t.Fatalf("tổng số phải là 5, nhận được %d", total)
	}
	if len(page1) != 2 {
		t.Fatalf("mỗi trang 2 bản, nhận được %d", len(page1))
	}
	// Mới trước: id của bản đầu trang một phải lớn hơn bản đầu trang hai.
	page2, _, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStateSending}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].ID >= page1[0].ID {
		t.Fatalf("thứ tự phân trang phải là mới trước, nhận được page1[0]=%d page2[0]=%d", page1[0].ID, page2[0].ID)
	}
	// Ngữ cảnh render phải trả về cùng lịch sử, nếu không danh sách không thể hiển thị "đã đẩy cái gì".
	if page1[0].ChannelName == "" || page1[0].FindingID == 0 {
		t.Fatalf("mục lịch sử thiếu trường hiển thị: %+v", page1[0])
	}

	// Lọc theo trạng thái: không có bản pending nào.
	pending, totalPending, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStatePending}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if totalPending != 0 || len(pending) != 0 {
		t.Fatalf("không nên có bản giao pending, nhận được %d bản (total=%d)", len(pending), totalPending)
	}
}

func TestSetFindingStatusWithNotifyOnlyEmitsOnRealChange(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	tk, err := d.CreateTask("kiểm thử đổi trạng thái thông báo", "mục tiêu", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es := d.Exploration(tk.ExplorationID)
	f, err := es.RecordFinding(ctx, RecordFindingInput{
		TaskID: tk.ID, Worker: "test", VulnClass: "SQL Injection", Name: "ca đổi trạng thái",
		Severity: "high", Summary: "tóm tắt",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE finding_id=$1`, f.FindingID) })

	// Khi vào DB đã đăng ký một sự kiện finding_created, đếm nó ra trước làm baseline.
	var base int
	if err := d.QueryRow(`SELECT count(*) FROM notification_events WHERE finding_id=$1`, f.FindingID).Scan(&base); err != nil {
		t.Fatal(err)
	}
	if base < 1 {
		t.Fatal("lỗ hổng vào DB phải đăng ký một sự kiện đẩy trong cùng transaction")
	}

	// Đổi thành cùng một trạng thái: không được tạo sự kiện (tránh gửi lặp tạo nhiễu đẩy).
	from, found, notified, err := d.SetFindingStatusWithNotify(ctx, f.FindingID, "pending")
	if err != nil || !found {
		t.Fatalf("đặt trạng thái thất bại: found=%v err=%v", found, err)
	}
	if notified {
		t.Fatal("trạng thái không đổi thì không được đăng ký sự kiện đẩy")
	}
	if from != "pending" {
		t.Fatalf("phải trả về trạng thái trước khi đổi pending, nhận được %q", from)
	}

	// Thay đổi thật: phải đăng ký sự kiện và ghi from/to.
	from, found, notified, err = d.SetFindingStatusWithNotify(ctx, f.FindingID, "fixed")
	if err != nil || !found {
		t.Fatalf("đặt trạng thái thất bại: found=%v err=%v", found, err)
	}
	if !notified {
		t.Fatal("khi trạng thái thực sự đổi phải đăng ký sự kiện đẩy")
	}
	if from != "pending" {
		t.Fatalf("from phải là pending, nhận được %q", from)
	}
	var snapshot []byte
	if err := d.QueryRow(`SELECT snapshot FROM notification_events WHERE finding_id=$1 AND kind=$2`,
		f.FindingID, notify.EventFindingStatusChanged).Scan(&snapshot); err != nil {
		t.Fatalf("không tìm thấy sự kiện đổi trạng thái: %v", err)
	}
	var snap notify.Snapshot
	if err := json.Unmarshal(snapshot, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.FromStatus != "pending" || snap.ToStatus != "fixed" {
		t.Fatalf("chuyển trạng thái trong snapshot không đúng: %s → %s", snap.FromStatus, snap.ToStatus)
	}
	// Snapshot phải mang các trường cần cho render, nếu không tin nhắn đổi trạng thái sẽ là vỏ rỗng.
	if snap.VulnClass != "SQL Injection" || snap.Severity != "high" || snap.Name != "ca đổi trạng thái" {
		t.Fatalf("snapshot thiếu trường render: %+v", snap)
	}
	var status string
	if err := d.QueryRow(`SELECT status FROM findings WHERE id=$1`, f.FindingID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "fixed" {
		t.Fatalf("trạng thái phải đã được cập nhật thành fixed, nhận được %s", status)
	}

	// Lỗ hổng không tồn tại: found=false, không báo lỗi.
	if _, found, _, err := d.SetFindingStatusWithNotify(ctx, 999999999, "fixed"); err != nil || found {
		t.Fatalf("lỗ hổng không tồn tại phải trả về found=false và không lỗi, nhận được found=%v err=%v", found, err)
	}
}

func TestNotificationStatsSnapshot(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 9001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	stats, err := d.NotificationStatsSnapshot(ctx)
	if err != nil {
		t.Fatalf("thống kê thất bại: %v", err)
	}
	if stats.Channels < 1 || stats.ChannelsOn < 1 {
		t.Fatalf("đếm kênh không đúng: %+v", stats)
	}
	if stats.Pending < 1 {
		t.Fatalf("phải thống kê được bản giao chờ gửi: %+v", stats)
	}
	// Tuổi tồn đọng của bản giao vừa tạo phải gần 0, chứ không phải số âm hay giá trị khổng lồ.
	if stats.BacklogAgeMS < 0 || stats.BacklogAgeMS > int64(time.Hour/time.Millisecond) {
		t.Fatalf("tuổi tồn đọng không hợp lệ: %d ms", stats.BacklogAgeMS)
	}
	_ = ch
}

func TestNotificationChannelCRUDRoundTrip(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	ch := &NotificationChannel{
		Name:       "CRUD round-trip",
		Kind:       notify.KindEmail,
		Mode:       NotifyModeDigest,
		Config:     json.RawMessage(`{"host":"smtp.example.com","port":587,"from":"a@b.c","to":["x@y.z"]}`),
		Filter:     json.RawMessage(`{"min_severity":"medium","on_status_change":true}`),
		RatePerMin: 42,
	}
	id, err := d.SaveNotificationChannel(ctx, ch)
	if err != nil {
		t.Fatalf("tạo mới thất bại: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })

	got, err := d.NotificationChannelByID(ctx, id)
	if err != nil {
		t.Fatalf("đọc thất bại: %v", err)
	}
	if got.Mode != NotifyModeDigest || got.RatePerMin != 42 || got.Name != "CRUD round-trip" {
		t.Fatalf("các trường round-trip không nhất quán: %+v", got)
	}
	if !got.IsEnabled() {
		t.Fatal("mặc định phải là đang bật")
	}
	var cfg map[string]any
	if err := json.Unmarshal(got.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["host"] != "smtp.example.com" {
		t.Fatalf("cấu hình chưa vào DB đúng: %v", cfg)
	}
	var filter notify.Filter
	if err := json.Unmarshal(got.Filter, &filter); err != nil {
		t.Fatal(err)
	}
	if filter.MinSeverity != "medium" || !filter.OnStatusChange {
		t.Fatalf("điều kiện lọc chưa vào DB đúng: %+v", filter)
	}

	// Đọc lại sau khi cập nhật.
	got.Name = "Đã đổi tên"
	off := false
	got.Enabled = &off
	if _, err := d.SaveNotificationChannel(ctx, got); err != nil {
		t.Fatal(err)
	}
	after, err := d.NotificationChannelByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "Đã đổi tên" || after.IsEnabled() {
		t.Fatalf("cập nhật chưa có hiệu lực: %+v", after)
	}

	// Sau khi xóa phải báo "không tồn tại" chứ không phải thành công âm thầm.
	if err := d.DeleteNotificationChannel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.NotificationChannelByID(ctx, id); err != ErrNotificationChannelNotFound {
		t.Fatalf("mong đợi ErrNotificationChannelNotFound, nhận được %v", err)
	}
	if err := d.DeleteNotificationChannel(ctx, id); err != ErrNotificationChannelNotFound {
		t.Fatalf("xóa lặp phải báo không tồn tại, nhận được %v", err)
	}
}

// TestSaveNotificationChannelKeepsExplicitZeroRate khóa lại một chỗ từng viết sai:
// **0 là cấu hình hợp lệ, nghĩa là "không giới hạn tốc độ", không được bị tầng db xem là "chưa chỉ định" rồi ghi đè thành giá trị mặc định**.
//
// Bug lịch sử: trong SaveNotificationChannel từng viết `if RatePerMin <= 0 { lấy giá trị mặc định }`,
// thế là tài liệu, gợi ý UI, takeTokens đều hiểu theo "0=không giới hạn", duy chỉ có tầng ghi DB lặng lẽ đổi thành
// 20 (DingTalk/WeCom/Telegram) hoặc 100 (Feishu) — người vận hành tưởng đã mở giới hạn nhưng thực tế vẫn bị kẹt,
// và không có bất kỳ gợi ý nào. Khác biệt giữa "chưa chỉ định" và "đặt 0 rõ ràng" chỉ request body mới diễn đạt được,
// nên giá trị mặc định được điền ở tầng server (xem notifyCreateChannel), tầng db chỉ lo lưu.
func TestSaveNotificationChannelKeepsExplicitZeroRate(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	// Đặt 0 rõ ràng (không giới hạn tốc độ): phải lưu nguyên xi.
	unlimited := &NotificationChannel{
		Name: "không giới hạn tốc độ", Kind: notify.KindDingTalk, RatePerMin: 0,
		Config: json.RawMessage(`{"webhook":"https://example.com/h"}`),
	}
	id, err := d.SaveNotificationChannel(ctx, unlimited)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })
	got, err := d.NotificationChannelByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.RatePerMin != 0 {
		t.Fatalf("đặt 0 rõ ràng nghĩa là không giới hạn tốc độ, phải lưu nguyên xi, nhận được %d", got.RatePerMin)
	}
	if got.Mode != NotifyModeRealtime {
		t.Fatalf("chế độ mặc định phải là realtime, nhận được %s", got.Mode)
	}

	// Giá trị âm là đầu vào bất hợp pháp, phải bị từ chối chứ không lặng lẽ đổi thành giá trị khác.
	bad := &NotificationChannel{
		Name: "giới hạn tốc độ âm", Kind: notify.KindDingTalk, RatePerMin: -1,
		Config: json.RawMessage(`{"webhook":"https://example.com/h"}`),
	}
	if _, err := d.SaveNotificationChannel(ctx, bad); err == nil {
		t.Fatal("giới hạn tốc độ âm phải bị từ chối")
	}
}

// TestDeleteChannelCascadesDeliveries khóa lại hành vi khóa ngoại: sau khi xóa kênh thì lịch sử giao của nó biến mất cùng
// (cấu hình đã mất, lịch sử không còn cách diễn giải), nhưng bản thân sự kiện phải được giữ lại — nó có thể còn được kênh khác tham chiếu.
func TestDeleteChannelCascadesDeliveries(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	ev := addTestEvent(t, d, notify.EventFindingCreated, 9101, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := d.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("điều kiện tiên quyết không thỏa: chưa tạo bản giao")
	}
	if err := d.DeleteNotificationChannel(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := d.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Fatalf("sau khi xóa kênh thì bản giao của nó phải bị xóa cascade, vẫn còn %d bản", after)
	}
	var evExists bool
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM notification_events WHERE id=$1)`, ev).Scan(&evExists); err != nil {
		t.Fatal(err)
	}
	if !evExists {
		t.Fatal("xóa kênh không được xóa kéo theo bản thân sự kiện")
	}
}

// TestClaimDigestBatchHonorsCallerLimit bao phủ một lỗ hổng mà kiểm toán chỉ ra:
// trước đây kênh tổng hợp hoàn toàn bỏ qua token bucket — allow bị takeTokens trừ nhưng không ai dùng,
// rate_per_min không có tác dụng gì với chế độ digest. Giờ limit cũng tham gia ràng buộc.
func TestClaimDigestBatchHonorsCallerLimit(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)
	for i := 0; i < 10; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(7000+i), notify.Snapshot{Severity: "high"})
	}
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// Lấy limit=3: chỉ nhận được 3 bản, phần còn lại ở trong DB.
	got, err := d.ClaimDigestBatch(ctx, ch.ID, 3, time.Minute)
	if err != nil {
		t.Fatalf("nhận thất bại: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("phải chỉ nhận 3 bản theo hạn mức của phía gọi, nhận được %d", len(got))
	}
	// limit=0 nghĩa là hạn mức vòng này đã cạn: không nhận bản nào, cũng không báo lỗi.
	if got, err := d.ClaimDigestBatch(ctx, ch.ID, 0, time.Minute); err != nil || len(got) != 0 {
		t.Fatalf("khi hạn mức bằng 0 phải nhận 0 bản và không báo lỗi, nhận được %d bản err=%v", len(got), err)
	}
}

// TestFinishFindingRetestEmitsStatusChange bao phủ một khe hở toàn vẹn mà kiểm toán chỉ ra:
// khi kết luận kiểm thử lại là "đã sửa", trạng thái thực sự đã đổi, nhưng câu UPDATE đó ghi DB trực tiếp,
// đi vòng qua bản có thông báo — thế nên các kênh cấu hình on_status_change hoàn toàn không nhận được đẩy
// cho loại chuyển trạng thái này, trạng thái trên giao diện lặng lẽ thay đổi, người vận hành phải mở nền tảng mới biết.
//
// Ca này khóa lại "mọi đường đổi trạng thái đều phải đăng ký sự kiện đổi trạng thái".
func TestFinishFindingRetestEmitsStatusChange(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	tk, err := d.CreateTask("kiểm thử đẩy kiểm thử lại", "mục tiêu", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es := d.Exploration(tk.ExplorationID)
	f, err := es.RecordFinding(ctx, RecordFindingInput{
		TaskID: tk.ID, Worker: "test", VulnClass: "SQL Injection", Name: "mục tiêu kiểm thử lại",
		Severity: "high", Summary: "tóm tắt",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE finding_id=$1`, f.FindingID) })

	// Tạo một bản ghi kiểm thử lại và đẩy thẳng tới trạng thái hoàn thành.
	rt, _, _, err := d.CreateFindingRetest(ctx, f.FindingID, "rà soát")
	if err != nil {
		t.Fatal(err)
	}
	if rt.ConversationID == nil {
		t.Fatal("kiểm thử lại phải liên kết một phiên")
	}
	// Kiểm thử lại phải vào running trước mới ghi được kết luận (giống luồng thật).
	if ok, err := d.StartFindingRetest(ctx, rt.ID); err != nil || !ok {
		t.Fatalf("khởi động kiểm thử lại thất bại: ok=%v err=%v", ok, err)
	}
	if err := d.RecordFindingRetestResult(ctx, *rt.ConversationID, "fixed", "đã sửa", "bằng chứng"); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishFindingRetest(rt.ID, "completed", ""); err != nil {
		t.Fatalf("kết thúc kiểm thử lại thất bại: %v", err)
	}

	var status string
	if err := d.QueryRow(`SELECT status FROM findings WHERE id=$1`, f.FindingID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != FindingFixed {
		t.Fatalf("sau khi kiểm thử lại phán đã sửa thì trạng thái phải là fixed, nhận được %s", status)
	}

	// Khẳng định then chốt: phải có một sự kiện đổi trạng thái, và from/to đúng.
	var snapshot []byte
	err = d.QueryRow(`SELECT snapshot FROM notification_events WHERE finding_id=$1 AND kind=$2 ORDER BY id DESC LIMIT 1`,
		f.FindingID, notify.EventFindingStatusChanged).Scan(&snapshot)
	if err != nil {
		t.Fatalf("kiểm thử lại phán đã sửa phải đăng ký sự kiện đẩy đổi trạng thái (nếu không kênh cấu hình on_status_change sẽ không nhận được): %v", err)
	}
	var snap notify.Snapshot
	if err := json.Unmarshal(snapshot, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.FromStatus != "pending" || snap.ToStatus != FindingFixed {
		t.Fatalf("chuyển trạng thái trong snapshot không đúng: %s → %s", snap.FromStatus, snap.ToStatus)
	}
	// Snapshot phải mang các trường cần cho render, nếu không đẩy ra là vỏ rỗng.
	if snap.Name != "mục tiêu kiểm thử lại" || snap.Severity != "high" {
		t.Fatalf("snapshot thiếu trường render: %+v", snap)
	}
}
