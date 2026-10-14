package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// File này là phần nhận tác vụ giao và chuyển trạng thái.
//
// Việc nhận dùng "lease" thay vì transaction dài: đặt hàng thành sending và đẩy next_attempt_at
// ra tương lai làm thời điểm hết hạn lease, commit transaction rồi mới thực hiện giao qua mạng. Như vậy trong
// thời gian giao không giữ khóa cơ sở dữ liệu — request mạng có thể mất vài giây (timeout của client là 15 giây),
// giữ khóa hàng không nhả sẽ kéo sập các thao tác ghi khác của cùng DB.
//
// Cái giá là nếu tiến trình crash giữa chừng khi giao, hàng sẽ dừng ở sending. Điều này **có thể tự phục hồi**: sau khi
// lease hết hạn, next_attempt_at rơi vào quá khứ, vòng nhận tiếp theo sẽ nhặt lại cùng hàng đó (xem điều kiện
// state IN ('pending','sending') trong phần nhận). Bộ đếm thử lại đã +1 ngay lúc nhận, nên crash không gây
// thử lại vô hạn — sau khi dùng hết MaxNotifyAttempts cơ hội thì rơi vào failed chờ xử lý thủ công.

// MaxNotifyAttempts là số lần thử tối đa của một bản giao (bao gồm lần đầu).
// Định nghĩa ở đây chứ không phải trong engine giao: nó là chính sách của chính máy trạng thái, engine chỉ là người thực thi.
const MaxNotifyAttempts = 3

// MaxDigestBatchSize là số bản giao tối đa được gộp trong một lần của một batch tổng hợp.
//
// Lý do tồn tại là tài nguyên: nếu trong một chu kỳ tổng hợp quét ra vài chục nghìn lỗ hổng (hoàn toàn có thể — một lần
// quét toàn bộ là đủ), nếu không đặt trần thì việc nhận sẽ đọc toàn bộ hàng vào bộ nhớ, render thành một tin nhắn siêu dài,
// rồi bị giới hạn độ dài của kênh cắt mất phần lớn — vừa lãng phí bộ nhớ, vừa **mất âm thầm** những lỗ hổng bị cắt.
// Sau khi đặt trần, phần vượt quá ở lại trong DB thành batch tiếp theo, chu kỳ sau tự nhiên gửi đi, không mất.
//
// Cơ sở chọn 500: đó là mức sau khi render thành tin nhắn vẫn "có nội dung đọc được" trong giới hạn 4096 byte của WeCom;
// lớn hơn nữa cũng chỉ khiến việc cắt xảy ra ở vị trí xa hơn thôi.
const MaxDigestBatchSize = 500

// NotificationDelivery là một tác vụ giao, chứa cấu hình kênh và snapshot sự kiện cần cho render.
type NotificationDelivery struct {
	ID            int64           `json:"id"`
	EventID       int64           `json:"event_id"`
	ChannelID     int64           `json:"channel_id"`
	State         string          `json:"state"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	LastError     string          `json:"last_error"`
	BatchID       *int64          `json:"batch_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	SentAt        *time.Time      `json:"sent_at,omitempty"`
	Snapshot      json.RawMessage `json:"snapshot,omitempty"`
	// Ngữ cảnh render được load join, không đưa vào JSON (do tầng server lắp ráp DTO).
	Channel *NotificationChannel `json:"-"`
	// FindingID/EventKind lấy từ sự kiện ra, để danh sách lịch sử nhảy thẳng tới chi tiết lỗ hổng.
	FindingID int64  `json:"finding_id,string"`
	EventKind string `json:"event_kind"`
	// ChannelName/ChannelKind là trường dư thừa phục vụ hiển thị danh sách, khỏi cần frontend truy vấn lần hai.
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
}

const notificationDeliveryCols = `d.id, d.event_id, d.channel_id, d.state, d.attempts, d.next_attempt_at,
       d.last_error, d.batch_id, d.created_at, d.sent_at`

// joinedDeliveryQuery là hình dạng đọc thống nhất của hàng giao: giao + snapshot sự kiện + cấu hình kênh.
// Render một tin nhắn thiếu một trong ba là không đủ, tách ra truy vấn sẽ phải viết ba lần round-trip.
const joinedDeliveryQuery = `SELECT ` + notificationDeliveryCols + `,
       e.snapshot, e.kind, e.finding_id,
       c.id, c.name, c.kind, c.enabled, c.config, c.mode, c.filter, c.rate_per_min
FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
JOIN notification_channels c ON c.id = d.channel_id`

func scanNotificationDelivery(sc interface{ Scan(...any) error }) (*NotificationDelivery, error) {
	var (
		dl        NotificationDelivery
		lastErr   sql.NullString
		batchID   sql.NullInt64
		sentAt    sql.NullTime
		snapshot  []byte
		eventKind string
		channel   NotificationChannel
		chEnabled bool
	)
	if err := sc.Scan(&dl.ID, &dl.EventID, &dl.ChannelID, &dl.State, &dl.Attempts, &dl.NextAttemptAt,
		&lastErr, &batchID, &dl.CreatedAt, &sentAt,
		&snapshot, &eventKind, &dl.FindingID,
		&channel.ID, &channel.Name, &channel.Kind, &chEnabled, &channel.Config, &channel.Mode, &channel.Filter, &channel.RatePerMin); err != nil {
		return nil, err
	}
	dl.LastError = lastErr.String
	if batchID.Valid {
		dl.BatchID = &batchID.Int64
	}
	if sentAt.Valid {
		dl.SentAt = &sentAt.Time
	}
	dl.Snapshot = json.RawMessage(snapshot)
	dl.EventKind = eventKind
	dl.ChannelName = channel.Name
	dl.ChannelKind = channel.Kind
	channel.Enabled = &chEnabled
	dl.Channel = &channel
	return &dl, nil
}

// claimQuery mô tả một lần nhận: trước chọn ứng viên theo sel và khóa, rồi đặt chúng thành sending và
// gia hạn lease. Vị trí lease trong sel do phía gọi đặt placeholder $n và tự truyền tham số.
type claimQuery struct {
	sql  string
	args []any
}

// ClaimRealtimeDeliveries nhận một loạt bản giao realtime đã đến hạn của một kênh, tối đa limit bản.
//
// Cố ý nhận theo **từng kênh** thay vì "nhận một loạt toàn cục rồi chọn mà gửi": van giới hạn tốc độ trong engine giao được
// duy trì theo kênh, chỉ khi biết trước kênh này vòng này còn gửi được mấy bản, rồi nhận đúng bấy nhiêu hàng, thì giới hạn tốc độ
// mới không tiêu hao số lần thử lại. Nếu ngược lại nhận trước bỏ sau, hàng bị van giới hạn chặn đã bị đếm một lần attempts,
// ngân sách 3 lần sẽ bị việc chờ thuần túy ngốn hết, cuối cùng rơi vào failed.
//
// Điều kiện bao gồm "sending mà lease đã hết hạn" — đó là điểm rơi của cơ chế tự phục hồi sau crash. lease phải lớn hơn rõ rệt
// thời gian xấu nhất của một lần giao (timeout HTTP client của kênh là 15 giây), nếu không cùng một hàng sẽ bị hai dispatcher
// giao đồng thời. Đồng thời chặn kênh đã tắt: thao tác tắt đã đánh dấu các bản giao tồn kho thành skipped,
// ở đây chặn thêm một lớp, tránh sót khi tắt và nhận xảy ra đồng thời.
func (d *DB) ClaimRealtimeDeliveries(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	return d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now()
  AND c.enabled AND c.mode = $4
ORDER BY dd.next_attempt_at, dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $5`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, NotifyModeRealtime, limit},
	}, nil)
}

// DigestBatchDue báo cáo kênh đã gom đủ một batch đến hạn chưa: tồn tại bản giao chờ gửi, và **bản lâu nhất**
// đã đạt tuổi của chu kỳ tổng hợp.
//
// Căn cứ phán định là tuổi của bản giao lâu nhất chứ không phải đồng hồ thực: như vậy kênh vừa tạo sẽ không vì canh theo giờ chẵn mà
// ngay lập tức nhả ra một "tổng hợp" chỉ có một bản, batch tồn đọng lâu cũng không phải chờ thêm một vòng.
//
// Tách khỏi ClaimDigestBatch vì ngữ nghĩa khác nhau: hàm này chỉ trả lời "có nên gửi không",
// còn việc nhận phải lấy **toàn bộ** hàng chờ gửi của kênh đó (kể cả những bản chưa đủ tuổi) — nếu không một chu kỳ
// sẽ bị chia thành nhiều tin nhắn, tổng hợp mất ý nghĩa.
func (d *DB) DigestBatchDue(ctx context.Context, channelID int64, minAge time.Duration) (bool, error) {
	var due bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM notification_deliveries d
  JOIN notification_channels c ON c.id = d.channel_id
  WHERE d.channel_id = $1 AND d.state IN ($2,$3) AND c.enabled
  GROUP BY d.channel_id
  HAVING min(d.created_at) <= now() - make_interval(secs => $4)
)`, channelID, NotifyStatePending, NotifyStateSending, int64(minAge.Seconds())).Scan(&due)
	return due, err
}

// ClaimDigestBatch nhận các bản giao chờ gửi đã đến hạn hiện tại của một kênh, làm một batch tổng hợp,
// một batch tối đa MaxDigestBatchSize bản.
//
// Tất cả bản giao của cùng batch chia sẻ batch_id, dùng id nhỏ nhất trong tập làm số batch (ổn định, dễ đọc,
// không cần sequence phụ). Khi thử lại dùng COALESCE giữ nguyên số batch, để "N bản này được gửi cùng nhau"
// vẫn đúng sau nhiều lần thử lại.
//
// Lấy N bản đầu theo id tăng dần thay vì lấy ngẫu nhiên: bản giao sinh ra sớm nhất được gửi trước, khi tồn đọng sẽ không xuất hiện
// tình trạng đói "lỗ hổng mới gửi trước, lỗ hổng cũ mãi xếp sau".
func (d *DB) ClaimDigestBatch(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	// limit là **trần bộ nhớ**, phía gọi truyền MaxDigestBatchSize; ở đây kẹp thêm một lớp,
	// phòng phía gọi truyền vào một giá trị lớn hơn.
	//
	// Cố ý không chấp nhận "hạn mức giới hạn tốc độ" làm kích thước batch: đơn vị của giới hạn tốc độ là số tin nhắn — một batch chỉ gửi
	// một tin nhắn, tiêu một token, do takeTokens ở tầng server trừ — khác thứ nguyên với "một batch chứa mấy
	// lỗ hổng". Từng có lần để rate_per_min có hiệu lực với digest mà truyền ngân sách mỗi vòng
	// vào làm kích thước batch, kết quả kênh rate=20/min mỗi batch chỉ chứa 1 lỗ hổng,
	// digest thoái hóa thành đẩy realtime có kèm văn bản tổng hợp. Muốn đổi giới hạn tốc độ hãy sửa want của takeTokens,
	// đừng động vào đây.
	if limit > MaxDigestBatchSize {
		limit = MaxDigestBatchSize
	}
	out, err := d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now() AND c.enabled
ORDER BY dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $4`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, limit},
	}, func(tx *sql.Tx, ids []int64) error {
		batchID := ids[0]
		for _, id := range ids {
			if id < batchID {
				batchID = id
			}
		}
		ph, idArgs := placeholders(2, ids)
		_, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET batch_id = COALESCE(batch_id, $1)
WHERE id IN (`+ph+`)`, append([]any{batchID}, idArgs...)...)
		return err
	})
	return out, err
}

// claimDeliveries thực thi "chọn + đặt sending gia hạn lease + đọc hàng đầy đủ", tất cả trong một transaction.
// postClaim là bước bổ sung tùy chọn (batch tổng hợp dùng nó để ghi batch_id).
func (d *DB) claimDeliveries(ctx context.Context, lease time.Duration, cq claimQuery, postClaim func(*sql.Tx, []int64) error) ([]*NotificationDelivery, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // sau khi commit thành công thì là no-op

	ids, err := selectForClaim(ctx, tx, cq.sql, cq.args...)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	// Đặt sending và đẩy next_attempt_at ra tương lai: thời điểm tương lai đó chính là lúc lease hết hạn,
	// "lease chưa hết hạn" và "chưa tới giờ thử lại" nhờ đó dùng chung một biểu thức điều kiện, không cần thêm cột.
	ph, idArgs := placeholders(3, ids)
	if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=attempts+1, next_attempt_at=now()+make_interval(secs => $2)
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateSending, lease.Seconds()}, idArgs...)...); err != nil {
		return nil, err
	}
	if postClaim != nil {
		if err := postClaim(tx, ids); err != nil {
			return nil, err
		}
	}
	out, err := loadDeliveriesTx(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func selectForClaim(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadDeliveriesTx(ctx context.Context, tx *sql.Tx, ids []int64) ([]*NotificationDelivery, error) {
	ph, args := placeholders(1, ids)
	rows, err := tx.QueryContext(ctx, joinedDeliveryQuery+` WHERE d.id IN (`+ph+`) ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// MarkDeliveriesSent đánh dấu một loạt bản giao là đã giao tới.
func (d *DB) MarkDeliveriesSent(ctx context.Context, ids []int64) error {
	ph, args := placeholders(2, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, sent_at=now(), last_error='' WHERE id IN (`+ph+`)`, append([]any{NotifyStateSent}, args...)...)
	return err
}

// RescheduleDeliveries trả một loạt bản giao về pending và đẩy lùi thời gian thử lại.
//
// Trả về pending thay vì đưa vào trạng thái trung gian mới, để "còn mấy cơ hội" chỉ do một nơi
// diễn đạt (MaxNotifyAttempts), tránh nhánh của máy trạng thái phình theo chiến lược thử lại.
func (d *DB) RescheduleDeliveries(ctx context.Context, ids []int64, delay time.Duration, errMsg string) error {
	ph, args := placeholders(4, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, next_attempt_at=now()+make_interval(secs => $2), last_error=$3
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, delay.Seconds(), truncateNotifyError(errMsg)}, args...)...)
	return err
}

// DeferDeliveries trả một loạt bản giao về pending, có thể nhận lại ngay, và **hủy lần thử đã đếm khi nhận**.
//
// Công dụng chỉ có một: khi tin nhắn tổng hợp được chia đoạn gửi theo giới hạn độ dài của kênh, các mục không nhét vào tin này phải để cho batch sau.
// Đó không phải thất bại, nên không nên tiêu hao ngân sách thử lại — lúc nhận attempts đã lạc quan +1 rồi,
// ở đây phải trừ lại. Nếu không, một đợt tồn đọng 500 bản sẽ bị cắt thành 25 đoạn mỗi đoạn 20 bản,
// các mục cuối ở đoạn thứ 3 đã bị MaxNotifyAttempts phán thành failed, trong khi chúng chưa từng gặp lỗi nào.
//
// GREATEST(...,0) chống lưng cho trường hợp "có người gửi lại thủ công đã xóa attempts về 0 rồi lại đi tới đây",
// không để bộ đếm thành số âm.
func (d *DB) DeferDeliveries(ctx context.Context, ids []int64, reason string) error {
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=GREATEST(attempts-1, 0), next_attempt_at=now(), last_error=$2
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, truncateNotifyError(reason)}, args...)...)
	return err
}

// FailDeliveries đánh dấu một loạt bản giao là thất bại cuối cùng, chờ con người gửi lại trong lịch sử giao.
func (d *DB) FailDeliveries(ctx context.Context, ids []int64, errMsg string) error {
	// Placeholder bắt đầu từ $3: $1 là state, $2 là last_error.
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries SET state=$1, last_error=$2 WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateFailed, truncateNotifyError(errMsg)}, args...)...)
	return err
}

// RetryNotificationDelivery gửi lại thủ công một bản giao: đặt lại về pending, xóa bộ đếm thử lại,
// đến hạn ngay lập tức. Xóa bộ đếm là cố ý — con người bấm "gửi lại" nghĩa là nguyên nhân của các lần thất bại trước đã được xử lý,
// lại lấy bộ đếm cũ ra giới hạn nó thì vô lý.
func (d *DB) RetryNotificationDelivery(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$2, attempts=0, next_attempt_at=now(), last_error=''
WHERE id=$1 AND state IN ($3,$4)`, id, NotifyStatePending, NotifyStateFailed, NotifyStateSkipped)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("bản giao %d không tồn tại hoặc trạng thái hiện tại không cho phép gửi lại", id)
	}
	return nil
}

// NotificationDeliveryFilter là điều kiện truy vấn lịch sử giao.
type NotificationDeliveryFilter struct {
	ChannelID int64
	State     string
	EventKind string
}

func (f NotificationDeliveryFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.ChannelID > 0 {
		args = append(args, f.ChannelID)
		conds = append(conds, fmt.Sprintf("d.channel_id=$%d", len(args)))
	}
	if f.State != "" {
		args = append(args, f.State)
		conds = append(conds, fmt.Sprintf("d.state=$%d", len(args)))
	}
	if f.EventKind != "" {
		args = append(args, f.EventKind)
		conds = append(conds, fmt.Sprintf("e.kind=$%d", len(args)))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListNotificationDeliveries trả về lịch sử giao theo trang, mới trước.
func (d *DB) ListNotificationDeliveries(ctx context.Context, f NotificationDeliveryFilter, page, pageSize int) ([]*NotificationDelivery, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	where, args := f.where()

	var total int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf("%s%s ORDER BY d.id DESC LIMIT $%d OFFSET $%d",
		joinedDeliveryQuery, where, len(args)+1, len(args)+2)
	rows, err := d.QueryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, dl)
	}
	return out, total, rows.Err()
}

// truncateNotifyError cắt thông báo lỗi về độ dài cột chấp nhận được. Response body mà kênh trả về có thể rất dài
// (đặc biệt khi Webhook chung bắn vào dịch vụ tự dựng), không cắt sẽ làm payload của danh sách lịch sử phình to.
func truncateNotifyError(msg string) string {
	const max = 500
	if len(msg) <= max {
		return msg
	}
	// Lùi theo biên ký tự, tránh để lại nửa ký tự UTF-8 khiến frontend hiển thị mã loạn.
	cut := max
	for cut > 0 && !isUTF8Start(msg[cut]) {
		cut--
	}
	return msg[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// placeholders sinh chuỗi placeholder $n bắt đầu từ start cùng tham số tương ứng, dùng cho IN (...).
// Ví dụ start=3, ids=[7,8] → "$3,$4", [7,8].
func placeholders(start int, ids []int64) (string, []any) {
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		ph = append(ph, fmt.Sprintf("$%d", start+i))
		args = append(args, id)
	}
	return strings.Join(ph, ","), args
}
