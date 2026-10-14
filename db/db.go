// Package db is the PostgreSQL data source for ARTEX (thay thế SQLite file đơn kiểu graph cũ).
// Nó mở kết nối, áp dụng schema, và seed các agent tích hợp cùng danh mục biến.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Autumn-27/artex/config"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver ("pgx")
)

//go:embed schema.sql
var schemaSQL string

const schemaMigrationLockKey int64 = 7337741001

// maxOpenConns là trần pool kết nối, xem giải thích trong Open. Giá trị thấp hơn nhiều so với
// max_connections=100 mặc định của PostgreSQL, đồng thời cao hơn nhiều so với độ sâu lấy kết nối
// lồng nhau của chính ứng dụng (advisory lock của schema lúc khởi động sẽ giữ một kết nối trong khi
// để seedBuiltins lấy kết nối khác), không tự khóa.
const maxOpenConns = 32

var schemaDeadlockRetryDelays = [...]time.Duration{
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
}

type schemaExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func isPostgresDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

func applySchemaWithRetry(ctx context.Context, execer schemaExecer, sleep func(time.Duration)) error {
	for attempt := 0; ; attempt++ {
		if _, err := execer.ExecContext(ctx, schemaSQL); err != nil {
			if !isPostgresDeadlock(err) || attempt >= len(schemaDeadlockRetryDelays) {
				return err
			}
			sleep(schemaDeadlockRetryDelays[attempt])
			continue
		}
		return nil
	}
}

// withSchemaMigrationLock pins the session-level lock to one checked-out
// connection. Running pg_advisory_lock through *sql.DB is incorrect because a
// later schema or unlock call may use a different pooled PostgreSQL session.
func withSchemaMigrationLock(ctx context.Context, sqlDB *sql.DB, action func(*sql.Conn) error) (err error) {
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	defer func() {
		if _, unlockErr := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, schemaMigrationLockKey); unlockErr != nil && err == nil {
			err = fmt.Errorf("advisory unlock: %w", unlockErr)
		}
	}()
	return action(conn)
}

// coordinateWithSchemaMigration makes long, multi-table archive transactions
// mutually exclusive with startup DDL while allowing ordinary runtime queries
// to continue normally.
func coordinateWithSchemaMigration(tx *sql.Tx) error {
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("coordinate with schema migration: %w", err)
	}
	return nil
}

// DSN resolves the PostgreSQL connection string and reports where it came from.
// Precedence: env ARTEX_PG_DSN > config file (config.json). There is no
// built-in default — it errors if neither source is configured.
func DSN() (dsn, source string, err error) {
	return config.PostgresDSN()
}

// DB wraps the shared *sql.DB. PG handles its own connection pool + concurrency
// (MVCC), so unlike the old SQLite store there is no process-wide write mutex.
type DB struct{ *sql.DB }

// ensureDatabase connects to the postgres system database and creates the target
// database if it does not exist. dsn must be a postgres:// URL.
func ensureDatabase(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil // unparseable DSN — let the normal Open fail with a clear error
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" || dbName == "postgres" {
		return nil
	}
	// connect to the postgres maintenance database instead
	adminDSN := *u
	adminDSN.Path = "/postgres"
	admin, err := sql.Open("pgx", adminDSN.String())
	if err != nil {
		return nil // best-effort; let Open surface the real error
	}
	defer admin.Close()
	if err := admin.Ping(); err != nil {
		return nil
	}
	var exists bool
	_ = admin.QueryRow(`SELECT true FROM pg_database WHERE datname=$1`, dbName).Scan(&exists)
	if !exists {
		if _, err := admin.Exec(`CREATE DATABASE "` + dbName + `"`); err != nil {
			return fmt.Errorf("create database %q: %w", dbName, err)
		}
	}
	return nil
}

// Open connects, applies the schema (idempotent), and seeds builtin rows.
func Open(dsn string) (*DB, error) {
	if err := ensureDatabase(dsn); err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	// database/sql mặc định không giới hạn số kết nối: khi pool không còn kết nối rảnh sẽ vô điều kiện
	// tạo mới, cho tới khi chạm max_connections của PostgreSQL (mặc định 100) mới bị từ chối, nên truy vấn
	// lúc cao điểm nhận về **lỗi** kiểu `FATAL: sorry, too many clients already`. Sau khi đặt trần, truy vấn
	// vượt mức chuyển sang xếp hàng chờ kết nối rảnh — cùng tải nhưng thành chậm đi thay vì báo lỗi, phía gọi
	// không cần phân biệt "không đọc được" và "không có". maxOpenConns phải chừa dư cho psql /
	// reset-password.sh và các instance khác có thể chạy song song; nếu max_connections bị hạ thấp thì chỗ này
	// cũng phải hạ theo.
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxOpenConns)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping postgres (%s): %w", config.Redact(dsn), err)
	}
	d := &DB{sqlDB}
	// pgx runs multi-statement Exec via the simple protocol when there are no args.
	// Keep the dedicated lock connection checked out until both DDL and seeding
	// finish so concurrent application instances cannot initialize out of order.
	err = withSchemaMigrationLock(context.Background(), sqlDB, func(conn *sql.Conn) error {
		if err := applySchemaWithRetry(context.Background(), conn, time.Sleep); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		if err := d.seedBuiltins(); err != nil {
			return fmt.Errorf("seed builtins: %w", err)
		}
		return nil
	})
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// builtinAgent describes one of the fixed agents and its prompt-variable catalog.
type builtinAgent struct {
	key, name, role, desc string
	vars                  []promptVar
	interactiveShell      bool // công tắc shell tương tác mặc định lúc tạo dòng; ON CONFLICT không ghi đè thao tác bật/tắt thủ công sau đó của người dùng
	runSeconds            *int // trần thời gian đồng hồ (giây) mỗi lần run lúc tạo dòng; nil=dùng mặc định seed (1200), 0=không giới hạn
}

type promptVar struct{ name, desc, example, source string }

// intp trả về con trỏ của v, dùng để gán tường minh cho trường tùy chọn của builtinAgent (như runSeconds).
func intp(v int) *int { return &v }

// builtinAgents mirrors docs §5(a). Công cụ tích hợp không nhập DB; ở đây chỉ seed agent + danh mục biến.
// Lưu ý: shell tương tác mặc định của planner/worker/mainagent/auto được khối
// interactive_shell_default_v1 bên dưới đặt true thống nhất (tôn trọng toggle sau đó); interactiveShell
// ở đây chỉ dành cho agent mới cần "tạo dòng là bật mặc định".
var builtinAgents = []builtinAgent{
	{"goals", "Phân rã mục tiêu", "goals", "Phân rã mục tiêu tác vụ pentest thành các mục tiêu con độc lập, có thể kiểm chứng.", []promptVar{
		{"EngagementDescription", "Mô tả tác vụ (đối tượng/bối cảnh kiểm thử)", "kiểm thử site example.com", "exploration"},
		// Now là biến runtime toàn cục (xem server.globalPromptVars), không còn định nghĩa lặp
		// trong danh mục từng agent, nếu không khi withGlobalVars nối thêm sẽ trùng tên với mục toàn cục.
	}, false, nil},
	{"planner", "Lập kế hoạch", "planner", "Đọc thế trận, phán đoán mục tiêu, chỉ bổ sung ý đồ thám thính khi thực sự có hướng mới chưa được phủ (mỗi tác vụ một vòng lập kế hoạch).", []promptVar{
		{"Goal", "Mục tiêu tổng của tác vụ", "chiếm quyền quản trị của example.com", "exploration"},
		{"AssetSummary", "Tóm tắt số lượng/phân bố loại tài sản (tùy chọn)", "domain:3 ip:5 site:2", "distilled"},
	}, false, nil},
	{"mainagent", "Chính", "main", "Giao diện người-máy: quan sát tiến triển, biến ý đồ của con người thành hint hoặc ý đồ ưu tiên cao.", []promptVar{
		{"Goal", "Mục tiêu tác vụ hiện tại", "chiếm quyền quản trị của example.com", "exploration"},
		{"AssetSummary", "Tóm tắt thế trận lúc mở đầu (tùy chọn)", "domain:3 ip:5", "distilled"},
		{"FindingsSummary", "Tóm tắt lỗ hổng đã xác nhận (tùy chọn)", "high:1 medium:2", "distilled"},
	}, false, nil},
	{"worker", "Thực thi", "worker", "Nhận một ý đồ để thực thi, ghi các sự thật/lỗ hổng phát hiện về đồ thị tri thức rồi dừng.", []promptVar{
		{"ProxyAddr", "Ghi nhận địa chỉ proxy (điều khiển hai phiên bản văn bản theo if)", "127.0.0.1:8080", "runtime"},
		{"WorkerName", "Định danh của worker (tùy chọn)", "worker-1", "runtime"},
	}, false, nil},
	// Auto: agent "thao tác nền tảng" tích hợp. Không tham gia vòng điều phối pentest, được điều khiển qua trang hội thoại, dùng công cụ để thao tác nền tảng.
	{"auto", "Auto", "assistant", "Trợ lý thao tác nền tảng: dùng công cụ quản lý tác vụ (tạo/xem/tạm dừng/gợi ý) và tài sản, đồng thời có thể tạo/chỉnh sửa skill, công cụ tùy chỉnh, MCP.", nil, false, nil},
	// pentest: agent "pentest độc lập" tích hợp. Được điều khiển qua trang hội thoại, một mình đi trọn chuỗi pentest từ trinh sát tới kết thúc, tự lập kế hoạch tự thực thi tự kiểm chứng. Mặc định bật shell tương tác.
	{"pentest", "Kiểm thử xâm nhập", "assistant", "Agent pentest độc lập: một mình đi trọn chuỗi trinh sát→tìm bề mặt tấn công→khai thác sâu→kiểm chứng→kết thúc, tự lập kế hoạch, tự thực thi, tự kiểm chứng đối kháng.", nil, true, intp(0)},
}

// seedBuiltins inserts the fixed built-in agents and their variable catalog (idempotent).
func (d *DB) seedBuiltins() error {
	for _, a := range builtinAgents {
		var agentID int64
		err := d.QueryRow(`
INSERT INTO agents(key, name, description, role, builtin, enabled, interactive_shell, run_seconds)
VALUES ($1, $2, NULLIF($3,''), $4, true, true, $5, COALESCE($6, 1200))
ON CONFLICT (key) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description
RETURNING id`, a.key, a.name, a.desc, a.role, a.interactiveShell, a.runSeconds).Scan(&agentID)
		if err != nil {
			return fmt.Errorf("agent %s: %w", a.key, err)
		}
		for _, v := range a.vars {
			if _, err := d.Exec(`
INSERT INTO agent_prompt_vars(agent_id, var_name, description, example, source)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (agent_id, var_name) DO UPDATE
  SET description = EXCLUDED.description, example = EXCLUDED.example, source = EXCLUDED.source`,
				agentID, v.name, v.desc, v.example, v.source); err != nil {
				return fmt.Errorf("agent %s var %s: %w", a.key, v.name, err)
			}
		}
	}
	// Drop catalog entries for variables that were renamed, so the white-list no
	// longer advertises a name templates can't resolve (EngagementTitle→Description).
	// Sau khi 'Now' được nâng từ danh mục từng agent thành biến runtime toàn cục, DB cũ vẫn còn sót một
	// mục 'Now' trong goals, sẽ trùng tên với mục toàn cục (key bị trùng trong danh sách biến frontend);
	// xóa luôn cả nó.
	if _, err := d.Exec(`DELETE FROM agent_prompt_vars WHERE var_name IN ('EngagementTitle', 'CoverageGaps', 'Now')`); err != nil {
		return fmt.Errorf("cleanup renamed vars: %w", err)
	}
	// Default-on interactive_shell for the runtime agents (planner/worker/mainagent/auto)
	// ONCE — respects a later user toggle-off (guarded by a settings flag). goals(one-shot
	// decomposer) stays off. Runs after the column exists (schema applied before seed).
	if v, _, _ := d.GetSetting("interactive_shell_default_v1"); v != "true" {
		if _, err := d.Exec(`UPDATE agents SET interactive_shell=true WHERE key IN ('planner','worker','mainagent','auto')`); err != nil {
			return fmt.Errorf("seed interactive_shell defaults: %w", err)
		}
		_ = d.SetSetting("interactive_shell_default_v1", "true")
	}
	// Seed the built-in browser (Playwright) MCP once — DISABLED by default (người dùng
	// tự bật khi cần), no proxy by default. The traffic-capture toggle injects/strips
	// the recording proxy + CA at runtime (server.Manager.syncBrowserMCPProxy).
	// Insert only if absent so we never clobber user edits (args/env/enabled/
	// visibility) on restart.
	if _, err := d.Exec(`
INSERT INTO mcp_servers(name, transport, command, args, env, enabled)
VALUES ('browser', 'stdio', 'npx', $1, '{}', false)
ON CONFLICT (name) DO NOTHING`,
		`["@playwright/mcp","--headless"]`); err != nil {
		return fmt.Errorf("seed browser mcp: %w", err)
	}
	// NOTE: the placeholder ScopeSentry data-source MCP (empty URL + empty X-API-Key,
	// disabled) is seeded directly in schema.sql §F so a raw `psql < schema.sql` init
	// also gets it. schema.sql is Exec'd on every startup, so it stays idempotent.
	if err := d.seedBuiltinSkillVisibility(); err != nil {
		return fmt.Errorf("seed skill visibility: %w", err)
	}
	if err := d.seedDefaultInterceptRules(); err != nil {
		return fmt.Errorf("seed intercept rules: %w", err)
	}
	if err := d.seedDefaultInterceptRulesV2(); err != nil {
		return fmt.Errorf("seed intercept rules v2: %w", err)
	}
	if err := d.seedDefaultInterceptRulesV3(); err != nil {
		return fmt.Errorf("seed intercept rules v3: %w", err)
	}
	if err := d.seedDefaultAssetInterceptRules(); err != nil {
		return fmt.Errorf("seed asset intercept rules: %w", err)
	}
	return nil
}

// seedDefaultAssetInterceptRules inserts the built-in asset blocklist (fuzzy
// domain matches for government / education sites) once on first startup. Gated
// by a settings flag so a user's later disable/delete is never resurrected on
// restart — same policy as the intercept-rule seed.
func (d *DB) seedDefaultAssetInterceptRules() error {
	if v, _, _ := d.GetSetting("asset_intercept_default_rules_v1"); v == "done" {
		return nil
	}
	rules := []struct {
		kind    string
		pattern string
		note    string
	}{
		{"fuzzy_domain", ".gov", "[tích hợp] trang chính phủ (.gov)"},
		{"fuzzy_domain", ".gov.cn", "[tích hợp] trang chính phủ (.gov.cn)"},
		{"fuzzy_domain", ".edu", "[tích hợp] trang giáo dục (.edu)"},
		{"fuzzy_domain", ".edu.cn", "[tích hợp] trang giáo dục (.edu.cn)"},
	}
	for _, r := range rules {
		if _, err := d.Exec(`
INSERT INTO asset_intercept_rules(enabled, kind, pattern, note, builtin)
VALUES (true, $1, $2, $3, true)
ON CONFLICT DO NOTHING`, r.kind, r.pattern, r.note); err != nil {
			return fmt.Errorf("asset rule %q: %w", r.pattern, err)
		}
	}
	return d.SetSetting("asset_intercept_default_rules_v1", "done")
}

// builtinSkillVisibility maps a shipped skill's directory name → the built-in
// agent keys that should see it by default. The skill FILES themselves live on the
// filesystem (SkillDir, loaded by norma at runtime); DB only carries this visibility
// binding. Skills omitted here (e.g. playwright-cli, scopesentry) ship invisible by
// default — the user turns them on per-agent when needed. scopesentry additionally
// declares `mcps: ScopeSentry`, which only takes effect once it's made visible and
// that MCP is enabled/configured.
var builtinSkillVisibility = map[string][]string{
	"api-recon": {"auto", "pentest", "worker"},
}

// seedBuiltinSkillVisibility binds the shipped built-in skills to their default
// agents. Insert-if-absent (ON CONFLICT DO NOTHING) so a user's later toggle-off is
// never resurrected on restart — matches the browser-MCP / intercept-rule seed policy.
func (d *DB) seedBuiltinSkillVisibility() error {
	for skillName, agentKeys := range builtinSkillVisibility {
		for _, key := range agentKeys {
			if _, err := d.Exec(`
INSERT INTO agent_skill_visibility(agent_id, skill_name, enabled)
SELECT id, $2, true FROM agents WHERE key=$1
ON CONFLICT (agent_id, skill_name) DO NOTHING`, key, skillName); err != nil {
				return fmt.Errorf("skill %s → agent %s: %w", skillName, key, err)
			}
		}
	}
	return nil
}

// seedDefaultInterceptRules inserts built-in safety intercept rules once on
// first startup. The seed is gated by a settings flag so user edits (disable,
// delete, re-order) are never overwritten on subsequent restarts.
func (d *DB) seedDefaultInterceptRules() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v1"); v == "done" {
		return nil
	}
	type rule struct {
		name     string
		target   string // tool_name | tool_input
		typ      string // string | regex
		pattern  string
		action   string
		message  string
		priority int
	}
	rules := []rule{
		// ── Lệnh phá hủy hệ thống (priority 100) ────────────────────────────
		{
			name:     "[tích hợp] xóa đệ quy cưỡng ép rm -rf",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\brm\b.{0,80}(?:-[a-z]*r[a-z]*f[a-z]*|-[a-z]*f[a-z]*r[a-z]*|--recursive|--no-preserve-root)`,
			action:   "deny",
			message:  "Cấm thực thi xóa đệ quy cưỡng ép (rm -rf / rm --recursive), có thể phá hỏng vĩnh viễn hệ thống hoặc môi trường mục tiêu",
			priority: 100,
		},
		{
			name:     "[tích hợp] xóa thư mục hệ thống quan trọng",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\brm\b[^"'\n]{0,60}["'\s](/|/etc|/bin|/usr|/boot|/var|/lib|/sys|/proc|/dev|/sbin|/root)`,
			action:   "deny",
			message:  "Cấm xóa các đường dẫn hệ thống quan trọng",
			priority: 100,
		},
		{
			name:     "[tích hợp] định dạng đĩa mkfs",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\bmkfs\b`,
			action:   "deny",
			message:  "Cấm định dạng đĩa (mkfs)",
			priority: 100,
		},
		{
			name:     "[tích hợp] ghi đè thiết bị đĩa dd",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\bdd\b[^|\n]{0,100}\bof=\s*/dev/[a-zA-Z]`,
			action:   "deny",
			message:  "Cấm dùng dd ghi đè thiết bị đĩa",
			priority: 100,
		},
		{
			name:     "[tích hợp] bom Fork",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `:\(\)\s*\{[^}]*:\|:`,
			action:   "deny",
			message:  "Cấm thực thi bom Fork",
			priority: 100,
		},
		{
			name:     "[tích hợp] tắt máy / khởi động lại",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\b(?:shutdown|reboot|halt|poweroff|init\s+[06])\b`,
			action:   "deny",
			message:  "Cấm thực thi lệnh tắt máy hoặc khởi động lại",
			priority: 100,
		},
		{
			name:     "[tích hợp] giết toàn bộ tiến trình",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\bkill\s+-9\s+-1\b|\bkillall\s+-9\b`,
			action:   "deny",
			message:  "Cấm kill -9 -1 hoặc killall -9 (giết mọi tiến trình)",
			priority: 100,
		},
		{
			name:     "[tích hợp] xóa sạch đĩa shred / wipe",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\b(?:shred|wipe)\b[^|\n]{0,80}/dev/[a-zA-Z]`,
			action:   "deny",
			message:  "Cấm thực thi shred/wipe xóa sạch thiết bị đĩa",
			priority: 100,
		},
		{
			name:     "[tích hợp] xóa sạch quy tắc tường lửa",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\biptables\s+(?:-F|--flush)\b|\bnft\s+flush\s+ruleset\b`,
			action:   "deny",
			message:  "Cấm xóa sạch quy tắc tường lửa (iptables -F / nft flush)",
			priority: 100,
		},
		// ── Thao tác phá hủy cơ sở dữ liệu (priority 90) ─────────────────────
		{
			name:     "[tích hợp] SQL DROP DATABASE / TABLE / SCHEMA",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\bDROP\s+(?:DATABASE|TABLE|SCHEMA|INDEX|VIEW|TABLESPACE|USER|ROLE)\b`,
			action:   "deny",
			message:  "Cấm thực thi thao tác DROP, có thể phá hủy đối tượng cơ sở dữ liệu không thể đảo ngược",
			priority: 90,
		},
		{
			name:     "[tích hợp] SQL TRUNCATE",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\bTRUNCATE\s+(?:TABLE\s+)?\w`,
			action:   "deny",
			message:  "Cấm thực thi TRUNCATE, có thể xóa sạch toàn bộ dữ liệu của bảng",
			priority: 90,
		},
		{
			name:     "[tích hợp] MongoDB drop / dropDatabase",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\.(?:dropDatabase|dropCollection|drop)\s*\(`,
			action:   "deny",
			message:  "Cấm thực thi thao tác drop của MongoDB",
			priority: 90,
		},
		{
			name:     "[tích hợp] Redis FLUSHALL / FLUSHDB",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\b(?:FLUSHALL|FLUSHDB)\b`,
			action:   "deny",
			message:  "Cấm thực thi Redis FLUSHALL / FLUSHDB, có thể xóa sạch toàn bộ dữ liệu cache",
			priority: 90,
		},
		// ── Request HTTP phá hủy (priority 80) ───────────────────────────────
		// Ba cách phổ biến để Agent gửi request DELETE:
		//   1. curl -X DELETE / --request DELETE (công cụ Bash thực thi trực tiếp hoặc ghi vào script)
		//   2. Phương thức .delete() của HTTP client Python
		//   3. method: 'DELETE' / method="DELETE" trong script JS/script nói chung
		{
			name:     "[tích hợp] curl / wget gửi request DELETE",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\bcurl\b[^|\n&;"]{0,300}(?:-X\s*DELETE|--request\s+DELETE|-XDELETE)|\bwget\b[^|\n&;"]{0,300}--method[=\s]+DELETE`,
			action:   "deny",
			message:  "Cấm gửi request HTTP DELETE qua curl/wget, có thể xóa dữ liệu hệ thống mục tiêu",
			priority: 80,
		},
		{
			name:     "[tích hợp] HTTP client Python DELETE (requests/httpx/aiohttp)",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\b(?:requests|httpx|aiohttp|urllib\.request)\.delete\s*\(|session\.delete\s*\(|client\.delete\s*\(`,
			action:   "deny",
			message:  "Cấm dùng HTTP client Python gửi request DELETE",
			priority: 80,
		},
		{
			name:     "[tích hợp] khai báo phương thức HTTP DELETE trong script (JS/nói chung)",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)axios\.delete\s*\(|method\s*[:=]\s*['"]DELETE['"]`,
			action:   "deny",
			message:  "Cấm khai báo và gửi request HTTP DELETE trong script",
			priority: 80,
		},
		{
			name:     "[tích hợp] đường dẫn API dạng xóa sạch hàng loạt",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)/(?:clear|wipe|flush|purge|truncate|drop|destroy|factory[-_]reset|reset[-_]all)(?:[/?#"'\s]|$)`,
			action:   "deny",
			message:  "Cấm gọi API dạng xóa sạch hoặc hủy diệt hàng loạt (/clear /wipe /flush /purge v.v.)",
			priority: 80,
		},
	}
	for _, r := range rules {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, true, $2, $3, $4, $5, $6, $7, false, 60, 'deny')
ON CONFLICT DO NOTHING`,
			r.name, r.priority, r.target, r.typ, r.pattern, r.action, r.message,
		); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v1", "done")
}

// seedDefaultInterceptRulesV2 migrates the two safety patterns that used to be
// hard-coded in guard.go (destructive shell + data-exfil pipe) into ordinary
// intercept rules. Gated by its own flag so it also lands on DBs that already ran
// v1. Unlike the old guard.go floor, these are plain [tích hợp] rules — the user can
// disable or delete them. The exfil rule ships DISABLED by default (its
// curl/wget/nc pipe pattern mis-fires on legitimate CTF/pentest reverse-shell and
// data-transfer pipes); enable it manually when exfil gating is actually wanted.
func (d *DB) seedDefaultInterceptRulesV2() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v2"); v == "done" {
		return nil
	}
	rules := []struct {
		name     string
		pattern  string
		action   string
		message  string
		enabled  bool
		priority int
	}{
		{
			name:     "[tích hợp] lệnh hệ thống phá hủy",
			pattern:  `(?i)\b(rm\s+-rf\s+/|mkfs|dd\s+if=|:\(\)\s*\{|shutdown|reboot|>\s*/dev/sd)`,
			action:   "deny",
			message:  "Lệnh phá hủy bị từ chối (rm -rf / / mkfs / dd / fork bomb / tắt máy khởi động lại / ghi đè thiết bị đĩa)",
			enabled:  true,
			priority: 100,
		},
		{
			name:     "[tích hợp] đường ống rò rỉ dữ liệu",
			pattern:  `(?i)(curl|wget|nc|ncat)\b[^|]*\b(\|\s*(curl|wget|nc))`,
			action:   "deny",
			message:  "Đường ống nghi rò rỉ dữ liệu bị từ chối (đầu ra lệnh bị chuyển ra ngoài qua curl/wget/nc)",
			enabled:  false,
			priority: 80,
		},
	}
	for _, r := range rules {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, $2, $3, 'tool_input', 'regex', $4, $5, $6, false, 60, 'deny')
ON CONFLICT DO NOTHING`,
			r.name, r.enabled, r.priority, r.pattern, r.action, r.message,
		); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v2", "done")
}

// seedDefaultInterceptRulesV3 adds the delete-endpoint path rule. The v1 HTTP rules
// only catch the DELETE *method* (curl -X DELETE, requests.delete(, method:'DELETE'),
// and v1's path rule covers only /clear /wipe /flush /purge /truncate /drop /destroy
// /factory-reset /reset-all — so a plain `curl 'http://t/api/user/delete?id=1'` (a
// delete endpoint reached with GET/POST, which is how most web apps expose deletion)
// slipped through every built-in rule. Own flag so it also lands on DBs that already
// ran v1/v2, where editing the v1 seed would have no effect.
//
// The pattern deliberately requires a separator after the verb so /delivery,
// /details, /delta and /delegate do not match, while /deleteAll, /delete_user and
// /delete-user do. destroy is re-covered here because v1's rule does not allow a
// suffix (/destroyAll was missed).
//
// Exported as a package const only so the seeded regex is unit-testable without a DB.
const deleteEndpointPathPattern = `(?i)/(?:(?:delete|remove|unlink|erase|destroy)[-\w]*|del)(?:[/?#"'\s]|$)`

func (d *DB) seedDefaultInterceptRulesV3() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v3"); v == "done" {
		return nil
	}
	const name = "[tích hợp] đường dẫn API dạng xóa"
	if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
SELECT $1, true, 80, 'tool_input', 'regex', $2, 'deny', $3, false, 60, 'deny'
WHERE NOT EXISTS (SELECT 1 FROM intercept_rules WHERE name = $1)`,
		name,
		deleteEndpointPathPattern,
		"Cấm gọi API dạng xóa (/delete /remove /unlink /erase v.v.), bất kể dùng phương thức HTTP nào — phần lớn ứng dụng chỉ cần GET/POST là kích hoạt được API xóa, và nó vẫn thực sự xóa dữ liệu mục tiêu",
	); err != nil {
		return fmt.Errorf("rule %q: %w", name, err)
	}
	return d.SetSetting("intercept_default_rules_v3", "done")
}
