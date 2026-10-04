package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n [당신의 할 일 계획(깨우기 후에도 유지되고 마지막 라운드에서 당신이 쓴 것)]: \n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("그에 따라 진행하십시오. [사전 단계가 완료되었습니다. / 에 따라 달라집니다 fact 이미 존재하는 다음 단계 의도; 사용 TodoWrite 목록을 업데이트합니다. fact 만족스러운 단계 completed）。이미 목록에 있는 파이를 반복하지 마세요. pending/in_progress 단계。")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = 요약).
//	"goal" — the human (via 마스터 agent의 set_goals) added one OR MORE goals in a
//	            single call(Goals = 이 새로운 대상 텍스트, 1+ 항목, set_goals는 일괄 처리를 지원합니다).
//	"goal_deleted" — the human deleted a goal from 대상 관리 개요(Detail = 삭제된 대상 텍스트).
//	"goal_edited" — the human edited a goal from 대상 관리 개요(OldGoal→NewGoal 텍스트).
//	"cancelled" — the human deleted intent IntentID (Detail = 삭제 이유). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" 특수: 삭제 전 캡처된 의도 요약(삭제 후 노드가 더 이상 존재하지 않으며 다시 확인할 수 없음)
	Goals    []string // Kind=="goal" 특수: 이번에 set_goals에 추가된 대상 텍스트(1개 이상)
	OldGoal  string   // Kind=="goal_edited" 특수: 수정 전 대상 텍스트
	NewGoal  string   // Kind=="goal_edited" 특수: 수정된 대상 텍스트
	Hints    []string // Kind=="hint" 독점: 이번에 add_hint에 프롬프트 텍스트가 새로 추가되었습니다(1개 이상).
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n [이번에 트리거된 실제 변경 사항(먼저 여기를 읽고 방향을 보완할지 결정)]:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- Man(메인 agent)이 새로운 목표를 추가했습니다: %s - 달성해야 할 새로운 목표입니다. 그에 따라 탐색 방향을 보완하세요(아직 해당 의도가 없는 경우).", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- Man(메인 agent)이 %d 목표를 추가했습니다: %s - 모두 달성해야 할 새로운 목표입니다. 해당 의도가 없는 목표에 대해서는 탐색 방향을 하나씩 추가해 주세요.", len(ev.Goals), strings.Join(ev.Goals, "；")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n-Ren(마스터 agent)이 새로운 전략 팁인 %s를 추가했습니다. - 탐사 지도에 연결되었습니다. 그에 따라 탐사 방향을 조정/보충하세요(해당 의도가 아직 없는 경우).", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- Man(메인 agent)이 %d 전략 팁: %s를 추가했습니다. 모두 탐사 지도에 연결되어 있으므로 이에 따라 탐사 방향을 조정/보충하세요.", len(ev.Hints), strings.Join(ev.Hints, "；")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- 누군가가 이 대상을 삭제했습니다: %s - 이 대상은 제거되었습니다. 그에 따라 나머지 대상/방향을 다시 판단하십시오(더 이상 의도를 보낼 필요가 없습니다).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- 대상이 ＂%s＂에서 ＂%s＂로 변경되었습니다. 새로운 대상에 따라 탐색 방향을 조정하십시오(원래 방향이 더 이상 적용되지 않는 경우 전송을 중지하십시오).", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n - #%d(%s) 의도가 있는 worker가 finding: %s를 보고했습니다.", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// 인텐트 콘텐츠는 먼저 삭제 중에 캡처된 Summary를 사용합니다(실제 삭제 후에는 노드가 더 이상 존재하지 않으며 intentSummary를 찾을 수 없습니다).
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- 의도 #%d는 사용자에 의해 삭제되었으며, 의도 콘텐츠는 %s, 삭제 이유는 %s입니다. 인텐트가 삭제되었습니다(더 이상 실행되지 않음). 그에 따라 계획을 세우십시오.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n - #%d(%s) 의도에 대한 worker가 종료되고 결론 출력: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf(";의도적으로 새로 생성된 사실 id: %s ", fids))
			}
		}
	}
	b.WriteString("\n (자세한 내용은 node_detail / get_worker_output / list_findings에서 확인하실 수 있습니다.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, "、")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(출력을 가져오지 못했습니다)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(이 work에는 아직 출력 기록이 없습니다)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …(잘림, 전체 내용은 get_worker_output 참조)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n [현재 상황(graph_overview 프리페치, 이 도구를 호출할 때의 반환과 동일합니다. 세부 정보가 필요한 경우 필요에 따라 node_detail/list_facts 등을 조정하세요.)]: \n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (부분 [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the 중간 제품 출력 프로토콜
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `당신은 네트워크 보안 플랫폼 공인 침투 테스트 시스템의 ＂기획자＂이며 자주 깨어납니다(그림이 바뀌자마자). 책임: 상황 읽기 → 대상 결정 → **아직 다루지 않은 새로운 방향이 있는 경우에만 탐색 의도를 추가합니다**. 당신은 실행자가 아니라 계획자입니다. 이 라운드의 모든 제품은 [의도 생성/상태] 또는 [목표 결정]만 가능하며 plan에서는 결코 완료되지 않습니다.

임무 대상: ​​{{.Goal}}

**이 라운드에서는 여러 가지 의도가 생성되어야 합니다(먼저 생각해 보세요)**:
- **확실한 결론(가장 높은 우선순위）**：[목표가 달성되지 않은 한] 및 [현재는 없습니다. open 또는 running 탐색 의도】（frontier_open=0 그리고 running_intents 비어 있음), 이 라운드는 [반드시] 목표를 향해 전진하려는 의도를 하나 이상 생성해야 합니다. 달리는 사람은 없습니다. work 방향을 기다릴 수 있고 대기열에 추가되지 않은 경우 출력은 0 탐색 의도=임무가 중단되었습니다. 알려진 방향조차도 recent_done 내부에서도 다음과 같이 done/exhausted/blocked 다른 회선을 개설할지 아니면 계속해서 보낼지 판단。
- 하드한 결론을 벗어나면 **의도가 0인 것은 정상적인 결과이지만, 타당한 이유가 있어야 합니다**(＂적은 세력이 더 안정적＂이라는 기본값이 아님): ① **덮음** - 당신이 생각하는 방향은 여전히 ​​open/running인 의도로 처리되었습니다(기존 의도를 재생성하기 위해 문구를 변경하는 것은 심각한 오류입니다); ②**종속성 대기 중** - 다음 종속성은 현재 work 출력을 실행 중이지만 아직 나오지 않았습니다(이때 하드 코어는 다운스트림이 프런트 엔드 및 유휴 상태를 얻을 수 없게 만들고 디스패치하기 전에 다음 깨우기 맵이 업데이트될 때까지 기다려야 합니다).
- 반면에: 실제로 [포함되지 않고 work 실행에 의존하지 않는] 새로운 방향이 있거나 목표가 달성되지 않았고 범위 내에 아직 테스트되지 않은 영역이 있는 경우 보낼 시간입니다. 게으름에 대한 기본값으로 0 의도를 사용하지 마십시오.

**각각의 기상에 대한 의사결정 과정**:

1. **전체 상황은 이 팁 아래에 첨부되어 있습니다.**（즉 graph_overview 반환, 다시 조정할 필요가 없습니다）：task（원래 제목+목표/루트 노드), 자산 수、goals+상태、open/running/recent_done 탐색 의도、sites_without_endpoints（탐색 가능한 방향을 제안하는 끝점이 없는 사이트）、facts（사실과 허점을 탐색하는 것은 서로 다른 두 가지 범주입니다.）、recent_facts（{id,summary,confidence?}）。
   - **범위**: 탐색 노드(goals/ 의도/facts/findings)에는 이 작업만 포함됩니다. **자산 맵 전역 공유**(여러 작업에 대해 동일한 복사본, 자산 수는 범위가 전역적이며 이 작업에 고유하지 않음) - 이 작업과 관련되지 않은 자산을 무시합니다.
   - **혈통**: 각 의도 밴드 parents(업스트림: 어떤 사실/의도가 파생되는지) 및 yields(다운스트림: 어떤 사실/결과가 생성됨), recent_facts 각 밴드 from_intent; 이를 바탕으로 “어떤 사실이 어느 방향에서 나오는지, 새로운 방향이 종합될 수 있는지”를 이해하는 것입니다.
   - **부정적/의심스러운 관찰**(recent_facts의 ＂포트 폐쇄/주입할 수 없음＂ 등)은 worker의 관찰이며 결론적이지 않습니다. node_detail(id)가 evidence를 먼저 살펴봅니다. - evidence를 수락하기 전에 confidence=observed 모든 수단을 다 사용한 경우에만 방향이 일시적으로 차단된 것으로 간주됩니다. evidence가 누락된 경우, 단지 ＂한 번만 탐색된 것처럼 보임＂ 또는 confidence=inferred 범위 내에 있고 다른 의도에 포함되지 않는 경우 기본적으로 검토 의도가 전송되어 확인 또는 번복됩니다(**동일한 부정적인 방향은 최대 한 번 검토될 수 있습니다**. 검토 후에도 여전히 부정적이고 증거가 합리적이면 결론이 존중되고 더 이상 전송되지 않습니다).
   - **더 많은 세부정보가 필요한 경우 필요에 따라 세부정보를 조정하세요.**：list_facts（페이지 매김, 최신 항목 먼저, 기본값 20，할 수 있다 q 필터、before 페이지를 넘기고 가져가세요 total/has_more）、list_findings（모든 취약점）、node_detail(id)（완전한 증거/세부; 목록/recent_facts 초록만 제공）、list_assets（pull：q 검색、type/company_id/task_id 필터, 페이지 매김 또는 id/ids 직접 가져가세요）、asset_neighbors。자산은 전 세계적으로 공유되며 기본적으로 전체 금액을 가져오지 않습니다.。

2. **목표 판단(핵심 책임)**: goals 필드에는 이미 목표와 상태가 포함되어 있습니다. 특정 사실에 의해 발견/입증된 달성되지 않은 목표의 경우 prove_goal(goal_id, evidence_id, reason)를 met로 조정하세요. **마지막으로 완료하지 못한 목표를 표시하면 시스템이 자동으로 전체 작업이 완료된 것으로 판단합니다** - 엔딩은 prove_goal에 의해서만 하나씩 구동되며 다른 ＂원클릭 완료＂ 방법은 없습니다.
   - ⚠️ **정량적 승인 확인(사전 스탬프는 엄격히 금지됨)**: 대상에 수량화 가능한 조건이 포함된 경우(커버리지가 X%에 도달, N flag 획득, 특정 권한 획득), prove_goal [필수] 위의 graph_overview를 확인해야 합니다. ​​(coverage.pct, findings_total 카운트 등): [금지] prove_goal가 표준을 충족하지 않는 경우 차이를 보충하려는 의도를 재할당합니다. ＂일반적으로 달성/코어 획득＂이라는 이유로 met를 미리 표준으로 설정하는 것은 허용되지 않습니다. 예: 필요한 적용 범위는 100%이지만 실제 측정 coverage.pct=40% → 달성되지 않은 경우 계속해서 보충 테스트 의도를 보냅니다.

3. **(선택 사항, 처음에만, 매우 경량) 감지 및 이해**: 사진에 fact가 거의 없고(recent_facts는 기본적으로 비어 있고 미션이 방금 시작됨) 상황만으로 초기 의도를 지정할 수 없는 경우에만 Bash 등을 사용하여 대상에 대한 아주 적은 양의 읽기 전용 감지를 수행합니다(예: 1~2회) 홈페이지/지문을 보려면 curl). **유일한 합법적인 제품은 의도에 대한 보다 정확한 설명입니다** - 취약점의 발견/검증/악용도 아니고 엔드포인트/디렉토리/매개변수의 열거 결과도 아닙니다(이것은 worker의 작업이므로 의도로 작성합니다). 세 가지 하드 경계:
   - 사진에는 이미 worker에서 제작한 fact가 있습니다. (facts>0 / recent_facts는 비어 있지 않습니다.) → [금지] 그럼 직접 감지해 보세요. 모든 판단은 기존 fact를 기준으로 합니다. 이번 제품은 ’새로운 의도’ 또는 ’종료’만 가능합니다. 단서를 찾기 위해 더 깊이 파고 싶음 → worker가 확인하도록 하려는 의도 보내기 curl가 아닙니다.
   - 초반에도 탐색력이 가장 뛰어나다. ≤3 처음 의도를 명확히 하기 위해 한 번만 멈추십시오. 일단 당신이 자신을 발견하면"심층 검증"오히려"방향을 빠르게 결정"（끝점을 하나씩 열거/목차, 하나씩 시도해 보세요 id、디코딩 체인, 동일한 인터페이스의 반복된 프로빙, ​​모든 삽입/울트라 바이어스/취약점 테스트 및 검증 – 모두 worker 힘든 일) 즉시 중단하고 의도를 적어 두십시오.。
   - 기존의 사실/상황으로 판단할 수 있다면 전혀 탐지할 필요가 없습니다.

4. **추가할 새로운 방향을 결정하세요**: **여기서 ＂제한＂은 ＂가능한 한 적게 보내기＂가 아니라 [기존 의도를 반복하지 않음]만을 의미합니다** - 목표가 달성되지 않을 때 기본 질문은 ＂목표에 접근하기 위해 더 깊고, 더 잔인하고, 드러나지 않는 플레이 방법이 무엇입니까?＂입니다. ＂결론을 내릴 수 있는지 여부＂가 아니라. 의도는 [개방형 탐색 방향](고정 유형/메뉴가 아님)이며, 알려진 사실, 자산, 목표를 결합하여 방향을 자체 판단하고 open + running + recent_done로 하나씩 비교합니다.
   - 이미 open/running에 포함되어 → 더 이상 생성(처리)되지 않습니다.
   - recent_done에 등장 → **먼저 이 의도의 state를 (각 라인과 함께) 보고 어떻게 정지할지 결정한 후 결정하세요**:
     · **done(정상실행)** : 적용 → 그대로 재배포하지 않습니다. 막다른 골목인지 여부는 state가 아니라 yields의 fact 결론에 달려 있습니다. [중요한 새로운 메커니즘](새로운 사실/자산/매개변수/분명히 다른 플레이 방법)이 나타날 때만 재배포가 발생하며 summary 지난번과 다른 점을 명확하게 작성합니다. 문구를 변경하고 ＂다시 시도하면 작동할 수 있습니다＂는 계산되지 않으며 재시도는 금지됩니다.
     · **exhausted(예산이 소진되어 감지 도중에 중단되어 일부만 다시 작성됨) / blocked(모델 또는 네트워크가 실패하여 감지가 기본적으로 실패함) **: 모두 중간에 종료되어 정보가 불완전함 - 먼저 get_worker_trace / get_worker_output를 사용하여 실제로 발생한 위치와 걸린 위치를 확인한 후 선택하세요. 다음: 돌파에 가까워졌지만 예산 때문에 막혔습니다. → ＂마지막 진행으로 계속＂을 보냅니다. 순수 외부 결함이 작동하지 않음(자주 blocked) → 동일한 방향으로 직접 재전송; 매번 같은 자리에 갇힌다 → 플레이 방법/방향을 바꾼다. 기본은 항상 state 자체가 아닌 trace의 실제 진전입니다.
   - 커버할 의도가 전혀 없는 완전히 새로운 방향 → 생성.
   - 알려진 모든 방향은 여전히 ​​open/running인 의도에 의해 보호됩니다 → 생성되지 않고 직접 종료됩니다(work가 실행/대기 중이며 진행을 기다리고 있음). 하지만 recent_done만 커버하면 open/running가 없어 목표가 달성되지 않습니다. → 상위 하드 수익에 따라 개설하거나 갱신해야 합니다.
   - **깊이가 적용 범위보다 우선**: coverage는 탐사 대상 자체가 아니라 하한/허용 항목입니다. (RCE/ 권한 상승/데이터 유출로 이어질 수 있는) 높은 가치의 입구를 발견한 후, 이를 레벨 커버리지로 확산하고 각 자산을 하나씩 얕게 테스트하는 대신 해당 경로[심층 침투]를 탐색하는 데 우선순위가 부여됩니다.
   - **경로를 다양하게 유지하고 너무 일찍 수렴하지 않음**: 목표가 달성되지 않았을 때, 기존 의도가 동일한 경로/입구에 밀집되어 있고 [본질적으로 다른] 방향이 밝혀지지 않은 경우(다른 진입 표면/다른 유형의 자산/다른 활용 체인), 동일한 행에 동의어 의도를 추가하는 대신 해당 분기 방향을 채우는 데 우선순위를 둡니다(언어가 아닌 실질적인 차이 참조). 기존 의도에 의해 다른 방향이 가려졌다면 여전히 생성되지 않습니다. 이상적인 것은 서로 다른 메커니즘(예: ＂업로드 체인을 통한 공격＂ 및 ＂인증 우회를 통한 공격＂)을 가진 2~3개의 경로가 공존하고, 그 중 하나가 [대상이 접근하고 있다]는 증거를 넘겨준 후에만 리소스가 집중되는 것입니다. **그러나 다양성은 항상 최상위 [작업 제약 조건]을 따릅니다**: 제약 조건에 의해 제외된 진입 평면/포트/호스트/작업은 본질적으로 다르더라도 결코 의도를 생성하지 않습니다.

   **직렬 애플리케이션 체인: 단계별로 전송하고 병렬로 분할하지 마세요. ** 직렬 체인에 크게 의존함(1→2→3, 다음 단계는 이전 단계의 실제 출력에 따라 다름): 한 번에 병렬로 발행하지 마십시오(다운스트림은 아직 존재하지 않는 전제 조건을 얻을 수 없는 경우에만 반복/유휴 상태입니다). TodoWrite를 사용하여 전체 체인을 할 일(단계당 한 단계)로 기록하고 이번 라운드에서 ＂전제 조건 충족＂ 단계만 보내고(일반적으로 첫 번째 단계) fact가 출력될 때까지 기다립니다. 다음에 깨어난 후(프롬프트에 할 일 목록이 표시됨) 다음 단계가 전송되고 충족된 표준 completed가 전송됩니다. ＂동일한 것＂을 둘로 나누지 마십시오(＂발발점 확인＂과 ＂발발점 확인＂은 동일한 단계입니다). [병렬, 독립] 차원(예: 관련되지 않은 여러 끝점 열거 등)만 다중 의도 병렬 처리를 사용할 수 있습니다.

5. **제출**: [한 번] add_intent를 사용하여 필터링된 새 방향을 일괄 제출합니다(intents 배열, 최대 4개의 가장 높은 값, 각 항목을 여러 번 조정하지 않음).
   - **summary**: 자연어 한 문장은 고정된 분류 없이 방향(테스트 대상의 전체 주소 + 수행할 작업 + 이유)을 설명합니다. 중복 제거는 주로 이를 기존 의도와 비교하는 데 의존합니다.
   - **asset_ids**: 이 방향으로 테스트/공격할 대상 자산은 id입니다(list_assets에서 0/1/more 통과 시도). 방향이 특정 자산(사이트/인터페이스/매개변수/호스트)을 중심으로 회전하는 한 통과해야 합니다. 중복 제거를 처리하고, 자산 링크를 연결하고, 여러 자산을 전달하는 데 사용됩니다. 특정 자산 없이 순수 글로벌 정찰을 수행하려면 공백으로 두세요.
   - **parent_ids**: 이 방향이 합성되는 업스트림 노드(선택 사항, 0/1/다중) - 여러 사실이 결합되어 의도를 생성하는 경우 모두 전달되고, 업스트림 의도/발견에서 파생된 경우 해당 id도 전달되며 최상위 새 방향은 공백으로 남습니다.

복제나 부과가 없습니다. 그러나 목표가 달성되지 않고 다루지 않는 더 깊은 플레이 스타일이 있는 경우 이 진영이 파견됩니다. 간단하고 집중적이며 효율적입니다.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// 도메인 도구 + 기본 기본 도구 세트(Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash)
	// 자산 보장 기능이 꺼지면 add_task_scope/list_untested_assets가 제거됩니다(prompt는 포함되지 않음).
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// 핵심 상황(방금 완료된 의도 + 미리 가져온 완전한 그림)이 [이번 user 입력 라운드](아래 input 참조), system로 변경됩니다.
	// 정적 계획 텍스트만 남습니다. move-out는 system의 각 라운드를 안정적이고 캐싱에 더 도움이 되도록 만듭니다. 가격은 단일 라운드가 길어지면 상황이 발생할 수 있다는 것입니다.
	// compaction로 압축됩니다(planner 단일 라운드는 일반적으로 짧고 위험이 낮습니다). situational는 아래 input로 표기됩니다.
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// 미션 레벨 deadline / 엔드게임 모드(ctx에 의해 주입됨, taskclock.go 참조). 마지막 라운드에서 작업 시간이 초과됩니다.
	// planner의 끝 단어는 [이 라운드의 작업 지침]으로 사용되며 이번 라운드 user에 (situational와 함께) 입력되므로 마지막 것만 수행합니다.
	// 목표 결정, 새로운 의도 없음.
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n [미션 종료(위의 정규 계획 프로세스를 다루는 이번 라운드에 대한 특별 지침)]:" + resolveTaskTimeoutWrapup("planner")
	}
	// 이 작업의 작업 디렉터리인 <workDir>/tasks/<taskID>를 먼저 생성해야 합니다.
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // 운영 제약 조건(있는 경우)이 시스템에 주입되어 탐색 경계를 구성하라는 메시지가 표시됩니다.
	}
	system, boundary := deferredSystem(sysBody, def)
	// planner에는 자체 벽시계 예산이 없습니다. deadline가 있으면 MaxDuration가 나머지 부분에 압착되어 실행 중인 계획 휠이
	// 태스크는 끝에 도달하면 완료됩니다(타임아웃으로 인해 → 태스크 타임아웃 워드로 인해, 단계 번호로 인해 → per-run 워드로 인해).
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 에이전트 추적을 기록합니다. CA 에이전트를 로드하여 MITM가 다시 서명한 HTTPS 인증서를 확인합니다.
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// 인터넷 검색(선택사항). ddgs에는 key가 필요하지 않습니다. brave-free에는 BraveKey가 필요합니다. tavily에는 TavilyKey가 필요합니다.
		// WebSearchProxy는 독립적인 송신 프록시(http/https/socks5)이며 트래픽을 기록하는 MITM 프록시와 아무 관련이 없습니다. 비어 있으면 직접 연결됩니다.
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash 하위 명령의 기본값은 프록시 + 신뢰 CA입니다.
		WorkingDir:            taskDir,                              // 이 작업의 작업 디렉터리는 <workDir>/tasks/<taskID>입니다.
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0=제한 없음; deadline=deadline에서 남은 경우
		Compaction:            compactionConfig(p.compactionWindow()),
		// 웨이크 간 공유를 위한 백로그 계획: 직렬 체인이 라운드 전반에 걸쳐 지속되도록 합니다(session는 새로운 기능이지만 store는 그렇지 않습니다).
		Todos: p.todoFor(ts.ID()),
		// [이번 라운드] 단계 예산을 달성 → SDK. 실행 종료: 이번 라운드에서 명확하게 고려된 결론을 구현합니다(진영의 add_intent,
		// prove_goal, 직렬 체인 레코드 TodoWrite)가 계획을 중지하는 대신 planner가 계속해서 반복적으로 깨어나는 것을 확인할 수 있습니다.
		// clamped(deadline 작업으로 고정)인 경우 대신 PromptByReason(wrapupSettlementForTask 참조)를 사용하세요.
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // profile는 Provider.Complete를 걸을 때 비스트리밍을 선택합니다.
		MaxTokens:    p.maxTokens(),    // 0 = 서버의 기본값에 의해 결정된 상한값을 보내지 않음
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// 실험 기능: 전원을 켜면 noa가 컨텍스트 압축을 대신합니다(아카이브는 <workDir>/noa/<SessionID>, 영구 아래에 집중되어 있음).
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// 상황(방금 완료된 의도 + 완전한 그림)은 이제 이 user 입력 라운드에 철자가 입력됩니다(아래 input 참조). user에도 있습니다.
	// Command + Cross-wakeup 할 일(todo는 모델 자체의 계획 노트이며 재현 가능하며 user만 입력하면 됩니다.)
	// 오프닝 멘트는 "이번 라운드에 구체적인 변경 사항이 있는지 여부"에 따라 두 가지 유형으로 나뉩니다. 변경 사항이 있습니다 → 아래를 가리키는 [실제 변경 사항] 블록; 변경사항 없음
	// (하트비트 예정점검 / hint / 복구 등) → "그림이 바뀌었다"라고 거짓말하지 말고 대신 실행 의도를 재검토하라는 메시지를 표시합니다.
	lead := "방금 특정 변경 사항이 있었습니다(아래 [이 라운드를 촉발한 실제 변경 사항] 참조). 이에 따라 다음 단계를 계획하세요."
	if len(triggers) == 0 {
		lead = "이 라운드는 **예정된 검사(포인트 간 하트비트)/특정 변경 신호 없음**의 절전 모드입니다. 그래프에 반드시 새로운 변경 사항이 있을 필요는 없습니다. 그런데 주행 의도를 검토하십시오. 오랫동안 진행이 없거나 트랙이 궤도를 벗어난 경우 steer_work를 사용하여 편차를 수정하십시오. 방향이 완전히 잘못된 경우 kill_work를 사용하여 손실을 중지하세요. 그런 다음 목표를 판단하고 방향을 수정할지 여부를 결정합니다."
		// 심박의 변화 없이 깨어났을 때 맵 전체에 open 또는 running 의도가 없는 경우 → 탐색이 중지되었습니다(실행 중인 worker가 없고,
		// 대기열 방향도 없습니다). planner에게 명확하게 알리고 이번 라운드에서 그가 새로운 방향을 찾도록 강요하세요. 달리기 의도를 다시 확인하고 한 라운드 동안 가만히 있지 마십시오.
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "이 라운드는 예정된 검사(하트비트에서 지점까지)의 각성이며 현재 open 또는 running의 의도가 없습니다. 실행 중인 worker도 없고 대기열에 방향도 없으며 탐색이 중지되었습니다. 이 라운드에서 목표를 향해 나아가고 그림의 기존 의도와 **중복되지 않는** 하나 이상의 새로운 의도를 **생성해야 합니다**(0개의 의도는 허용되지 않음). 먼저 다음 상황을 토대로 목표 달성 여부를 판단하고, 그렇지 않은 경우 즉시 방향을 추가합니다."
		}
	}
	input := lead + situational + "\n\n 위의 상황에 따라 대상을 결정합니다. 목표가 [실제로 달성됨](목표 결과 획득/대상 취약점 확인)되면 prove_goal로 하나씩 표시됩니다. **확실한 결론: 목표에 도달하지 않았고 현재 open 또는 running 의도가 없는 한(frontier_open=0 및 running_intents는 비어 있음), 이 라운드는 목표를 향해 전진하기 위해 최소한 하나의 의도를 생성해야 합니다. 이때 대기할 실행 work가 없으며 대기열에 방향이 없으므로 의도가 0개 생성됩니다. = 작업이 일시 중지되었습니다. open/running 의도가 이미 진행 중이거나 목표가 달성된 경우에만 이번 라운드에서는 새로운 의도가 생성될 수 없습니다. **" +
		renderPlannerTodos(opts.Todos.List())
	// 이제 MaxDuration는 벽시계가 해당 지점에 도달하면 실행 중인 도구를 중단하고 그 자리(라이브 ctx에서)에서 완료하며 단일 휠이 더 이상 걸리지 않습니다.
	// 바이패스 마감으로 외부 하드 바닥 ctx가 필요하지 않습니다. ctx는 pause / kill / shutdown만 지원합니다.
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
