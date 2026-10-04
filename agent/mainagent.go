package agent

import (
	"context"
	"fmt"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// MainAgent is the thin human-interface orchestrator (docs §4.2 / §7). The human
// chats with it; it observes (read tools), and steers by injecting hints
// (→planner) or direct high-priority intents (→frontier). It does NOT run the
// autonomous intent-generation loop (that is the planner's job).
type MainAgent struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	tx              *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window          int                                    // context window in tokens (for compaction)
	windowFn        func() int                             // optional dynamic task-chain minimum
	maxTurns        int                                    // max agent turns per run (0 = unlimited)
	proxyAddr       string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert     string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch       WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir         string                                 // shared work dir (surfaced in prompt as artifact-output target)
	steerWork       func(intentID int64, msg string) error // engine callback: steer a running work (nil = off)
	nonStreamingFn  func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn    func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn     func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
}

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (m *MainAgent) SetNoaEnabled(fn func() bool) { m.noaEnabledFn = fn }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (m *MainAgent) SetNonStreaming(fn func() bool) { m.nonStreamingFn = fn }

func (m *MainAgent) nonStreaming() bool { return m.nonStreamingFn != nil && m.nonStreamingFn() }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (m *MainAgent) SetMaxTokens(fn func() int) { m.maxTokensFn = fn }

func (m *MainAgent) maxTokens() int {
	if m.maxTokensFn == nil {
		return 0
	}
	return m.maxTokensFn()
}

func NewMainAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *MainAgent {
	return &MainAgent{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns}
}

func (m *MainAgent) SetCompactionWindowResolver(fn func() int) { m.windowFn = fn }

func (m *MainAgent) compactionWindow() int {
	if m.windowFn != nil {
		return m.windowFn()
	}
	return m.window
}

// SetProxy points the main agent's WebFetch at the recording proxy plus the CA
// cert it trusts to verify HTTPS through it (empty addr = direct).
func (m *MainAgent) SetProxy(addr, caCert string) { m.proxyAddr, m.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the main agent (off by default).
func (m *MainAgent) SetWebSearch(o WebSearchOpts) { m.webSearch = o }

// SetSteerWork wires the engine callback that lets the main agent's steer_work
// tool inject a mid-run course-correction into a running work (nil = tool off).
func (m *MainAgent) SetSteerWork(fn func(intentID int64, msg string) error) { m.steerWork = fn }

// mainAgentDefaultTmpl is the built-in EDITABLE body (부분 [A]) of the main agent
// prompt, seeded into agent_prompts. Goal is a {{.Goal}} template var; the 가운데
// 제품 출력 사양 tail is code-owned (artifactSpec), appended after rendering.
const mainAgentDefaultTmpl = `공인 침투 테스트 시스템인가요?"주인 agent"，인간 운영자를 위한 인터페이스입니다. 스스로 탐색하지도 않고 스스로 의도를 지속적으로 생성하지도 않습니다(이것이 기획자의 일입니다). 당신의 책임：

1. 관찰: graph_overview / list_findings / list_facts / list_assets / get_worker_output를 사용하여 현재 진행 상황에 대한 사람들의 질문에 답하세요.
2. 조종(인간의 의도를 시스템에 주입):
   - 사람들은 ＂방향을 바꾸다/특정 유형의 취약점을 강조/특정 영역에 집중＂하고 싶어합니다. → add_hint로 프롬프트를 작성합니다(기획자가 다음에 읽을 것입니다).
   - ＂특정 대상을 즉시 테스트＂하고 싶어함 → add_intent를 사용하여 우선순위가 높은 인텐트(priority 8-10)를 직접 주입함. 시스템은 완료된 작업을 자동으로 실행 상태로 되돌리고 worker가 이 의도의 실행을 주도하도록 합니다. 실행 후에는 완료 상태로 돌아갑니다.
     **모든 작업 목표가 달성된 경우**(graph_overview의 goals는 모두 met입니다): 발행하기 전에 먼저 이 의도 뒤에 ＂달성할 새로운 결과＂가 있는지 확인합니다. 암시적이라면 추측한 목표를 사람들에게 한 문장으로 반복하고 **정식 목표로 등록할 것인지 물어보세요** - 사람들이 원함 → set_goals에 등록(그러면 작업은 정규 계획에 들어가고 기획자는 독립적으로 진행됩니다); 사람들은 그것을 원하지 않습니다 / 단지 일시적으로 탐색하고 싶어합니다 → add_intent만 이 문제를 발행하고, worker는 작업을 실행한 후 완료된 상태로 돌아갑니다(독립적으로 계속되지 않습니다). 이 의도가 명백히 일회성 검증이고 새로운 목표를 의미하지 않는다면 매번 묻지 않고 add_intent를 입력하면 됩니다.
   - 사람들은 ＂실행 의도(work)를 실시간으로 수정하고 싶어합니다(X로 가지 말고 Y에 집중하세요)＂ → steer_work를 사용하세요(중단 없음, 진행 손실 없음, worker는 다음 작업 전에 적용됩니다). 먼저 get_worker_output를 사용하여 무엇을 하고 있는지 확인하세요. 방향이 완전히 틀리면 대신 add_intent를 사용하고 새로운 의도를 설정하세요.
   - ＂달성할 최종 목표를 추가＂하고 싶은 사람들 → set_goals로 목표를 추가합니다. 시스템은 작업 맵에 목표를 기록하고 수동으로 재개를 클릭할 필요 없이 자동으로 완료/일시 중지된 작업을 실행 상태로 다시 가져와 계속 실행합니다(그런 다음 플래너는 목표 달성 여부를 다시 판단합니다).
   - 사람들은 ＂테스트 제약 조건 추가/변경(＂현재 포트만 테스트＂, ＂블라스팅 없음＂, ＂수동 정찰만＂과 같은 특정 유형의 작업을 허용/금지)＂을 원합니다. → set_constraints에 등록합니다(type=allow 허용/type=deny 금지). 탐사 경계를 설정하기 위한 다음 계획 단계에서는 프롬프트 단어 planner/worker와 함께 제약 조건이 주입됩니다. 또한 ＂제약 관리＂ 개요에서 추가, 삭제 및 수정할 수도 있습니다.
3. 인간의 언어로 간결하게 답하고, 무엇을 했는지 설명하세요.

현재 임무 목표: {{.Goal}}

결과를 꾸며내지 마십시오. 도구에서 반환된 실제 데이터를 기반으로만 답변합니다.`

func mainAgentSystem(goal, dataDir, workDir string) string {
	body := renderSystem("mainagent", mainAgentDefaultTmpl, MainVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Chat handles one human message and returns the assistant reply. emit, if
// non-nil, receives each execution step (thinking / tool_use / tool_result /
// text / result) so the main-agent session shows its work — exactly like the
// worker/planner sessions — not just the final answer.
func (m *MainAgent) Chat(ctx context.Context, taskID int64, mainSeg int, as *db.AssetStore, ts *db.ExplorationStore, goal, message string, emit func(db.Activity), notify, resume func(), notifyGoal, notifyHint func([]string)) (string, error) {
	tsx := NewToolSet(ts, "human")
	tsx.SetFindingRecorder(m.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.SetNotify(notify)         // 범용 웨이크업(전용 콜백 없이 쓰기 작업이 수행됨, debounced)
	tsx.SetResumeTask(resume)     // set_goals 새 대상 추가 → 완료/일시 중지된 작업을 뒤로 가져오기 running
	tsx.SetNotifyGoal(notifyGoal) // set_goals가 새 대상을 추가합니다 → planner에 메모 쓰기 "People added a new target:..." 트리거
	tsx.SetNotifyHint(notifyHint) // add_hint가 새로운 팁을 추가합니다 → planner에 메모 쓰기 "사람들이 N 전략 팁을 추가했습니다:..." 트리거
	tsx.steerWork = m.steerWork   // enable steer_work tool (nil = unavailable)
	// 도메인 도구 + 기본 기본 도구 세트(Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash)
	// 자산 보장 기능이 꺼지면 add_task_scope/list_untested_assets가 제거됩니다(prompt는 포함되지 않음).
	base := append(tsx.DropCoverageTools(tsx.MainAgentTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "mainagent", base)
	defer cleanup()
	// 이 작업의 작업 디렉터리인 <workDir>/tasks/<taskID>를 먼저 생성해야 합니다.
	mainDir := ensureRunDir(m.workDir, taskID, 0)
	ctx = intercept.WithReviewWorkingDirectory(ctx, mainDir)
	system, boundary := deferredSystem(mainAgentSystem(goal, m.workDir, mainDir), def)
	opts := agentcore.Options{
		Provider:        m.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 에이전트 추적을 기록합니다. CA 에이전트를 로드하여 MITM가 다시 서명한 HTTPS 인증서를 확인합니다.
		WebFetchProxy:   m.proxyAddr,
		WebFetchCACert:  m.proxyCACert,
		// 인터넷 검색(선택사항). ddgs에는 key가 필요하지 않습니다. brave-free에는 BraveKey가 필요합니다. tavily에는 TavilyKey가 필요합니다.
		// WebSearchProxy는 독립적인 송신 프록시(http/https/socks5)이며 트래픽을 기록하는 MITM 프록시와 아무 관련이 없습니다. 비어 있으면 직접 연결됩니다.
		EnableWebSearch:       m.webSearch.Enabled,
		WebSearchBackend:      m.webSearch.Backend,
		BraveSearchAPIKey:     m.webSearch.BraveKey,
		TavilySearchAPIKey:    m.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: m.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  m.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   m.webSearch.DeepSeekModel,
		WebSearchProxy:        m.webSearch.Proxy,
		BashEnv:               proxyEnv(m.proxyAddr, m.proxyCACert), // Bash 하위 명령의 기본값은 프록시 + 신뢰 CA입니다.
		WorkingDir:            mainDir,                              // 이 작업의 작업 디렉터리는 <workDir>/tasks/<taskID>입니다.
		ToolOutputDir:         cmdOutDir(mainDir),
		MaxTurns:              m.maxTurns,                             // 0 = unlimited (configurable in agent management)
		Compaction:            compactionConfig(m.compactionWindow()), // long chats stay within the window
		Todos:                 actool.NewTodoStore(),                  // 순전히 계획 목적을 위한 세션 수준 임시 작업(TodoWrite)은 종료 시 손실됩니다.
		// 적중 예산(단계 수) → SDK 실행 종료: 사용자에게 진행 요약을 출력합니다. Prompt 및 마감 라운드 수는 백그라운드에서 편집할 수 있습니다(기본값은 10라운드).
		Settlement:   wrapupSettlement("mainagent", nil),
		NonStreaming: m.nonStreaming(), // profile는 Provider.Complete를 걸을 때 비스트리밍을 선택합니다.
		MaxTokens:    m.maxTokens(),    // 0 = 서버의 기본값에 의해 결정된 상한값을 보내지 않음
	}
	if m.tx != nil { // persist raw human↔AI conversation; one accumulating file per segment
		opts.Transcript = m.tx
		// Segment 0 keeps the legacy "exp%d-main" name so existing transcripts still
		// load; each new session (seg>=1) gets its own file for a clean context.
		opts.SessionID = fmt.Sprintf("exp%d-main", ts.ID())
		if mainSeg > 0 {
			opts.SessionID = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
		}
	}
	// 실험 기능: 전원을 켜면 noa가 컨텍스트 압축을 대신합니다(아카이브는 <workDir>/noa/<SessionID>, 영구 아래에 집중되어 있음).
	// session id 및 transcript에는 아카이브와 복구를 정렬하는 동일한 규칙(세그먼트 인식)이 있습니다.
	noaSession := fmt.Sprintf("exp%d-main", ts.ID())
	if mainSeg > 0 {
		noaSession = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
	}
	enableNoa(&opts, m.noaEnabledFn, m.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close()
	// reload the prior conversation from the transcript so the agent has context
	// across turns (each Chat is a fresh session; without this it can't see earlier
	// messages). First turn: no file yet → Resume loads nothing and proceeds.
	if m.tx != nil {
		_ = s.Resume(opts.SessionID)
	}
	// C2: this session is fresh each turn; re-unlock skill-gated MCPs from prior
	// Skill() calls in the reloaded history so revealed tools stay callable.
	seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
	text, _, err := captureRunSession(ctx, s, message, func(r db.Activity) {
		if emit != nil {
			r.Worker = "mainagent"
			emit(r)
		}
	})
	return text, err
}
