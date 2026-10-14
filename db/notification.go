package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/artex/notify"
)

// File này là tầng cấu hình kênh và sự kiện của việc đẩy IM. Việc nhận tác vụ giao
// và chuyển trạng thái xem ở db/notification_delivery.go.
//
// Hai bất biến, bắt buộc giữ khi sửa file này:
//
//  1. Transaction ghi lỗ hổng (RecordFindingTx) chỉ gọi InsertNotificationEventTx để chèn mù một lần,
//     không đọc bất kỳ bảng nào liên quan đến thông báo, không chạy khớp bộ lọc. Bất kỳ thao tác đọc nào
//     được đưa vào đây đều có thể vì điều kiện lọc người dùng cấu hình sai mà làm nhiễm bẩn thậm chí hủy
//     transaction ghi lỗ hổng.
//  2. Khớp bộ lọc không bao giờ báo lỗi: cấu hình dị dạng đều xử lý như "khớp" (xem notify.Match). Thà đẩy thừa,
//     không bỏ sót.

// ErrNotificationChannelNotFound kênh không tồn tại.
var ErrNotificationChannelNotFound = errors.New("Kênh thông báo không tồn tại")

// Trạng thái giao.
const (
	NotifyStatePending = "pending" // chờ gửi
	NotifyStateSending = "sending" // đã được một dispatcher nào đó nhận, lease chưa hết hạn
	NotifyStateSent    = "sent"    // đã giao tới
	NotifyStateFailed  = "failed"  // hết lượt thử lại hoặc lỗi vĩnh viễn, có thể gửi lại thủ công
	NotifyStateSkipped = "skipped" // kênh đã bị tắt, không gửi nữa
)

// Chế độ đẩy.
const (
	NotifyModeRealtime = "realtime"
	NotifyModeDigest   = "digest"
)

// ValidNotifyMode kiểm tra chế độ đẩy theo whitelist (giống findings.status: không dùng DB CHECK,
// để tiện mở rộng sau này).
func ValidNotifyMode(m string) bool {
	return m == NotifyModeRealtime || m == NotifyModeDigest
}

// NotificationChannel là cấu hình của một phiên bản kênh. Config và Filter giữ JSON nguyên bản,
// việc phân tích giao cho package notify — tầng db không hiểu ý nghĩa các trường của chúng.
type NotificationChannel struct {
	ID     int64           `json:"id"`
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	Mode   string          `json:"mode"`
	Config json.RawMessage `json:"config"`
	Filter json.RawMessage `json:"filter"`
	// Enabled dùng con trỏ để phân biệt "không truyền trường này" với "truyền false một cách rõ ràng" —
	// control bật/tắt ở frontend chỉ gửi các trường đã thay đổi.
	Enabled    *bool     `json:"enabled,omitempty"`
	RatePerMin int       `json:"rate_per_min"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// IsEnabled trả về kênh có đang bật không; khi Enabled là nil (chưa load) thì xem như đang bật.
func (c *NotificationChannel) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// NotificationEvent là một sự kiện thực tế.
type NotificationEvent struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	FindingID int64           `json:"finding_id"`
	Snapshot  json.RawMessage `json:"snapshot"`
	CreatedAt time.Time       `json:"created_at"`
}

const notificationChannelCols = `id, name, kind, enabled, config, mode, filter, rate_per_min, created_at, updated_at`

func scanNotificationChannel(sc interface{ Scan(...any) error }) (*NotificationChannel, error) {
	var c NotificationChannel
	var enabled bool
	if err := sc.Scan(&c.ID, &c.Name, &c.Kind, &enabled, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Enabled = &enabled
	return &c, nil
}

// ListNotificationChannels trả về toàn bộ phiên bản kênh, kênh đang bật xếp trước, cùng cấp thì theo id.
// Sắp xếp đặt trong SQL để UI và dispatcher nhìn thấy cùng một thứ tự ổn định.
func (d *DB) ListNotificationChannels(ctx context.Context) ([]*NotificationChannel, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels
ORDER BY enabled DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		c, err := scanNotificationChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NotificationChannelByID lấy một kênh.
func (d *DB) NotificationChannelByID(ctx context.Context, id int64) (*NotificationChannel, error) {
	row := d.QueryRowContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels WHERE id=$1`, id)
	c, err := scanNotificationChannel(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotificationChannelNotFound
	}
	return c, err
}

// SaveNotificationChannel tạo mới hoặc cập nhật một kênh.
//
// Khi cập nhật chỉ ghi đè các trường mà phía gọi cung cấp rõ ràng (khác nil / khác rỗng), như vậy frontend có thể
// gửi form drawer chỉ sửa một phần, mà không phải gửi lại các trường trong config mà nó không hiển thị — gửi lại
// ngược lại còn gây ra sự cố "giá trị che dấu ghi đè mất khóa thật".
func (d *DB) SaveNotificationChannel(ctx context.Context, c *NotificationChannel) (int64, error) {
	if c.Mode == "" {
		c.Mode = NotifyModeRealtime
	}
	// Ở đây cố ý **không** xử lý gì với 0: 0 là cấu hình hợp lệ, nghĩa là "không giới hạn tốc độ".
	//
	// Từng viết thành `if c.RatePerMin <= 0 { c.RatePerMin = giá trị mặc định }`, ý đồ ban đầu là "khi chưa chỉ định thì
	// cho một giá trị mặc định an toàn", nhưng điều đó nuốt luôn cả "đặt 0 một cách rõ ràng" — tài liệu, gợi ý UI và
	// takeTokens đều hiểu 0 là không giới hạn, duy chỉ có chỗ này lặng lẽ đổi thành 20 (DingTalk/WeCom/Telegram)
	// hoặc 100 (Feishu), người vận hành tưởng đã mở giới hạn nhưng thực tế bị kẹt ở 20/phút mà không hề có thông báo nào.
	//
	// Sự khác biệt giữa "chưa chỉ định" và "đặt 0 rõ ràng" chỉ phía gọi mới biết (trường bị thiếu trong request body so với truyền 0 rõ ràng),
	// nên giá trị mặc định do tầng server điền khi trường bị thiếu, xem notifyCreateChannel.
	if c.RatePerMin < 0 {
		return 0, errors.New("Giá trị giới hạn tốc độ không được âm")
	}
	if c.Config == nil {
		c.Config = json.RawMessage(`{}`)
	}
	if c.Filter == nil {
		c.Filter = json.RawMessage(`{}`)
	}
	enabled := c.IsEnabled()

	if c.ID == 0 {
		var id int64
		err := d.QueryRowContext(ctx, `INSERT INTO notification_channels(name,kind,enabled,config,mode,filter,rate_per_min)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin).Scan(&id)
		return id, err
	}
	res, err := d.ExecContext(ctx, `UPDATE notification_channels
SET name=$2, kind=$3, enabled=$4, config=$5, mode=$6, filter=$7, rate_per_min=$8
WHERE id=$1`,
		c.ID, c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotificationChannelNotFound
	}
	return c.ID, nil
}

// SetNotificationChannelEnabled chuyển trạng thái bật/tắt.
//
// Khi tắt một kênh, đánh dấu các bản giao chưa gửi của nó thành skipped: nếu không, sau khi bật lại sẽ
// đột ngột nhận một loạt lỗ hổng cũ "tồn đọng trong thời gian tắt", đã mất tính thời hiệu và dễ bị nhầm là mới phát sinh.
func (d *DB) SetNotificationChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	return d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE notification_channels SET enabled=$2 WHERE id=$1`, id, enabled)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotificationChannelNotFound
		}
		if !enabled {
			if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET state=$2, last_error=$3
WHERE channel_id=$1 AND state IN ($4,$5)`,
				id, NotifyStateSkipped, "Kênh đã bị tắt", NotifyStatePending, NotifyStateSending); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteNotificationChannel xóa kênh. Lịch sử giao của nó bị xóa cascade theo khóa ngoại
// (cấu hình kênh đã mất thì lịch sử cũng không còn cách nào diễn giải).
func (d *DB) DeleteNotificationChannel(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotificationChannelNotFound
	}
	return nil
}

// RecordNotificationEventTx ghi **hết sức có thể** một sự kiện đẩy trong transaction của phía gọi.
//
// Đây là thay đổi duy nhất liên quan đến thông báo trên đường ghi lỗ hổng: một lần INSERT, không đọc bảng nào,
// không biết kênh, không chạy bộ lọc. Transaction commit tức đảm bảo "lỗ hổng vào DB" và "tác vụ đẩy tồn tại"
// nhất quán nguyên tử, không tồn tại cửa sổ commit thành công nhưng chưa vào hàng đợi, tin nhắn mất vĩnh viễn.
//
// Hai thiết kế then chốt, không cái nào viết tùy tiện:
//
//  1. **Vì sao dùng SAVEPOINT**: trong PostgreSQL, bất kỳ câu lệnh nào lỗi trong transaction đều khiến toàn bộ
//     transaction rơi vào trạng thái aborted, mọi câu lệnh sau đó (kể cả COMMIT) đều thất bại. Vì vậy "bỏ qua lỗi của
//     INSERT này, để phía gọi tiếp tục commit" trong PG là không làm được — trừ khi dùng savepoint cô lập lỗi
//     trên đúng câu lệnh này. Không có savepoint thì chỉ còn một lựa chọn "rollback toàn bộ".
//
//  2. **Vì sao rollback toàn bộ là sai**: đẩy thông báo là tính năng tiện ích, bản ghi lỗ hổng mới là sản phẩm cốt lõi.
//     Một vấn đề ở bảng thông báo (DB cũ chưa migrate, lỗi đĩa tức thời) không nên khiến lỗ hổng cao không lưu được.
//     Vì vậy ở đây cô lập lỗi, ghi log, trả về false, để việc ghi lỗ hổng vẫn commit bình thường — cái giá là mất một
//     bản đẩy này. Trả về bool thay vì error là cố ý: phía gọi không nên xem nó là lỗi ảnh hưởng đến thành bại của việc ghi.
func RecordNotificationEventTx(ctx context.Context, tx *sql.Tx, kind string, findingID int64, snap notify.Snapshot) bool {
	raw, err := json.Marshal(snap)
	if err != nil {
		log.Printf("[notify] tuần tự hóa sự kiện đẩy thất bại finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT notify_event`); err != nil {
		log.Printf("[notify] tạo savepoint thất bại finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3)`,
		kind, findingID, string(raw)); err != nil {
		log.Printf("[notify] ghi sự kiện đẩy thất bại finding=%d (bản ghi lỗ hổng không bị ảnh hưởng): %v", findingID, err)
		// Rollback về savepoint, cứu transaction ra khỏi trạng thái aborted.
		if _, rbErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT notify_event`); rbErr != nil {
			log.Printf("[notify] rollback về savepoint thất bại finding=%d: %v", findingID, rbErr)
		}
		return false
	}
	// Giải phóng savepoint, tránh tích lũy savepoint vô dụng trong transaction dài.
	_, _ = tx.ExecContext(ctx, `RELEASE SAVEPOINT notify_event`)
	return true
}

// AddNotificationEvent là phiên bản transaction độc lập của InsertNotificationEventTx, dành cho các điểm gọi
// không nằm trong transaction có sẵn (như "gửi tin nhắn thử" của kênh, nó không có finding thật).
func (d *DB) AddNotificationEvent(ctx context.Context, kind string, findingID int64, snap notify.Snapshot) (int64, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return 0, fmt.Errorf("tuần tự hóa snapshot sự kiện thông báo thất bại: %w", err)
	}
	var id int64
	err = d.QueryRowContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3) RETURNING id`,
		kind, findingID, string(raw)).Scan(&id)
	return id, err
}

// FanOutPendingEvents bung các sự kiện lỗ hổng chưa phân phối thành các tác vụ giao theo các kênh đang bật hiện tại,
// trả về số sự kiện xử lý trong vòng này và số bản giao mới tạo.
//
// Toàn bộ vòng nằm trong một transaction: sự kiện được nhận bằng FOR UPDATE SKIP LOCKED, nhiều tiến trình chạy đồng thời
// cũng nhận được các hàng khác nhau (cách nhận hàng đợi lưu trữ trong dự án dùng cùng một kỹ thuật, xem
// completeNextArchiveJob ở db/task_archives.go).
//
// Khớp bộ lọc cố ý đặt ở phía Go chứ không phải SQL: điều kiện lọc của kênh là một JSONB gồm các trường tùy chọn,
// diễn đạt sáu tổ hợp khớp bằng SQL sẽ khiến truy vấn khó bảo trì, trong khi số lượng kênh chỉ là "vài cái do con người cấu hình",
// load toàn bộ rồi so từng cái trong bộ nhớ nhanh hơn và dễ test hơn.
//
// Sự kiện không khớp kênh nào cũng sẽ được đánh dấu fanned_out — nếu không nó sẽ nằm mãi trong tập chờ phân phối,
// bị quét lại mỗi tick.
func (d *DB) FanOutPendingEvents(ctx context.Context, limit int) (eventCount, deliveryCount int, err error) {
	if limit <= 0 {
		limit = 200
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // sau khi commit thành công thì là no-op

	channels, err := listEnabledNotificationChannelsTx(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, finding_id, snapshot FROM notification_events
WHERE NOT fanned_out ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return 0, 0, err
	}
	var (
		events      []NotificationEvent
		parsedSnaps []notify.Snapshot
	)
	for rows.Next() {
		var ev NotificationEvent
		if err := rows.Scan(&ev.ID, &ev.Kind, &ev.FindingID, &ev.Snapshot); err != nil {
			rows.Close()
			return 0, 0, err
		}
		var snap notify.Snapshot
		// Snapshot do chính chúng ta ghi, về lý thuyết chắc chắn phân tích được; nếu phân tích thất bại cũng không chặn luồng giao,
		// nhưng sự kiện này sẽ vì toàn bộ trường rỗng mà bị mọi kênh có điều kiện lọc bỏ qua — thà bớt đẩy một tin
		// còn hơn để một hàng hỏng chặn chết cả hàng đợi.
		_ = json.Unmarshal(ev.Snapshot, &snap)
		// kind lấy theo giá trị trong hàng: bản trong snapshot là bản sao để render, có thể đã bị phiên bản cũ ghi.
		snap.Kind = ev.Kind
		events = append(events, ev)
		parsedSnaps = append(parsedSnaps, snap)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if len(events) == 0 {
		return 0, 0, tx.Commit()
	}

	type pending struct {
		eventID   int64
		channelID int64
	}
	var toInsert []pending
	for i, snap := range parsedSnaps {
		for _, ch := range channels {
			if !notify.Match(notify.ParseFilter(ch.Filter), snap) {
				continue
			}
			toInsert = append(toInsert, pending{eventID: events[i].ID, channelID: ch.ID})
		}
	}
	if len(toInsert) > 0 {
		var (
			vals []string
			args []any
		)
		for _, p := range toInsert {
			vals = append(vals, fmt.Sprintf("($%d,$%d)", len(args)+1, len(args)+2))
			args = append(args, p.eventID, p.channelID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries(event_id,channel_id) VALUES `+strings.Join(vals, ","), args...); err != nil {
			return 0, 0, err
		}
	}

	// Đánh dấu các sự kiện vòng này đã phân phối. Sự kiện không khớp kênh nào cũng được đánh dấu cùng (xem chú thích hàm).
	ids := make([]string, 0, len(events))
	markArgs := make([]any, 0, len(events))
	for _, ev := range events {
		markArgs = append(markArgs, ev.ID)
		ids = append(ids, fmt.Sprintf("$%d", len(markArgs)))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notification_events SET fanned_out=true WHERE id IN (`+strings.Join(ids, ",")+`)`, markArgs...); err != nil {
		return 0, 0, err
	}
	return len(events), len(toInsert), tx.Commit()
}

// listEnabledNotificationChannelsTx lấy các kênh đang bật trong transaction. Số lượng rất ít,
// không phân trang cũng không cache — cache sẽ đưa vào thêm vấn đề thời tự "đổi cấu hình khi nào có hiệu lực".
func listEnabledNotificationChannelsTx(ctx context.Context, tx *sql.Tx) ([]*NotificationChannel, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, kind, config, mode, filter, rate_per_min
FROM notification_channels WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		var c NotificationChannel
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// NotificationAssetNames phân giải id tài sản thành tên hiển thị ngắn gọn, dùng cho tin nhắn đẩy.
//
// Thứ tự trả về giống thứ tự tham số đầu vào, độ dài có thể nhỏ hơn tham số (id không tồn tại bị bỏ qua). Giữ thứ tự đầu vào là
// để thứ tự tài sản trong tin nhắn của cùng một lỗ hổng ổn định qua nhiều lần giao — nếu không, sau khi thử lại, thứ tự tài sản
// trong tin nhắn nhận được đổi, sẽ bị đọc nhầm là "tài sản đã thay đổi".
func (d *DB) NotificationAssetNames(ctx context.Context, ids []int64) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph, args := placeholders(1, ids)
	rows, err := d.QueryContext(ctx, `SELECT id, type, domain, ip, url, app_name, bundle_id FROM assets WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := map[int64]string{}
	for rows.Next() {
		var (
			id                int64
			typ               string
			domain, ip, url   sql.NullString
			appName, bundleID sql.NullString
		)
		if err := rows.Scan(&id, &typ, &domain, &ip, &url, &appName, &bundleID); err != nil {
			return nil, err
		}
		labels[id] = assetDisplayName(typ, domain.String, ip.String, url.String, appName.String, bundleID.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if label, ok := labels[id]; ok && label != "" {
			out = append(out, label)
		}
	}
	return out, nil
}

// assetDisplayName chọn định danh dễ nhận biết nhất theo loại tài sản.
// Dự phòng trả về chuỗi rỗng, để phía gọi quyết định trình bày "tài sản không lấy được tên" thế nào — hàm này không bịa placeholder,
// nếu không thứ nhiễu kiểu "tài sản#42" sẽ lẫn vào tin nhắn đẩy, người đọc còn tưởng là tên miền thật.
func assetDisplayName(typ, domain, ip, url, appName, bundleID string) string {
	pick := func(vals ...string) string {
		for _, v := range vals {
			if strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	switch typ {
	case "root_domain", "subdomain":
		return domain
	case "ip":
		return ip
	case "app":
		return pick(appName, bundleID)
	case "service", "endpoint":
		return pick(url, domain, ip)
	default:
		return pick(domain, ip, url, appName)
	}
}

// SetFindingStatusWithNotify cập nhật trạng thái xử lý của lỗ hổng, đồng thời đăng ký một sự kiện đẩy
// thay đổi trạng thái trong cùng transaction.
//
// Trả về from=trạng thái trước khi thay đổi; found=lỗ hổng có tồn tại không; notified=sự kiện có đăng ký thành công không.
//
// Ba hành vi cố ý:
//   - Không đăng ký sự kiện khi trạng thái không thực sự thay đổi. Drawer frontend gửi lặp cùng một giá trị, hoặc script tự động
//     phát lại idempotent, đều không nên tạo ra nhiễu đẩy.
//   - Khi lỗ hổng không tồn tại thì trả về found=false và không ghi gì, phía gọi tự dịch thành 404.
//   - Đăng ký sự kiện thất bại không ảnh hưởng cập nhật trạng thái (xem phần giải thích savepoint của RecordNotificationEventTx),
//     nên khi notified=false thì trạng thái đã đổi thành công, phía gọi không nên vì thế mà báo lỗi.
func (d *DB) SetFindingStatusWithNotify(ctx context.Context, id int64, status string) (from string, found bool, notified bool, err error) {
	err = d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		from, found, _, notified, txErr = SetFindingStatusTx(ctx, tx, id, status)
		return txErr
	})
	return from, found, notified, err
}

// SetFindingStatusTx cập nhật trạng thái lỗ hổng và đăng ký sự kiện đẩy thay đổi trạng thái **trong transaction của phía gọi**.
//
// Tách thành hàm cấp transaction để mọi đường đổi trạng thái dùng chung một bộ ngữ nghĩa — trước đây chỉ có
// patchFinding đi bản có thông báo, còn **khi kết luận kiểm thử lại là "đã sửa"** (câu
// `UPDATE findings SET status=...` trong finding_retests) thì ghi DB trực tiếp, thế nên các kênh cấu hình
// `on_status_change` hoàn toàn không nhận được đẩy cho loại chuyển trạng thái này: trạng thái trên giao diện lặng lẽ thay đổi,
// người vận hành phải mở nền tảng mới phát hiện.
//
// Trả về from=trạng thái trước khi đổi, found=lỗ hổng có tồn tại không, changed=trạng thái có thực sự đổi không,
// notified=sự kiện có đăng ký thành công không (đăng ký thất bại không ảnh hưởng cập nhật trạng thái, xem RecordNotificationEventTx).
func SetFindingStatusTx(ctx context.Context, tx *sql.Tx, id int64, status string) (from string, found bool, changed bool, notified bool, err error) {
	var (
		vulnclass, name, severity, summary string
		taskID                             sql.NullInt64
		assetIDs                           []byte
	)
	scanErr := tx.QueryRowContext(ctx, `SELECT vulnclass, name, severity, summary, task_id, asset_ids, status
FROM findings WHERE id=$1 FOR UPDATE`, id).
		Scan(&vulnclass, &name, &severity, &summary, &taskID, &assetIDs, &from)
	if scanErr == sql.ErrNoRows {
		return "", false, false, false, nil
	}
	if scanErr != nil {
		return "", false, false, false, scanErr
	}
	found = true
	if from == status {
		// Trạng thái không thực sự thay đổi thì không đăng ký sự kiện: gửi lặp cùng một giá trị, phát lại idempotent
		// đều không nên tạo ra nhiễu đẩy.
		return from, true, false, false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE findings SET status=$2 WHERE id=$1`, id, status); err != nil {
		return from, true, false, false, err
	}
	var assets []int64
	_ = json.Unmarshal(assetIDs, &assets)
	notified = RecordNotificationEventTx(ctx, tx, notify.EventFindingStatusChanged, id, notify.Snapshot{
		Kind:       notify.EventFindingStatusChanged,
		FindingID:  id,
		TaskID:     taskID.Int64,
		VulnClass:  vulnclass,
		Name:       name,
		Severity:   severity,
		Summary:    summary,
		AssetIDs:   assets,
		FromStatus: from,
		ToStatus:   status,
	})
	return from, true, true, notified, nil
}

// NotificationStats là bộ đếm tổng quan ở đầu trang thông báo.
type NotificationStats struct {
	Channels     int   `json:"channels"`
	ChannelsOn   int   `json:"channels_on"`
	Pending      int   `json:"pending"`
	Failed       int   `json:"failed"`
	SentToday    int   `json:"sent_today"`
	BacklogAgeMS int64 `json:"backlog_age_ms"` // số mili giây từ bản giao chờ gửi lâu nhất đến nay
}

// NotificationStatsSnapshot tổng hợp sức khỏe của hệ thống thông báo.
// BacklogAgeMS là chỉ số trực tiếp nhất cho câu hỏi "đẩy có đang bị kẹt không" — hữu ích hơn nhiều so với đếm pending,
// vì chênh lệch giữa tồn đọng 3 bản và tồn đọng 3 bản có thể là từ 3 giây đến 3 giờ.
func (d *DB) NotificationStatsSnapshot(ctx context.Context) (*NotificationStats, error) {
	var s NotificationStats
	if err := d.QueryRowContext(ctx, `SELECT
    (SELECT count(*) FROM notification_channels),
    (SELECT count(*) FROM notification_channels WHERE enabled),
    (SELECT count(*) FROM notification_deliveries WHERE state IN ($1,$2)),
    (SELECT count(*) FROM notification_deliveries WHERE state=$3),
    (SELECT count(*) FROM notification_deliveries WHERE state=$4 AND sent_at >= date_trunc('day', now())),
    COALESCE((SELECT EXTRACT(EPOCH FROM (now() - min(created_at))) * 1000 FROM notification_deliveries WHERE state=$1), 0)::bigint`,
		NotifyStatePending, NotifyStateSending, NotifyStateFailed, NotifyStateSent).
		Scan(&s.Channels, &s.ChannelsOn, &s.Pending, &s.Failed, &s.SentToday, &s.BacklogAgeMS); err != nil {
		return nil, err
	}
	return &s, nil
}
