package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// compactIntents distills intents to {id, summary, state, asset_ids, parents,
// yields} so the planner sees both the direction and its LINEAGE — parents (the
// upstream nodes it derived from: facts/intents/findings) and yields (the facts/
// findings it produced) — without pulling full payloads. parentsOf/yieldsOf are
// built from the exploration edges in graph_overview.
func compactIntents(ns []*db.Node, parentsOf, yieldsOf map[int64][]int64) []map[string]any {
	out := make([]map[string]any, 0, len(ns))
	for _, n := range ns {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		m := map[string]any{"id": n.ID, "summary": p["summary"], "state": n.State}
		if n.Inherited {
			m["source_task_id"] = n.SourceTaskID
			m["inherited"] = true
		}
		// asset_ids is the structured "which assets this direction covers" signal for
		// dedup; fall back to legacy payload keys (target_ids plural, then target_id
		// single) so intents stored before the rename still surface their anchors.
		if tg, ok := p["asset_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_id"]; ok && tg != nil && tg != "" {
			m["asset_ids"] = []any{tg}
		}
		if ps := parentsOf[n.ID]; len(ps) > 0 {
			m["parents"] = ps // thượng nguồn: ý đồ này phái sinh từ những node nào (nhiều dữ kiện có thể cùng tạo ra một ý đồ)
		}
		if ys := yieldsOf[n.ID]; len(ys) > 0 {
			m["yields"] = ys // hạ nguồn: ý đồ này đã sinh ra những dữ kiện/phát hiện nào
		}
		out = append(out, m)
	}
	return out
}

// ToolSet exposes the PG-backed dual graph (asset + exploration) to an LLM agent.
// One ToolSet is created per planner/worker run; per-run signals live here.
type ToolSet struct {
	findingRecorder FindingRecorder
	as              *db.AssetStore   // asset store (optional; nil = asset tools not available)
	cs              *db.CompanyStore // company store (optional)
	ts              *db.ExplorationStore
	worker          string
	taskID          int64 // PG tasks.id; 0 when unknown (tests / orchestrator cross-task reads)
	// coverageDisabled mirrors tasks.coverage_enabled=false. Stored inverted so the
	// zero value (all existing ToolSet constructions) means ENABLED — matching the
	// DB default (true). When true: graphOverviewData drops the coverage block, the
	// auto-scope hook (insertAssets) is skipped, and add_task_scope/list_untested_assets
	// are filtered out of the agent's tool list. The scope field stays regardless.
	coverageDisabled bool
	// ownerNode is the exploration node that writes attach to: assets this run
	// touches get anchored to it as lineage/provenance (NOT visibility — the asset
	// graph is global and shared). Worker = its claimed intent; planner = begin root.
	ownerNode int64
	GoalMet   bool
	Reason    string
	writes    WriteCounts
	// killWork, if set, terminates a running work by intent id (engine callback,
	// wired by the planner). nil = the kill_work tool reports unavailable.
	killWork func(intentID int64) error
	// steerWork, if set, queues a mid-run course-correction for the work running an
	// intent id (engine callback, wired by the planner): the worker injects it before
	// its next tool call and re-plans, without being killed. nil = tool unavailable.
	steerWork func(intentID int64, msg string) error
	// enrich, if set, receives async auto-completion triggers (DNS resolve for a
	// domain, HTTP probe for a site). nil = no engine enrichment.
	enrich EnrichTrigger
	// notify, if set, wakes the task's planner after a graph change that should be
	// re-planned promptly (currently: a new hint). nil = no wake (the hint is still
	// stored and read on the next round triggered by other events). debounced.
	notify func()
	// notifyFinding, if set, wakes the task's planner when this run reports a finding,
	// carrying (intentID, summary) so the round can spell out which intent found what.
	// Wired for workers; nil elsewhere → falls back to notify (bare wake).
	notifyFinding func(intentID int64, summary string)
	// resumeTask, if set, revives the task after a graph change that should make a
	// stopped task run again (currently: set_goals adds a goal). It flips a terminal/
	// paused task back to running and (re)starts the engine loops — a plain notify()
	// can't, because the planner's terminal gate swallows wakes. Wired ONLY for the
	// main agent (human steering); nil for the goals decomposer and workers.
	resumeTask func()
	// notifyGoal, if set, wakes the planner AND records ONE "người dùng đã thêm N mục tiêu: …" trigger
	// for a whole set_goals call (batch-aware — one call, one trigger, not one per goal)
	// so the next round spells out the added goals (instead of the planner having to
	// spot new open goals in the overview). Wired ONLY for the main agent; nil for the
	// goals decomposer (round-0 has no running planner to inform) and workers → those
	// fall back to the bare notify.
	notifyGoal func(texts []string)
	// notifyHint, if set, wakes the planner AND records ONE "người dùng đã thêm N gợi ý chiến lược: …"
	// trigger for a whole add_hint call (batch-aware — one call, one trigger) so the next
	// round is told the round was fired by a new hint and spells the hint out, instead of
	// the planner having to spot it folded into the graph overview. Wired for the main
	// agent + cross-task orchestration; nil elsewhere → falls back to the bare notify.
	notifyHint func(texts []string)
}

// SetNotifyGoal wires the goal-add trigger callback (see ToolSet.notifyGoal). Set only
// by the main-agent chat, so runtime-added goals are announced to the planner by name.
func (t *ToolSet) SetNotifyGoal(fn func([]string)) { t.notifyGoal = fn }

// SetNotifyHint wires the hint-add trigger callback (see ToolSet.notifyHint). Set by
// the main-agent chat and cross-task orchestration, so a runtime-added hint fires a
// planner round announced by name instead of a bare wake.
func (t *ToolSet) SetNotifyHint(fn func([]string)) { t.notifyHint = fn }

// SetResumeTask wires the task-revive callback (see ToolSet.resumeTask). Set only by
// the main-agent chat, so runtime-added goals can pull a finished task back to running.
func (t *ToolSet) SetResumeTask(fn func()) { t.resumeTask = fn }

// SetNotify wires the planner-wake callback (see ToolSet.notify). Set by callers
// that hold the task handle (main-agent chat, cross-task orchestration).
func (t *ToolSet) SetNotify(fn func()) { t.notify = fn }

// SetNotifyFinding wires the finding-wake callback (see ToolSet.notifyFinding).
func (t *ToolSet) SetNotifyFinding(fn func(int64, string)) { t.notifyFinding = fn }

// EnrichTrigger is the enrichment engine seen from the tool layer (see package
// enrich). Kept as an interface here to avoid coupling agent → enrich.
type EnrichTrigger interface {
	ResolveDomain(id int64, host string)
	ProbeSite(id int64, url string)
}

// WriteCounts breaks down what a worker persisted this run, by node kind, so the
// engine can log an accurate "wrote back" summary instead of lumping assets and
// findings under "facts" (record_fact → Facts, insert_assets → Assets,
// report_finding → Findings; each element of a batch counts once).
type WriteCounts struct {
	Facts    int
	Assets   int
	Findings int
}

// Total is every node persisted this run, regardless of kind — the
// "explored but persisted nothing" signal (Total == 0).
func (w WriteCounts) Total() int { return w.Facts + w.Assets + w.Findings }

// String renders the per-kind breakdown for logs, e.g. "dữ kiện1 tài sản25 lỗ hổng0".
func (w WriteCounts) String() string {
	return fmt.Sprintf("dữ kiện%d tài sản%d lỗ hổng%d", w.Facts, w.Assets, w.Findings)
}

// Writes reports what this run wrote back, split by node kind (so the engine can
// tell "explored but persisted nothing" apart from a completed intent, and log an
// honest breakdown instead of calling assets/findings "facts").
func (t *ToolSet) Writes() WriteCounts { return t.writes }

func NewToolSet(ts *db.ExplorationStore, worker string) *ToolSet {
	return &ToolSet{ts: ts, worker: worker}
}

// SetTaskID sets the PG task id on this ToolSet so that report_finding can
// dual-write to the standalone findings table (which survives task deletion).
func (t *ToolSet) SetTaskID(id int64) { t.taskID = id }

// SetCoverageEnabled records whether this task has the asset-coverage feature on
// (default enabled). Passing false makes graphOverviewData omit the coverage block
// and DropCoverageTools filter the two coverage-only tools out of the agent's tool
// list. It does NOT stop scope accumulation: insertAssets' auto-scope hook runs
// either way, because task_scope is the task's range boundary (the filter basis for
// asset queries), not merely a coverage denominator.
func (t *ToolSet) SetCoverageEnabled(enabled bool) { t.coverageDisabled = !enabled }

// CoverageDisabled reports whether the coverage feature is off for this task.
func (t *ToolSet) CoverageDisabled() bool { return t.coverageDisabled }

// coverageOnlyTools are the LLM tools that only make sense when asset coverage is
// on. When the feature is off they are filtered out of the agent's tool list so
// they neither pollute the prompt nor let the model build a disabled denominator.
// add_task_scope is deliberately NOT here: task_scope is the task's range boundary
// (the filter basis for asset queries), not merely a coverage denominator, so the
// agents that own định nghĩa phạm vi keep it either way — in lockstep with insertAssets'
// auto-scope hook, which also runs regardless of the switch.
var coverageOnlyTools = map[string]bool{"list_untested_assets": true}

// DropCoverageTools returns tools with the coverage-only ones removed when this
// task has the feature disabled; otherwise it returns tools unchanged.
func (t *ToolSet) DropCoverageTools(tools []actool.CoreTool) []actool.CoreTool {
	if !t.coverageDisabled {
		return tools
	}
	out := tools[:0:0]
	for _, tool := range tools {
		if coverageOnlyTools[tool.Name()] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// Cross-task reuse: exported accessors returning the per-task tool logic bound to
// THIS ToolSet's store. Host-side orchestration tools build a ToolSet for an
// arbitrary task, then Call these — so cross-task reads/hint reuse the exact
// same logic as the in-task tools. (readTool ignores ToolContext, so Call(…,nil)
// is safe; add_hint is a writeTool but also doesn't deref the context here.)
func (t *ToolSet) GraphOverviewTool() actool.CoreTool      { return t.graphOverview() }
func (t *ToolSet) ListFindingsTool() actool.CoreTool       { return t.listFindings() }
func (t *ToolSet) GetWorkerTraceTool() actool.CoreTool     { return t.getWorkerTrace() }
func (t *ToolSet) ListWorkerTracesTool() actool.CoreTool   { return t.listWorkerTraces() }
func (t *ToolSet) SearchWorkerTracesTool() actool.CoreTool { return t.searchAllWorkerTraces() }
func (t *ToolSet) NodeDetailTool() actool.CoreTool         { return t.nodeDetail() }
func (t *ToolSet) AddHintTool() actool.CoreTool            { return t.addHint() }

// SetEnrich wires the async enrichment engine (DNS/HTTP auto-completion).
func (t *ToolSet) SetEnrich(e EnrichTrigger) { t.enrich = e }

// SetOwnerNode sets the exploration node that writes anchor to (worker: its
// intent node; planner/main: the begin root). Assets created/referenced while
// ownerNode is set are anchored to it as lineage (not visibility).
func (t *ToolSet) SetOwnerNode(id int64) { t.ownerNode = id }

// anchorOwner records a lineage edge from this run's owner node to an asset
// (no-op if unset). Provenance only — the asset graph is global and shared, so
// this no longer affects which assets a task can read.
func (t *ToolSet) anchorOwner(assetID int64) {
	if t.ts != nil && t.ownerNode > 0 && assetID > 0 {
		_ = t.ts.Anchor(t.ownerNode, assetID)
	}
}

// pid parses an id that may arrive as a JSON number or string ("" / 0 → 0).
func pid(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	return 0
}

// pidList parses a list of ids (number|string), dropping zeros/invalids.
func pidList(raw []json.RawMessage) []int64 {
	var out []int64
	for _, r := range raw {
		if v := pid(r); v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func intp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func idp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func readTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:    func(json.RawMessage) bool { return true },
		Concurrent:  func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func writeTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// readExpTool / writeExpTool build a domain tool whose handler dereferences the
// task-bound ExplorationStore. Two ToolSets carry a nil store: the catalog's
// seed-only shell (never called) and the server-level one behind buildDomainReg,
// which the tools table can bind to ANY agent — including ones that never run
// inside a task (auto/pentest/reporter/agent tùy chỉnh/câu hỏi nhánh). Refusing there
// keeps a mis-bound tool a bad tool call; without the guard it was a nil deref,
// and tool handlers run on the harness's own goroutine, so the panic is out of
// reach of every recover() in the server and kills the whole process.
func (t *ToolSet) readExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return readTool(name, desc, schema, t.needExploration(name, run))
}

func (t *ToolSet) writeExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return writeTool(name, desc, schema, t.needExploration(name, run))
}

// needExploration wraps a handler so it only runs with an exploration store.
// Tools that degrade more usefully than "unavailable" (report_finding points at
// add_task_hint, set_goals/set_constraints at the task itself) keep their own
// bespoke guard instead.
func (t *ToolSet) needExploration(name string, run func(context.Context, json.RawMessage) (actool.Result, error)) func(context.Context, json.RawMessage) (actool.Result, error) {
	return func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		if t.ts == nil {
			return actool.Errorf(name + " cần ngữ cảnh tác vụ (đồ thị thám thính): agent hiện tại không chạy trong một tác vụ nào, không lấy được đồ thị thám thính của tác vụ, công cụ này không khả dụng. Hãy dùng nó trong tác vụ, hoặc chuyển sang công cụ đọc liên tác vụ có task_id (get_task_node_detail / list_task_findings / get_task_graph ...)."), nil
		}
		return run(ctx, in)
	}
}

func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// --- read tools (planner + worker) ---

func (t *ToolSet) graphOverview() actool.CoreTool {
	return t.readExpTool("graph_overview",
		"(đồ thị chuỗi thám thính) bản tóm tắt chưng cất tình hình thám thính: đếm tài sản, site không có interface, frontier, phát hiện, hints (gợi ý chiến lược của người dùng/agent chính, phải đưa vào khi sinh ý đồ). Khi lập kế hoạch hãy gọi nó trước.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			return jsonResult(t.graphOverviewData())
		})
}

// graphOverviewData computes the distilled situational snapshot shared by the
// graph_overview tool and the planner's wake-up prompt (which pre-injects it so
// the model needn't spend a turn calling the tool — every plan round starts with
// an empty context and always needs this first).
func (t *ToolSet) graphOverviewData() map[string]any {
	out := map[string]any{}
	// goals summary folded in so the planner needn't call list_goals each round.
	goals, _ := t.ts.ListByKind(db.KindGoal, 100)
	gsum := make([]map[string]any, 0, len(goals))
	for _, g := range goals {
		var p map[string]any
		_ = json.Unmarshal(g.Payload, &p)
		gsum = append(gsum, map[string]any{"id": g.ID, "state": g.State, "text": p["text"]})
	}
	out["goals"] = gsum
	// hints: gợi ý chiến lược mà người dùng/agent chính gắn lên đồ thị qua add_hint;
	// folded in so the planner reads them every round when generating intents (nếu không
	// thì chỉ ghi mà không đọc).
	hints, _ := t.ts.ListByKind(db.KindHint, 50)
	hsum := make([]map[string]any, 0, len(hints))
	for _, h := range hints {
		var p map[string]any
		_ = json.Unmarshal(h.Payload, &p)
		hint := map[string]any{"id": h.ID, "state": h.State, "text": p["text"]}
		if findingTrafficBindingEnabled() && p["traffic_refs"] != nil {
			hint["traffic_refs"] = p["traffic_refs"]
		}
		hsum = append(hsum, hint)
	}
	out["hints"] = hsum
	// lineage from the exploration edges: an intent's parents (what it
	// derived_from — possibly several facts combined) and its yields (the
	// facts/findings it produced). factFrom maps a fact → the intent that
	// produced it. This is the relationship layer the flat lists lacked.
	edges, _ := t.ts.Edges(5000)
	parentsOf := map[int64][]int64{}
	yieldsOf := map[int64][]int64{}
	factFrom := map[int64]int64{}
	for _, e := range edges {
		switch e.Rel {
		case db.RelDerivedFrom, db.RelSpawns: // upstream: derived_from (fact/finding/intent→intent) or spawns (origin fact→goal, legacy begin→intent)
			parentsOf[e.To] = append(parentsOf[e.To], e.From)
		case db.RelYields: // intent --yields--> fact/finding
			yieldsOf[e.From] = append(yieldsOf[e.From], e.To)
			factFrom[e.To] = e.From
		}
	}
	// cold-digest §6: members folded into an active digest are shown via cold_digests
	// (below), not the flat recent_* lists. `covered` maps member id → its digest id.
	// §6 render-time revival check: a covered member that has become hot again (a new
	// intent derived from it) must reappear this round — so `hidden` folds a member out
	// only when it is covered AND still cold.
	covered, _ := t.ts.CoveredMembers()
	// Render-time hot set (ancestor of a live intent / fact under a live intent).
	// §6 revival check: a covered member that revived (now hot) must NOT stay folded
	// — hidden() only folds a member out when it is covered AND still cold. Computed
	// every round (cheap for real graph sizes); nil map degrades safely.
	var hotAtRender map[int64]bool
	if cg, _, err := loadColdGraph(t.ts); err == nil {
		hotAtRender = cg.hotSet()
	}
	hidden := func(id int64) bool { _, c := covered[id]; return c && !hotAtRender[id] }
	const openIntentsCap = 30
	fr, _ := t.ts.Frontier(openIntentsCap) // priority DESC, id ASC — N mục ưu tiên cao nhất; tổng thật xem frontier_open
	out["open_intents"] = compactIntents(fr, parentsOf, yieldsOf)
	all, _ := t.ts.ListByKind(db.KindIntent, 300)
	var running, recentDone []*db.Node
	for _, n := range all {
		switch n.State {
		case "running":
			running = append(running, n)
		case "done", "blocked", "exhausted":
			if hidden(n.ID) {
				continue // in a cold_digest and still cold — shown via cold_digests (§6.2)
			}
			recentDone = append(recentDone, n) // mới nhất trước (all sắp theo id giảm dần); mục đã gấp đã loại, khi xuất cắt lấy N mục mới nhất
		}
	}
	out["running_intents"] = compactIntents(running, parentsOf, yieldsOf)
	// done_intents_total: tổng số ý đồ đã kết thúc (done/blocked/exhausted), đặt tên song
	// song với recent_done_intents — cái sau chỉ là khung nhìn cắt cửa sổ mới nhất của nó.
	// Hai key đặt cạnh nhau tự mô tả: "thấy N/tổng", để planner khi khử trùng đừng coi
	// "không hiển thị" là "chưa từng phái", không cần giải thích thêm trong prompt.
	if dt, err := t.ts.CountFinishedIntents(); err == nil {
		out["done_intents_total"] = dt
	}
	// frontier_open: tổng thật của ý đồ đang mở (open_intents chỉ là khung nhìn cắt N mục ưu tiên cao nhất trong đó).
	if fo, err := t.ts.CountOpenIntents(); err == nil {
		out["frontier_open"] = fo
	} else {
		out["frontier_open"] = len(fr)
	}
	// findings (confirmed vulns) and facts (worker exploration results) are
	// now distinct node kinds. recent_facts surfaces fact summaries (esp.
	// negative results) so the planner sees them in one call; full content
	// via node_detail(id).
	vulnNodes, _ := t.ts.ListByKind(db.KindFinding, 1000)
	factNodes, _ := t.ts.ListByKind(db.KindFact, 1000) // newest first
	out["findings_total"] = len(vulnNodes)             // tổng số lỗ hổng đã xác nhận (phán mục tiêu nhìn nó); chi tiết xem finding_list (cửa sổ mới nhất)
	out["facts"] = len(factNodes)                      // số dữ kiện/kết luận thám thính (gồm kết luận phủ định)
	// findings là sản phẩm giá trị cao nhất của tác vụ → tổng quan mang theo cửa sổ mới nhất
	// (≤10 mục, vulnNodes đã theo id giảm dần tức mới nhất trước), để planner mỗi lượt phán
	// mục tiêu thấy ngay lỗ hổng xác nhận gần đây; toàn bộ/cũ hơn dùng list_findings lấy.
	// Mỗi mục chỉ giữ {id, summary, from_intent?}: from_intent là ý đồ đã sinh ra lỗ hổng này.
	// evidence/assets/vulnclass/severity/state... vẫn lấy qua list_findings / node_detail(id).
	const findingListCap = 10
	findingList := make([]map[string]any, 0, findingListCap)
	for _, n := range vulnNodes {
		if len(findingList) >= findingListCap {
			break
		}
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		m := map[string]any{"id": n.ID, "summary": fp["summary"]}
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // lỗ hổng này do ý đồ nào sinh ra
		}
		findingList = append(findingList, m)
	}
	out["finding_list"] = findingList
	// recent_facts: cửa sổ mới nhất trong các dữ kiện chưa gấp (≤N, factNodes theo id giảm
	// dần tức mới nhất trước). Mục đã gấp vào digest và vẫn lạnh (hidden) đi qua
	// cold_digests, không lặp lại ở đây. Mỗi mục {id, summary, from_intent?, confidence?};
	// chi tiết evidence... dùng node_detail(id). Cũ hơn dùng list_facts lật trang.
	const recentFactsCap = 20
	recentFacts := make([]map[string]any, 0, recentFactsCap)
	for _, n := range factNodes {
		if len(recentFacts) >= recentFactsCap {
			break
		}
		if hidden(n.ID) {
			continue // đã gấp vào digest và vẫn lạnh — xem cold_digests
		}
		m := compactNode(n)
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // dữ kiện này do ý đồ nào sinh ra
		}
		// đưa confidence vào tổng quan: để planner thấy ngay kết luận nào chỉ là inferred
		// (đặc biệt kết luận phủ định đừng coi là án đóng đinh); evidence dài, để cho
		// node_detail(id).
		var fp map[string]any
		if json.Unmarshal(n.Payload, &fp) == nil {
			if c, ok := fp["confidence"].(string); ok && c != "" {
				m["confidence"] = c
			}
		}
		recentFacts = append(recentFacts, m)
	}
	out["recent_facts"] = recentFacts
	// recent_done_intents: cửa sổ mới nhất trong các ý đồ đã kết thúc chưa gấp (≤N,
	// recentDone đã theo id giảm dần). Cũ hơn xem đếm done_intents_total + node_detail(id).
	const recentDoneCap = 12
	if len(recentDone) > recentDoneCap {
		recentDone = recentDone[:recentDoneCap]
	}
	out["recent_done_intents"] = compactIntents(recentDone, parentsOf, yieldsOf)
	// cold-digest §6.1: body của digest gấp vùng lạnh, lấy N mục theo thời gian thành viên
	// mới nhất giảm dần; digest cũ hơn bị cắt chỉ cho id trần (vẫn expand_digest mở được),
	// tránh lối ra duy nhất của vùng lạnh bị kéo dài vô hạn.
	const coldDigestsCap = 15
	if cds, more := coldDigestsRecent(t.ts, coldDigestsCap); len(cds) > 0 {
		out["cold_digests"] = cds // [{id, body, member_count}] — đọc body trực tiếp (§6.1)
		if len(more) > 0 {
			out["cold_digests_more"] = more // id của các digest cũ hơn bị cắt; dùng expand_digest(id) mở rộng
		}
	}
	// the original task (root) so the planner always has it, not just the
	// decomposed goals.
	if description, goal, err := t.ts.Root(); err == nil {
		out["task"] = map[string]any{"description": description, "goal": goal}
	}
	// Direct source tasks are a live, read-only blackboard view. Keep their
	// summaries in a separate field so their intents never enter this task's
	// frontier or get mistaken for locally claimable work.
	out["related_tasks"] = t.relatedTaskOverviews()
	// coverage: tham chiếu độ bao phủ kiểm thử tài sản sơ bộ — tỷ lệ tài sản trong phạm vi
	// (task_scope) đã bị fact chạm tới + by_type (tổng/đã kiểm theo loại). Muốn xem tài sản
	// chưa kiểm cụ thể thì agent tự gọi list_untested_assets theo nhu cầu. Chỉ có trong ngữ
	// cảnh tác vụ.
	// Khi tính năng độ bao phủ tài sản tắt (coverageDisabled): chỉ giữ host_count (thông tin
	// cảm nhận số host mục tiêu), bỏ denominator/tested/pct/by_type/note..., tránh gây nhiễu
	// ngữ cảnh, cũng không dụ dỗ các add_task_scope/list_untested_assets đã bị ẩn.
	if t.as != nil && t.ts != nil && t.taskID > 0 {
		{
			m := map[string]any{}
			if !t.coverageDisabled {
				if cov, err := t.as.TaskCoverageWithSources(t.taskID); err == nil {
					m["denominator"] = cov.Denominator
					m["tested"] = cov.Tested
					m["by_type"] = cov.ByType
					m["note"] = "độ bao phủ kiểm thử tài sản coverage (bao gồm interface và mọi tài sản liên quan), ước lượng sơ bộ, chỉ để tham khảo: gồm scope của tác vụ hiện tại và tác vụ liên quan trực tiếp, neo dữ kiện; scope liên quan chỉ đọc. Tài sản dạng container/kiểu liệt kê lớn sẽ làm nó thấp đi, đừng dựa vào đó mà cho rằng đã kiểm xong; có thể dùng add_task_scope bổ sung phạm vi tác vụ này, list_untested_assets xem tài sản chưa kiểm [thường không cần gọi list_untested_assets, cứ đẩy tác vụ tiến tới là được];"
					if cov.Denominator == 0 {
						m["pct"] = nil
						m["status"] = "phạm vi chưa neo"
					} else {
						m["pct"] = cov.Pct
					}
				}
			}
			if hosts, err := t.as.HostsByTaskWithSources(t.taskID); err == nil {
				// chỉ cho tổng số host, không còn trải phẳng danh sách host vào graph_overview
				// (trong tác vụ phạm vi lớn đó là khối chuỗi lớn lặp lại mỗi lượt, giá trị cho
				// quyết định lập kế hoạch hạn chế); host cụ thể tra list_assets khi cần.
				m["host_count"] = len(hosts)
			}
			if len(m) > 0 {
				out["coverage"] = m
			}
		}
	}
	return out
}

func inheritedMap(m map[string]any, sourceTaskID int64) map[string]any {
	m["source_task_id"] = sourceTaskID
	m["inherited"] = true
	return m
}

const (
	relatedOverviewTotalTextRunes      = 48_000
	relatedOverviewMaxTextPerSource    = 8_000
	relatedOverviewMaxGoalsPerSource   = 8
	relatedOverviewMaxHintsPerSource   = 6
	relatedOverviewMaxFactsPerSource   = 12
	relatedOverviewMaxFindingsPerTask  = 6
	relatedOverviewMaxIntentsPerTask   = 8
	relatedOverviewMaxScopePerSource   = 12
	relatedOverviewMaxDigestsPerSource = 6
)

// overviewTextBudget bounds inherited prompt text while preserving a fair slice
// for every direct source. Full evidence remains available through the on-demand
// read tools, so truncation here does not discard persisted blackboard data.
type overviewTextBudget struct {
	remaining int
	truncated bool
}

func relatedOverviewBudgetForSources(sourceCount int) int {
	if sourceCount <= 0 {
		return 0
	}
	if sourceCount > db.MaxTaskSourceCount {
		sourceCount = db.MaxTaskSourceCount
	}
	perSource := relatedOverviewTotalTextRunes / sourceCount
	if perSource > relatedOverviewMaxTextPerSource {
		perSource = relatedOverviewMaxTextPerSource
	}
	return perSource
}

func (b *overviewTextBudget) take(value any, fieldLimit int) string {
	var text string
	switch value := value.(type) {
	case string:
		text = strings.TrimSpace(value)
	case nil:
		return ""
	default:
		text = strings.TrimSpace(fmt.Sprint(value))
	}
	if text == "" {
		return ""
	}
	if b.remaining <= 0 || fieldLimit <= 0 {
		b.truncated = true
		return ""
	}
	runes := []rune(text)
	limit := fieldLimit
	if limit > b.remaining {
		limit = b.remaining
	}
	if len(runes) > limit {
		b.truncated = true
		if limit == 1 {
			text = "…"
		} else {
			text = string(runes[:limit-1]) + "…"
		}
		runes = []rune(text)
	}
	b.remaining -= len(runes)
	return text
}

func recentTerminalIntents(store *db.ExplorationStore, limit int) []*db.Node {
	if limit <= 0 {
		return []*db.Node{}
	}
	const batch = 300
	cursor := int64(0)
	out := make([]*db.Node, 0, limit)
	for len(out) < limit {
		page, more, err := store.ListByKindPage(db.KindIntent, cursor, batch)
		if err != nil || len(page) == 0 {
			break
		}
		for _, intent := range page {
			switch intent.State {
			case "done", "blocked", "exhausted", "stopped":
				out = append(out, intent)
			}
			if len(out) >= limit {
				break
			}
		}
		if !more {
			break
		}
		cursor = page[len(page)-1].ID
	}
	return out
}

// relatedTaskOverviews distills persistent blackboard state from direct source
// tasks. It intentionally reads each source's local store methods, never its own
// related sources, so inheritance is one level only.
func (t *ToolSet) relatedTaskOverviews() []map[string]any {
	sources, err := t.ts.DirectSourceStores()
	if err != nil {
		return []map[string]any{}
	}
	if len(sources) > db.MaxTaskSourceCount {
		sources = sources[:db.MaxTaskSourceCount]
	}
	perSourceTextBudget := relatedOverviewBudgetForSources(len(sources))
	out := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		ts := source.Store
		// §2 cross-task: render the source task's OWN folded view — fold out the
		// members it has already folded, and surface its cold_digests read-only.
		hidden := hiddenMembersFor(ts)
		budget := overviewTextBudget{remaining: perSourceTextBudget}
		item := map[string]any{
			"source_task_id": source.Task.TaskID,
			"inherited":      true,
			"task": map[string]any{
				"description": budget.take(source.Task.Description, 800),
				"goal":        budget.take(source.Task.Goal, 800),
				"status":      source.Task.Status,
			},
		}
		stats, statsErr := ts.Stats()

		edges, _ := ts.Edges(5000)
		parentsOf := map[int64][]int64{}
		yieldsOf := map[int64][]int64{}
		factFrom := map[int64]int64{}
		for _, edge := range edges {
			switch edge.Rel {
			case db.RelDerivedFrom, db.RelSpawns:
				parentsOf[edge.To] = append(parentsOf[edge.To], edge.From)
			case db.RelYields:
				yieldsOf[edge.From] = append(yieldsOf[edge.From], edge.To)
				factFrom[edge.To] = edge.From
			}
		}

		goals, _ := ts.ListByKind(db.KindGoal, relatedOverviewMaxGoalsPerSource)
		goalSummary := make([]map[string]any, 0, len(goals))
		for _, goal := range goals {
			var payload map[string]any
			_ = json.Unmarshal(goal.Payload, &payload)
			goalSummary = append(goalSummary, inheritedMap(map[string]any{
				"id": goal.ID, "state": goal.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["goals"] = goalSummary

		hints, _ := ts.ListByKind(db.KindHint, relatedOverviewMaxHintsPerSource)
		hintSummary := make([]map[string]any, 0, len(hints))
		for _, hint := range hints {
			var payload map[string]any
			_ = json.Unmarshal(hint.Payload, &payload)
			hintSummary = append(hintSummary, inheritedMap(map[string]any{
				"id": hint.ID, "state": hint.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["hints"] = hintSummary

		facts, _ := ts.ListByKind(db.KindFact, relatedOverviewMaxFactsPerSource)
		findings, _ := ts.ListByKind(db.KindFinding, relatedOverviewMaxFindingsPerTask)
		intentNodes, _ := ts.ListByKind(db.KindIntent, 300)
		terminalIntent := make(map[int64]bool, len(intentNodes))
		for _, intent := range intentNodes {
			terminalIntent[intent.ID] = inheritedIntentSummaryState(intent.State)
		}
		item["facts"] = len(facts)
		item["findings"] = len(findings)
		if statsErr == nil {
			item["facts"] = stats[db.KindFact]
			item["findings"] = stats[db.KindFinding]
			if stats[db.KindGoal] > len(goals) || stats[db.KindHint] > len(hints) ||
				stats[db.KindFact] > len(facts) || stats[db.KindFinding] > len(findings) {
				budget.truncated = true
			}
		}
		recentFindings := make([]map[string]any, 0, len(findings))
		for _, finding := range findings {
			entry := inheritedMap(compactFinding(finding), source.Task.TaskID)
			entry["summary"] = budget.take(entry["summary"], 400)
			recentFindings = append(recentFindings, entry)
		}
		item["recent_findings"] = recentFindings
		recentFacts := make([]map[string]any, 0, len(facts))
		for _, fact := range facts {
			if hidden(fact.ID) {
				continue // folded into this source's cold_digests — shown there (§2/§6.2)
			}
			m := inheritedMap(compactNode(fact), source.Task.TaskID)
			m["summary"] = budget.take(m["summary"], 400)
			if from := factFrom[fact.ID]; from > 0 && terminalIntent[from] {
				m["from_intent"] = from
			}
			var payload map[string]any
			if json.Unmarshal(fact.Payload, &payload) == nil {
				if confidence, ok := payload["confidence"].(string); ok && confidence != "" {
					m["confidence"] = confidence
				}
			}
			recentFacts = append(recentFacts, m)
		}
		item["recent_facts"] = recentFacts

		recentDoneRaw := recentTerminalIntents(ts, relatedOverviewMaxIntentsPerTask)
		recentDone := recentDoneRaw[:0] // in-place filter: drop this source's folded intents (§2)
		for _, intent := range recentDoneRaw {
			if hidden(intent.ID) {
				continue
			}
			recentDone = append(recentDone, intent)
		}
		for _, intent := range recentDone {
			intent.Inherited = true
			intent.SourceTaskID = source.Task.TaskID
		}
		intentResults := compactIntents(recentDone, parentsOf, yieldsOf)
		for i, intent := range recentDone {
			intentResults[i]["summary"] = budget.take(intentResults[i]["summary"], 400)
			acts, _, err := ts.ActivityPageForTerminalIntent(intent.ID, 0, 20)
			if err != nil {
				continue
			}
			var resultSummary, textFallback string
			for _, activity := range acts {
				switch activity.Kind {
				case "result":
					resultSummary = activity.Summary
				case "text":
					textFallback = activity.Summary
				}
			}
			if resultSummary == "" {
				resultSummary = textFallback
			}
			if resultSummary != "" {
				intentResults[i]["result_summary"] = budget.take(resultSummary, 800)
			}
		}
		item["recent_intent_results"] = intentResults
		// §2 cross-task: the source task's folded cold region, read-only, newest-member
		// first & capped like the current task's. Members (and overflow digests) are
		// resolvable via expand_digest(id)/node_detail(id), which search source tasks.
		if cds, more := coldDigestsRecent(ts, relatedOverviewMaxDigestsPerSource); len(cds) > 0 {
			for _, cd := range cds {
				cd["inherited"] = true
				cd["source_task_id"] = source.Task.TaskID
			}
			item["cold_digests"] = cds
			if len(more) > 0 {
				item["cold_digests_more"] = more // id của các digest cũ hơn bị cắt; expand_digest(id) mở rộng
			}
		}
		if statsErr == nil {
			item["node_stats"] = stats
		}

		if t.as != nil {
			if scopeRows, err := t.as.ListTaskScope(source.Task.TaskID); err == nil && len(scopeRows) > 0 {
				scopeCount := len(scopeRows)
				if len(scopeRows) > relatedOverviewMaxScopePerSource {
					scopeRows = scopeRows[:relatedOverviewMaxScopePerSource]
					budget.truncated = true
				}
				scope := make([]map[string]any, 0, len(scopeRows))
				for _, row := range scopeRows {
					entry := map[string]any{"kind": row.Kind, "source": budget.take(row.Source, 300)}
					switch {
					case row.Domain != "":
						entry["value"] = budget.take(row.Domain, 400)
					case row.Net != "":
						entry["value"] = budget.take(row.Net, 400)
					case row.Value != "":
						entry["value"] = budget.take(row.Value, 400)
					case row.CompanyID != nil:
						entry["company_id"] = *row.CompanyID
					}
					scope = append(scope, entry)
				}
				item["asset_scope"] = scope
				item["asset_scope_count"] = scopeCount
			}
			if coverage, err := t.as.TaskCoverage(source.Task.TaskID, source.Task.ExplorationID); err == nil {
				item["asset_coverage"] = map[string]any{
					"denominator": coverage.Denominator,
					"tested":      coverage.Tested,
					"pct":         coverage.Pct,
					"by_type":     coverage.ByType,
				}
			}
		}
		if budget.truncated {
			item["summary_truncated"] = true
		}
		out = append(out, item)
	}
	return out
}

func inheritedIntentSummaryState(state string) bool {
	switch state {
	case "done", "blocked", "exhausted", "stopped":
		return true
	default:
		return false
	}
}

// compactNode distills any exploration node to id + summary + state, dropping the
// big detail/evidence (fetch that on demand via node_detail).
func compactNode(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	return m
}

// compactFinding is compactNode plus the vuln-specific vulnclass/severity.
func compactFinding(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	if vc, ok := p["vulnclass"]; ok && vc != nil && vc != "" {
		m["vulnclass"] = vc
	}
	if sv, ok := p["severity"]; ok && sv != nil && sv != "" {
		m["severity"] = sv
	}
	return m
}

func (t *ToolSet) listFindings() actool.CoreTool {
	return t.readExpTool("list_findings", "Liệt kê [lỗ hổng đã xác nhận] của tác vụ này và các tác vụ liên quan trực tiếp (gọn: id+task_id+intent_id+vulnclass+severity+tóm tắt+trạng thái). Mục của tác vụ liên quan kèm source_task_id/inherited=true và chỉ đọc. Ở đây chỉ có lỗ hổng; dữ kiện thám thính thường dùng list_facts, chi tiết dùng node_detail(id).",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			f, _ := t.ts.ListByKindWithSources(db.KindFinding, 500)
			if err := t.ts.PopulateFindingTrafficIDs(f); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			intentOf, _ := t.ts.FindingIntentsWithSources() // finding id -> id intent đã sinh ra nó
			taskID := t.taskID
			if taskID <= 0 {
				taskID, _ = t.ts.TaskID()
			}
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				m := compactFinding(n)
				if n.FindingID > 0 {
					m["finding_id"], m["finding_node_id"], m["traffic_count"] = n.FindingID, n.ID, n.TrafficCount
				}
				if n.Inherited {
					m["task_id"] = n.SourceTaskID
				} else {
					m["task_id"] = taskID
				}
				if iid, ok := intentOf[n.ID]; ok {
					m["intent_id"] = iid
				}
				out = append(out, m)
			}
			return jsonResult(out)
		})
}

// factsPageSize is the default page size for list_facts. Facts pile up on long
// tasks; returning all of them at once (the old behaviour) could blow up the
// context, so default to the newest page and let the agent page/filter for more.
const factsPageSize = 20

func (t *ToolSet) listFacts() actool.CoreTool {
	return t.readExpTool("list_facts", "Liệt kê phân trang [dữ kiện/kết luận thám thính] của tác vụ này và các tác vụ liên quan trực tiếp, mới nhất trước (gọn: id+tóm tắt+trạng thái, tóm tắt quá dài sẽ bị cắt, toàn văn dùng node_detail(id)). Tham số đều tùy chọn: limit (mặc định 20, tối đa 100), before (con trỏ, truyền next_before của trang trước để lấy trang cũ hơn; bỏ qua/0=trang mới nhất), q (lọc theo từ khóa tóm tắt). Trả về {facts, total, has_more, next_before}: total là tổng sau lọc, khi has_more=true dùng next_before lật trang tiếp. Mục của tác vụ liên quan kèm source_task_id/inherited=true và chỉ đọc. Lỗ hổng xem list_findings.",
		obj(map[string]any{
			"limit":  intp("số mục trả về, mặc định 20, tối đa 100"),
			"before": intp("con trỏ phân trang: chỉ trả về dữ kiện cũ hơn có id nhỏ hơn giá trị này; bỏ qua hoặc 0 = trang mới nhất"),
			"q":      str("lọc theo từ khóa trong tóm tắt dữ kiện (không phân biệt hoa thường); bỏ qua = không lọc"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Limit  int    `json:"limit"`
				Before int64  `json:"before"`
				Q      string `json:"q"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = factsPageSize
			}
			if limit > 100 {
				limit = 100
			}
			f, hasMore, total, err := t.ts.ListByKindPageWithSources(db.KindFact, a.Before, limit, strings.TrimSpace(a.Q))
			if err != nil {
				return actool.Result{}, err
			}
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				out = append(out, compactFact(n))
			}
			res := map[string]any{"facts": out, "total": total, "has_more": hasMore}
			if hasMore && len(f) > 0 {
				res["next_before"] = f[len(f)-1].ID // truyền lại nó để lấy trang tiếp (cũ hơn)
			}
			return jsonResult(res)
		})
}

// factSummaryMax caps a fact summary in list_facts output. Facts carry one-line
// conclusions, but nothing enforces brevity; a runaway summary must not bloat a
// whole page. Full text stays available via node_detail(id).
const factSummaryMax = 160

// compactFact is compactNode with the summary rune-capped for list_facts, so a
// page of facts stays bounded regardless of how long any single summary grew.
func compactFact(n *db.Node) map[string]any {
	m := compactNode(n)
	if s, ok := m["summary"].(string); ok && len([]rune(s)) > factSummaryMax {
		m["summary"] = string([]rune(s)[:factSummaryMax]) + "…"
		m["summary_truncated"] = true
	}
	return m
}

func (t *ToolSet) nodeDetail() actool.CoreTool {
	return t.readExpTool("node_detail", "Lấy theo id nội dung đầy đủ của một [node đồ thị thám thính] thuộc tác vụ này hoặc tác vụ liên quan trực tiếp. Node kế thừa kèm source_task_id/inherited=true và chỉ đọc. Chỉ nhận id node thám thính do list_facts/list_findings/graph_overview trả về; tài sản hãy dùng list_assets/asset_neighbors.",
		obj(map[string]any{"id": idp("id node đồ thị thám thính (không phải id tài sản)")}, "id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.ID)
			if id <= 0 {
				return actool.Errorf("id bắt buộc"), nil
			}
			n, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == nil {
				return actool.Errorf(fmt.Sprintf("không tìm thấy node thám thính %d. Nếu bạn muốn tra tài sản, hãy dùng list_assets / asset_neighbors (tài sản và node thám thính là hai không gian id khác nhau, id tài sản không truyền cho node_detail được).", id)), nil
			}
			if err := t.ts.PopulateFindingTrafficIDs([]*db.Node{n}); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(n) // full payload incl. detail / evidence, plus explicit finding IDs
		})
}

// --- planner write tools ---

// intentItem là một hướng thám thính của add_intent dạng batch/lẻ.
type intentItem struct {
	Summary   string            `json:"summary"`
	AssetIDs  []json.RawMessage `json:"asset_ids"`
	ParentIDs []json.RawMessage `json:"parent_ids"`
	Priority  int               `json:"priority"`
}

// addOneIntent tạo một node ý đồ và nối huyết thống thượng nguồn, trả về id.
// Ràng buộc: ý đồ chỉ được neo trên tri thức đã xác nhận — mỗi parent_id phải là node
// fact/finding đã tồn tại (không được treo lên ý đồ/mục tiêu/gợi ý khác). Hướng hoàn
// toàn mới cấp trên để trống parent_ids, mặc định nối vào origin fact.
// Như vậy "mỗi ý đồ đều nối vào node fact, và là điều khiển bởi phát hiện chứ không
// phải lập kế hoạch từ hư không" được ép ngay từ đường tạo.
func (t *ToolSet) addOneIntent(it intentItem) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary không được để trống")
	}
	// Kiểm tra neo trước (trước khi tạo node, tránh neo hỏng để lại ý đồ mồ côi).
	parents := pidList(it.ParentIDs)
	for _, pidv := range parents {
		n, err := t.ts.GetNodeWithSources(pidv)
		if err != nil || n == nil {
			return 0, fmt.Errorf("parent_id %d không tồn tại trong tác vụ này hoặc tác vụ liên quan trực tiếp: parent_ids phải là id node [dữ kiện(fact)/phát hiện(finding)] đã tồn tại; hướng hoàn toàn mới cấp trên hãy để trống parent_ids", pidv)
		}
		if n.Kind != db.KindFact && n.Kind != db.KindFinding {
			return 0, fmt.Errorf("parent_id %d là node %q, không thể làm neo ý đồ: ý đồ chỉ neo trên [dữ kiện(fact)/phát hiện(finding)] đã xác nhận, không treo lên ý đồ/mục tiêu/gợi ý; hướng hoàn toàn mới cấp trên hãy để trống parent_ids", pidv, n.Kind)
		}
	}
	priority := it.Priority
	if priority == 0 {
		priority = 5
	}
	anchors := pidList(it.AssetIDs)
	// Chặn tài sản: nếu tài sản mà ý đồ gắn với trúng quy tắc chặn tài sản của hệ thống
	// thì cấm phát hành ý đồ đó.
	if t.as != nil && len(anchors) > 0 {
		hits, err := t.as.CheckAssetsIntercept(t.taskID, anchors)
		if err != nil {
			return 0, fmt.Errorf("kiểm tra chặn tài sản thất bại: %w", err)
		}
		if len(hits) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "tài sản mà ý đồ \"%s\" gắn với không qua được kiểm tra phạm vi kiểm thử, hãy dừng kiểm thử các tài sản liên quan:", it.Summary)
			for _, h := range hits {
				fmt.Fprintf(&b, "\n - %s", h.Describe())
			}
			return 0, fmt.Errorf("%s", b.String())
		}
	}
	payload := map[string]any{"summary": it.Summary}
	if len(anchors) > 0 {
		payload["asset_ids"] = anchors
	}
	id, err := t.ts.AddIntent(payload, priority, anchors, "planner")
	if err != nil {
		return 0, err
	}
	// upstream lineage: link each (validated) fact/finding parent → this intent, so
	// "multiple facts combine into one new intent" is expressible.
	for _, parent := range parents {
		_ = t.ts.Link(parent, db.RelDerivedFrom, id)
	}
	// a top-level intent (no explicit parent) connects to the origin fact, so every
	// intent still traces back to a fact node — at task start the only fact is the
	// origin, and the first intents derive from it.
	if len(parents) == 0 {
		if origin, _ := t.ts.OriginFactID(); origin > 0 {
			_ = t.ts.Link(origin, db.RelDerivedFrom, id)
		}
	}
	return id, nil
}

func (t *ToolSet) addIntent() actool.CoreTool {
	return t.writeExpTool("add_intent", "Sinh [hướng thám thính] ghi vào frontier, và nối vào chuỗi thám thính. Ý đồ là hướng thám thính mở, không phải loại cố định — dùng summary mô tả tự do một câu về việc cần thám thính/xác minh/khai thác gì.\n"+
		"★Ưu tiên batch: nhiều hướng mới sàng lọc được trong một lượt đặt vào mảng intents gửi một lần (tiết kiệm vòng lặp hơn gọi từng mục). Trả về mảng ids, dài bằng và cùng thứ tự với intents (mục thất bại id=0, chi tiết xem errors). Lẻ thì bỏ qua intents, cho summary trực tiếp ở cấp trên.",
		obj(map[string]any{
			"intents":    map[string]any{"type": "array", "description": "[Ưu tiên dùng cái này] Mảng các hướng thám thính cần thêm, xử lý theo thứ tự. Mỗi phần tử có trường như các trường cấp trên bên dưới (summary/asset_ids/parent_ids/priority). Trả về ids dài bằng và cùng thứ tự với mảng này.", "items": map[string]any{"type": "object"}},
			"summary":    str("[lẻ] Một câu mô tả hướng thám thính này: làm gì + vì sao. Viết rõ hướng là đủ, không phụ thuộc id tài sản."),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "[id tài sản mục tiêu] mà hướng này muốn kiểm thử/tấn công (**cố gắng truyền**, 0/1/nhiều; là id tài sản do list_assets trả về, không phải id node thám thính): hướng thám thính này nhắm vào những tài sản nào (site/interface/tham số/host...). Chừng nào hướng xoay quanh tài sản cụ thể thì nhất định truyền — nó là đánh dấu có cấu trúc của 'hướng thám thính này đánh vào mục tiêu nào', dùng cho khử trùng bao phủ, nối ý đồ vào chuỗi tài sản. Chỉ để trống khi thuần trinh sát toàn cục, thực sự không có tài sản mục tiêu cụ thể."},
			"parent_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "id neo thượng nguồn (tùy chọn, 0/1/nhiều): hướng này được tổng hợp từ những [dữ kiện(fact)/phát hiện(finding) đã xác nhận] nào. **Chỉ điền id node fact/finding đã tồn tại, không điền ý đồ/mục tiêu/gợi ý** — ý đồ phải neo trên tri thức đã xác nhận, điều khiển bởi phát hiện chứ không lập kế hoạch từ hư không. Nhiều dữ kiện cùng sinh một ý đồ mới thì truyền nhiều; hướng trinh sát hoàn toàn mới cấp trên hãy để trống (sẽ tự động gắn vào origin fact điểm xuất phát của tác vụ)."},
			"priority":   intp("độ ưu tiên 0-10, mặc định 5"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Intents    []intentItem `json:"intents"`
				intentItem              // chế độ lẻ: summary/asset_ids/parent_ids/priority cấp trên
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Intents) > 0
			items := a.Intents
			if !batch {
				items = []intentItem{a.intentItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			createdAny := false
			for i, it := range items {
				id, err := t.addOneIntent(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				createdAny = true
			}

			// Người dùng trực tiếp đưa ý đồ qua agent chính → nếu tác vụ đã done (nhánh
			// goalless không có mục tiêu open), kéo nó về running, worker mới nhận ý đồ này
			// để thực thi. resumeTask chỉ được nối bởi Chat của agent chính (SetResumeTask);
			// ToolSet của planner là nil, nên khi planner tự gọi add_intent đoạn này no-op,
			// không ảnh hưởng việc sinh ý đồ bình thường của nó. Node ý đồ đã tạo ở trên
			// (open), khi hồi sinh sẽ không bị phán nhầm là đã hút cạn.
			if createdAny && t.resumeTask != nil {
				t.resumeTask()
			}

			if !batch { // lẻ: giữ nguyên dạng trả về
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("intent created: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) listGoals() actool.CoreTool {
	return t.readExpTool("list_goals", "Liệt kê các node mục tiêu của tác vụ này và trạng thái (open/met), dùng để phán đoán đã đạt hay chưa.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			g, _ := t.ts.ListByKind(db.KindGoal, 100)
			return jsonResult(g)
		})
}

func (t *ToolSet) proveGoal() actool.CoreTool {
	return t.writeExpTool("prove_goal", "Gọi khi bạn phán đoán một phát hiện/dữ kiện chứng minh một mục tiêu đã đạt: nối node bằng chứng vào node mục tiêu, và đánh dấu mục tiêu met.",
		obj(map[string]any{
			"goal_id":     idp("id node mục tiêu"),
			"evidence_id": idp("id node phát hiện/dữ kiện chứng minh nó"),
			"reason":      str("vì sao bằng chứng này thỏa mãn mục tiêu đó"),
		}, "goal_id", "evidence_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				GoalID     json.RawMessage `json:"goal_id"`
				EvidenceID json.RawMessage `json:"evidence_id"`
				Reason     string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			goal, ev := pid(a.GoalID), pid(a.EvidenceID)
			if goal == 0 || ev == 0 {
				return actool.Errorf("goal_id và evidence_id bắt buộc"), nil
			}
			goalNode, err := t.ts.GetNode(goal)
			if err != nil || goalNode == nil || goalNode.Kind != db.KindGoal {
				return actool.Errorf("goal_id phải là node mục tiêu của tác vụ này (mục tiêu của tác vụ liên quan chỉ đọc)"), nil
			}
			evidenceNode, err := t.ts.GetNodeWithSources(ev)
			if err != nil || evidenceNode == nil || (evidenceNode.Kind != db.KindFact && evidenceNode.Kind != db.KindFinding) {
				return actool.Errorf("evidence_id phải là node dữ kiện/lỗ hổng của tác vụ này hoặc tác vụ liên quan trực tiếp"), nil
			}
			_ = t.ts.Link(ev, db.RelProves, goal)
			_ = t.ts.SetNodeState(goal, "met")
			// Mỗi lần đánh dấu một mục tiêu met, kiểm tra [tất cả mục tiêu] của tác vụ này đã
			// met chưa; nếu có, tự động phán tác vụ hoàn thành (đặt GoalMet), không cần dựa
			// vào việc mô hình gọi goal_met tường minh nữa.
			if goals, err := t.ts.ListByKind(db.KindGoal, 1000); err == nil && len(goals) > 0 {
				allMet := true
				for _, g := range goals {
					if g.State != "met" {
						allMet = false
						break
					}
				}
				if allMet {
					t.GoalMet = true
					t.Reason = fmt.Sprintf("tất cả %d mục tiêu đều đã met (cuối cùng do goal %d kích hoạt)", len(goals), goal)
					return actool.Text(fmt.Sprintf("goal %d marked met; tất cả mục tiêu của tác vụ này đã đạt, tác vụ tự động phán hoàn thành", goal)), nil
				}
			}
			return actool.Text(fmt.Sprintf("goal %d marked met", goal)), nil
		})
}

func (t *ToolSet) goalMet() actool.CoreTool {
	return writeTool("goal_met", "[Kết thúc ngay toàn bộ tác vụ] — chỉ gọi khi bạn xác nhận [toàn bộ mục tiêu của tác vụ đã thực sự đạt, khép lại tổng thể] (chú ý là tác vụ [tổng thể] hoàn thành; chỉ đạt được một mục tiêu/một flag/một lỗ hổng trong đó [không tính] — trường hợp đó dùng prove_goal đánh dấu mục tiêu đó là đủ). ⚠️Nó không dùng để \"kết thúc lượt lập kế hoạch này\": lượt này không có ý đồ mới để phái, hoặc đang chờ worker xuất kết quả, đều [kết thúc lượt trực tiếp là được, đừng gọi công cụ này] (0 ý đồ là hoàn toàn bình thường). Phán đoán bình thường ưu tiên dùng prove_goal chứng minh từng mục tiêu; goal_met chỉ là phương tiện bỏ qua chứng minh từng cái, khép lại trực tiếp từ toàn cục.",
		obj(map[string]any{"reason": str("lý do đạt được (phải là bằng chứng mục tiêu thực sự đạt, không thể là lý do kiểu \"lượt này không có hướng mới\" để kết thúc lượt)")}, "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Reason string }
			_ = json.Unmarshal(in, &a)
			t.GoalMet = true
			t.Reason = a.Reason
			return actool.Text("acknowledged: goal marked met"), nil
		})
}

// --- worker write tools ---

func (t *ToolSet) addFinding() actool.CoreTool {
	return writeTool("report_finding", "Ghi nhận lỗ hổng đã xác nhận, dùng evidence cung cấp output lệnh, nhật ký... làm bằng chứng có thể xác minh. Trong ngữ cảnh tác vụ truyền intent_id hiện tại. finding_id trả về là ID bản ghi lỗ hổng độc lập, finding_node_id là ID node thám thính (dòng đầu tiên giữ số node đó).", obj(map[string]any{
		"vulnclass": str("loại lỗ hổng"), "name": str("tên lỗ hổng"), "severity": str("critical|high|medium|low"), "summary": str("tóm tắt phát hiện"),
		"intent_id": idp("id ý đồ của tác vụ hiện tại"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "id tài sản bị ảnh hưởng"},
		"evidence":         str("văn bản bằng chứng/PoC"),
		"evidence_hint_id": idp("tùy chọn: ID node gợi ý tương ứng lỗ hổng này trong tác vụ, tự động mang theo traffic_refs có cấu trúc của nó; không được tham chiếu gợi ý kế thừa hoặc gợi ý của lỗ hổng khác"),
		"traffic_refs": map[string]any{"type": "array", "description": "tùy chọn; lỗ hổng HTTP/HTTPS trước hết truy xuất và rà soát từng request/response có thực sự ủng hộ kết luận lỗ hổng không, rồi điền ID thật theo thứ tự tái hiện. Lỗ hổng không phải HTTP như TCP, chưa thu thập hoặc không tìm thấy bản ghi chính xác thì bỏ qua hoặc truyền [], không chặn báo cáo; có thể giải thích lý do trong evidence và cung cấp bằng chứng có thể xác minh khác. Đừng đoán ID, đừng suy ra liên quan theo tên miền/thời gian, hoặc dò lại chỉ để bù gói. Vai trò: baseline đối chứng bình thường / proof chứng minh lỗ hổng / verification xác minh bổ sung / supporting bằng chứng phụ.",
			"items": obj(map[string]any{"traffic_id": str("ID lưu lượng thật do traffic_search trả về"), "role": map[string]any{"type": "string", "enum": []string{"baseline", "proof", "verification", "supporting"}}, "note": str("lưu lượng này ủng hộ kết luận lỗ hổng như thế nào")}, "traffic_id")},
	}, "vulnclass", "severity", "summary"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		var a struct {
			VulnClass, Name, Severity, Summary, Evidence string
			IntentID                                     json.RawMessage   `json:"intent_id"`
			AssetIDs                                     []json.RawMessage `json:"asset_ids"`
			TrafficRefs                                  []db.TrafficRef   `json:"traffic_refs"`
			EvidenceHintID                               json.RawMessage   `json:"evidence_hint_id"`
		}
		if err := json.Unmarshal(in, &a); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.ts == nil {
			return actool.Errorf("report_finding cần ngữ cảnh tác vụ; hội thoại nền tảng hãy bàn giao lỗ hổng cho tác vụ tương ứng qua add_task_hint, và mang theo traffic_refs đã có trong gợi ý, do Agent tác vụ đăng ký. Lỗ hổng đã đăng ký có thể dùng bind_finding_traffic gắn bổ sung."), nil
		}
		// Auto-binding off: ignore the evidence params instead of rejecting the call.
		// stripTrafficParameters already removes them from the advertised schema, but
		// models routinely emit fields anyway — failing here would discard a confirmed
		// finding over a stray parameter. The success path below reports evidence_status
		// "not_bound" with the "đã tắt, có thể liên kết thủ công trên trang" note, which is what the caller needs.
		if !findingTrafficBindingEnabled() {
			a.TrafficRefs, a.EvidenceHintID = nil, nil
		}
		if len(a.EvidenceHintID) > 0 && pid(a.EvidenceHintID) <= 0 {
			return actool.Errorf("evidence_hint_id phải là ID node gợi ý hợp lệ; không có gợi ý bàn giao thì bỏ qua"), nil
		}
		refs, err := t.findingRefsFromHint(pid(a.EvidenceHintID), a.TrafficRefs)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		input := db.RecordFindingInput{TaskID: t.taskID, ExplorationID: t.ts.ID(), IntentID: pid(a.IntentID), VulnClass: a.VulnClass, Name: a.Name, Severity: a.Severity, Summary: a.Summary, Evidence: a.Evidence, Worker: t.worker, AssetIDs: pidList(a.AssetIDs)}
		var recorded *db.RecordedFinding
		if t.findingRecorder != nil {
			recorded, err = t.findingRecorder.Record(ctx, input, refs)
		} else if len(refs) > 0 {
			return actool.Errorf("kho bằng chứng lưu lượng không khả dụng; chưa đăng ký lỗ hổng"), nil
		} else {
			recorded, err = t.ts.RecordFinding(ctx, input)
		}
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.notifyFinding != nil {
			iid := input.IntentID
			if iid <= 0 {
				iid = t.ownerNode
			}
			t.notifyFinding(iid, a.Summary)
		} else if t.notify != nil {
			t.notify()
		}
		t.writes.Findings++
		// Keep the first line's node-ID contract for existing reporter triggers.
		for i := range recorded.Traffic.Bindings {
			recorded.Traffic.Bindings[i].Snapshot.ReqHead = ""
			recorded.Traffic.Bindings[i].Snapshot.RespHead = ""
		}
		result := struct {
			*db.RecordedFinding
			EvidenceStatus string `json:"evidence_status"`
			EvidenceNote   string `json:"evidence_note,omitempty"`
		}{RecordedFinding: recorded, EvidenceStatus: "bound"}
		if len(recorded.Traffic.Bindings) == 0 {
			result.EvidenceStatus = "not_bound"
			result.EvidenceNote = "Lỗ hổng đã lưu, chưa gắn lưu lượng. Trường hợp TCP/không có gói có thể tiếp tục bình thường; nếu đã có lưu lượng HTTP đã rà soát, hãy dùng bind_finding_traffic khả dụng hoặc trang lỗ hổng để gắn bổ sung, rồi hoàn tất bàn giao bằng chứng. Đừng tạo lặp lại lỗ hổng."
			if !findingTrafficBindingEnabled() {
				result.EvidenceNote = "Lỗ hổng đã lưu. Agent tự động gắn lưu lượng đã tắt, có thể liên kết lưu lượng thủ công trên trang."
			}
		}
		raw, _ := json.Marshal(result)
		return actool.Text(fmt.Sprintf("finding recorded: %d\n%s", recorded.NodeID, raw)), nil
	})
}

// recordFact writes a general exploration RESULT/conclusion (not a vuln, not a
// new asset) into the EXPLORATION graph, chained to the intent that produced it.
// This is the home for observations and — importantly — negative results
// ("port closed", "param not injectable", "no login found"). Such conclusions
// must NOT be stuffed into the asset graph via upsert_asset.
// factItem là một dữ kiện của record_fact dạng batch/lẻ.
type factItem struct {
	Summary    string            `json:"summary"`
	Detail     string            `json:"detail"`
	Evidence   string            `json:"evidence"`   // một dòng bằng chứng then chốt (lệnh + dòng output then chốt), chống lưng kết luận, tiện rà soát lại sau
	Confidence string            `json:"confidence"` // observed (nhìn thấy trực tiếp) | inferred (suy ra từ hiện tượng)
	IntentID   json.RawMessage   `json:"intent_id"`
	AssetIDs   []json.RawMessage `json:"asset_ids"`
}

// recordOneFact ghi một node fact và nối vào ý đồ (intent→yields→fact). defaultIntent
// là ý đồ mặc định khi batch (dùng khi mục này không cho intent_id).
func (t *ToolSet) recordOneFact(it factItem, defaultIntent int64) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary không được để trống")
	}
	payload := map[string]any{"summary": it.Summary}
	if it.Detail != "" {
		payload["detail"] = it.Detail
	}
	if e := strings.TrimSpace(it.Evidence); e != "" {
		payload["evidence"] = e
	}
	if c := strings.TrimSpace(it.Confidence); c != "" {
		payload["confidence"] = c
	}
	intent := pid(it.IntentID)
	if intent <= 0 {
		intent = defaultIntent
	}
	if intent > 0 {
		node, err := t.ts.GetNode(intent)
		if err != nil || node == nil || node.Kind != db.KindIntent {
			return 0, fmt.Errorf("intent_id phải là ý đồ của tác vụ này (ý đồ của tác vụ liên quan chỉ đọc)")
		}
	}
	// a fact is its OWN node kind (distinct from a vuln finding).
	id, err := t.ts.AddNode(db.KindFact, payload, 5, "confirmed", t.worker, pidList(it.AssetIDs))
	if err != nil {
		return 0, err
	}
	if intent > 0 {
		_ = t.ts.Link(intent, db.RelYields, id) // chain: intent -> fact
	}
	t.writes.Facts++
	return id, nil
}

func (t *ToolSet) recordFact() actool.CoreTool {
	return t.writeExpTool("record_fact", "Ghi [dữ kiện/kết luận thám thính] vào đồ thị thám thính, nối vào ý đồ đã sinh ra nó (intent_id). Dùng để ghi kết quả thám thính — gồm [kết luận khẳng định] như fingerprint/liệt kê, và [kết luận phủ định] như 'cổng đóng'/'tham số không tiêm được'/'không tìm thấy lối vào đăng nhập'.\n"+
		"⚠️Nhiều quan sát của một lần thám thính phải [gộp thành một dữ kiện], đừng tách thành nhiều mục, cái gì gộp được thành một dữ kiện thì cố gắng dùng một dữ kiện biểu thị: summary=một câu tổng kết kết luận lần này, detail=chi tiết liên quan (có thể chứa nhiều mục cụ thể). Ví dụ: ý đồ fingerprint → một dữ kiện {summary:'đã nhận diện tech stack và đặc trưng phản hồi của site X', detail:'nginx 1.25 / Vue3 / 200 / title=.. / body_len=..'}, chứ không phải status code, fingerprint, title mỗi thứ một mục. Một ý đồ thường chỉ sinh một dữ kiện, tách quá nhỏ sẽ làm đồ thị phình to vô hạn.\n"+
		"★mảng facts dùng để một lần ghi nhiều kết luận [khác nhau hẳn] (mỗi mục có thể bỏ qua intent_id, mặc định dùng intent_id cấp trên). Trả về mảng ids, dài bằng và cùng thứ tự với facts.\n"+
		"⚠️Chỉ viết kết luận bạn [thực sự nhìn thấy] trong output công cụ, đừng tự tưởng tượng. evidence và confidence dùng để ngăn kết luận không chính xác làm bẩn đồ thị:\n"+
		"  · evidence=[một dòng] bằng chứng then chốt chống lưng kết luận này (lệnh + một hai dòng output chứng minh nhất), **nhất định súc tích** — chi tiết đã ở detail, đừng dán cả đoạn output dài vào đây.\n"+
		"  · confidence=observed (nhìn thấy trực tiếp trong output) | inferred (suy ra từ hiện tượng).\n"+
		"  · **Kết luận loại phủ định** (không tiêm được/cổng đóng/không tìm thấy lối vào...) chỉ viết \"quan sát + cách đọc thăm dò\" — trình bày bạn thực tế thấy gì, có bỏ hướng hay không do planner tổng hợp toàn cục quyết; nhất định cho evidence, phương tiện chưa cạn hoặc bằng chứng yếu (kể cả chỉ dò một lần, trông giống) thì đánh dấu inferred, chỉ khi thực sự đã cạn và nhìn thấy trực tiếp mới đánh dấu observed.",
		obj(map[string]any{
			"facts":      map[string]any{"type": "array", "description": "[Dùng khi có nhiều kết luận khác nhau] mảng dữ kiện, trường phần tử như các trường cấp trên bên dưới (summary/detail/evidence/confidence/intent_id/asset_ids); bỏ qua intent_id thì dùng intent_id cấp trên. Trả về ids dài bằng và cùng thứ tự với mảng này.", "items": map[string]any{"type": "object"}},
			"summary":    str("[Một câu tổng kết] kết luận thám thính lần này (là khái quát của detail)"),
			"intent_id":  idp("id ý đồ đã sinh ra dữ kiện này (ý đồ bạn nhận; khi batch làm mặc định cho các mục)"),
			"detail":     str("chi tiết liên quan của dữ kiện này: viết mọi dữ kiện quan sát của lần thám thính này vào đây"),
			"evidence":   str("[một dòng] bằng chứng then chốt: lệnh + một hai dòng output chứng minh kết luận nhất. Nhất định súc tích, đừng dán cả đoạn output dài (chi tiết để ở detail)."),
			"confidence": str("observed (nhìn thấy trực tiếp trong output) | inferred (suy ra từ hiện tượng). Kết luận phủ định nhất định phải đánh dấu trung thực."),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "id tài sản liên quan (tùy chọn, 0/1/nhiều): dữ kiện này liên quan những tài sản nào"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Facts    []factItem `json:"facts"`
				factItem            // chế độ lẻ + intent_id mặc định cho batch
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Facts) > 0
			items := a.Facts
			if !batch {
				items = []factItem{a.factItem}
			}
			defaultIntent := pid(a.factItem.IntentID) // intent_id cấp trên = mặc định cho batch

			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.recordOneFact(it, defaultIntent)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}

			if !batch { // lẻ: giữ nguyên dạng trả về
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("fact recorded: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type hintItem struct {
	Text        string            `json:"text"`
	AssetIDs    []json.RawMessage `json:"asset_ids"`
	TrafficRefs []db.TrafficRef   `json:"traffic_refs"`
}

// addOneHint treo một node hint (active/human) lên đồ thị thám thính, có thể neo tài
// sản, trả về id.
func (t *ToolSet) addOneHint(it hintItem) (int64, error) {
	if len(it.TrafficRefs) > 0 && !findingTrafficBindingEnabled() {
		return 0, fmt.Errorf("Agent tự động gắn lưu lượng đã tắt, chưa lưu gợi ý mang traffic_refs; có thể bật trong cài đặt hệ thống, hoặc chỉ bàn giao văn bản")
	}
	if strings.TrimSpace(it.Text) == "" {
		return 0, fmt.Errorf("text không được để trống")
	}
	var anchors []int64
	for _, raw := range it.AssetIDs {
		if tid := pid(raw); tid > 0 {
			anchors = append(anchors, tid)
		}
	}
	refs, err := db.NormalizeTrafficRefs(it.TrafficRefs)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{"text": it.Text}
	if len(refs) > 0 {
		payload["traffic_refs"] = refs
	}
	// Không đánh thức planner từng mục ở đây — addHint sẽ kích hoạt một lần thống nhất sau
	// khi ghi xong cả batch (kèm văn bản gợi ý), tránh một lần add_hint nhiều gợi ý spam
	// từng dòng vào hàng trigger của planner.
	return t.ts.AddNode(db.KindHint, payload, 0, "active", "human", anchors)
}

type goalItem struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass"`
}

// addOneGoal treo một node goal (open) lên đồ thị thám thính: nối vào gốc tác vụ
// (origin fact, rel spawns). origin lấy t.worker (mặc định system): do bộ phân rã goals
// ghi thì ghi "goals", do agent chính lúc chạy ghi thì ghi "human". Đánh thức planner
// do setGoals làm một lần thống nhất sau khi ghi xong cả batch (xem dưới), ở đây chỉ
// lo ghi vào kho.
func (t *ToolSet) addOneGoal(it goalItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text không được để trống")
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(it.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	origin := t.worker
	if origin == "" {
		origin = "system"
	}
	id, err := t.ts.AddNode(db.KindGoal, payload, 0, "open", origin, nil)
	if err != nil {
		return 0, err
	}
	if of, _ := t.ts.OriginFactID(); of > 0 && id > 0 {
		_ = t.ts.Link(of, db.RelSpawns, id) // goals descend from the task root (origin fact)
	}
	return id, nil
}

// setGoals thêm mục tiêu thám thính (node goal) cho [tác vụ này]. Vừa là công cụ gửi
// của bộ phân rã mục tiêu, vừa là công cụ bổ sung mục tiêu lúc chạy của agent chính —
// cùng một công cụ được quản, có thể sửa mô tả/schema ở web, gắn theo agent.
func (t *ToolSet) setGoals() actool.CoreTool {
	return writeTool("set_goals",
		"Thêm mục tiêu thám thính (goal) cho [tác vụ này]. Mục tiêu = kết quả cuối cùng có thể bàn giao/có thể kiểm chứng, không phải bước tấn công hay hành động trinh sát.\n"+
			"★Ưu tiên batch: nhiều mục tiêu đặt vào mảng goals gửi một lần, trả về ids dài bằng và cùng thứ tự (mục thất bại id=0, chi tiết xem errors). Lẻ thì bỏ qua goals, cho text trực tiếp ở cấp trên.\n"+
			"vulnclass tùy chọn: loại lỗ hổng tương ứng (như SQLi/IDOR), mục tiêu loại logic nghiệp vụ để trống. Mục tiêu có đạt không do hệ thống phán và đánh dấu met, công cụ này chỉ lo thêm mới.",
		obj(map[string]any{
			"goals":     map[string]any{"type": "array", "description": "[Ưu tiên dùng cái này] Mảng mục tiêu cần thêm, xử lý theo thứ tự. Mỗi phần tử: text (bắt buộc, một mục tiêu cuối cùng độc lập có thể kiểm chứng) + vulnclass (tùy chọn). Trả về ids dài bằng và cùng thứ tự với mảng này.", "items": map[string]any{"type": "object"}},
			"text":      str("[lẻ] một mục tiêu cuối cùng độc lập có thể kiểm chứng"),
			"vulnclass": str("[lẻ] loại lỗ hổng tương ứng (nếu rõ), như SQLi/IDOR; mục tiêu logic nghiệp vụ có thể để trống"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_goals chưa bật: ExplorationStore chưa khởi tạo"), nil
			}
			var a struct {
				Goals    []goalItem `json:"goals"`
				goalItem            // chế độ lẻ: text/vulnclass cấp trên
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Goals) > 0
			items := a.Goals
			if !batch {
				items = []goalItem{a.goalItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				id, err := t.addOneGoal(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// Đánh thức planner (một lần cho cả batch). Ưu tiên notifyGoal: một lần
				// set_goals ghi một trigger "người dùng đã thêm N mục tiêu: …", không spam
				// từng mục; bộ phân rã/worker không có callback này → lùi về notify thuần
				// (round-0 của bộ phân rã còn chưa nối cả notify, tức không làm gì, vì lúc
				// đó planner chưa khởi động).
				switch {
				case t.notifyGoal != nil:
					t.notifyGoal(addedTexts)
				case t.notify != nil:
					t.notify()
				}
				// Agent chính thêm mục tiêu lúc chạy → kéo tác vụ đã hoàn thành/tạm dừng về
				// running chạy tiếp (cổng trạng thái cuối sẽ nuốt notify thường, phải hồi sinh
				// tường minh). Chỉ mainagent nối callback này; bộ phân rã/worker là nil.
				if t.resumeTask != nil {
					t.resumeTask()
				}
			}

			if !batch { // lẻ: giữ nguyên dạng trả về
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("goal added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type constraintItem struct {
	Text string `json:"text"`
	Type string `json:"type"` // allow | deny
}

// addOneConstraint ghi một ràng buộc thao tác vào task_constraints. origin lấy
// t.worker (mặc định system): bộ phân rã ghi "goals", agent chính ghi "human".
func (t *ToolSet) addOneConstraint(it constraintItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text không được để trống")
	}
	kind := strings.TrimSpace(strings.ToLower(it.Type))
	if kind == "" {
		kind = "deny" // mặc định xử lý như cấm: khi chưa ghi loại thì bảo thủ hơn
	}
	if kind != "allow" && kind != "deny" {
		return 0, fmt.Errorf("type phải là allow hoặc deny")
	}
	return t.ts.AddConstraint(kind, text, t.worker)
}

// setConstraints thêm ràng buộc thao tác cho [tác vụ này] (allow=được làm gì /
// deny=cấm làm gì). Vừa là công cụ gửi ràng buộc round-0 của bộ phân rã mục tiêu, vừa
// là công cụ bổ sung ràng buộc lúc chạy của agent chính — cùng một công cụ được quản,
// có thể sửa mô tả/schema ở web, gắn theo agent. Ràng buộc sẽ được tiêm vào system
// prompt của planner/worker để khung ranh giới thám thính.
func (t *ToolSet) setConstraints() actool.CoreTool {
	return writeTool("set_constraints",
		"Thêm ràng buộc thao tác cho [tác vụ này], dùng để khung ranh giới thám thính: type=allow (thao tác được làm) hoặc deny (thao tác bị cấm).\n"+
			"Ràng buộc = quy định về [được/không được làm những thao tác nào] (như \"chỉ kiểm cổng hiện tại, không quét cổng khác\", \"cấm thao tác ghi lên cơ sở dữ liệu production\", \"chỉ cho phép trinh sát thụ động\"), không phải mục tiêu, cũng không phải bước tấn công.\n"+
			"★Ưu tiên batch: nhiều mục đặt vào mảng constraints gửi một lần, trả về ids dài bằng và cùng thứ tự (mục thất bại id=0, chi tiết xem errors). Lẻ thì bỏ qua constraints, cho text/type trực tiếp ở cấp trên.\n"+
			"Chỉ đăng ký ràng buộc [được viết rõ] trong mục tiêu/mô tả tác vụ, đừng tự bịa; khi không chắc loại thì dùng deny (bảo thủ hơn).",
		obj(map[string]any{
			"constraints": map[string]any{"type": "array", "description": "[Ưu tiên dùng cái này] Mảng ràng buộc cần thêm, xử lý theo thứ tự. Mỗi phần tử: text (bắt buộc, một ràng buộc) + type (allow|deny). Trả về ids dài bằng và cùng thứ tự với mảng này.", "items": map[string]any{"type": "object"}},
			"text":        str("[lẻ] nội dung một ràng buộc thao tác"),
			"type":        str("[lẻ] allow (cho phép) hoặc deny (cấm); mặc định xử lý theo deny"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_constraints chưa bật: ExplorationStore chưa khởi tạo"), nil
			}
			var a struct {
				Constraints    []constraintItem `json:"constraints"`
				constraintItem                  // chế độ lẻ: text/type cấp trên
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Constraints) > 0
			items := a.Constraints
			if !batch {
				items = []constraintItem{a.constraintItem}
			}
			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.addOneConstraint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}
			if !batch { // lẻ: giữ dạng trả về đơn giản
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("constraint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) addHint() actool.CoreTool {
	return t.writeExpTool("add_hint", "Treo gợi ý chiến lược của người dùng/agent chính lên đồ thị thám thính, planner sẽ đọc nó ở lần sinh ý đồ tiếp theo.\n"+
		"★Ưu tiên batch: nhiều gợi ý đặt vào mảng hints gửi một lần (tiết kiệm vòng lặp hơn gọi từng mục). Trả về mảng ids, dài bằng và cùng thứ tự với hints (mục thất bại id=0, chi tiết xem errors). Lẻ thì bỏ qua hints, cho text trực tiếp ở cấp trên.",
		obj(map[string]any{
			"hints":        map[string]any{"type": "array", "description": "[Ưu tiên dùng cái này] Mảng gợi ý cần thêm, xử lý theo thứ tự. Mỗi phần tử có trường như các trường cấp trên bên dưới (text/asset_ids/traffic_refs). Trả về ids dài bằng và cùng thứ tự với mảng này.", "items": obj(map[string]any{"text": str("nội dung gợi ý"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": HintTrafficSchema()})},
			"text":         str("[lẻ] nội dung gợi ý, như 'tập trung đào các interface sau xác thực'"),
			"traffic_refs": HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "id tài sản được neo (tùy chọn, 0/1/nhiều)"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Hints    []hintItem `json:"hints"`
				hintItem            // chế độ lẻ: text/asset_ids cấp trên
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Hints) > 0
			items := a.Hints
			if !batch {
				items = []hintItem{a.hintItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				id, err := t.addOneHint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// Đánh thức planner (một lần cho cả batch). Ưu tiên notifyHint: một lần
				// add_hint ghi một trigger "người dùng đã thêm N gợi ý chiến lược: …", để
				// planner biết rõ "lượt này do hint mới kích hoạt" và thấy nội dung gợi ý;
				// khi chưa nối callback đó thì lùi về notify thuần (bare wake, hint vẫn được
				// gấp trong đồ thị để nó tự đọc).
				switch {
				case t.notifyHint != nil:
					t.notifyHint(addedTexts)
				case t.notify != nil:
					t.notify()
				}
			}

			if !batch { // lẻ: giữ nguyên dạng trả về
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("hint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

// killWorkTool lets the planner terminate a single running work (by intent id).
func (t *ToolSet) killWorkTool() actool.CoreTool {
	return t.writeExpTool("kill_work", "Chấm dứt một ý đồ (work) đang chạy. Dùng để ngắt thám thính chệch hướng/vô nghĩa; ý đồ bị chấm dứt được đánh dấu stopped, không tự động được nhận lại. Hãy dùng get_worker_output xem nó đang làm gì rồi mới quyết định.",
		obj(map[string]any{"intent_id": idp("id ý đồ cần chấm dứt (= handle work)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.killWork == nil {
				return actool.Errorf("kill_work hiện không khả dụng"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id bắt buộc"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id phải là ý đồ của tác vụ này (ý đồ của tác vụ liên quan chỉ đọc)"), nil
			}
			if err := t.killWork(id); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("đã gửi tín hiệu chấm dứt đến work của ý đồ %d", id)), nil
		})
}

// steerWorkTool lets the planner inject a mid-run course-correction into a running
// work WITHOUT killing it: the message reaches the worker before its next tool call,
// which re-plans its next step (already-gathered context is kept). For in-intent
// nudges ("ngừng làm X, tập trung Y"); if the whole direction is wrong use kill_work + a new intent.
func (t *ToolSet) steerWorkTool() actool.CoreTool {
	return t.writeExpTool("steer_work", "Tiêm chỉ thị chỉnh hướng thời gian thực vào một ý đồ (work) đang chạy, không ngắt nó, không mất tiến triển đã có: worker sẽ nhận chỉ thị của bạn trước hành động tiếp theo và điều chỉnh theo. Dùng cho chỉnh hướng [trong ý đồ] kiểu 'đừng đi X nữa, tập trung Y'; nếu sai cả hướng thì nên đổi sang kill_work rồi phát ý đồ mới. Nên dùng get_worker_output xem nó đang làm gì trước.",
		obj(map[string]any{
			"intent_id": idp("id ý đồ cần chỉnh hướng (= handle work)"),
			"message":   str("chỉ thị chỉnh hướng cho worker, nêu rõ bắt nó dừng gì, chuyển sang gì"),
		}, "intent_id", "message"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.steerWork == nil {
				return actool.Errorf("steer_work hiện không khả dụng"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Message  string          `json:"message"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id bắt buộc"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id phải là ý đồ của tác vụ này (ý đồ của tác vụ liên quan chỉ đọc)"), nil
			}
			if strings.TrimSpace(a.Message) == "" {
				return actool.Errorf("message bắt buộc"), nil
			}
			if err := t.steerWork(id, a.Message); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("đã tiêm chỉ thị chỉnh hướng vào work của ý đồ %d (có hiệu lực ở bước tiếp theo)", id)), nil
		})
}

// getWorkerOutput returns a work's final (hoặc tính đến lúc bị ngắt) conclusion text by intent id.
func (t *ToolSet) getWorkerOutput() actool.CoreTool {
	return t.readExpTool("get_worker_output", "Lấy kết luận output cuối cùng của một ý đồ (work) thuộc tác vụ này hoặc tác vụ liên quan trực tiếp. Kết quả của tác vụ liên quan kèm source_task_id/inherited=true và chỉ đọc. Kết thúc bình thường thì trả về tóm tắt của nó; work bị chấm dứt (stopped)/bất thường trả về output cuối cùng tính đến lúc bị ngắt.",
		obj(map[string]any{"intent_id": idp("id ý đồ (= handle work)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id bắt buộc"), nil
			}
			intentNode, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if intentNode == nil || intentNode.Kind != db.KindIntent {
				return actool.Errorf("intent_id không thuộc tác vụ này hoặc tác vụ liên quan trực tiếp của nó"), nil
			}
			acts, _, err := t.ts.ActivityListWithSources(id, 0, 1000)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var chosen, fallback *db.Activity
			for i := range acts {
				switch acts[i].Kind {
				case "result":
					chosen = &acts[i]
					fallback = &acts[i]
				case "text":
					fallback = &acts[i]
				}
			}
			pick := chosen
			if pick == nil {
				pick = fallback
			}
			if pick == nil {
				if intentNode.Inherited {
					return jsonResult(inheritedMap(map[string]any{
						"intent_id": id, "final_text": "(work này chưa có bất kỳ output nào)",
					}, intentNode.SourceTaskID))
				}
				return actool.Text("(work này chưa có bất kỳ output nào)"), nil
			}
			detail, _ := t.ts.ActivityDetailWithSources(pick.ID)
			if detail == "" {
				detail = pick.Summary
			}
			result := map[string]any{
				"intent_id": id, "final_text": detail,
				"summary": pick.Summary, "is_error": pick.IsError,
			}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// traceSteps renders summary-only trace rows, re-truncating each summary to 100
// chars — the stored summary is capped at 200 for the UI transcript; the trace
// tools want it tighter since a whole work's step list is many rows.
func traceSteps(acts []db.Activity) []map[string]any {
	steps := make([]map[string]any, 0, len(acts))
	for i := range acts {
		step := map[string]any{
			"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
			"is_error": acts[i].IsError, "summary": firstLine(acts[i].Summary, 100),
		}
		if acts[i].Inherited {
			inheritedMap(step, acts[i].SourceTaskID)
		}
		steps = append(steps, step)
	}
	return steps
}

// getWorkerTrace exposes a work's execution PROCESS (not just its final output):
// list step summaries, keyword-search within one work, or pull full detail of a
// few specific steps. Thinking steps are excluded everywhere.
func (t *ToolSet) getWorkerTrace() actool.CoreTool {
	return t.readExpTool("get_worker_trace",
		"Xem [quá trình thực thi] của một ý đồ (work) (khác với get_worker_output chỉ cho kết luận cuối). Ba cách dùng:\n"+
			"① chỉ truyền intent_id → trả về dòng tóm tắt từng bước của work đó (summary≤100 chữ, có step_id; chỉ là đường nét hành động, không gồm output đầy đủ);\n"+
			"② intent_id + q → chỉ trả về tóm tắt các bước khớp từ khóa (tìm trong cả tóm tắt và output đầy đủ; vẫn chỉ cho summary, muốn xem nội dung dùng ③);\n"+
			"③ intent_id + step_ids → trả về nội dung đầy đủ (detail) của các bước này; mỗi lần lấy tối đa 5 mục, vượt quá chỉ trả 5 mục đầu và báo các mục chưa lấy trong notice/omitted_step_ids.\n"+
			"Quy trình điển hình: dùng ①/② định vị step_id của bước khả nghi trước, rồi dùng ③ lấy output đầy đủ. Không gồm bước suy nghĩ (thinking). Hỗ trợ trace lịch sử của tác vụ liên quan trực tiếp; kết quả kèm source_task_id/inherited=true và chỉ đọc.",
		obj(map[string]any{
			"intent_id": idp("id ý đồ (= handle work)"),
			"q":         str("từ khóa: chỉ trả về các bước có tóm tắt/output đầy đủ khớp nó (tùy chọn; loại trừ lẫn nhau với step_ids)"),
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "step_id cần lấy nội dung đầy đủ (từ kết quả ①/②; mỗi lần tối đa 5 mục, truyền thừa chỉ trả 5 mục đầu, phần còn lại liệt kê trong omitted_step_ids)"},
			"limit":     intp("giới hạn trả về của dòng tóm tắt/truy xuất (tùy chọn)"),
		}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage   `json:"intent_id"`
				Q        string            `json:"q"`
				StepIDs  []json.RawMessage `json:"step_ids"`
				Limit    int               `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id bắt buộc"), nil
			}
			intentNode, nodeErr := t.ts.GetNodeWithSources(id)
			if nodeErr != nil {
				return actool.Errorf(nodeErr.Error()), nil
			}
			if intentNode == nil || intentNode.Kind != db.KindIntent {
				return actool.Errorf("intent_id không thuộc tác vụ này hoặc tác vụ liên quan trực tiếp của nó"), nil
			}
			// ③ detail drill-down by step ids, thinking excluded by the store.
			if len(a.StepIDs) > 0 {
				// Dedup + drop invalid ids first so garbage/duplicates don't eat into
				// the per-call cap. detail is returned in full (untruncated), so the
				// cap bounds one tool result; over the cap we serve the first N and
				// tell the model exactly which ids were deferred, instead of erroring
				// and forcing it to re-plan the call.
				const maxStepIDs = 5
				var ids []int64
				seen := make(map[int64]bool)
				for _, raw := range a.StepIDs {
					if v := pid(raw); v > 0 && !seen[v] {
						seen[v] = true
						ids = append(ids, v)
					}
				}
				var omitted []int64
				if len(ids) > maxStepIDs {
					omitted = append(omitted, ids[maxStepIDs:]...)
					ids = ids[:maxStepIDs]
				}
				acts, err := t.ts.ActivityByIDsWithSources(ids)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				steps := make([]map[string]any, 0, len(acts))
				for i := range acts {
					if acts[i].NodeID == nil || *acts[i].NodeID != id || acts[i].Inherited != intentNode.Inherited ||
						(acts[i].Inherited && acts[i].SourceTaskID != intentNode.SourceTaskID) {
						continue
					}
					step := map[string]any{
						"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
						"is_error": acts[i].IsError, "detail": acts[i].Detail,
					}
					if acts[i].Inherited {
						inheritedMap(step, acts[i].SourceTaskID)
					}
					steps = append(steps, step)
				}
				result := map[string]any{"intent_id": id, "steps": steps, "returned_step_ids": ids}
				if len(omitted) > 0 {
					// returned_step_ids/omitted_step_ids let the model decide programmatically
					// whether another call is worth it; the notice states the same in prose.
					result["omitted_step_ids"] = omitted
					result["notice"] = fmt.Sprintf(
						"mỗi lần lấy tối đa nội dung đầy đủ của %d bước, lần này đã trả %d bước đầu (%v), %d bước chưa lấy là %v."+
							"Nếu các nội dung này đã đủ để định vị thì không cần lấy các bước còn lại; thực sự cần tiếp thì dùng các step_id này gọi lại một lần nữa.",
						maxStepIDs, len(ids), ids, len(omitted), omitted)
				}
				if intentNode.Inherited {
					inheritedMap(result, intentNode.SourceTaskID)
				}
				return jsonResult(result)
			}
			// ①/② summary stream, optionally keyword-filtered; 100-char summaries.
			var acts []db.Activity
			var err error
			if strings.TrimSpace(a.Q) != "" {
				acts, err = t.ts.ActivityTraceSearchWithSources(id, a.Q, a.Limit)
			} else {
				acts, err = t.ts.ActivityTraceWithSources(id, a.Limit)
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			result := map[string]any{"intent_id": id, "steps": traceSteps(acts)}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// searchAllWorkerTraces keyword-searches EVERY work's process in this task — for
// finding what a worker saw but never wrote back as a fact. Returns only matching
// summaries (≤100 chars), each tagged with its intent_id for follow-up drill-down.
func (t *ToolSet) searchAllWorkerTraces() actool.CoreTool {
	return t.readExpTool("search_all_worker_traces",
		"[Thường không khuyến nghị dùng, vì hệ thống đã cung cấp phần lớn thông tin] Tìm theo từ khóa (q) trong [quá trình thực thi của các work khác của tác vụ này] — dùng để tìm lại thứ mà một worker đã thấy nhưng chưa ghi vào fact (đường dẫn/token/báo lỗi...)."+
			"Đã tự động loại trừ các bước của chính ý đồ bạn đang chạy (chúng vốn đã ở trong ngữ cảnh của bạn)."+
			"Chỉ trả về tóm tắt của các bước khớp (summary≤100 chữ), mỗi mục kèm intent_id; dựa vào đó dùng get_worker_trace(intent_id, step_ids=[...]) lấy nội dung đầy đủ.",
		obj(map[string]any{
			"q":     str("từ khóa (tìm trong tóm tắt + output đầy đủ của mọi bước work)"),
			"limit": intp("giới hạn trả về, mặc định 100 (tùy chọn)"),
		}, "q"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Q) == "" {
				return actool.Errorf("q bắt buộc"), nil
			}
			// Loại trừ các bước của ý đồ chính phía gọi (trace riêng của worker đã ở trong
			// ngữ cảnh của nó).
			acts, err := t.ts.ActivityTraceSearchAllWithSources(t.ownerNode, a.Q, a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			hits := make([]map[string]any, 0, len(acts))
			for i := range acts {
				var intent int64
				if acts[i].NodeID != nil {
					intent = *acts[i].NodeID
				}
				hit := map[string]any{
					"intent_id": intent, "step_id": acts[i].ID, "worker": acts[i].Worker,
					"kind": acts[i].Kind, "tool": acts[i].Tool, "is_error": acts[i].IsError,
					"summary": firstLine(acts[i].Summary, 100),
				}
				if acts[i].Inherited {
					inheritedMap(hit, acts[i].SourceTaskID)
				}
				hits = append(hits, hit)
			}
			return jsonResult(map[string]any{"query": a.Q, "hits": hits})
		})
}

// listWorkerTraces gives a worker (which has no graph_overview and can't see the
// intent graph) a lightweight index of the works in this task — intent_id +
// one-line summary + state — so it can DISCOVER which works to inspect via
// get_worker_trace. Without this a worker only knows intent_ids that come back
// from search_all_worker_traces hits. Excludes still-open intents (not yet run →
// no process to inspect).
func (t *ToolSet) listWorkerTraces() actool.CoreTool {
	return t.readExpTool("list_worker_traces",
		"[Thường không khuyến nghị dùng, vì hệ thống đã cung cấp phần lớn thông tin] Liệt kê chỉ mục các [work (ý đồ) đã chạy] trong tác vụ này: intent_id + hướng một câu (summary) + trạng thái."+
			"Bạn (worker) không nhìn thấy đồ thị thám thính, hãy dùng nó để phát hiện work nào đáng lật xem — rồi dùng get_worker_trace(intent_id) xem các bước, get_worker_trace(intent_id, step_ids=[...]) lấy chi tiết."+
			"Chỉ liệt kê cái đã thực thi (running/done/exhausted/blocked/stopped), không gồm open chưa chạy. Lưu ý: ranh giới tác vụ của bạn vẫn là ý đồ bạn đã nhận, xem work khác chỉ để tái dùng quan sát/tránh làm trùng.",
		obj(map[string]any{
			"q":     str("lọc theo từ khóa summary (tùy chọn)"),
			"limit": intp("giới hạn trả về, mặc định 50 (tùy chọn)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = 50
			}
			all, err := t.ts.ListByKindWithSources(db.KindIntent, 500)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Q))
			out := make([]map[string]any, 0, limit)
			for _, n := range all {
				if n.Inherited && n.State == "running" {
					continue
				}
				switch n.State {
				case "running", "done", "exhausted", "blocked", "stopped": // has run → has a process
				default:
					continue
				}
				var p map[string]any
				_ = json.Unmarshal(n.Payload, &p)
				summary, _ := p["summary"].(string)
				if q != "" && !strings.Contains(strings.ToLower(summary), q) {
					continue
				}
				item := map[string]any{"intent_id": n.ID, "summary": summary, "state": n.State}
				if n.Inherited {
					inheritedMap(item, n.SourceTaskID)
				}
				out = append(out, item)
				if len(out) >= limit {
					break
				}
			}
			return jsonResult(map[string]any{"works": out})
		})
}

// PlannerTools is the read + intent-generation + goal-judgement tool set.
func (t *ToolSet) PlannerTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		// cold-digest §6.1: restore folded cold nodes (digest body → members → detail).
		t.expandDigest(),
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.listGoals(), t.addIntent(), t.proveGoal(), t.goalMet(),
		t.killWorkTool(), t.steerWorkTool(),
		// report_finding: khi phán đoán tình hình, nếu bản thân đã xác chứng lỗ hổng, có thể
		// đăng ký trực tiếp (cùng công cụ với worker).
		t.addFinding(),
		// list_companies: xem danh sách doanh nghiệp + scope + số tài sản (lấy company_id /
		// hiểu phạm vi thuộc về).
		t.listCompanies(),
		// list_assets: khi lập kế hoạch, truy xuất toàn kho tài sản theo DSL (phối hợp góc
		// nhìn "chưa kiểm trong phạm vi" của list_untested_assets, bổ sung khả năng "tra trong
		// toàn kho theo tên miền/dấu vân tay/cổng/status code...").
		t.listAssets(),
		// add_company_scope: khi lập kế hoạch có thể đưa tên miền/IP/CIDR/ICP/từ khóa vào
		// phạm vi tài sản của một công ty (tự động nhận tài sản khớp).
		t.addCompanyScope(),
		// add_task_scope: chủ động đưa cả tên miền gốc/cả công ty/một tên miền con/IP vào
		// phạm vi kiểm thử của tác vụ này (mẫu số độ bao phủ).
		t.addTaskScope(),
		// list_untested_assets: tra tài sản chưa kiểm trong phạm vi tác vụ này khi cần
		// (loại + phân trang), tự quyết định kiểm bổ sung.
		t.listUntestedAssets(),
	}
}
