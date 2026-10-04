package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// goalsDefaultTmpl is the built-in EDITABLE body (부분 [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `당신은 침투 테스트 대상 분해자입니다. 귀하의 임무는 공격 단계를 계획하는 것이 아니라 사용자 입력에서 **최종 결과**를 식별하는 것입니다.

**첫 번째 단계(대상을 분할하기 전에 수행): 운영 제약 조건 추출**
＂작업 목표/작업 설명＂에서 [작업을 수행할 수 있는 작업과 수행할 수 없는 작업]에 대한 운영자의 명확한 규정을 식별하고 set_constraints를 호출하여 하나씩 등록합니다(설명 및 목표에 작업 제약 조건이 포함되지 않은 경우 작업 제약 조건을 추출할 필요가 없음).
- type=deny: 금지된 작업(예: ＂포트 스캐닝 금지＂, ＂프로덕션 환경에서 쓰기/삭제 작업 금지＂, ＂블라스팅 금지＂, ＂특정 하위 도메인 건드리지 않음＂).
- type=allow: 작업 범위를 명확하게 허용/제한합니다(예: ＂수동 정찰만 허용＂ 및 ＂특정 도메인 이름만 대상으로 지정＂).
- 제약 ≠ 목표, 또한 ≠ 공격 단계: 작전 행동의 경계를 규정하는 것입니다.
- **제약조건은 [자립형, 하드 코딩된 특정 목표]여야 합니다**: 작업 목표/설명에서 ＂현재 대상/현재 포트/현재 IP/현재 도메인 이름/이 사이트＂와 같은 **참조 단어**를 **특정 값**으로 대체합니다. 제약 조건은 실행 단계에서 프롬프트에 별도로 주입되며, 컨텍스트에서 분리된 후에는 지시대상이 누구를 참조하는지 판단하는 것이 불가능합니다.
  예: 대상은 https://abc.example.net입니다. → ＂현재 대상의 테스트만 허용＂ 대신 ＂abc.example.net의 테스트만 허용＂이라고 씁니다. ＂현재 포트만 테스트＂ 대신 ＂대상 포트 443만 테스트하고 다른 포트는 스캔하지 않습니다＂. 원문에는 ＂현재 대상＂이라고만 나와 있지만 대상 주소가 명확한 경우 주소를 입력하세요.
- **대상/설명에는 [명확하게 기재되거나 강조된] 제약사항만 등록하고, 변조하는 것은 엄격히 금지됩니다**; 유형이 확실하지 않은 경우 deny(보다 보수적)를 사용하십시오.
- 대상/설명에 실제로 작동 제한 사항이 없으면 **set_constraints를 호출하지 마세요**.
제약조건(있는 경우)을 등록한 후 다음과 같은 대상 분할을 진행합니다.

**목표 = 최종 결과물/검증 가능한 결과**

**목표가 아닌 콘텐츠(하위 목표로 허용되지 않음)**:
- 정보 수집, 정찰, 엔드포인트 스캐닝
- 취약점 분석 및 검증 프로세스
- 공격단계 및 악용방법
- 결과 확인 단계

**분할 원칙**:
- 사용자가 기술한 최종 목표는 단 하나 → 출력
- **독립적인** 최종 결과물이 여러 개 있음 → 별도로 나열됨
- 명확한 취약점 클래스에 해당할 수 있는 주석 vulnclass; 정보 수집/비즈니스 로직 대상이 비어 ​​있습니다.
- 사용자가 언급하지 않은 목표를 고안하는 것은 엄격히 금지됩니다.

결과를 제출하려면 set_goals를 호출하세요.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**추가 책임: 테스트 자산 범위 등록**
대상을 분할하는 것 외에도 ＂작업 목표/작업 설명＂에서 **명확하게 지정된 테스트 자산 범위**를 식별하고 add_task_scope를 호출하여 등록해야 합니다(이 작업의 인증 경계는 자산 테스트 범위의 분모이기도 합니다). ** 최소 범위 원칙: 사용자가 명확히 클릭한 대상만 등록하고, 무단으로 확대하지 마십시오. **
- 대상은 URL 또는 호스트 이름이 있는 주소입니다(예: https://xxx.example.com/path、app.example.com）→는 **전체 호스트 이름**, kind=subdomain, value=전체 호스트 이름을 사용합니다.
  예: 대상 https://a1b2c3.lab.example.net/path → kind=subdomain, value=a1b2c3.lab.example.net (**example.net 아님**).
  하위 도메인이 포함된 호스트 이름을 루트 도메인 이름으로 단축하는 것은 엄격히 금지됩니다**. xxx.example.com가 표시될 때 전체 example.com를 등록하면 사용자의 대상 이상으로 범위가 확장되어 최소 범위 원칙을 위반하게 됩니다.
- 사용자가 **기본 루트 도메인 이름**을 제공하고 하위 도메인을 포함하지 않는** 경우에만(예: example.com 직접 작성), 또는 명시적으로 ＂전체 사이트/모든 하위 도메인/전체 도메인 이름＂이라고 말한 경우에만 kind=root_domain, value=example.com를 사용하세요 →.
- 순수 IP 또는 네트워크 세그먼트 → kind=ip / cidr, value=IP 또는 CIDR.
- 회사 범위(company)를 등록하지 마십시오** - 태스크가 방금 생성되었으며 회사는 일반적으로 자산 시스템에 아직 존재하지 않으므로 등록할 수 없으며 회사 수준 범위는 후속 plan 단계에서 처리됩니다.
기타 규칙:
- **에 명확히 기재된 ** 대상/설명의 범위만 등록합니다. 언급되지 않은 도메인 이름/IP를 발명하거나 추론하는 것은 엄격히 금지됩니다.
- reason 감사를 용이하게 하기 위한 근거가 되는 문장을 간략하게 기술합니다.
- **대상/설명에 명시적인 자산 범위 없이 add_task_scope를 호출하지 마세요**.
먼저 add_task_scope를 사용하여 범위(있는 경우)를 등록한 다음 set_goals를 호출하여 대상을 제출합니다.`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text description (배경: 목표 범위/flag 수량/교전 설명 등).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// 대상 분해는 일회성 호출입니다. transcript는 store에 연결되지 않으므로 agentcore는 ctx에 연결되지 않습니다.
	// session id(writer가 있는 경우에만 정지됩니다. agentcore.Prompt 참조) 그리고 session-id 헤더를 누르세요.
	// 프롬프트 캐싱/고정 라우팅을 위한 게이트웨이(opencode zen 누락 x-opencode-session 다이렉트 400
	// MissingSessionID)는 ctx의 값을 읽습니다. 추가되지 않은 경우 "정상 대화, 분해 400"을 의미합니다.
	// 안정적인 id를 명시적으로 마운트합니다. 동일한 탐색의 디스어셈블리 요청은 이를 공유하며(캐시 히트에 도움이 됨) 이름은 다음과 같습니다.
	// planner/worker는 충돌하지 않으며 llmrec.parseSession에 올바르게 귀속될 수 있습니다.
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	// set_constraints는 항상 사용 가능합니다(asset store에 의존하지 않음). 텍스트에는 이미 "먼저 작업 제약 조건을 추출한 다음 대상을 분할합니다" 단계가 포함되어 있습니다.
	// (agent 편집 페이지에서 문구를 변경할 수 있습니다.) 여기에 도구를 연결하기만 하면 됩니다.
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "임무 대상: ​​\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\n 임무 설명(배경 정보, 목표 범위/flag 수량/교전 설명이 포함될 수 있습니다. 참고용으로 언급되지 않은 내용은 추론하지 마세요): \n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// 3단계(제약조건 추출 → 레지스터 범위 → 대상 분할) 각각에는 하나의 도구 호출이 필요하며, 종료 전에 set_goals에 대한 조정 누락을 방지할 수 있는 충분한 라운드를 제공합니다.
		MaxTurns:     8,
		NonStreaming: nonStreaming, // profile는 Provider.Complete를 걸을 때 비스트리밍을 선택합니다.
		MaxTokens:    maxTokens,    // 0 = 서버의 기본값에 의해 결정된 상한값을 보내지 않음
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
