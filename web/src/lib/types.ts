// ARTEX domain model — types used across the UI.
// Derived from the functional spec (section 7: các dạng dữ liệu chính).

export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout";
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export interface Task {
  id: string;
  name?: string; // tên tác vụ tùy chọn; rỗng/không có = chưa đặt tên, khi hiển thị quay về mô tả
  category_id?: number;
  category_name?: string;
  pinned?: boolean;
  pinned_at?: string | null;
  description: string;
  goal: string;
  status: TaskStatus;
  created_at: string;
  created_unix?: number; // created_at as unix seconds (run-duration calc)
  completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
  completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
  last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
  paused?: boolean;
  queued?: boolean;
  active?: boolean;
  in_flight?: number;
  findings?: { critical: number; high: number; medium: number; low: number }; // số lỗ hổng đã ghi nhận (phân theo mức độ nghiêm trọng)
  last_activity?: string;
  stalled?: boolean;
  goals_total?: number;
  goals_met?: number;
  engine_mode?: EngineMode;
  tokens?: TokenTotal; // whole-task token consumption
  llm_profile_id?: number; // LLM profile used; absent = default profile
  llm_profile_ids?: number[]; // ordered task-level failover chain
  active_llm_profile_id?: number; // profile used by the next LLM call
  llm_failover_state?: "default" | "ready" | "chain_exhausted" | string;
  llm_failover_reason?: string;
  source_task_ids?: string[]; // directly related tasks inherited as read-only context
  archive_blocked_by_task_id?: string; // live direct dependent that must be archived first
  company_ids?: number[]; // associated company scopes; current company assets join the task at creation
  coverage_enabled?: boolean; // công tắc tính năng độ bao phủ tài sản (định lúc tạo, mặc định bật); false = không tính/không hiển thị độ bao phủ
}

export interface TaskCategory {
  id: number;
  name: string;
  task_count: number;
  created_at: string;
  updated_at: string;
}

export interface TaskTemplate {
  id: number;
  name: string;
  description: string;
  goal: string;
  category_id?: number | null; // phân loại đặt sẵn; null/không có = không có
  intercept_rules?: AssetInterceptRuleInput[]; // quy tắc chặn/cho phép cấp tác vụ đặt sẵn
  created_at: string;
  updated_at: string;
}

export interface DeleteTaskOptions {
  delete_assets: boolean;
  delete_traffic: boolean;
  delete_files: boolean;
  delete_findings: boolean;
  delete_llm_records: boolean;
}

export interface DeleteTaskResult {
  deleted: string;
  assets_deleted: number;
  assets_detached: number;
  traffic_deleted: number;
  files_deleted: boolean;
  findings_deleted: number;
  llm_records_deleted: number;
  cleanup_warning?: string;
}

export type TaskArchiveState =
  | "archive_queued"
  | "archiving"
  | "archive_failed"
  | "ready"
  | "restore_queued"
  | "restoring"
  | "restore_failed"
  | "delete_queued"
  | "deleting"
  | "delete_failed";

export interface TaskArchiveTokenStats {
  calls?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface TaskArchive {
  id: number;
  task_id: number;
  state: TaskArchiveState;
  phase: string;
  progress: number;
  error?: string;
  warnings?: string[];
  format_version: number;
  sha256?: string;
  original_size: number;
  compressed_size: number;
  task_name: string;
  task_description: string;
  task_goal: string;
  original_status: TaskStatus;
  category_id?: number;
  category_name?: string;
  source_task_ids: number[];
  remaining_timeout_seconds: number;
  data_counts: Record<string, number>;
  aggregate_stats: {
    tokens?: TaskArchiveTokenStats;
    skills?: Record<string, number>;
    tools?: Record<string, number>;
    findings?: Record<string, number>;
  };
  archived_at?: string;
  requested_at: string;
  created_at: string;
  updated_at: string;
}

export interface TaskArchivePage {
  items: TaskArchive[];
  total: number;
  page: number;
  size: number;
}

export interface ArchiveBatchItem {
  id: string;
  archive_id?: number;
  ok: boolean;
  queued: boolean;
  error?: string;
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
  | "company"
  | "domain"
  | "ip"
  | "port"
  | "service"
  | "site"
  | "endpoint"
  | "parameter"
  | "tech"
  | "credential"
  | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode {
  id: string;
  type: AssetType;
  name: string;
  key: string; // nkey
  value?: string;
  company_id?: string; // id tài sản doanh nghiệp sở hữu; rỗng = chưa gán thuộc
  state: NodeState;
  confidence: number; // 0..1
  attrs?: Record<string, unknown>;
  first_seen: string;
  last_seen: string;
}

export type AssetRel =
  | "owns"
  | "resolves"
  | "exposes"
  | "runs"
  | "serves"
  | "has_endpoint"
  | "has_param"
  | "fingerprinted"
  | "authenticates_as"
  | "reachable"
  | "has_subdomain";

export interface Edge {
  src: string;
  dst: string;
  rel: AssetRel | ExploreRel;
}

// Task asset view — server-side enriched, paginated.
export interface TaskAssetRef {
  id: string;
  name?: string;
  key: string;
  attrs?: Record<string, unknown>;
}

export interface TaskAssetItem extends AssetNode {
  techs?: TaskAssetRef[];
  auth?: TaskAssetRef[];
  params?: TaskAssetRef[];
}

export interface TaskAssetView {
  counts: Record<string, number>;
  total: number;
  items: TaskAssetItem[];
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface Asset {
  id: number;
  type: NewAssetType;
  company_id?: number;
  task_ids: number[];
  domain?: string;
  root_domain?: string;
  ip?: string;
  c_segment?: string;
  port?: number;
  icp?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  record_type?: string;
  record_value?: string[] | string;
  bundle_id?: string;
  app_name?: string;
  category?: string;
  app_description?: string;
  app_icp?: string;
  url?: string;
  service_type?: string;
  service_name?: string;
  favicon_mmh3?: string;
  status_code?: number;
  content_length?: number;
  page_title?: string;
  technologies?: string[];
  auth?: Record<string, unknown>[];
  method?: string;
  params?: Record<string, unknown>[];
  extra?: Record<string, unknown>;
  last_seen: string;
  task_source?: string;
  task_source_summary?: string;
  task_source_node_id?: number;
}

export interface IntentAsset {
  intent_id: number | string;
  asset_id: number;
  type: NewAssetType;
  label: string;
  source: string;
  source_summary: string;
  source_node_id?: number;
  source_task_id: number;
  inherited: boolean;
}

export interface TaskAssetMutation {
  requested: number;
  attached: number;
  existing: number;
}

export interface TaskAssetScopeMutation {
  requested: number;
  assets_linked: number;
  assets_existing: number;
  scopes_added: number;
  scopes_existing: number;
}

// ---- Asset coverage graph (per task) ----
// Một node của "đồ thị bao phủ tài sản" force-directed. key duy nhất: tài sản="a:<id>",
// doanh nghiệp="c:<id>", tên miền gốc không có dòng tài sản="r:<domain>". Node có
// in_scope=false là node ngữ cảnh màu xám chỉ dùng để nối cạnh.
export interface CoverageGraphNode {
  key: string;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
  label: string;
  tested: boolean;
  in_scope: boolean;
  asset_id?: number;
  company_id?: number;
  domain?: string;
  root_domain?: string;
  ip?: string;
  url?: string;
  port?: number;
  service_type?: string;
  app_name?: string;
  page_title?: string;
  status_code?: number;
}

export interface CoverageGraphEdge {
  src: string;
  dst: string;
}

export interface CoverageGraphData {
  nodes: CoverageGraphNode[];
  edges: CoverageGraphEdge[];
}

// Các ý đồ/sự thật/phát hiện mà một tài sản liên kết tới trong đồ thị thăm dò của tác vụ này (dùng cho ngăn kéo node của đồ thị bao phủ).
export interface CoverageAssetRef {
  id: number;
  kind: string;
  state: string;
  summary: string;
  source_task_id?: string;
  inherited?: boolean;
}
export interface CoverageAssetRefs {
  intents: CoverageAssetRef[];
  facts: CoverageAssetRef[];
  findings: CoverageAssetRef[];
}

// ---- Workspace file manager (workDir) ----
export interface WorkspaceEntry {
  name: string;
  path: string; // workspace-relative, forward slashes
  dir: boolean;
  size: number;
  mtime: number; // unix millis
}
export interface WorkspaceListing {
  path: string;
  entries: WorkspaceEntry[];
}
export interface WorkspaceFile {
  path: string;
  size: number;
  binary: boolean;
  too_large?: boolean;
  content?: string;
}

// Một dòng trong phạm vi kiểm thử của tác vụ (mẫu số của độ bao phủ + ranh giới ủy quyền).
export interface TaskScopeRow {
  id: number;
  task_id: number;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "cidr" | "icp" | "keyword";
  company_id?: number;
  company_name?: string; // backend JOIN companies để phân giải, chỉ có giá trị khi kind=company
  domain?: string;
  net?: string;
  value?: string;
  source: "auto" | "agent" | "manual";
  reason?: string;
}

export type CompanyScopeKind = "domain" | "ip" | "cidr" | "icp" | "keyword";

// Quy tắc phạm vi tài sản có cấu trúc được gửi khi thêm doanh nghiệp mới.
export interface CompanyScopeRule {
  kind: CompanyScopeKind;
  value: string;
}

// Kết quả ghi phạm vi tài sản. errors là các dòng không hợp lệ trong lần gửi này; warnings
// là các vấn đề dữ liệu hiện có không liên quan tới lần gửi này nhưng sẽ khiến kết quả gán
// thuộc không như mong đợi (ví dụ tài sản có trường ip lưu hostname).
export interface CompanyScopeMutation {
  added: number;
  skipped: number;
  invalid: number;
  errors?: string[];
  warnings?: string[];
}

// Một dòng quy tắc phạm vi tài sản của doanh nghiệp (nguồn chân lý duy nhất về quy thuộc).
export interface ScopeRow {
  id: number;
  company_id: number;
  kind: CompanyScopeKind;
  domain?: string; // có giá trị khi kind=domain
  net?: string; // có giá trị khi kind=ip|cidr
  value?: string; // khi kind=icp|keyword backend có thể trả về trực tiếp
  raw: string; // đầu vào nguyên bản của người dùng, dùng để hiển thị và điền lại
  reason?: string;
}

// Doanh nghiệp: node tài sản type=company + icon + số lượng tài sản + quy tắc phạm vi tài sản.
export interface Company {
  id: number;
  name: string;
  logo?: string; // URL icon từ xa; nếu rỗng frontend dùng chữ cái đầu của tên
  asset_count: number;
  scope?: ScopeRow[];
}

// ---- Exploration graph (per task) ----
export type ExploreKind = "task" | "begin" | "goal" | "intent" | "fact" | "finding" | "hint" | "digest";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState = "open" | "running" | "paused" | "done" | "blocked" | "exhausted" | "stopped";
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel = "spawns" | "derived_from" | "yields" | "proves" | "covers";

export interface TaskNode {
  id: string;
  type: ExploreKind;
  payload?: string;
  priority: number; // 0..10
  state: string; // GoalState | IntentState | FindingState | HintState
  origin: string;
  ts: string;
  source_task_id?: string;
  inherited?: boolean;
  delete_reason?: string; // lý do xóa khi ý đồ bị xóa giả (state='deleted')
}

// Một trang của bảng thông báo: các node phân trang theo thứ tự tạo + các cạnh liên quan
// tới trang này + node ở đầu kia của cạnh (refs, đánh chỉ mục theo id), để mỗi thông báo
// đều nói rõ "từ đâu tới, sinh ra gì" mà không cần kéo cả đồ thị về.
export interface ExplorationNodePage {
  items: TaskNode[];
  total: number;
  page: number;
  size: number;
  edges: Edge[];
  refs: Record<string, TaskNode>;
  // node id → tài sản mà node đó neo tới (hiển thị kèm khi mở rộng bảng thông báo, gồm cả node trang này và hàng xóm của chúng).
  assets: Record<string, FindingAsset[]>;
}

export interface ExplorationNodeQuery {
  page?: number;
  size?: number;
  kinds?: ExploreKind[];
  states?: string[];
  q?: string;
  order?: "asc" | "desc";
}

// Mục tiêu cho thẻ quản lý mục tiêu (backend đã tách payload thành text/vulnclass).
export interface TaskGoal {
  id: string;
  text: string;
  vulnclass?: string;
  state: string; // GoalState
  origin?: string;
  ts: string;
}

// Ràng buộc thao tác cho thẻ quản lý ràng buộc (allow=cho phép / deny=cấm).
export type ConstraintKind = "allow" | "deny";
export interface TaskConstraint {
  id: string;
  kind: ConstraintKind;
  text: string;
  origin?: string;
  ts?: string;
}

// ---- Findings ----
export type Severity = "critical" | "high" | "medium" | "low";

// Trạng thái xử lý lỗ hổng: chờ xử lý / đang xử lý / đã xác nhận / đã xử lý / đã sửa / dương tính giả / bỏ qua / trùng lặp / chấp nhận rủi ro.
export type FindingStatus =
  | "pending"
  | "in_progress"
  | "confirmed"
  | "resolved"
  | "fixed"
  | "false_positive"
  | "ignored"
  | "duplicate"
  | "risk_accepted";

// FindingAsset là một tài sản gắn với lỗ hổng (label đã được backend render sẵn).
export interface FindingAsset {
  id: string;
  type: string;
  label: string;
}

export interface Finding {
  traffic_count?: number;
  evidence_version?: number;
  report_evidence_version?: number;
  report_stale?: boolean;
  id: string;
  finding_id?: string; // id dòng của bảng findings độc lập, là handle để cập nhật trạng thái (node cũ trong tác vụ có thể thiếu)
  vulnclass: string;
  name?: string; // tên lỗ hổng; khi rỗng hiển thị quay về vulnclass
  severity: Severity;
  status: FindingStatus;
  summary: string;
  evidence: string;
  report?: string; // báo cáo chi tiết (Markdown); chỉ API chi tiết trả về, danh sách để rỗng
  intent_id?: string;
  param_id?: string;
  task_id?: string;
  task_description?: string;
  source_task_id?: string;
  inherited?: boolean;
  assets?: FindingAsset[];
  ts: string;
}

// FindingsPage là phản hồi phân trang phía server của danh sách phát hiện.
export interface FindingsPage {
  items: Finding[];
  total: number;
  page: number;
  page_size: number;
}

export interface FindingGroup {
  task_id: string | number | null;
  task_name?: string; // tên tác vụ tùy chọn; rỗng/không có = chưa đặt tên
  task_description: string;
  task_status: string;
  count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingGroupsPage {
  items: FindingGroup[];
  total: number;
  finding_total: number;
  page: number;
  page_size: number;
}

export interface FindingDeepenResponse {
  task_id: string;
  intent_id: string;
  state: IntentState;
  queued: boolean;
}

// FindingStats là tổng hợp toàn bảng của phát hiện (thẻ thống kê + dropdown loại lỗ hổng), tính phía server, không bị phân trang ảnh hưởng.
export interface FindingStats {
  total: number;
  pending: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  vulnclasses: string[];
  tasks: FindingTaskOption[];
}

// FindingTaskOption là một mục trong dropdown lọc "theo tác vụ" của trang phát hiện: tác vụ có lỗ hổng
// (mô tả rỗng nghĩa là tác vụ đã bị xóa, frontend quay về hiển thị id) cùng số lỗ hổng của nó.
export interface FindingTaskOption {
  id: string | number;
  name?: string; // tên tác vụ tùy chọn; rỗng/không có = chưa đặt tên
  description: string;
  count: number;
}

// FindingQuery là tham số phân trang/lọc/sắp xếp của danh sách phát hiện.
export interface FindingQuery {
  page: number;
  pageSize: number;
  severity?: "all" | Severity;
  status?: "all" | FindingStatus;
  vulnclass?: string;
  task?: string; // id tác vụ; "all"/rỗng = không lọc theo tác vụ
  query?: string;
  sort?: "severity" | "time";
  // key node cây tài sản; chọn một node = chọn cả cây con của nó. Rỗng = không lọc theo tài sản.
  assetScope?: string;
}

// ---- Findings by asset (chế độ xem tài sản) ----
export type FindingAssetKind = "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint" | "none";

// FindingAssetNode là một node của cây tài sản. key dạng a:<id> (tài sản), c:<id> (doanh nghiệp),
// r:<domain> (tên miền gốc không có dòng tài sản trong kho), __none__ (chưa liên kết tài sản).
export interface FindingAssetNode {
  key: string;
  parent?: string;
  kind: FindingAssetKind;
  label: string;
  asset_id?: number;
  company_id?: number;
  self: number; // số phát hiện gắn trực tiếp vào tài sản này
  total: number; // gồm cả con cháu, khử trùng theo phát hiện
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingAssetTree {
  nodes: FindingAssetNode[];
  finding_total: number;
  truncated: boolean;
  dropped_kinds?: string[];
}

// FINDING_UNASSIGNED_ASSET tương ứng với db.FindingUnassignedAsset ở backend.
export const FINDING_UNASSIGNED_ASSET = "__none__";

// ---- Activity / sessions ----
export type ActivityKind =
  | "tool_use"
  | "tool_result"
  | "text"
  | "thinking"
  | "result"
  | "user"
  | "intent" // LLM-generated exploration objective leading a worker session (UI-synthesized)
  | "round" // planner round boundary marker (engine-emitted)
  | "usage" // live cumulative token usage (per model turn); not rendered
  | "llm_switch" // automatic/manual task-level LLM switch
  | "llm_failover" // task-level provider switch / chain exhaustion audit event
  | "intercept_request"; // user-approval request from the intercept layer

// ChatAttachment là một file đã tải lên: path tương đối so với thư mục làm việc của phiên/tác vụ đó (tức CWD của agent).
export interface ChatAttachment {
  name: string;
  path: string;
  size: number;
  abs?: string; // đường dẫn tuyệt đối (trả về khi tải lên tạm scope=staging; trước khi tạo tác vụ hãy ghi nó vào mô tả)
}

export interface Activity {
  seq: number;
  intent_id?: string;
  worker: string; // session owner: planner | mainagent | work#1 ...
  ts: string;
  kind: ActivityKind;
  tool?: string;
  tool_use_id?: string;
  is_error?: boolean;
  summary: string;
  detail?: string;
  metadata?: {
    llm_transition?: LLMTransition;
  };
  source_task_id?: string;
  inherited?: boolean;
  main_seg?: number; // main-agent conversation segment (present only on worker="mainagent" rows)
  // token usage (present only on kind='result')
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface LLMAuditProfile {
  id: number;
  name: string;
  format: string;
  model: string;
}

export interface LLMTransition {
  mode: "automatic" | "manual" | "exhausted";
  reason: string;
  previous?: LLMAuditProfile;
  next?: LLMAuditProfile;
}

export interface TaskLLMResolution {
  profile_id?: number;
  name: string;
  format: string;
  model: string;
  source: "task_chain" | "agent_binding" | "global_profile" | "environment" | "global";
  available: boolean;
  reason?: string;
}

export interface TaskLLMResolutions {
  mainagent: TaskLLMResolution;
  planner: TaskLLMResolution;
  worker: TaskLLMResolution;
}

// ---- Agent triggers (P3 lập lịch, chỉ agent tùy chỉnh) ----
export interface AgentTrigger {
  id: number;
  agent_key: string;
  enabled: boolean;
  interval_sec: number; // định kỳ: mỗi N giây (0 = không định kỳ)
  on_finding: boolean; // kích hoạt khi bất kỳ tác vụ nào phát hiện finding
  on_goal_met: boolean; // kích hoạt khi bất kỳ tác vụ nào đạt mục tiêu
  on_task_timeout: boolean; // kích hoạt khi bất kỳ tác vụ nào quá thời gian
  on_tool_call: boolean; // kích hoạt khi công cụ đã chọn được gọi (thực thi xong)
  on_task_create: boolean; // kích hoạt khi bất kỳ tác vụ nào được tạo
  interval_message: string; // tin nhắn người dùng riêng cho từng điều kiện kích hoạt
  finding_message: string;
  goal_message: string;
  task_timeout_message: string;
  tool_call_message: string;
  task_create_message: string;
  tool_names: string[]; // key công cụ đã chọn cho on_tool_call (ít nhất một)
  last_fire?: string;
}

// ---- Conversations (chat page) ----
export interface ActiveFindingRetest {
  id: number;
  finding_id: string;
  conversation_id: number;
  status: "pending" | "running";
}

export interface FindingRetest {
  id: number;
  finding_id: number;
  conversation_id: number | null;
  status: "pending" | "running" | "completed" | "failed" | "stopped";
  verdict: "" | "reproduced" | "fixed" | "inconclusive";
  notes: string;
  summary: string;
  evidence: string;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Conversation {
  id: number;
  running?: boolean; // live server state, returned with the conversation list
  agent_key: string;
  title: string;
  llm_profile_id?: number;
  pinned?: boolean;
  pinned_at?: string | null;
  created_at: string;
  updated_at: string;
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
  seq: number;
  db_id?: number; // server_logs.id; present for DB-persisted lines
  ts: string;
  level: "info" | "warn" | "error";
  tag: string;
  text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker" | "system";
export type SessionStatus = "running" | "paused" | "done" | "blocked" | "exhausted" | "pending" | "stopped" | "deleted";

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket {
  date: string; // "YYYY-MM-DD"
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage {
  worker: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface SessionTokenUsage {
  session: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface BatchControlItem {
  id: string;
  ok: boolean;
  status?: string;
  queued?: boolean;
  error?: string;
}

// Kết quả theo từng tác vụ của đổi phân loại hàng loạt. Thất bại chỉ có thể do tác vụ đã bị xóa, bản thân việc ghi phân loại là nguyên tử.
export interface BatchCategoryItem {
  id: string;
  ok: boolean;
  error?: string;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Global per-profile token spend from the llm_usage ledger (GET /api/tokens/usage).
export interface ProfileUsage {
  profile_name: string;
  calls: number;
  tasks: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// One (profile, UTC day) token bucket for the dashboard's daily chart (new source).
export interface ProfileDayUsage {
  profile_name: string;
  date: string; // YYYY-MM-DD
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
}

// Response of GET /api/tokens/usage — the dashboard's "new" (llm_usage) token view.
export interface UsageStats {
  by_profile: ProfileUsage[];
  daily: ProfileDayUsage[];
}

// Per-model token usage for one task (GET /api/llm/records/by-model), from the
// always-on llm_usage metering ledger. calls = number of LLM calls on this model.
export interface ModelTokenStat {
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface Session {
  id: string;
  role: SessionRole;
  title: string;
  status: SessionStatus;
  live: boolean;
  last_activity: string;
  intent_id?: string;
  source_task_id?: string;
  inherited?: boolean;
  seg?: number; // main-agent session: which conversation segment (0 = original)
}

// ---- Security ----
export interface AuditEntry {
  ts: string;
  tool: string;
  action: "allow" | "block";
  reason?: string;
  command?: string;
}

export interface Audit {
  entries?: AuditEntry[];
  attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange {
  id: string;
  ts: string;
  host: string;
  method: string;
  url: string;
  status: number;
  content_type: string;
  resp_len: number;
}

export interface TrafficResp {
  enabled: boolean;
  proxy?: string;
  count?: number; // global total (unfiltered)
  total?: number; // rows matching the current filter (for pagination)
  page?: number;
  size?: number;
  exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
  req: string;
  resp: string;
}

// One distinct recorded host with its exchange count (target picker).
export interface TrafficHost {
  host: string;
  count: number;
}

// ---- App settings (runtime toggles) ----
export interface Settings {
  traffic_capture: boolean;
  agent_traffic_binding: boolean; // Agent tự động gắn bằng chứng lưu lượng, mặc định tắt; không ảnh hưởng gắn thủ công
  llm_record: boolean; // công tắc ghi LLM (mặc định tắt); khi tắt không ghi bất kỳ lời gọi LLM nào
  // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
  // (the values are never returned). On PUT, send the corresponding field to set/clear.
  web_search_enabled: boolean;
  web_search_backend: string; // "ddgs" | "brave-free" | "tavily" | "deepseek"
  brave_key_set: boolean;
  tavily_key_set: boolean;
  // write-only: only sent on PUT to store/clear the key.
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
  // Proxy đầu ra riêng (http/https/socks5), dùng để truy cập endpoint tìm kiếm; không liên quan tới proxy MITM ghi lưu lượng. Rỗng = kết nối trực tiếp.
  web_search_proxy?: string;
  // Proxy đầu ra toàn cục (http/https/socks5, có thể kèm user:pass), mọi lưu lượng mục tiêu đều đi qua nó. Khi bật bắt lưu lượng thì làm
  // upstream của MITM; khi tắt bắt thì inject trực tiếp vào bash/WebFetch của agent. Rỗng = kết nối trực tiếp.
  global_proxy?: string;
  python_interpreter?: string; // đường dẫn trình thông dịch python của công cụ script tùy chỉnh (rỗng = phát hiện lúc chạy)
  workers?: number; // số agent công việc đồng thời (mặc định 3); có hiệu lực với các tác vụ khởi động sau đó
  // Giới hạn đồng thời tác vụ: số tác vụ "đang chạy" tối đa cùng lúc. Tắt = không giới hạn; bật thì tác vụ mới vượt hạn mức sẽ xếp hàng, tự khởi động khi có chỗ trống.
  task_concurrency_enabled?: boolean; // mặc định false
  task_concurrency_limit?: number; // khi bật mặc định 5
  // Vòng quay LLM (chuyển đổi dự phòng). Mặc định tắt; khi bật, agent "không chỉ định mô hình" sẽ tự chuyển sang cấu hình tiếp theo
  // khi cấu hình hiện tại không khả dụng (hết số dư/key hết hạn/giới hạn tần suất/dịch vụ bất thường).
  llm_pool_enabled?: boolean; // mặc định false
  // Khi agent/tác vụ gắn cấu hình chỉ định thất bại, có fallback về chuỗi vòng quay không. Mặc định false = gắn là độc quyền.
  llm_pool_bind_fallback?: boolean;
  // Phạm vi inject ràng buộc thao tác (mặc định đều bật): ghép ràng buộc allow/deny của tác vụ vào system prompt của agent tương ứng.
  constraints_inject_planner?: boolean;
  constraints_inject_worker?: boolean;
  // Tính năng thử nghiệm: nén ngữ cảnh do mô hình noa điều khiển (mặc định tắt). Khi bật, bốn loại agent mà nền tảng tích hợp (planner/
  // worker/agent chính/hội thoại) sẽ do noa tiếp quản nén ngữ cảnh, thay thế compaction tích hợp; đọc một lần mỗi run, có hiệu lực
  // với các run khởi động sau đó.
  noa_compaction?: boolean;
  // ---- Đẩy lỗ hổng qua IM (bản thân kênh là tài nguyên độc lập, xem /api/notify/*, ở đây chỉ có ba cấu hình toàn cục) ----
  notify_enabled?: boolean; // công tắc tổng đẩy, mặc định bật; dùng để dừng khẩn một chạm trong thời gian bảo trì
  notify_public_base_url?: string; // địa chỉ truy cập bên ngoài của liên kết ngược chi tiết lỗ hổng; rỗng = tin nhắn không kèm liên kết ngược
  notify_digest_interval_min?: number; // chu kỳ chế độ tổng hợp (phút), mặc định 30
}

// ---- Đẩy lỗ hổng qua IM ----

// NotificationFilter là điều kiện lọc của kênh, mọi trường đều tùy chọn, không có nghĩa là không lọc.
// Backend không kiểm tra bất kỳ trường nào: cấu hình dị dạng được xử lý như "trúng" (thà đẩy thừa còn hơn bỏ sót).
export interface NotificationFilter {
  min_severity?: string; // "" | low | medium | high | critical
  task_ids?: number[]; // rỗng = không giới hạn; không rỗng thì yêu cầu có giao với tác vụ sở hữu lỗ hổng
  asset_ids?: number[]; // rỗng = không giới hạn; không rỗng thì yêu cầu có giao với tài sản neo của lỗ hổng
  vulnclass_include?: string[]; // rỗng = nhận tất cả; không rỗng thì yêu cầu loại lỗ hổng trúng một từ khóa bất kỳ (chuỗi con không phân biệt hoa thường)
  vulnclass_exclude?: string[]; // trúng một từ khóa bất kỳ là loại trừ (loại trừ ưu tiên hơn bao gồm)
  on_status_change?: boolean; // có nhận cả sự kiện thay đổi trạng thái xử lý lỗ hổng không
}

// NotificationChannel là một thực thể kênh. Các trường của config khác nhau tùy kind,
// và các trường thông tin xác thực khi đọc bị thay bằng giá trị che bắt đầu bằng "__masked__" —— gửi lại nguyên văn tức là "không đổi".
export interface NotificationChannel {
  id: number;
  name: string;
  kind: string;
  enabled: boolean;
  mode: "realtime" | "digest";
  config: Record<string, unknown>;
  filter: NotificationFilter;
  rate_per_min: number;
  created_at: string;
  updated_at: string;
  // secret_keys do backend đưa ra theo loại kênh, frontend dựa vào đó render ô mật khẩu và gợi ý "để trống tức không đổi",
  // không hard-code bất kỳ kiến thức kênh nào.
  secret_keys: string[];
}

// NotificationKind là metadata loại kênh do /api/notify/meta trả về.
export interface NotificationKind {
  kind: string;
  default_rate_per_min: number;
  secret_keys: string[];
}

export interface NotificationMeta {
  kinds: NotificationKind[];
  enabled: boolean;
  public_base_url: string;
  digest_interval_min: string;
  defaults: { digest_interval_min: number };
  stats: {
    channels: number;
    channels_on: number;
    pending: number;
    failed: number;
    sent_today: number;
    backlog_age_ms: number;
  };
}

// NotificationDelivery là một bản ghi giao hàng, dùng cho lịch sử giao hàng và gửi lại khi thất bại.
export interface NotificationDelivery {
  id: number;
  finding_id: string;
  event_kind: string; // finding_created | finding_status_changed
  channel_id: number;
  channel_name: string;
  channel_kind: string;
  state: "pending" | "sending" | "sent" | "failed" | "skipped";
  attempts: number;
  last_error: string;
  batch_id?: number;
  created_at: string;
  sent_at?: string;
  next_attempt_at: string;
  title: string;
  severity: string;
}

// ---- LLM config ----
export interface LLMProfile {
  id: string;
  name: string;
  format: "openai" | "anthropic" | "openai-responses";
  base_url?: string;
  proxy?: string;
  model: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  context_window_k?: number;
  // Công tắc suy nghĩ (thinking.type): ""=không gửi (mặc định) | "disabled"=tắt | "enabled"=bật
  thinking_type?: string;
  // Cường độ suy nghĩ: ""=không gửi (mặc định) | "low"/"medium"/"high"/"xhigh"/"max"
  reasoning_effort?: string;
  is_default: boolean;
  // Thứ tự vòng quay: càng lớn càng được chọn trước. Cấu hình đang kích hoạt luôn là đầu chuỗi, không liên quan tới giá trị này.
  priority?: number;
  // true = không làm mục tiêu chuyển đổi dự phòng (vẫn có thể được agent/tác vụ gắn tường minh để dùng).
  pool_exclude?: boolean;
  // true (mặc định) = streaming (SSE) | false = không streaming thật (stream:false, trả về một lần).
  streaming?: boolean;
  // Giới hạn đầu ra của một lần trả lời (token). 0 = không gửi trường này, do giá trị mặc định phía server quyết định.
  // Lưu ý phân biệt với context_window_k: cái sau là tổng dung lượng mô hình, chỉ dùng cục bộ cho ngưỡng nén.
  max_tokens?: number;
  // Giới hạn dùng tên trường request nào, chỉ có ý nghĩa khi format="openai":
  // ""=max_tokens (mặc định) | "max_completion_tokens" (mô hình reasoning của OpenAI chỉ nhận nó)
  max_tokens_field?: string;
  // Tên header phiên tùy chỉnh: khi không rỗng, mỗi request mang HTTP header này, giá trị header = session id của phiên/ý đồ hiện tại.
  // ""=không gửi. Dùng cho gateway làm cache prompt/định tuyến dính theo header session-id.
  session_header_key?: string;
  // Ghi đè retry của cấu hình này (kết nối/phản hồi rỗng/cửa sổ an toàn cùng provider). Để trống/toàn 0 = theo chính sách toàn cục.
  retry?: LLMRetryOverride;
}

// ---- Chính sách thử lại LLM ----
// Hai núm vặn của một lớp thử lại. Cả hai đều là "0 = chưa cấu hình":
//   attempts    0=dùng số lần mặc định | -1=tắt lớp thử lại này | >0=số lần thử lại
//   interval_ms 0=dùng backoff lũy tiến mặc định | >0=đổi sang khoảng cố định mili giây này
export interface LLMRetryRule {
  attempts: number;
  interval_ms: number;
}

// Ba lớp mà một cấu hình LLM có thể ghi đè (đều là retry "đi theo endpoint").
export interface LLMRetryOverride {
  connect: LLMRetryRule; // thử lại kết nối: reset kết nối/timeout/429/5xx, trước khi stream bắt đầu
  empty: LLMRetryRule; // thử lại phản hồi rỗng: hoàn thành nhưng không có nội dung nào (chỉ format openai)
  stream: LLMRetryRule; // thử lại cửa sổ an toàn cùng provider: phát lại khi đứt stream trước khi giao đầu ra
}

// Chính sách toàn cục = giá trị mặc định của ba lớp trên + hai lớp chỉ có ở toàn cục:
//   breaker cầu dao vòng quay (attempts=bao nhiêu lần thất bại tức thời liên tiếp thì ngắt, interval_ms=thời gian làm nguội cố định)
//   intent  chạy lại ý đồ (sau khi worker kết thúc bằng model_error thì chạy lại cả ý đồ đó)
export interface LLMRetryPolicy extends LLMRetryOverride {
  breaker: LLMRetryRule;
  intent: LLMRetryRule;
}

// ---- Vòng quay LLM (chuyển đổi dự phòng) ----
// Vị trí và tình trạng sức khỏe của một cấu hình trong chuỗi vòng quay. state:
//   ok       bình thường
//   degraded có thất bại liên tiếp nhưng chưa đạt ngưỡng ngắt
//   tripped  đã ngắt, bị bỏ qua trong thời gian làm nguội (cooldown_secs là số giây còn lại)
export interface LLMPoolMember {
  profile_id: string;
  name: string;
  model: string;
  format: string;
  priority: number;
  active: boolean; // có phải cấu hình đang kích hoạt không (luôn là đầu chuỗi)
  excluded: boolean; // pool_exclude: không tham gia vòng quay
  state: "ok" | "degraded" | "tripped";
  fails: number;
  trips: number;
  cooldown_secs: number;
  last_error?: string;
  last_at?: string;
}

export interface LLMPoolStatus {
  enabled: boolean;
  bind_fallback: boolean;
  chain: LLMPoolMember[];
}

// ---- Agents ----
export interface Agent {
  id: string;
  key: string; // tích hợp sẵn là goals/planner/mainagent/worker; tùy chỉnh là key do người dùng tự đặt
  name: string;
  description?: string;
  role: string;
  builtin: boolean;
  enabled: boolean;
  llm_profile_id?: number | null; // cấu hình LLM đã gắn; null/không có = theo tác vụ/phiên/toàn cục
  max_turns?: number; // 0 = không giới hạn
  run_seconds?: number; // giới hạn thời gian đồng hồ thực (giây) của một lần chạy worker; 0 = không giới hạn
  web_search?: boolean; // có bật tìm kiếm mạng không (bị công tắc toàn cục của hệ thống kiểm soát)
  interactive_shell?: boolean; // có bật shell tương tác không (họ công cụ phiên PTY bền)
  // Chiến lược xử lý sau kích hoạt P3 (chỉ có ý nghĩa với agent tùy chỉnh)
  trigger_run_mode?: "serial" | "parallel"; // xếp hàng tuần tự / mỗi lần kích hoạt chạy đồng thời một phiên riêng
  trigger_merge_mode?: "by_task" | "all" | "none"; // chỉ serial: gộp theo tác vụ / gộp tất cả / không gộp
  trigger_max_parallel?: number; // chỉ parallel: giới hạn đồng thời mỗi agent; 0=không giới hạn
  // Số lượng gắn (chỉ API danh sách trả về): MCP khả kiến / Skill khả kiến / công cụ đã gắn
  mcp_count?: number;
  skill_count?: number;
  tool_count?: number;
}

export interface PromptVar {
  name: string;
  description: string;
  example: string;
  source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
  version: number;
  ts: string;
  note: string;
  template_text: string;
}

export interface AgentDetail {
  agent: Agent;
  prompt: string;
  variables: PromptVar[];
  versions: PromptVersion[];
  visibility: { mcp: number[]; skill: string[] };
  // Các cấu hình LLM ứng viên có thể gắn (cho dropdown "mô hình mặc định"); gắn hiện tại xem agent.llm_profile_id
  llm_profiles?: { id: number; name: string; model: string; is_default: boolean }[];
  wrapup_prompt?: string; // prompt kết thúc đã lưu (rỗng = dùng mặc định tích hợp)
  wrapup_default?: string; // prompt kết thúc mặc định tích hợp (placeholder/khôi phục mặc định)
  wrapup_max_turns?: number; // số vòng kết thúc đã lưu (0 = dùng mặc định tích hợp)
  wrapup_max_turns_default?: number; // số vòng kết thúc mặc định tích hợp (cho gợi ý "0=mặc định N")
  // Từ kết thúc quá thời gian cấp tác vụ (chỉ worker/planner, chỉ hiển thị phân vùng này khi task_timeout_wrapup_supported=true)
  task_timeout_wrapup_supported?: boolean;
  task_timeout_wrapup_prompt?: string;
  task_timeout_wrapup_default?: string;
  task_timeout_wrapup_max_turns?: number;
  task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer {
  id: number;
  name: string;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  insecure?: boolean; // http: skip TLS cert verification (self-signed servers)
  tools?: string[]; // mcp_tools_cache (names only, for the count)
}

export interface MCPTool {
  name: string;
  description: string;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
  name: string; // unique key = directory name
  description?: string; // required per spec; covers what + when to use
  license?: string; // optional: SPDX identifier or free text
  compatibility?: string; // optional: environment requirements
  mcps?: string[]; // MCP server names this skill unlocks on load
  files: string[]; // files in the skill directory
  // Thống kê gọi (sổ cái skill_usage). Skill chưa từng được gọi: calls=0, thiếu last_used.
  calls: number;
  tasks: number; // số tác vụ đã tải nó (phiên chat không tính)
  usage_agents: string[]; // key agent đã tải nó
  last_used?: string;
}

// SkillCall là một lần gọi Skill() (danh sách gọi gần nhất của một skill).
export interface SkillCall {
  ts: string;
  agent_key: string;
  task_id: number; // 0 = ngữ cảnh không phải tác vụ (phiên hội thoại)
  session_id: string;
  args_len: number;
}

// MissingSkill là skill được gọi tên nhưng không tồn tại —— khoảng trống "muốn dùng mà không có".
export interface MissingSkill {
  skill: string;
  calls: number;
  agents: string[];
  last_used?: string;
}

// ---- Tools (danh mục công cụ tích hợp) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
  key: string;
  system: boolean;
  description: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  schema: Record<string, any>; // full JSON-Schema (object with properties)
  agents: string[]; // bound agent keys
  enabled: boolean;
  kind?: "builtin" | "shell" | "command" | "script" | "http"; // loại công cụ tùy chỉnh
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exec?: Record<string, any>; // đặc tả thực thi của công cụ tùy chỉnh (kind!=builtin)
  deferred?: boolean; // schema trì hoãn (SearchExtraTools/ExecuteExtraTool)
  calls?: number; // persistent runtime invocation count (older APIs may omit it)
}

// ---- Stats ----
export interface Stats {
  assets: number;
  engine_mode: EngineMode;
  llm_configured: boolean;
  roe_enabled: boolean;
  findings_confirmed: number;
  active_task?: Partial<Task>;
}

// ---- Intercept Rules ----
export type InterceptAction = "allow" | "deny" | "ask";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

export interface InterceptRule {
  id: number;
  name: string;
  enabled: boolean;
  priority: number;
  match_target: InterceptMatchTarget;
  match_type: InterceptMatchType;
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
  created_at: string;
  updated_at: string;
}

// ---- Asset Intercept Rules (chặn tài sản: danh sách đen toàn cục) ----
export type AssetInterceptKind =
  | "exact_domain"
  | "exact_ip"
  | "exact_url"
  | "fuzzy_domain"
  | "fuzzy_ip"
  | "fuzzy_url"
  | "cidr";

// action chỉ dùng cho quy tắc cấp tác vụ: block=chặn (cấm kiểm thử) allow=cho phép (danh sách trắng).
export type AssetInterceptAction = "block" | "allow";

// Mục nhập của quy tắc chặn/cho phép tài sản cấp tác vụ (dùng khi tạo tác vụ, chỉnh sửa chi tiết tác vụ).
export interface AssetInterceptRuleInput {
  action: AssetInterceptAction;
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  enabled: boolean;
}

export interface AssetInterceptRule {
  id: number;
  enabled: boolean;
  action?: AssetInterceptAction; // quy tắc toàn cục không mang trường này (luôn là chặn); quy tắc cấp tác vụ phân biệt block/allow
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

export interface InterceptPending {
  decision_source?: "rule" | "model" | "unknown" | "";
  id: number;
  rule_id?: number;
  conversation_id?: number;
  task_id?: string;
  agent_name: string;
  tool_name: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  tool_input: Record<string, any>;
  status: "pending" | "allowed" | "denied" | "timeout";
  reason: string; // message của quy tắc hoặc lý do phán đoán của mô hình (phán đoán mô hình mang tiền tố [mô hình])
  decided_at?: string;
  created_at: string;
}

// JudgeConfig: cấu hình toàn cục của phê duyệt dự phòng bằng mô hình (chỉ khi không có quy tắc chặn nào trúng thì mô hình phán đoán).
export interface JudgeConfig {
  enabled: boolean;
  profile_id: number; // 0 = theo cấu hình đang kích hoạt/mặc định
  prompt: string; // prompt phán đoán; khi GET chưa thiết lập, backend điền lại toàn văn template tích hợp
  timeout_seconds: number; // timeout lời gọi mô hình
  fail_action: "allow" | "ask" | "deny"; // hành động quay lui khi mô hình lỗi/timeout/không phân tích được
  ask_timeout_seconds: number; // timeout chờ phê duyệt sau khi mô hình phán ask chuyển sang con người
  ask_timeout_action: "allow" | "deny"; // hành động mặc định sau khi phê duyệt quá hạn
}

// JudgeUsage: tổng lượng dùng token tích lũy của phê duyệt dự phòng bằng mô hình (kênh judge) + chuỗi theo ngày trong N ngày gần đây.
export interface JudgeDayUsage {
  date: string; // YYYY-MM-DD (UTC)
  calls: number;
  input_tokens: number;
  output_tokens: number;
}
export interface JudgeUsage {
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  daily: JudgeDayUsage[];
}

export interface InterceptApprovalFilter {
  status?: InterceptPending["status"];
  decision_source?: "rule" | "model" | "unknown";
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
export interface InterceptApprovalRow extends InterceptPending {
  conv_title: string; // "" if no linked conversation
  conv_agent_key: string; // "" if no linked conversation
  rule_name: string; // "" if rule was deleted
}

// ── Đồng bộ tài sản (nguồn dữ liệu ScopeSentry) ───────────────────────────────
export interface SSProject {
  id: string; // MongoDB ObjectID — used as filter.project
  name: string;
  logo?: string;
  AssetCount?: number;
  tag?: string;
}

export interface SSTask {
  id: string;
  name: string; // used as filter.task
  status?: number;
  progress?: number;
  creatTime?: string;
  endTime?: string;
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary {
  llm_profile_id: number | null;
  created_at: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
  id: number;
  exploration_id: number;
  worker: string;
  tool: string;
  command: string; // raw tool input (JSON)
  output: string;
  is_error: boolean;
  created_at: string;
}

// Thống kê gọi của một công cụ (/commands/stats); errors là số lần thất bại trong đó.
export interface ToolStat {
  tool: string;
  total: number;
  errors: number;
}

// ---- LLM recording ----
export interface LLMRecordItem {
  id: number;
  ts: string;
  model: string;
  profile_name: string;
  session_id: string;
  task_id: string;
  worker: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read: number;
  cache_write: number;
  status: string;
  error?: string;
}

export interface LLMRecordDetail extends LLMRecordItem {
  request_body: string;
  response_body: string;
  // Văn bản HTTP nguyên gốc mà provider thực sự thu phát: request là body hoàn chỉnh mà buildBody()
  // phát đi (gồm cả schema công cụ), response là các khung SSE nguyên bản. request_body/response_body
  // ở trên là khung nhìn đã chuẩn hóa, đã loại bỏ schema công cụ và khối tool_use. Bản ghi cũ để rỗng.
  raw_request?: string;
  raw_response?: string;
}

// One distinct task with its LLM-record count (task picker on the records page).
export interface LLMTask {
  task_id: string;
  count: number;
}

// The exact JSON sent to the review model, retained for all model verdicts.
export interface InterceptReviewInput {
  version: number;
  background?: {
    // worker_summary is retained only for immutable v2/v3 snapshots.
    source: "user_message" | "worker_summary";
    text: string;
    truncated?: boolean;
  };
  // Version 1 snapshots are immutable and remain readable in historical audits.
  task?: {
    task_id: number;
    description: string;
    goal: string;
    constraints: { id: number; kind: string; text: string; origin: string; created_at: number }[];
    truncated?: boolean;
  };
  working_directory?: string;
  worker_intent?: string;
  turn_input?: string;
  background_truncated?: boolean;
  // Legacy v1/v2 snapshots only; v3 never sends execution history.
  history?: {
    tool_use_id: string;
    tool: string;
    arguments_preview: string;
    result: string;
    status: "succeeded" | "failed";
    truncated?: boolean;
  }[];
  history_truncated?: boolean;
  correlation?: "exact" | "ambiguous" | "unavailable";
  tool_name: string;
  arguments: Record<string, unknown>;
}

// Immutable review snapshot plus separately recorded execution outcome.
export interface InterceptAudit {
  model_input?: InterceptReviewInput;
  model_input_digest?: string;
  run_id?: string;
  tool_use_id?: string;
  correlation: "exact" | "ambiguous" | "unavailable";
  input_digest: string;
  user_message: string;
  user_truncated?: boolean;
  context:
    | { kind: string; tool?: string; tool_use_id?: string; text: string; is_error?: boolean; truncated?: boolean }[]
    | null;
  context_truncated?: boolean;
  captured_at: string;
  model_fallback?: boolean;
  initial_action: "allow" | "ask" | "deny";
  initial_reason: string;
  effective_action?: "allow" | "deny";
  decision_reason?: string;
  rule_name?: string;
  config_digest?: string;
  profile_id?: number;
  execution_status: "not_started" | "not_executed" | "awaiting_result" | "succeeded" | "failed" | "unknown";
  output?: string;
  output_truncated?: boolean;
  execution_ended_at?: string;
}
export interface InterceptDetail extends InterceptApprovalRow {
  audit: InterceptAudit | null;
}

export type TrafficEvidenceRole = "baseline" | "proof" | "verification" | "supporting";
export interface TrafficEvidenceRef {
  traffic_id: string;
  role?: TrafficEvidenceRole;
  note?: string;
}
export interface TrafficEvidenceSnapshot {
  id: string;
  source_traffic_id: string;
  captured_at: number;
  url: string;
  method: string;
  status: number;
  content_type: string;
  req_head?: string;
  resp_head?: string;
  req_hash: string;
  resp_hash: string;
  req_len: number;
  resp_len: number;
}
export interface FindingTrafficBinding {
  id: string;
  finding_id: string;
  snapshot_id: string;
  role: TrafficEvidenceRole;
  note: string;
  position: number;
  created_at: string;
  snapshot: TrafficEvidenceSnapshot;
}
export interface FindingTraffic {
  finding_id: string;
  version: number;
  report_version: number;
  bindings: FindingTrafficBinding[];
}
export interface EvidenceBodyPreview {
  content: string;
  offset: number;
  total: number;
  next_offset: number;
  truncated: boolean;
  binary: boolean;
}
export interface FindingTrafficDetail {
  binding: FindingTrafficBinding;
  request: EvidenceBodyPreview;
  response: EvidenceBodyPreview;
}

/** GET /api/update/check —— kết quả so sánh phiên bản hiện tại với bản chính thức mới nhất trên GitHub. */
export interface UpdateCheck {
  /** Phiên bản đang chạy; bản build dev là "dev" hoặc dạng git describe có hậu tố. */
  current: string;
  /** Dạng chạy. Dưới docker việc thay thế chỉ tác động lớp ghi được của container, dựng lại container sẽ quay về phiên bản image. */
  mode: "docker" | "binary";
  os: string;
  arch: string;
  repo: string;
  /** Có tồn tại phiên bản trước có thể khôi phục không (artex.old). */
  has_backup: boolean;
  /** Kết luận tự khởi động của tự cập nhật khi khởi động lần này (thay thế thất bại / đã khôi phục v.v.), rỗng khi không có gì xảy ra. */
  boot_notice?: string;
  rolled_back?: boolean;
  /** Khi truy vấn GitHub thất bại thì đưa ra lý do, lúc đó các trường bên dưới đều không có. */
  error?: string;
  latest?: string;
  notes?: string;
  html_url?: string;
  published_at?: string;
  /** Tên gói phát hành tương ứng nền tảng hiện tại, và Release đó có thực sự mang nó không. */
  asset?: string;
  asset_available?: boolean;
  size?: number;
  has_update?: boolean;
  /** Số phiên bản hai bên có so sánh được không; bản build dev là false, lúc đó tắt cập nhật một chạm. */
  comparable?: boolean;
  /** Giải thích khi comparable là false. */
  reason?: string;
}

/** Một mục tiến độ cập nhật do /api/update/stream đẩy. */
export interface UpdateProgress {
  phase: "idle" | "downloading" | "verifying" | "extracting" | "staged" | "failed";
  /** Chỉ có ý nghĩa ở giai đoạn tải xuống (0-100); các giai đoạn khác là -1. */
  percent: number;
  message: string;
  version?: string;
  error?: string;
}

// Original execution selected from an approval, never submitted to the reviewer.
export interface InterceptExecution {
  conversation_id: number | null;
  task_id: string | null;
  session: string;
  seq: number;
  items: Activity[];
}
