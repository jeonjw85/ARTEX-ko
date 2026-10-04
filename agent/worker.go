package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Worker is an LLM work agent (docs §4.4): it claims ONE intent, completes it
// with real tools (Bash: kali tooling through the recording proxy), writes the
// FACTS it found back into the graph, and stops. It does NOT generate new
// directions (that is the planner's job) and does NOT keep exploring toward the
// goal on its own. Multiple workers run concurrently as goroutines.
// WebSearchOpts is the web-search backend selection the server pushes into each
// agent (planner/worker/main). Enabled=false leaves the web_search tool off.
// Backend is "ddgs" (no key), "brave-free" (BraveKey required), "tavily"
// (TavilyKey required), or "deepseek" (DeepSeek* required, filled from the
// active LLM profile). It maps directly onto agentcore.Options.
// Proxy is a dedicated egress proxy for the search request (http/https/socks5),
// independent of the traffic-recording MITM proxy — set it when the search endpoint
// is only reachable via a VPN/SOCKS proxy. Empty = direct.
//
// deepseek 백엔드의 특성은 다른 세 가지 백엔드와 다릅니다. DeepSeek에는 직접 호출 가능한 검색 인터페이스가 없습니다.
// 검색은 Anthropic 호환 messages 인터페이스(web_search_20250305 server) 내부에만 존재합니다.
// tool), 따라서 각 검색은 하나의 모델 호출을 소비하고 검색 요청은 DeepSeek 서버에서 발행됩니다.
// 이 시스템 Proxy를 통과하지 않으면 들어오는 트래픽의 흔적이 없습니다.
type WebSearchOpts struct {
	Enabled   bool
	Backend   string
	BraveKey  string
	TavilyKey string
	Proxy     string
	// 현재 활성화된 LLM 구성의 DeepSeek*(anthropic 형식의 DeepSeek 공식 엔드포인트만 해당),
	// 별도로 구성되지 않으며 LLM 구성 전환으로 변경됩니다.
	DeepSeekBaseURL string
	DeepSeekAPIKey  string
	DeepSeekModel   string
}

type Worker struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	workDir         string
	proxyAddr       string
	proxyCACert     string            // recording proxy's CA cert path (for WebFetch HTTPS verify)
	webSearch       WebSearchOpts     // web_search tool backend selection (off by default)
	tx              *transcript.Store // raw LLM conversation persistence (nil = off)
	window          int               // context window in tokens (for compaction)
	windowFn        func() int        // optional dynamic task-chain minimum
	maxTurns        int               // max agent turns per run (0 = unlimited)
	// runTimeout is the wall-clock budget for the main exploration of one intent
	// (0 = unlimited). When it fires, the run is cut and a settlement round is
	// forced so already-identified facts get written back instead of being lost.
	runTimeout time.Duration
	// extraTools are host-provided tools (e.g. traffic query, oast) appended to
	// the worker's graph write-back tools.
	extraTools []actool.CoreTool
	// injectConstraints resolves whether this task's operation constraints get
	// injected into the worker system prompt. Read per run so the settings toggle
	// takes effect without rebuilding the agent. nil = inject (default).
	injectConstraints func() bool
	// nonStreamingFn resolves whether this run uses the non-streaming (Complete)
	// path. Read per run so a profile/task toggle takes effect without rebuilding
	// the agent. nil = streaming (default).
	nonStreamingFn func() bool
	// noaEnabledFn resolves whether this run uses the experimental noa context-
	// compression mechanism. Read per run, like nonStreaming. nil = off (built-in
	// compaction).
	noaEnabledFn func() bool
	// maxTokensFn resolves the per-reply output cap in tokens, on the same
	// per-run basis. nil or 0 = send no cap and let the endpoint decide.
	maxTokensFn func() int
}

// WorkerSessionID returns the stable transcript key used by a worker intent.
// Worker slots are reusable, so the intent id (rather than work#N) is the
// session identity. Keep this helper public so the Worker message API and UI
// can refer to exactly the conversation that will be resumed.
func WorkerSessionID(explorationID, intentID int64) string {
	return fmt.Sprintf("exp%d-worker-i%d", explorationID, intentID)
}

const workerChatMarkerPrefix = "<!-- ARTEX_WORKER_CHAT:"

func workerChatMarker(requestID string) string {
	return workerChatMarkerPrefix + requestID + " -->"
}

func hasWorkerChatMessage(messages []llm.Message, requestID string) bool {
	marker := workerChatMarker(requestID)
	for _, message := range messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Text(), marker) {
			return true
		}
	}
	return false
}

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default). Read per
// run so a profile or task-chain toggle takes effect without rebuilding.
func (w *Worker) SetNonStreaming(fn func() bool) { w.nonStreamingFn = fn }

func (w *Worker) nonStreaming() bool { return w.nonStreamingFn != nil && w.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (w *Worker) SetNoaEnabled(fn func() bool) { w.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (w *Worker) SetMaxTokens(fn func() int) { w.maxTokensFn = fn }

func (w *Worker) maxTokens() int {
	if w.maxTokensFn == nil {
		return 0
	}
	return w.maxTokensFn()
}

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the worker system prompt. nil = inject (default).
func (w *Worker) SetConstraintInject(fn func() bool) { w.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (w *Worker) wantConstraints() bool { return w.injectConstraints == nil || w.injectConstraints() }

// SetRunTimeout configures the per-intent wall-clock budget for the main
// exploration (0 = unlimited). When it fires, the SDK settlement phase still runs
// so facts are never lost to a timeout. Safe to call before Execute.
func (w *Worker) SetRunTimeout(run time.Duration) {
	w.runTimeout = run
}

// settleWrapUpPrompt is injected by the SDK settlement phase when a worker hits its
// turn/time budget: stop probing, write back what was found, then end with a
// plain-text one-liner (which becomes this run's displayed result).
const settleWrapUpPrompt = "예산 부족으로 인해 곧 종료될 예정입니다. 더 이상 명령/프로브를 실행하지 마십시오. 다음 순서를 따르십시오. (1) 식별했지만 다시 작성하지 않은 콘텐츠를 하나씩 다시 작성하십시오. 새 자산에는 insert_assets를 사용하고, 결론/사실을 탐색하려면 record_fact를 사용하고, 취약점을 확인하려면 report_finding를 사용하십시오. (2) **마지막으로 단일 문장의 일반 텍스트**를 사용하여 수행한 작업과 얻은 주요 결론을 요약합니다(이 문장은 이 실행의 결과로 표시되며 출력되어야 함)."

func NewWorker(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int, extra ...actool.CoreTool) *Worker {
	return &Worker{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, extraTools: extra}
}

// defaultToolsExcept returns actool.DefaultTools() minus the named tools (by
// CoreTool.Name()). Used to trim SDK default tools an agent shouldn't have.
func defaultToolsExcept(exclude ...string) []actool.CoreTool {
	drop := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		drop[n] = true
	}
	all := actool.DefaultTools()
	out := make([]actool.CoreTool, 0, len(all))
	for _, t := range all {
		if !drop[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

func (w *Worker) SetCompactionWindowResolver(fn func() int) { w.windowFn = fn }

func (w *Worker) compactionWindow() int {
	if w.windowFn != nil {
		return w.windowFn()
	}
	return w.window
}

// SetProxy configures the recording proxy address that workers route target
// traffic through, plus the CA cert path WebFetch trusts to verify HTTPS through
// that MITM proxy. Empty addr disables the hint.
func (w *Worker) SetProxy(addr, caCert string) { w.proxyAddr, w.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for this worker (off by default).
func (w *Worker) SetWebSearch(o WebSearchOpts) { w.webSearch = o }

// proxyEnv builds the Bash-subprocess env that routes child-command HTTP through
// the egress proxy (the recording MITM when capture is on, or the global proxy
// directly when it is off) and, only when a MITM CA is present, makes the common
// toolchain trust it — so tools need no manual -x/--proxy/-k. Each ecosystem reads
// a different CA var (verified empirically): SSL_CERT_FILE→curl/urllib/Go/openssl,
// REQUESTS_CA_BUNDLE→python requests (it ignores SSL_CERT_FILE), CURL_CA_BUNDLE→curl,
// GIT_SSL_CAINFO→git, NODE_EXTRA_CA_CERTS→node; NODE_USE_ENV_PROXY makes Node 24+
// honor the proxy vars. ALL_PROXY is set too so a socks5 egress proxy (which curl
// only reads from ALL_PROXY, not HTTP(S)_PROXY) works in the capture-off path.
// Empty proxyAddr → nil (direct, unchanged env).
func proxyEnv(proxyAddr, caCert string) []string {
	if proxyAddr == "" {
		return nil
	}
	env := []string{
		"HTTP_PROXY=" + proxyAddr, "HTTPS_PROXY=" + proxyAddr,
		"http_proxy=" + proxyAddr, "https_proxy=" + proxyAddr,
		"ALL_PROXY=" + proxyAddr, "all_proxy=" + proxyAddr, // socks5 egress: curl reads only this
		"NODE_USE_ENV_PROXY=1", // Node 24+: honor HTTP(S)_PROXY in built-in fetch/http
	}
	if caCert != "" {
		env = append(env,
			"SSL_CERT_FILE="+caCert,
			"CURL_CA_BUNDLE="+caCert,
			"REQUESTS_CA_BUNDLE="+caCert,
			"GIT_SSL_CAINFO="+caCert,
			"NODE_EXTRA_CA_CERTS="+caCert,
		)
	}
	return env
}

// workerDefaultTmpl is the built-in EDITABLE body (부분 [A]) of the worker system
// prompt, seeded into agent_prompts. The trafficTool block and the 중간 제품 출력 프로토콜
// are NOT here — they are code-owned and appended by workerSystem after rendering
// (부분 [B]/[C]), so editing the DB body can never drop them.
const workerDefaultTmpl = `귀하는 네트워크 보안 플랫폼 공인 침투 테스트 시스템(work agent)의 ＂수행자＂입니다. 당신은 [의도](방향을 탐구하기 위한 한 문장)를 받고, 당신의 유일한 책임은 이 의도를 완성하고, 결과를 다시 지식 그래프에 기록한 다음 반환을 중단하는 것입니다. **

**경계(빨간색 선)**:
1. **주어진 한 가지 의도만 수행하세요**. **원래 의도를 탐색할 때 추가 탐색할 가치가 있는 단서**(오류 보고 누출 경로, 다른 자산과 연결될 수 있는 지점, 다른 사용 체인으로 의심되는 입구)를 엿볼 수 있다면 **fact에서 summary를 클릭하여 기획자에게 넘겨주세요**.
2. 처음으로 차단되었다고 해서(payload는 필터링됨 / 404 / 에코 없이 주입됨) 탐색되었다는 의미는 아닙니다. 원래 의도의 우회 방법을 모두 거친 후 결론을 출력합니다.
3. 승인된 범위 내에서만 작동하십시오. 시스템 프롬프트 상단에 [Operational Constraint]가 있는 경우 우선순위가 가장 높은 빨간색 선입니다. 각 명령/감지는 실행 전 자체 점검을 하며, 이를 위반할 경우 (받은 의도에 해당하더라도) 실행되지 않습니다.

**발견한 대로 다시 작성**(그림에 쓴 내용만 중요하며 마음/말에 있는 내용은 중요하지 않습니다. 결과가 나올 때마다 즉시 기록하고 단계 수가 소진될 때까지 저장하지 말고 버립니다). 답장을 보내는 세 가지 방법, 사진을 함께 묶지 마세요:
- **새 자산/리소스 → insert_assets(자산 맵)**: 하위 도메인/service/endpoint/지문/자격증명 및 기타 모든 자산[자체]. **여기에는 자산만 등록됩니다. 탐사 결론/판단은 여기에 기록되지 않습니다. record_fact를 사용하세요. **
- **탐사 결론/사실 → record_fact (탐사 지도, intent_id 통과)**: 사용하세요. **다수의 관찰은 [하나의] 사실로 요약됩니다**(summary, 한 문장 요약 + 요약을 확장하기 위한 세부 작성, 실제 실행 프로세스에 의존), 각 속성에 대해 하나의 속성이 없으며 일반적으로 각 의도에 대해 하나만 있습니다. 분할하면 지도가 무한히 확장됩니다. - **기본적으로 하나 작성하고 병합할 수 있는 모든 것을 detail**에 병합합니다. ＂서로 완전히 독립적이고 병합할 수 없다＂는 결론이 있는 경우에만 facts를 사용하십시오. 어레이 스트라이핑은 규칙이 아닌 드문 예외입니다. **증분만 쓰기**: 이번에 [새로 얻은 것]만 기억하고, 기존 사실을 바꿔서 다시 쓰지 마십시오(기존 사실만 확인하고 새로 추가한 내용이 없으면 기억할 필요 없음). **실제로 본 것만 작성하세요**: evidence(한 줄: 명령 + 이를 가장 잘 증명할 수 있는 출력 한두 줄, 간결함, 세부 정보는 detail에 있음), 라벨 confidence(observed=직접 본 / inferred=현상에서 추론)을 제공합니다.
- **취약성 확인 → report_finding(PoC를 포함한 탐색 맵이 intent_id로 전달됨)**: **이번에 실제로 트리거하고 재현 가능한 증거(요청/응답 또는 명령 출력)를 얻은 경우에만 사용하세요**. 확인 시 ＂외부 취약점 라이브러리/업데이트 로그/코드 diff 유추＂에 ＂CVE에 대한 버전/지문 매칭＂ 또는 ＂삽입 가능한 것으로 보이는 매개변수＂를 사용하는 것은 엄격히 금지됩니다. 실제 트리거링 대신 CVE 라이브러리를 사용하거나 패치 버전을 비교하지 마십시오. 발동은 안 되지만 의심이 갑니다 → record_fact를 이용해서 inferred를 적어주세요. 사실(의심점+발동되지 않은 이유)을 기획자에게 제출합니다. 그냥 finding로 기억하지 마세요.


이 의도를 완성한 후 한 문장을 사용하여 자신이 한 일과 어떤 사실을 답장했는지 요약하세요.`

// workerTrafficBlock is 부분 [B]: the traffic-tool note, code-injected only when
// traffic capture (recording) is on — i.e. the traffic_* tools actually exist.
// Gated on recording, NOT on the egress proxy: a global proxy with capture off
// routes traffic but records nothing, so the tools would not be there. Not stored,
// not editable.
func workerTrafficBlock(recording bool) string {
	if !recording {
		return ""
	}
	return "\n\n**교통 도구**：\n- traffic_search / traffic_get / traffic_blob：응답을 검토하고 방문한 리소스를 찾습니다.，**교통상황을 먼저 확인하고 반복하지 마세요 curl 같은 URL**。traffic_search **지정해야 합니다. host**、기본적으로만 반환 3 매우 가벼운 인덱스(id/method/url/status/resp_len，응답 내용 없음)，더 명시적인 크기 조정이 필요함 limit；사용 가능 body_contains 요청 중/응답 본문에서 전체 텍스트 검색(적어도 3 비밀번호 찾기와 같은 하위 문자열 및 한국어를 지원하는 문자/열쇠/오류 신고/인트라넷 주소)；특정 기사의 원문을 읽으려면 다음을 사용하세요. traffic_get(id)，너무 큰 텍스트는 다음과 같이 표시됩니다. @blob sha256:<hash>，사용 traffic_blob(hash) 섹션의 전체 텍스트 가져오기。"
}

// artifactSpec is [C]: the code-owned, non-editable tail appended to every
// pentest agent's prompt — intermediate artifacts must land in the shared work
// dir, never /tmp. Guaranteed present regardless of how the DB body is edited.
func artifactSpec(dir string) string {
	return "\n\n**중간 산출물 저장 규칙**: 스크립트, payload, 응답 본문, 임시 데이터 등은 모두 작업 디렉터리 **" + dir + "**에 저장하세요. 상대 경로 또는 이 디렉터리의 절대 경로를 사용할 수 있습니다. **/tmp나 다른 디렉터리에는 저장하지 마세요**."
}

// workerArtifactSpec is the worker's 부분 [C]: its per-intent run dir is pre-created
// by the engine (ensureRunDir), so it just writes relative paths there — no manual
// mkdir, no cross-worker name collisions.
func workerArtifactSpec(runDir string) string {
	return "\n\n**중간 산출물 저장 규칙**: 스크립트, payload, 응답 본문, 임시 데이터 등은 모두 탐색 의도의 전용 디렉터리 **" + runDir + "**에 상대 경로로 저장하세요. 디렉터리는 이미 생성되어 있습니다. **/tmp나 다른 디렉터리에는 저장하지 마세요**."
}

// ensureRunDir builds and creates an agent's working directory under base:
// <base>/tasks/<taskID> for planner/main; <base>/tasks/<taskID>/i<intentID> for a
// worker (intentID<=0 → task dir only). The "tasks/" segment groups per-task dirs
// symmetrically with the chat agent's "sessions/<sessionID>". Best-effort mkdir — on
// failure, writes fail the same way an unwritable CWD would.
func ensureRunDir(base string, taskID, intentID int64) string {
	dir := filepath.Join(base, "tasks", strconv.FormatInt(taskID, 10))
	if intentID > 0 {
		dir = filepath.Join(dir, "i"+strconv.FormatInt(intentID, 10))
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// cmdOutDir is the SDK large-tool-output spill dir under an agent's run dir.
func cmdOutDir(dir string) string { return filepath.Join(dir, "cmd-output") }

func workerSystem(proxyAddr, caCert, dataDir, runDir string) string {
	body := renderSystem("worker", workerDefaultTmpl, WorkerVars{ProxyAddr: proxyAddr, DataDir: dataDir, Now: nowStr()})
	// caCert is present only when the recording MITM is on, which is exactly when
	// the traffic_* tools are registered — so it gates the traffic-tool note.
	// Optional finding guidance is added for every role after tool resolution.
	return body + workerTrafficBlock(caCert != "") + workerArtifactSpec(runDir)
}

// renderIntentTask formats the claimed intent for the worker's launch USER message:
// the intent is the worker's whole job. It used to live in the system prompt; it now
// rides in the first user turn (together with the situational overview) so the system
// prompt stays static/role-only — same move as the planner's situational block.
// intentAssetIDs pulls the intent's target asset ids out of its payload
// (planner's add_intent stores them as a numeric asset_ids array). nil on absence
// or malformed payload.
func intentAssetIDs(intent *db.Node) []int64 {
	if intent == nil {
		return nil
	}
	var p struct {
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.Unmarshal(intent.Payload, &p); err != nil {
		return nil
	}
	return p.AssetIDs
}

func renderIntentTask(intent *db.Node) string {
	return fmt.Sprintf("\n\n【받은 의도(이번에 유일한 작업: 이것만 하고, 사실만 생산하고, 끝나면 중지)）】：\n%s\n탐색 의도 id: %d（답장하다 record_fact / report_finding 퍼뜨려라）", string(intent.Payload), intent.ID)
}

// renderWorkerGraphOverview folds the global situational snapshot into the worker's
// launch USER message for AWARENESS ONLY. The framing is deliberately strong: the overview
// must NOT widen the worker's job — it still does only its assigned intent. Its sole
// purpose is letting the worker read context (existing facts/assets/hints)
// so it avoids redundant work and doesn't re-derive what others already found.
func renderWorkerGraphOverview(data map[string]any) string {
	// coverage는 기획자가 "어떤 측정 유형이 적은지 / 범위를 확장할지 여부"를 판단하는 신호이고, worker는 "얻는 것만 수행"하는 것입니다.
	// 그 의도, 발견된 점을 쫓지 마세요"는 책임 경계에 어긋납니다 → worker 보기에서 제거되었습니다. data는 이번에 worker입니다.
	// 독점적인 새로운 map, 키 삭제는 planner에 영향을 미치지 않습니다.
	delete(data, "coverage")
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back silently: the worker just won't have the global context
	}
	return "\n\n [전역 탐사 상황(읽기 전용, 전체 그림에 의도를 넣는 데 도움이 됨)]: \n" +
		"다음은 전체 임무의 현재 탐색에 대한 개요입니다. 여기에는 두 가지 목적이 있습니다. 하나는 다른 사람들이 발견한 내용을 알고 이를 반복하지 않는 것입니다. 다른 하나는 자신의 의도를 탐색할 때 전체 상황과의 관계를 생각하게 하는 것입니다. \n" +
		"**다양성은 좋은 것입니다**: 원래 의도를 탐색할 때 깊이 생각하고 깊이 생각하세요. 유일한 제한은 실제로 다른 의도를 구현하기 위한 조치를 취하지 않는다는 것입니다(이는 기획자가 예약한 또 다른 worker 문제입니다). 귀중한 단서(자산 간 연결, 다른 활용 체인으로의 의심되는 진입, 글로벌 수준의 의심스러운 지점)가 떠오를 때마다 반드시 fact에 작성하여 기획자에게 전달하세요. 이는 필수가 아닌 중요한 결과물입니다. 스스로 삼키는 것보다 한 가지라도 더 보고하고 기획자에게 판단을 맡기는 것이 좋다. \n" +
		string(b)
}

// Execute runs one intent. hooks (the per-task Guard) gates every tool call; may
// be nil. emit, if non-nil, receives one ActivityRecord per execution step.
// notifyFinding, if non-nil, is called (intentID, summary) when this worker writes
// a finding (report_finding) so the task's planner wakes mid-flight — with context
// on which intent found what — instead of waiting for the worker to finish.
// Returns the terminal reason (so the engine can distinguish completed vs
// max_turns) and a per-kind breakdown of what was written back (so an intent that
// explored but persisted nothing isn't mistaken for done, and the engine can log
// facts/assets/findings separately instead of lumping them under "facts").
func (w *Worker) Execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string)) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, "", "")
}

// ExecuteWithMessage runs the next turn in the same intent conversation with a
// human-authored message. The HTTP handler does not edit the transcript;
// agentcore records the message as a normal user turn when this Worker starts.
// This keeps Worker continuation identical to the regular agent chat flow.
func (w *Worker) ExecuteWithMessage(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, strings.TrimSpace(requestID), strings.TrimSpace(message))
}

func (w *Worker) execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	tsx := NewToolSet(ts, name)
	tsx.SetFindingRecorder(w.findingRecorder)
	tsx.SetTaskID(taskID)
	coverageEnabled := as == nil || as.CoverageEnabled(taskID)
	tsx.SetCoverageEnabled(coverageEnabled)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetOwnerNode(intent.ID)         // assets this worker discovers anchor to its intent → visible to the task
	tsx.SetEnrich(enr)                  // async DNS/HTTP auto-completion for assets this worker writes
	tsx.SetNotifyFinding(notifyFinding) // report_finding가 떨어지면 그 자리에서 planner를 깨우고 "어떤 의도 + finding"를 가져옵니다.
	// base = built-in worker tools ∪ host tools (traffic) ∪ default tools (incl. Bash);
	// then augment with the agent's visible skills/MCP. During the SDK settlement
	// phase, Bash is hidden via Settlement.DisabledTools (no local gating needed).
	base := append(tsx.WorkerTools(), w.extraTools...)
	// worker는 의도적으로 MultiEdit/Glob/Grep를 제공하지 않습니다. 파일은 Edit를 사용하도록 정제되고 검색은 Bash(grep/find)로 수행됩니다.
	// 도구 표면을 통합하고 가치가 낮은 호출을 줄입니다. 나머지 SDK 기본 도구(Read/Write/Edit/LS/Bash/Sleep)는 평소와 같습니다.
	base = append(base, defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts), IntentID: intent.ID})
	tools, def, cleanup := AugmentTools(ctx, "worker", base)
	defer cleanup()

	// 의도는 worker의 [run 전체에 걸쳐 유일한 책임이자 불변] → 시작 명령 및 의도에 의해 고정된 대상 자산과 함께
	// 원본 데이터를 system prompt: system에 넣습니다. run를 다시 입력할 때마다 compaction에 의해 억제되지 않습니다.
	// 의도는 항상 긴 run에 존재하며 계속할 때 transcript 기록이 첫 번째 메시지를 유지하는지 여부에 의존하지 않습니다. 가격은 system입니다
	// per-intent 휘발성 데이터를 혼합하고 교차 의도 캐시 재사용을 잃습니다. 이는 의도적인 절충입니다(의도 손실은 token를 아끼는 것보다 훨씬 더 심각합니다).
	// planner "user turn"가 있는 포크는 의도적인 것입니다. planner 자체는 생산용이며 단일 mandate는 없습니다.
	// worker 예. user를 시작하라는 메시지에는 [전역 상황 overview]만 남아 있습니다. stale를 다운그레이드하고 허용할 수 있으며 문제 없이 억제됩니다.
	// 이 목적을 위한 전용 작업 디렉토리는 <workDir>/tasks/<taskID>/i<intentID>입니다. 엔진쪽이 먼저 만들어집니다.
	runDir := ensureRunDir(w.workDir, taskID, intent.ID)
	// The run-wide intent is not the current tool action. Do not forward it or
	// inherit a parent run's background into the action reviewer.
	ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
	overview := renderWorkerGraphOverview(tsx.graphOverviewData())
	sysBody := workerSystem(w.proxyAddr, w.proxyCACert, w.workDir, runDir)
	if w.wantConstraints() {
		sysBody += constraintBlock(ts) // 작업 제약 조건(있는 경우)이 시스템 프롬프트에 삽입되고 실행 중에 worker가 엄격하게 준수됩니다.
	}
	// 의도 블록 → 의도 고정 자산 블록 → 시작 지침, system 끝에 순서대로 추가합니다(constraintBlock와 동일한 추가 방법).
	sysBody += renderIntentTask(intent)
	if as != nil {
		if ids := intentAssetIDs(intent); len(ids) > 0 {
			if assets, err := as.GetByIDs(ids); err == nil && len(assets) > 0 {
				if b, err := json.Marshal(assets); err == nil {
					sysBody += "\n\nasset_ids의 원래 의도는 해당 대상 자산: \n입니다." + string(b)
				}
				// 의도에 따라 명확하게 타겟팅된 이러한 자산 → 작업 테스트 범위에 자동으로 포함됩니다(insertAssets와 동일한 세트).
				// 보수적인 세분성). upsertTaskScope의 ON CONFLICT DO NOTHING + uq_task_scope
				// 고유 인덱스는 반복적으로 추가되지 않도록 보장됩니다. 재실행/재시도 역시 멱등성 no-op입니다.
				// 자산 커버리지 기능이 꺼지면 테스트 커버리지(분모)가 더 이상 누적되지 않습니다.
				if coverageEnabled {
					for _, a := range assets {
						_ = as.AddAutoScope(taskID, a.Type, a.Domain, a.URL, a.IP)
					}
				}
			}
		}
	}
	sysBody += "\n\n는 위의 의도를 실행하기 시작합니다. 수행만 하고, 사실만 생성하고, assets, finding, 완료되면 중지합니다."
	system, boundary := deferredSystem(sysBody, def)
	// 작업 수준 deadline(ctx에 의해 주입됨)는 이 run의 벽시계 예산을 꼬집고 + 끝 단어를 결정합니다(taskclock.go 참조).
	tc := taskClockFrom(ctx)
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, w.runTimeout)
	settle := wrapupSettlement("worker", []string{"Bash"})
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("worker", []string{"Bash"}, clamped)
	}
	opts := agentcore.Options{
		Provider:        w.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		// WebFetch는 에이전트를 기록하고 해당 HTTP는 curl처럼 추적됩니다. 로드 에이전트 CA는 MITM를 통과합니다.
		// 재서명된 HTTPS 인증서는 (검증을 끄는 대신) [일반 검증을 통과]할 수 있습니다. proxy 비어 있으면 직접 연결하세요.
		EnableWebFetch: true,
		WebFetchProxy:  w.proxyAddr,
		WebFetchCACert: w.proxyCACert,
		// 인터넷 검색(선택사항). ddgs에는 key가 필요하지 않습니다. brave-free에는 BraveKey가 필요합니다. tavily에는 TavilyKey가 필요합니다.
		// WebSearchProxy는 독립적인 송신 프록시(http/https/socks5)이며 트래픽을 기록하는 MITM 프록시와 아무 관련이 없습니다. 비어 있으면 직접 연결됩니다.
		EnableWebSearch:       w.webSearch.Enabled,
		WebSearchBackend:      w.webSearch.Backend,
		BraveSearchAPIKey:     w.webSearch.BraveKey,
		TavilySearchAPIKey:    w.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: w.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  w.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   w.webSearch.DeepSeekModel,
		WebSearchProxy:        w.webSearch.Proxy,
		// Bash 하위 명령의 HTTP는 기본적으로 녹음 에이전트로 설정되고 해당 CA를 신뢰합니다(도구에는 -x/-k가 필요하지 않음).
		BashEnv:    proxyEnv(w.proxyAddr, w.proxyCACert),
		WorkingDir: runDir,
		MaxTurns:   w.maxTurns, // 0 = unlimited (configurable in agent management)
		// 벽시계 예산, 경계 판단, 중단 없음; 0 = 제한 없음. 태스크 레벨 deadline가 있는 경우 min(자체 예산,
		// (deadline에서 남음) 작업이 끝나면 run가 자연스럽게 끝나도록 합니다(taskclock.go 참조).
		MaxDuration: maxDur,
		// 예산 적중(라운드 OR 기간) → SDK 마감 라운드를 실행하고(Bash 숨기기) 확인된 항목을 다시 작성하여 완료되지 않은 작업을 방지합니다.
		// clamped(작업 deadline에 의해 포착됨)는 PromptByReason:를 사용할 때 사용됩니다. 왜냐하면 시간 초과 = 작업이 지점에 도달 → 작업 시간 초과 단어이기 때문입니다.
		// 단계 수 = 핀치 창 내의 단계 수가 먼저 소진되므로 → per-run 단어로 돌아갑니다. 비 clamped는 순수한 per-run로 유지됩니다.
		Settlement: settle,
		// large tool output spills to cmd-output/ with a head + pointer (SDK tool.Capture);
		// full output preserved on disk. 기본적으로 SDK를 잘림 상한값으로 사용합니다(30000자).
		ToolOutputDir: cmdOutDir(runDir),
		Compaction:    compactionConfig(w.compactionWindow()), // long tool-heavy runs stay within the window
		Todos:         actool.NewTodoStore(),                  // 순전히 계획 목적을 위한 세션 수준 임시 작업(TodoWrite)은 종료 시 손실됩니다.
		NonStreaming:  w.nonStreaming(),                       // profile는 Provider.Complete를 걸을 때 비스트리밍을 선택합니다.
		MaxTokens:     w.maxTokens(),                          // 0 = 서버의 기본값에 의해 결정된 상한값을 보내지 않음
	}
	if hooks != nil { // typed-nil guard: only set when concrete (avoids harness panic)
		opts.Hooks = hooks
	}
	if w.tx != nil { // persist raw LLM conversation; one file per worked intent
		opts.Transcript = w.tx
		opts.SessionID = WorkerSessionID(ts.ID(), intent.ID)
	}
	intentID := intent.ID
	emitWrap := func(r db.Activity) {
		if emit != nil {
			r.NodeID, r.Worker = &intentID, name
			emit(r)
		}
	}
	// 의도/활성화 명령/의도 고정 자산이 system prompt와 함께 발행되었습니다(위의 sysBody 어셈블리 참조).
	// 이 시작 user 메시지는 [Global Situation overview]만 전달합니다. 즉, 문제 없이 다운그레이드하고 억제할 수 있는 전반적인 상황 정보입니다.
	// overview marshal가 비어 있는 경우가 거의 없으면 시작 단어로 대체하여 첫 번째 라운드에서 빈 user 메시지를 방지합니다.
	input := overview
	if strings.TrimSpace(input) == "" {
		input = "system에서 받은 의도를 실행하기 시작합니다. 실행하기만 하고, 사실만 생성하고, assets, finding를 생성하고, 완료되면 중지합니다."
	}

	// 실험 기능: 전원을 켜면 noa가 컨텍스트 압축을 대신합니다(아카이브는 <workDir>/noa/<SessionID>, 영구 아래에 집중되어 있음).
	noaSession := WorkerSessionID(ts.ID(), intent.ID)
	enableNoa(&opts, w.noaEnabledFn, w.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close() // release the session's background-task manager (temp dir + processes)

	// Resume prior conversation if this intent was paused/blocked/exhausted and is
	// being re-run. The transcript ID is deterministic per intent, so if a prior
	// session exists the worker continues from where it left off instead of
	// restarting from scratch.
	alreadyRecorded := false
	if w.tx != nil {
		_ = s.Resume(opts.SessionID)
		alreadyRecorded = requestID != "" && hasWorkerChatMessage(s.Messages(), requestID)
		if len(s.Messages()) > 0 && message == "" {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
			input = "계속해서 실행하세요."
		} else if len(s.Messages()) > 0 {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
		}
	}
	if message != "" {
		if alreadyRecorded {
			input = "마지막 인간 대화에서 입력된 새 인텐트를 계속 실행합니다. 이미 한 행동을 반복하지 마세요."
		} else if len(s.Messages()) > 0 {
			input = workerChatMarker(requestID) + "\n [인공대화입력의 새로운 의도] \n" + message +
				"\n\n이 수동 입력을 즉시 실행하십시오. 완료 후 상황에 따라 원래 작업을 계속해야 하는지 여부를 결정합니다."
		} else {
			input += "\n\n" + workerChatMarker(requestID) + "\n [인공대화입력의 새로운 의도] \n" + message +
				"\n\n본 수동 입력을 우선으로 해주시기 바랍니다."
		}
	}

	// Budgets + settlement are owned by the SDK (MaxTurns/MaxDuration + Settlement):
	// on hit it runs a wrap-up turn and finishes with ReasonMaxTurns/ReasonTimeout.
	// MaxDuration now interrupts an in-flight tool at the wall-clock deadline and
	// enters the wrap-up phase on the live ctx, so a run whose tool overran the budget
	// still settles (no external hard-timeout backstop needed). ctx itself carries only
	// pause / planner kill / shutdown, which the engine distinguishes and re-queues/stops.
	_, reason, err := captureRunSession(ctx, s, input, emitWrap)
	return reason, tsx.Writes(), err
}
