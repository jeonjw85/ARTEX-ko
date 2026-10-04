package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/jackc/pgx/v5/pgconn"
)

// isFKViolation reports whether err is a Postgres foreign-key violation (SQLSTATE
// 23503) — e.g. an activity insert whose exploration_id has no parent row.
func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// dropReason classifies why an activity write was dropped, so the log can be
// grouped/analysed by cause rather than by raw error text.
func dropReason(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return "fk_violation(23503, 상위 탐색이 존재하지 않음)"
		case "23505":
			return "unique_violation(23505)"
		default:
			return "pg_error(" + pgErr.Code + ")"
		}
	}
	return "write_error"
}

// bumpDrop increments and returns the running count of dropped (unpersistable)
// activity records for a task. Concurrent planner + worker emits race here, so the
// counter is an atomic behind sync.Map. The count in the log shows loss scale at a
// glance instead of forcing a grep-and-count.
func (e *Engine) bumpDrop(taskID string) int64 {
	v, _ := e.dropCnt.LoadOrStore(taskID, new(int64))
	return atomic.AddInt64(v.(*int64), 1)
}

// preview collapses newlines and trims s to a short rune-safe snippet for one-line
// log output (avoids dumping a multi-KB summary/detail into the log).
func preview(s string, n int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// model_error (provider/API 오류: LLM 레이어 순간 재시도가 소진되거나 흐름이 시작된 후 중간에 중단됨)
// work의 최종 결과는 "시도했지만 완료되지 않음"도 아니고 실제 실패도 아닌 외부 지터입니다. 기본적으로 영구적으로 처리
// blocked는 의도를 헛되이 잃을 것이므로 여기서는 매번 조금씩 후퇴하면서 최종 상태를 몇 번 다시 실행합니다.
// provider 복구 시간; 재시도 기간 동안 일시 중단/종료/취소된 경우 처리를 위해 즉시 해당 분기로 전달됩니다.
const (
	modelErrorRetries      = 2               // model_error 종료 후 추가 재시도 횟수
	modelErrorRetryBackoff = 3 * time.Second // 재시도할 때마다 백오프
	workControlWaitTimeout = 30 * time.Second
)

var errWorkControlConflict = errors.New("work control conflict")

// retryableWorkerModelError excludes errors already handled by the task router.
// In particular, a quota error after partial streaming advances the task cursor
// for the next LLM call but must not replay this whole intent on the backup.
func retryableWorkerModelError(reason harness.TerminalReason, err error) bool {
	return reason == harness.ReasonModelError && !isTaskLLMRuntimeError(err)
}

// Engine drives the event-driven exploration loop with real LLM agents
// (docs §4.3/§4.4): on asset/exploration-graph change (debounced) it wakes the
// planner, which reads the route, queries assets, judges goals and emits intents;
// N concurrent work agents claim intents and execute them. There is no
// simulation mode — an LLM provider is required. The planner/worker can be
// (re)installed at runtime (LLM configured from the UI); the loops always run
// but idle until an LLM is set.
type Engine struct {
	m        *Manager
	debounce time.Duration

	bc *Broadcaster // live activity pub/sub (SSE)

	started  sync.Map // taskID -> bool, so Run is idempotent per task
	lastAct  sync.Map // taskID -> int64 unix, last planner/worker activity (heartbeat)
	llmCalls sync.Map // taskID -> *int64, actual planner/worker/main-agent LLM calls
	paused   sync.Map // taskID -> bool, user-paused (planner + workers idle but loops alive)
	deleting sync.Map // taskID -> bool, delete barrier (no new task-owned writes)
	dropCnt  sync.Map // taskID -> *int64, running count of dropped (unpersistable) activity records

	// deleteMu makes installing the delete barrier atomic with registering a new
	// task operation. Once BeginDelete returns, every admitted writer is reflected
	// in inflight and every later writer is rejected.
	deleteMu sync.RWMutex

	// Every long-lived task goroutine (planner, workers and deadline coordinator)
	// runs under one task-scoped context. Successful deletion cancels that context,
	// waits for all goroutines, then releases every task-level Engine reference.
	runtimeMu sync.Mutex
	runtimes  map[string]*taskRuntime

	// per-task execution context: each planner.Plan / worker.Execute runs under it,
	// so pausing can CANCEL an in-flight run (not just skip the next one). Recreated
	// on resume since cancelling is one-shot. Every cancellation carries a named
	// cause so the activity trace can identify the initiating control path.
	execMu     sync.Mutex
	execCancel map[string]context.CancelCauseFunc
	execCtx    map[string]context.Context

	// Per-work control lets the planner kill a worker and lets the UI pause/cancel
	// one intent without pausing the whole task. The done channel closes only after
	// runWorkerStep has stopped writing and committed its final state.
	workMu sync.Mutex
	work   map[int64]*workExecution

	// steerBox queues planner course-corrections for a running work (keyed by intent
	// id). The worker's PreToolUse hook drains it before its next tool call and hands
	// the message to the model (blocking that call) so it re-plans — no kill needed.
	steerMu  sync.Mutex
	steerBox map[int64][]string

	plannerRound sync.Map // taskID -> int, planner round counter (for UI round separators)

	// 작업 수준 시간 초과(docs/ 작업 수준 시간 초과 및 설계 종료.md 참조):
	settling     sync.Map // taskID -> bool, 작업이 최종 시퀀스에 진입했습니다. (새로운 의도 보내기/받기 중지)
	deadline     sync.Map // taskID -> int64 unix, 절대 기한(첫 번째 실행 시 스탬프 처리됨, 0/기본값 = 제한 없음)
	stamped      sync.Map // taskID -> bool, first_run_at 스탬프 여부(이 과정에서 한 번만 스탬프 찍힘)
	inflight     sync.Map // taskID -> *int64, planner.Plan + worker.Execute 카운트 실행(drain의 경우)
	coordStarted sync.Map // taskID -> bool, deadline 코디네이터 시작 여부(Run/reload 중복제거)

	// resolve returns a task's dedicated planner/worker (wired by the server as the
	// authoritative task-router). nil,nil means this task is deliberately unavailable
	// (for example an exhausted failover chain) — there is no global-pair fallback.
	resolve              func(t *Task) (*agent.Planner, *agent.Worker)
	resolveAuthoritative bool
	// readiness reports whether a global LLM provider is configured — the signal behind
	// Ready()/the llm_configured indicator. Wired once at startup; nil → not ready.
	readiness func() bool
}

type taskRuntime struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type workExecution struct {
	cancel context.CancelCauseFunc
	done   chan error
	action string // user action: pause | cancel
}

// nextPlannerRound returns the next planner round number for a task (1-based).
func (e *Engine) nextPlannerRound(taskID string) int {
	v, _ := e.plannerRound.LoadOrStore(taskID, 0)
	n := v.(int) + 1
	e.plannerRound.Store(taskID, n)
	return n
}

// Pause stops a task: marks it paused AND cancels any in-flight planner/worker run
// for it (a long worker.Execute would otherwise keep going until it finishes).
func (e *Engine) Pause(taskID string, cause error) {
	e.paused.Store(taskID, true)
	e.cancelExec(taskID, cause)
}

// BeginDelete installs an execution barrier before task data/files are removed.
// The temporary pause is not a user pause. The server serializes this transition
// with lifecycle admission and tells AbortDelete whether the persisted task is
// paused/queued if cleanup fails.
func (e *Engine) BeginDelete(taskID string) bool {
	e.deleteMu.Lock()
	if _, loaded := e.deleting.LoadOrStore(taskID, true); loaded {
		e.deleteMu.Unlock()
		return false
	}
	e.paused.Store(taskID, true)
	e.deleteMu.Unlock()
	e.cancelExec(taskID, agent.AbortTaskDeleted)
	return true
}

func (e *Engine) AbortDelete(taskID string, keepPaused bool) {
	e.deleteMu.Lock()
	if !e.IsDeleting(taskID) {
		e.deleteMu.Unlock()
		return
	}
	e.deleting.Delete(taskID)
	if !keepPaused {
		e.paused.Delete(taskID)
	}
	e.deleteMu.Unlock()
	if !keepPaused && e.m != nil {
		if t, ok := e.m.Task(taskID); ok {
			t.Notify()
		}
	}
}

func (e *Engine) IsDeleting(taskID string) bool {
	_, ok := e.deleting.Load(taskID)
	return ok
}

// registerTaskRoutines reserves count goroutines in the task runtime. Callers
// hold deleteMu for reading so StopTask cannot race WaitGroup.Add with Wait.
func (e *Engine) registerTaskRoutines(parent context.Context, taskID string, count int) *taskRuntime {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	rt := e.runtimes[taskID]
	if rt == nil {
		ctx, cancel := context.WithCancel(parent)
		rt = &taskRuntime{ctx: ctx, cancel: cancel}
		e.runtimes[taskID] = rt
	}
	rt.wg.Add(count)
	return rt
}

func runTaskRoutine(rt *taskRuntime, fn func(context.Context)) {
	go func() {
		defer rt.wg.Done()
		fn(rt.ctx)
	}()
}

// StopTask permanently stops every long-lived goroutine and removes all Engine
// state for a successfully deleted task. The delete barrier remains installed
// until cleanup finishes, so no new task operation can race the teardown.
func (e *Engine) StopTask(taskID string) {
	e.deleteMu.Lock()
	e.deleting.Store(taskID, true)
	e.deleteMu.Unlock()

	e.cancelExec(taskID, agent.AbortTaskDeleted)
	e.runtimeMu.Lock()
	rt := e.runtimes[taskID]
	if rt != nil {
		rt.cancel()
	}
	e.runtimeMu.Unlock()
	if rt != nil {
		rt.wg.Wait()
	}

	e.execMu.Lock()
	if cancel := e.execCancel[taskID]; cancel != nil {
		cancel(agent.AbortTaskDeleted)
	}
	delete(e.execCancel, taskID)
	delete(e.execCtx, taskID)
	e.execMu.Unlock()

	e.runtimeMu.Lock()
	if e.runtimes[taskID] == rt {
		delete(e.runtimes, taskID)
	}
	e.runtimeMu.Unlock()

	e.started.Delete(taskID)
	e.lastAct.Delete(taskID)
	e.llmCalls.Delete(taskID)
	e.paused.Delete(taskID)
	e.dropCnt.Delete(taskID)
	e.plannerRound.Delete(taskID)
	e.settling.Delete(taskID)
	e.deadline.Delete(taskID)
	e.stamped.Delete(taskID)
	e.inflight.Delete(taskID)
	e.coordStarted.Delete(taskID)
	e.deleteMu.Lock()
	e.deleting.Delete(taskID)
	e.deleteMu.Unlock()
}

// cancelExec cancels a task's current per-task exec context (any in-flight
// planner.Plan / worker.Execute), if present. Shared by Pause and the settle
// sequence's hard-drain backstop.
func (e *Engine) cancelExec(taskID string, cause error) {
	e.execMu.Lock()
	if cancel := e.execCancel[taskID]; cancel != nil {
		cancel(cause)
	}
	e.execMu.Unlock()
}

// Resume un-pauses a task and nudges a fresh planning round. The next exec under
// it gets a fresh (uncancelled) context.
func (e *Engine) Resume(t *Task) {
	// BeginDelete owns the pause barrier once deletion starts. A concurrent
	// resume must never clear it and let a planner/worker re-enter while cleanup
	// is waiting for task operations to drain.
	if t == nil {
		return
	}
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.IsDeleting(t.ID) {
		return
	}
	e.paused.Delete(t.ID)
	t.Notify()
}

// execContextFor returns a live per-task context derived from parent, recreating
// it if a prior pause cancelled it.
func (e *Engine) execContextFor(parent context.Context, taskID string) context.Context {
	e.execMu.Lock()
	defer e.execMu.Unlock()
	if e.IsPaused(taskID) {
		// never hand out a live context while paused (guards the claim→Execute race)
		c, cancel := context.WithCancelCause(parent)
		cancel(agent.AbortPausedRaceGuard)
		return c
	}
	if c := e.execCtx[taskID]; c != nil && c.Err() == nil {
		return c
	}
	c, cancel := context.WithCancelCause(parent)
	e.execCtx[taskID] = c
	e.execCancel[taskID] = cancel
	return c
}

// IsPaused reports whether a task is user-paused.
func (e *Engine) IsPaused(taskID string) bool {
	v, ok := e.paused.Load(taskID)
	return ok && v.(bool)
}

// Started reports whether the engine loops are running for a task.
func (e *Engine) Started(taskID string) bool {
	_, ok := e.started.Load(taskID)
	return ok
}

// LastActivity returns the unix time of the last planner/worker activity for a
// task (0 if none yet).
func (e *Engine) LastActivity(taskID string) int64 {
	if v, ok := e.lastAct.Load(taskID); ok {
		return v.(int64)
	}
	return 0
}

// BeginLLMCall/EndLLMCall track actual provider calls separately from the
// scheduler's task-operation counter. A task can have live loops while all of
// them are waiting for a trigger; that state must remain idle in the UI.
func (e *Engine) BeginLLMCall(taskID string) {
	v, _ := e.llmCalls.LoadOrStore(taskID, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

func (e *Engine) EndLLMCall(taskID string) {
	if v, ok := e.llmCalls.Load(taskID); ok {
		p := v.(*int64)
		if atomic.AddInt64(p, -1) <= 0 {
			atomic.StoreInt64(p, 0)
		}
	}
}

func (e *Engine) ActiveLLMCalls(taskID string) int64 {
	if v, ok := e.llmCalls.Load(taskID); ok {
		return atomic.LoadInt64(v.(*int64))
	}
	return 0
}

func (e *Engine) touch(taskID string) { e.lastAct.Store(taskID, time.Now().Unix()) }

func NewEngine(m *Manager) *Engine {
	return &Engine{m: m, debounce: 800 * time.Millisecond, bc: NewBroadcaster(),
		execCancel: map[string]context.CancelCauseFunc{}, execCtx: map[string]context.Context{},
		work: map[int64]*workExecution{}, steerBox: map[int64][]string{},
		runtimes: map[string]*taskRuntime{}}
}

// registerWork records the cancel for the work currently running intentID.
func (e *Engine) registerWork(intentID int64, cancel context.CancelCauseFunc) {
	e.workMu.Lock()
	e.work[intentID] = &workExecution{cancel: cancel, done: make(chan error, 1)}
	e.workMu.Unlock()
}

// detachWork removes the live control handle once Execute has returned. complete
// must be called after the final intent state write so a waiting cancel handler can
// safely delete the worker's blackboard output without racing a late write.
func (e *Engine) detachWork(intentID int64) (action string, complete func(error)) {
	e.workMu.Lock()
	run := e.work[intentID]
	if run != nil {
		delete(e.work, intentID)
		action = run.action
		run.cancel(agent.AbortWorkFinished) // release resources (no-op if already cancelled)
	}
	e.workMu.Unlock()
	e.steerMu.Lock()
	delete(e.steerBox, intentID) // drop any undelivered steering for a finished work
	e.steerMu.Unlock()
	if run == nil {
		return action, func(error) {}
	}
	return action, func(err error) { run.done <- err }
}

// ControlWork requests a user-visible pause or cancellation and waits until the
// worker has fully stopped writing. Cancellation cleanup is performed by the API
// handler after this returns; pause state is committed by runWorkerStep itself.
func (e *Engine) ControlWork(ctx context.Context, intentID int64, action string) error {
	if action != "pause" && action != "cancel" {
		return fmt.Errorf("unsupported work action %q", action)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.workMu.Lock()
	run := e.work[intentID]
	if run == nil {
		e.workMu.Unlock()
		return fmt.Errorf("%w: 인텐트 %d에는 현재 실행 중인 work가 없습니다(종료되었거나 청구되지 않았을 수 있음).", errWorkControlConflict, intentID)
	}
	if run.action != "" {
		e.workMu.Unlock()
		return fmt.Errorf("%w: 인텐트 %d가 %s 작업을 실행 중입니다.", errWorkControlConflict, intentID, run.action)
	}
	run.action = action
	done := run.done
	cause := error(agent.AbortWorkPausedByUser)
	if action == "cancel" {
		cause = agent.AbortWorkCancelledByUser
	}
	run.cancel(cause)
	e.workMu.Unlock()

	timer := time.NewTimer(workControlWaitTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		e.releaseWorkControl(intentID, run, action)
		return fmt.Errorf("의도 %d %s 대기 중 종료: %w", intentID, action, ctx.Err())
	case <-timer.C:
		e.releaseWorkControl(intentID, run, action)
		return fmt.Errorf("의도 %d %s 대기 중 종료: %w", intentID, action, context.DeadlineExceeded)
	}
}

// releaseWorkControl drops only this caller's reservation after its wait is
// cancelled. The work context stays cancelled; runWorkerStep recognizes the
// named cancellation cause and settles the intent into the recoverable paused
// state even if the HTTP caller has gone away.
func (e *Engine) releaseWorkControl(intentID int64, run *workExecution, action string) {
	e.workMu.Lock()
	if current := e.work[intentID]; current == run && current.action == action {
		current.action = ""
	}
	e.workMu.Unlock()
}

func transitionIntentState(store *db.ExplorationStore, intentID int64, expected, state string) error {
	changed, err := store.CompareAndSetIntentState(intentID, expected, state)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("%w: %d 인텐트는 더 이상 %s 상태가 아닙니다.", db.ErrIntentStateConflict, intentID, expected)
	}
	return nil
}

// SteerWork queues a mid-run course-correction for the work running intentID (the
// planner's steer_work tool). The worker delivers it before its next tool call and
// re-plans — no kill. Errors if no work is currently running that intent.
func (e *Engine) SteerWork(intentID int64, msg string) error {
	if strings.TrimSpace(msg) == "" {
		return fmt.Errorf("수정 메시지는 비워둘 수 없습니다.")
	}
	e.workMu.Lock()
	running := e.work[intentID] != nil
	e.workMu.Unlock()
	if !running {
		return fmt.Errorf("Intent %d 현재 실행 중인 work가 없습니다(종료되었거나 청구되지 않았을 수 있음).", intentID)
	}
	e.steerMu.Lock()
	e.steerBox[intentID] = append(e.steerBox[intentID], msg)
	e.steerMu.Unlock()
	return nil
}

// drainSteer pops the oldest queued steering message for intentID (FIFO), if any.
func (e *Engine) drainSteer(intentID int64) (string, bool) {
	e.steerMu.Lock()
	defer e.steerMu.Unlock()
	q := e.steerBox[intentID]
	if len(q) == 0 {
		return "", false
	}
	msg := q[0]
	if len(q) == 1 {
		delete(e.steerBox, intentID)
	} else {
		e.steerBox[intentID] = q[1:]
	}
	return msg, true
}

// steerHooks wraps the guard's hook runner so the planner can steer a running work:
// before each tool call it drains a queued course-correction (if any) and blocks the
// call, handing the message back to the model — which re-plans its next step instead
// of running the tool. No queued message → the guard behaves exactly as before.
// 또한 "유휴 라운드"의 지속을 담당합니다. Stop를 참조하세요.
type steerHooks struct {
	inner harness.HookRunner
	drain func() (string, bool)
	// nudges는 이 인텐션에 주입된 유휴 연속 횟수이며, 상한은 limit입니다. 포인터: harness 보유
	// steerHooks 값 복사, 개수는 동일한 복사본을 공유해야 합니다.
	nudges *atomic.Int64
	// limit는 공회전 연속 횟수의 상한입니다. Engine.emptyTurnNudgeLimit()는 "빈 응답"에서 다시 시도합니다.
	// "숫자"가 분석됩니다. <=0 = 개입 없음(사용자가 이 레이어를 명시적으로 껐습니다).
	limit int
	// label는 "worker-1·#42" 형태로 로깅에만 사용된다.
	label string
}

// 유휴 라운드 연속 횟수(생각만, 텍스트 또는 도구 호출 없음) 및 SDK 빈 응답 재시도에 대한 기본값
// 내장된 기본값(norma/llm/openai.go의 emptyResponseRetries)은 일관되게 유지됩니다. 두 레이어는 동일하게 공유합니다.
// 구성되지 않은 경우 노브의 동작도 정렬되어야 합니다. 분석은 Engine.emptyTurnNudgeLimit를 참조하세요.
//
// 참고로 이 숫자는 "연속 횟수"가 아니라 "의도의 총량"입니다. harness 자신의 stopHookActive
// 지속적인 공회전을 위해 한 번만 누르도록 제한되었습니다. 라운드를 누른 후에도 여전히 공회전 상태인 경우 Stop 후크는 다시 조정되지 않으며 run는 직접 조정됩니다.
// 종결; 할당량은 공구 회전이 실제로 발생할 때만 새로 고쳐집니다(norma/harness/query.go:534). 그래서 이것은
// 길을 막고 있는 것은 '도구 → 유휴 → 푸시 → 도구 → 유휴'의 병리적 순환이다. 의도한 예산을 낭비하지 마십시오.
const defaultEmptyTurnNudges = 2

// emptyTurnNudge는 유휴 라운드 중에 주입되는 계속 명령입니다.
//
// 이번 라운드는 harness(stop_reason=end_turn 및 tool_use 없음) 5층의 눈에는 자연스러운 엔드입니다.
// LLM 한 단계 다시 시도해도 적용되지 않습니다. 오류가 아니라 "생각은 끝났지만 실행하지 않은" 모델입니다. SDK의 빈 응답도 재시도
// 도달 범위 외 : "yield 이벤트가 있는지 여부"로 판단하고, 증가 자체가 이벤트 (norma/llm/openai.go)
// SEThinkingDelta), 따라서 thinking-only는 비어 있는 것으로 간주되지 않습니다. 게다가 해당 레이어는 prompt 전체를 그대로 다시 보냅니다.
// 컨텍스트의 모양에 따라 결정되는 이러한 유휴 상태의 경우 재전송은 모델을 다시 생각하게 만듭니다. 대신 여기에 지침을 추가하여 이를 수행하세요.
// 지금까지 나온 생각을 계속하세요. 입력이 변경될 때만 다른 동작을 제공할 이유가 있습니다.
const emptyTurnNudge = "[유휴 알림] 마지막 라운드에서는 당신의 사고 과정만 출력합니다. 당신은 문자 답장도 하지 않았고 어떤 도구도 사용하지 않았습니다." +
	"이 라운드는 출력이 없는 것과 같습니다. 방금 생각한 다음 단계로 이동하세요. 도구를 호출하거나 결론 텍스트를 제공하세요. 두 번 생각하지 마십시오."

// isThinkingOnlyTurn reports whether the latest assistant turn produced neither
// text nor a tool call — i.e. the model spent the whole round thinking.
func isThinkingOnlyTurn(messages []llm.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != llm.RoleAssistant {
			continue
		}
		return strings.TrimSpace(m.Text()) == "" && len(m.ToolUses()) == 0
	}
	return false
}

func (h steerHooks) PreToolUse(ctx context.Context, name string, input []byte) (bool, string, []byte) {
	if msg, ok := h.drain(); ok {
		return true, "[기획자가 실시간으로 편차를 수정]" + msg +
			"\n(이러한 의도에 대한 기획자의 즉각적인 지시입니다. 이 도구 호출이 실행되지 않았습니다. 이에 따라 다음 단계를 조정하십시오. 현재 계획과 충돌하는 경우 이것이 우선합니다.)", nil
	}
	if h.inner != nil {
		return h.inner.PreToolUse(ctx, name, input)
	}
	return false, "", nil
}

func (h steerHooks) PostToolUse(ctx context.Context, name string, input, result []byte, isErr bool) {
	if h.inner != nil {
		h.inner.PostToolUse(ctx, name, input, result, isErr)
	}
}

// Stop는 guard의 원래 의미 위에 "유휴 라운드" 레이어를 추가하여 계속 실행합니다. 모델은 생각만 출력하고 텍스트는 출력하지 않습니다.
// 도구가 조정되지 않으면 harness는 이를 자연스러운 끝으로 처리하고 빈 summary(query.go's)로 끝납니다.
// ReasonCompleted + asst.Text()), 아직 완성되지 않은 의도가 중간에 끊어졌습니다. 이때 주사한다
// 계속 명령을 사용하면 모델이 기존 생각으로 계속 실행될 수 있습니다.
func (h steerHooks) Stop(ctx context.Context, messages []llm.Message) (bool, []string, string) {
	var (
		prevent  bool
		blocking []string
		msg      string
	)
	if h.inner != nil {
		prevent, blocking, msg = h.inner.Stop(ctx, messages)
	}
	// inner는 강제 중지를 결정했거나 이미 자체 계속 메시지를 삽입했습니다. → 이를 존중하고 더 이상 중첩하지 않습니다.
	// limit<=0 = 사용자가 "빈 응답 재시도 횟수"를 -1로 설정했습니다. 이는 이 레이어를 명시적으로 끄는 것을 의미합니다.
	if prevent || len(blocking) > 0 || h.nudges == nil || h.limit <= 0 || !isThinkingOnlyTurn(messages) {
		return prevent, blocking, msg
	}
	n := h.nudges.Add(1)
	if n > int64(h.limit) {
		log.Printf("[work %s] 유휴 라운드(생각만 하면 텍스트도 없고 도구도 없습니다.)실행 한도에 도달했습니다. %d，출시 및 종료", h.label, h.limit)
		return prevent, blocking, msg
	}
	log.Printf("[work %s] 유휴 라운드(생각만 하면 텍스트도 없고 도구도 없습니다.)，계속 명령 삽입 (%d/%d)", h.label, n, h.limit)
	return false, []string{emptyTurnNudge}, ""
}

// KillWork cancels the in-flight work running intentID (planner's kill_work tool).
// The work's agent-core session honors ctx cancellation and aborts promptly.
func (e *Engine) KillWork(intentID int64) error {
	e.workMu.Lock()
	run := e.work[intentID]
	e.workMu.Unlock()
	if run == nil {
		return fmt.Errorf("Intent %d 현재 실행 중인 work가 없습니다(종료되었거나 청구되지 않았을 수 있음).", intentID)
	}
	run.cancel(agent.AbortKilledByPlanner)
	return nil
}

// Broadcaster exposes the engine's live activity pub/sub (used by the SSE handler).
func (e *Engine) Broadcaster() *Broadcaster { return e.bc }

// emitActivity persists one captured step AND fans it out to live subscribers,
// from a single point so storage and the SSE stream never diverge.
func (e *Engine) emitActivity(t *Task, r db.Activity) db.Activity {
	id, err := e.appendActivity(t, r)
	if err != nil {
		// NO LONGER SILENT: dropping a record breaks command↔result pairing in the
		// trace — a tool_use whose tool_result was lost shows as "실행 중" forever, and
		// a lost 'result'/'round' record leaves the session with no summary ("요약 없음").
		// Everything needed 근본 원인 분석 goes into ONE error-level line: reason class,
		// summary preview, running drop count for this task, and — on the FK case — a
		// live probe of WHY the parent exploration is unreachable.
		n := e.bumpDrop(t.ID)
		diag := ""
		// On the FK-parent failure (23503) probe the live DB so the log records WHY the
		// exploration is unreachable (row gone / wrong expID) instead of just that it is.
		if isFKViolation(err) {
			storeID := t.Store.ID()
			if exists, refs, maxID, dErr := e.m.pg.ExplorationDiag(storeID); dErr != nil {
				diag = fmt.Sprintf(" | FK 진단 쿼리 실패(store.expID=%d task.ExpID=%d): %v", storeID, t.ExpID, dErr)
			} else {
				diag = fmt.Sprintf(" | FK 진단: store.expID=%d task.ExpID=%d 탐색 존재=%v 이를 참조하는 작업 수=%d MAX(exploration.id)=%d",
					storeID, t.ExpID, exists, refs, maxID)
			}
		}
		log.Printf("[activity] task %s 활동 삭제(이 작업의 누적 개수는 %d 조각) worker=%s kind=%s tool=%s tuid=%s reason=%s summary=%q: %v%s",
			t.ID, n, r.Worker, r.Kind, r.Tool, r.ToolUseID, dropReason(err), preview(r.Summary, 80), err, diag)
		e.touch(t.ID)
		return r
	}
	r.ID = id
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	e.bc.Publish(t.ID, r)
	e.touch(t.ID)
	return r
}

// appendActivity persists one activity row, retrying briefly on write failure.
// Concurrent planner + worker inserts into the same exploration's activity log
// occasionally fail; a couple of quick retries recover most. Crucially, every
// failure is now LOGGED (it used to be swallowed by an `if err == nil`), so the
// underlying DB error is finally visible for diagnosis.
func (e *Engine) appendActivity(t *Task, r db.Activity) (int64, error) {
	var id int64
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if id, err = t.Store.AppendActivity(r); err == nil {
			if attempt > 1 {
				log.Printf("[activity] task %s 첫 번째를 쓰세요 %d 성공적인 재시도 (worker=%s kind=%s tool=%s)",
					t.ID, attempt, r.Worker, r.Kind, r.Tool)
			}
			return id, nil
		}
		log.Printf("[activity] task %s 쓰기 실패 (아니요. %d/3 이류, worker=%s kind=%s tool=%s expID=%d): %v",
			t.ID, attempt, r.Worker, r.Kind, r.Tool, t.Store.ID(), err)
		time.Sleep(time.Duration(attempt) * 25 * time.Millisecond)
	}
	return 0, err
}

// SetReadiness wires the global "an LLM provider is configured" predicate (read by
// Ready() / the llm_configured indicator). Called once at startup.
func (e *Engine) SetReadiness(fn func() bool) { e.readiness = fn }

// SetAgentResolver installs a per-task planner/worker resolver (wired by the server).
// Called once at startup before any task loop runs, so no lock is needed on reads.
func (e *Engine) SetAgentResolver(fn func(t *Task) (*agent.Planner, *agent.Worker)) {
	e.resolve = fn
	e.resolveAuthoritative = false
}

// SetAuthoritativeAgentResolver installs a resolver whose nil result must not
// fall through to the global provider. Task-level failover chains use this so a
// fully exhausted chain cannot silently bypass its configured boundary.
func (e *Engine) SetAuthoritativeAgentResolver(fn func(t *Task) (*agent.Planner, *agent.Worker)) {
	e.resolve = fn
	e.resolveAuthoritative = true
}

// snapshotFor returns the planner/worker a task should run on, from the task-router
// resolver. nil,nil means the task is deliberately unavailable (e.g. an exhausted
// failover chain); there is no global-pair fallback.
func (e *Engine) snapshotFor(t *Task) (*agent.Planner, *agent.Worker) {
	if e.resolve != nil {
		p, w := e.resolve(t)
		if (p != nil && w != nil) || e.resolveAuthoritative {
			return p, w
		}
	}
	return nil, nil
}

// Ready reports whether a global LLM provider is configured (via the readiness
// predicate wired at startup).
func (e *Engine) Ready() bool {
	return e.readiness != nil && e.readiness()
}

// ReadyFor reports whether a specific task can resolve a planner/worker pair.
// An explicit task profile chain can be runnable even when no global default
// provider is configured, so task status must not rely on Ready alone.
func (e *Engine) ReadyFor(t *Task) bool {
	p, w := e.snapshotFor(t)
	return p != nil && w != nil
}

// Run starts the planner loop + N worker loops for a task. The loops always run
// but no-op until an LLM is configured (so a task created while idle picks up
// automatically once LLM is set from the UI).
func (e *Engine) Run(ctx context.Context, t *Task) {
	workers := e.m.Workers()
	e.deleteMu.RLock()
	if e.IsDeleting(t.ID) {
		e.deleteMu.RUnlock()
		return
	}
	if _, loaded := e.started.LoadOrStore(t.ID, true); loaded {
		e.deleteMu.RUnlock()
		t.Notify() // already running — just nudge a planning round
		return
	}
	rt := e.registerTaskRoutines(ctx, t.ID, 1+workers)
	e.deleteMu.RUnlock()
	e.touch(t.ID)
	runTaskRoutine(rt, func(loopCtx context.Context) { e.plannerLoop(loopCtx, t) })
	for i := 0; i < workers; i++ {
		name := fmt.Sprintf("work#%d", i+1)
		runTaskRoutine(rt, func(loopCtx context.Context) { e.workerLoop(loopCtx, t, name) })
	}
	e.startDeadlineCoordinator(ctx, t) // 작업 수준 시간 초과 타이머(timeout>0만 해당, 중복 제거)
	// kick는 "활동 의향이 전혀 없음(open+running)"인 경우에만 1차 기획이다. 시드 의도가 있는 작업: 시드에는
	// open이거나 새로 생성된 worker가 claim를 선점하여 running가 되었습니다. 둘 다 "해야 할 작업"으로 간주되므로 건너뛰어야 합니다.
	// 1라운드에서는 planner와 worker가 직접 시드를 받고 레이스를 시작하려고 합니다. 경주가 끝난 후 NotifyDone/의 심장 박동이 planner를 깨웁니다.
	// ⚠️ Frontier를 사용할 수 없습니다(open만 해당): 수신할 worker(open → running)는 이 검사와 경쟁 상태를 가지며, kick 오류가 발생합니다.
	// 자동 복구를 다시 시작할 때 running 의도만 남을 수 있으므로 이 역시 건너뛰어야 합니다.
	if has, _ := t.Store.HasActiveIntent(); !has {
		t.Notify() // kick the first planning round (acted on once LLM is ready)
	}
}

// plannerHeartbeatInterval 구문 분석 작업의 planner 하트비트 간격입니다. db.CreateTask가 정규화되었습니다.
// (600보다 낮을 경우 600으로 올라갑니다.) 메모리 상태의 비정상적인 값을 피하기 위한 또 다른 단계는 다음과 같습니다.
func plannerHeartbeatInterval(t *Task) time.Duration {
	sec := t.PlanHeartbeatSeconds
	if sec < db.MinPlanHeartbeatSeconds { // 하한=기본값=600(10분)
		sec = db.MinPlanHeartbeatSeconds
	}
	return time.Duration(sec) * time.Second
}

// resetPlannerTimer 안전 붐은 트리거되었을 수 있는 Timer입니다(표준 Stop→drain→Reset 모드).
func resetPlannerTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

func (e *Engine) plannerLoop(ctx context.Context, t *Task) {
	interval := plannerHeartbeatInterval(t)
	// loop 항목 arm의 하트비트 타이머 = 작업 start의 타이밍: planner의 첫 번째 라운드를 건너뛰는 seed 작업도
	// (Run의 frontier는 비어 있지 않으며 첫 번째 라운드에서는 kick가 아닙니다.) 여기에서는 항상 차단되며 하트비트도 "작업 start + interval"에 있습니다.
	// 첫 번째 계획 단계를 시작합니다. 각 후속 웨이크업(에지/하트비트)은 마지막 임의 계획된 트리거 이후의 시간 길이와 동일합니다.
	heartbeat := time.NewTimer(interval)
	defer heartbeat.Stop()

	// runRound는 한 번의 계획 계획(debounce + 각 guard의 합병 포함)을 실행합니다. src는 트리거 소스를 구별하기 위한 로그에만 사용됩니다.
	runRound := func(src string) {
		// debounce: coalesce a burst of changes into one planning round
		timer := time.NewTimer(e.debounce)
	drain:
		for {
			select {
			case <-t.notify:
			case <-timer.C:
				break drain
			}
		}
		planner, _ := e.snapshotFor(t)
		if planner == nil {
			return // idle until LLM configured
		}
		if e.IsPaused(t.ID) {
			return // user-paused: don't plan
		}
		if e.IsDeleting(t.ID) {
			return
		}
		// terminal task (goals all met → done, or failed): the run is over. A
		// resume/nudge — e.g. auto-resume of the active task on restart — must NOT
		// re-plan (it would burn an LLM round and re-confirm a settled result).
		if isTerminalStatus(t.lifecycleSnapshot().Status) {
			return
		}
		// 작업 수준 시간 초과 닫기: 일반 깨우기 삭제 - worker 쓰기 저장 닫기, Resume의 Notify는 더 이상 없습니다.
		// 정기적인 계획 라운드를 시작합니다. 최종 라운드는 코디네이터(settleTask)에 의해 직접 구동되며 여기에는 포함되지 않습니다.
		if e.isSettling(t.ID) {
			return
		}
		// goalless(수동 직접 투자) 분기: 해당 작업을 더 이상 사용할 수 없습니다. open가 대상입니다. planner는 실행되지 않습니다. 실행하면 심각한 처벌을 받게 됩니다.
		// met→cancelExec는 소유자 agent의 직접 투자 의도를 죽입니다. 종료할지 여부는 frontier에 의해 결정됩니다.
		// open/running 의도도 있습니다. → running를 유지하고 조용히 기다리세요. 모든 의도가 도망갔습니다 → done를 삭제합니다.
		// 전체 섹션은 순수 Go이며 LLM 호출을 트리거하지 않으며 계획 휠 marker를 트리거하지 않습니다.
		if open, err := t.Store.HasOpenGoal(); err == nil && !open {
			t.drainTriggers() // 긴 세션에서 goalless의 무한한 증가를 방지하기 위해 누적된 done/finding 트리거를 폐기합니다.
			if active, err := t.Store.HasActiveIntent(); err == nil && !active {
				// frontier가 배수되어 실행 → 종료할 의도가 없습니다. 동시 실행을 방지하려면 Guarded 버전을 사용하여 CAS를 만드세요.
				// pause/delete/ 시간 초과 종료 상태 전환.
				if won, err := e.m.SetTaskStatusGuarded(t.ID, "done"); err != nil {
					log.Printf("[goalless] task %s 종결 done 실패: %v", t.ID, err)
				} else if won {
					e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
						Summary: "모든 목표가 달성되었고, 직접 투자 의도가 구현되었으며, 미션이 종료되었습니다."})
				}
			}
			return // goalless 분기는 planner.Plan를 입력하지 않습니다.
		}
		if !e.beginTaskOperation(t.ID) {
			return
		}
		defer e.decInflight(t.ID)
		e.stampFirstRun(t) // 첫 번째 실제 계획 → first_run_at 포함 + deadline 계산(timeout가 있는 작업만 해당)
		e.touch(t.ID)
		emit := func(r db.Activity) { e.emitActivity(t, r) }
		ectx := e.clockCtx(e.execContextFor(ctx, t.ID), t, false) // cancellable by Pause; 작업 deadline 포함
		if ectx.Err() != nil || e.IsDeleting(t.ID) {
			return
		}
		log.Printf("[planner] task %s 계획중…(%s 방아쇠)", t.ID, src)
		// round marker: each Plan() is one planner round; emit a boundary so the
		// UI can separate rounds in the transcript (kind='round').
		e.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
			Summary: fmt.Sprintf("%d 기획 라운드", e.nextPlannerRound(t.ID))})
		// what fired this round (worker done / finding; may be several — debounce
		// coalesces a burst; empty for time/heartbeat wakes).
		triggers := t.drainTriggers()
		taskIDInt, _ := strconv.ParseInt(t.ID, 10, 64)
		e.BeginLLMCall(t.ID)
		met, reason, err := planner.Plan(ectx, taskIDInt, e.m.assets, t.Store, t.Goal, triggers, emit)
		e.EndLLMCall(t.ID)
		switch {
		case err != nil && ectx.Err() == nil:
			log.Printf("[planner] task %s 계획이 잘못됐어: %v", t.ID, err)
		case met:
			log.Printf("[planner] task %s 달성된 목표 결정: %s", t.ID, reason)
			// 모든 목표 달성 → 지속적인 작업 상태는 done입니다(프론트 엔드 DTO는 이 최종 상태를 먼저 표시합니다).
			if err := e.m.SetTaskStatus(t.ID, "done"); err != nil {
				log.Printf("[planner] task %s 완료 표시 실패: %v", t.ID, err)
			}
			// 작업이 완료된 것으로 판단되었습니다. → 실행 중인 worker를 즉시 취소합니다. 현재 의도를 다 써도 소용이 없습니다.
			// worker의 다음 라운드에서 주기가 최종 상태 게이트에 도달하면 새로운 의도가 수신되지 않습니다. 취소된 배치는 아래의 "작업 완료" 분기로 이동됩니다.
			// stopped(blocked 아님)로 분류됩니다.
			e.cancelExec(t.ID, agent.AbortGoalMet)
		default:
			log.Printf("[planner] task %s 기획완료", t.ID)
		}
		e.touch(t.ID)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.notify:
			runRound("edge") // worker 종료 / finding / kill / resume / seed 1차 라운드
		case <-heartbeat.C:
			// 사이클 하단: 데드 락 하단 + 비행 중 worker(steer/kill) 감독을 위한 절전 모드 해제 + 사이클 검토.
			runRound("heartbeat")
		}
		// 각 기상 후 무거운 팔의 심장 박동(가장자리 또는 심장 박동): 계획된 트리거는 이 휴식 시간을 다시 계산합니다.
		resetPlannerTimer(heartbeat, interval)
	}
}

func (e *Engine) workerLoop(ctx context.Context, t *Task, name string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, worker := e.snapshotFor(t)
		if worker == nil {
			if sleepCtx(ctx, 1500*time.Millisecond) {
				return
			}
			continue
		}
		if e.IsPaused(t.ID) {
			if sleepCtx(ctx, 1000*time.Millisecond) {
				return
			}
			continue // user-paused: don't claim/execute intents
		}
		if e.IsDeleting(t.ID) {
			return
		}
		if e.isSettling(t.ID) {
			if sleepCtx(ctx, 1000*time.Millisecond) {
				return
			}
			continue // 작업 시간이 초과되어 마무리 중입니다. 더 이상 새로운 인텐션을 받지 않습니다. (작업이 실행되는 동안 저절로 완료되며 코디네이터가 기다리고 있습니다. drain)
		}
		if isTerminalStatus(e.m.TaskStatus(t.ID)) {
			if sleepCtx(ctx, 1000*time.Millisecond) {
				return
			}
			continue // 작업이 종료되었습니다(done/failed/timeout): 레거시 의도 수신을 중지하고 완료 후 헛되이 실행하지 마십시오 frontier
		}
		if !e.beginTaskOperation(t.ID) {
			return
		}
		claimed := e.runWorkerStep(ctx, t, name, worker)
		e.decInflight(t.ID)
		if !claimed && sleepCtx(ctx, 800*time.Millisecond) {
			return
		}
	}
}

// runWorkerStep claims one intent from the frontier and fully settles it via
// runIntent. Returns false when nothing was claimable. The pool worker loop is its
// only caller.
func (e *Engine) runWorkerStep(ctx context.Context, t *Task, name string, worker *agent.Worker) bool {
	intent := e.claimNext(t, name)
	if intent == nil {
		return false
	}
	log.Printf("[worker %s] task %s 수집의도 #%d", name, t.ID, intent.ID)
	return e.runIntent(ctx, t, name, worker, intent, "", "")
}

// runIntent executes and fully settles one already-claimed (state=running) intent.
// Both the pool worker loop (via runWorkerStep) and the human-message handler (via
// runDetachedIntent, a dedicated goroutine outside the worker pool) call it, so the
// execute/retry/state-write logic lives in exactly one place. A non-empty message
// is injected as this turn's input through ExecuteWithMessage; requestID keys the
// transcript marker that dedups re-injection across model_error retries. The caller
// must already hold one task-operation admission for the whole sequence so a delete
// cannot observe quiescence between the LLM return and the final DB writes.
func (e *Engine) runIntent(ctx context.Context, t *Task, name string, worker *agent.Worker, intent *db.Node, requestID, message string) bool {
	hasChatMessage := message != ""
	e.stampFirstRun(t) // 첫 번째 실제 실행 → first_run_at 포함 + deadline 계산(timeout가 있는 작업만 해당)
	e.touch(t.ID)
	emit := func(r db.Activity) { e.emitActivity(t, r) }
	ectx := e.clockCtx(e.execContextFor(ctx, t.ID), t, false) // cancellable by Pause; 작업 deadline 포함
	if ectx.Err() != nil || e.IsDeleting(t.ID) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "open"); err != nil {
			log.Printf("[worker %s] task %s 탐색 의도 #%d 수령 후 반품 실패: %v", name, t.ID, intent.ID, err)
		}
		return true
	}
	// per-work child context so the planner's kill_work can stop just this work.
	workCtx, workCancel := context.WithCancelCause(ectx)
	e.registerWork(intent.ID, workCancel)
	// wrap the guard hooks so steer_work can inject a mid-run course-correction
	// for THIS intent (drained before the worker's next tool call).
	iid := intent.ID
	taskEmit := func(a db.Activity) {
		nid := iid
		a.NodeID, a.Worker = &nid, name
		emit(a)
	}
	label := fmt.Sprintf("%s · #%d", name, iid)
	workCtx = intercept.WithTaskContext(workCtx, t.ID, label, taskEmit)
	// nudges는 의도적으로 model_error 재실행 주기 외부에 구축되었습니다. 공회전 지속의 상한은 "이 의도"의 총액입니다.
	// 라운드를 다시 실행할 때 할당량을 지우고 다시 시작해서는 안 됩니다.
	hooks := steerHooks{
		inner:  t.Guard.Hooks(),
		drain:  func() (string, bool) { return e.drainSteer(iid) },
		nudges: &atomic.Int64{},
		limit:  e.emptyTurnNudgeLimit(),
		label:  label,
	}
	wTaskID, _ := strconv.ParseInt(t.ID, 10, 64)
	e.BeginLLMCall(t.ID)
	var reason harness.TerminalReason
	var wrote agent.WriteCounts
	var err error
	if hasChatMessage {
		reason, wrote, err = worker.ExecuteWithMessage(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich, t.NotifyFinding, requestID, message)
	} else {
		reason, wrote, err = worker.Execute(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich, t.NotifyFinding)
	}
	e.EndLLMCall(t.ID)
	// model_error 종료 → 몇 번 더 재실행(후퇴하고 다시 시도). 의도가 work로 유지되는 경우에만 작업
	// 일시정지/종료/취소되지 않은 경우(마감 기간에 진입하지 않은 경우) 재시도합니다. 그렇지 않은 경우 해당 지점에서 처리됩니다. (마감 기간은 그렇지 않습니다.)
	// 다른 worker의 창을 우아하게 닫는 백오프를 방지하려면 다시 시도하세요.
	maxRetries, retryBackoff := e.modelErrorRetryPolicy()
	for attempt := 1; attempt <= maxRetries &&
		retryableWorkerModelError(reason, err) &&
		workCtx.Err() == nil && ectx.Err() == nil && !e.IsPaused(t.ID) && !e.isSettling(t.ID); attempt++ {
		log.Printf("[worker %s] task %s 탐색 의도 #%d model_error 끝，%v 나중에 다시 시도하세요 (%d/%d)",
			name, t.ID, intent.ID, retryBackoff, attempt, maxRetries)
		if sleepCtx(workCtx, retryBackoff) {
			break // 휴방기간 중 취소(해지/정지) → 아래 지점으로 인계하여 처리
		}
		e.BeginLLMCall(t.ID)
		if hasChatMessage {
			reason, wrote, err = worker.ExecuteWithMessage(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich, t.NotifyFinding, requestID, message)
		} else {
			reason, wrote, err = worker.Execute(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich, t.NotifyFinding)
		}
		e.EndLLMCall(t.ID)
	}
	// Capture kill state before detachWork cancels workCtx. kill = this work's
	// ctx was cancelled (planner kill_work) while the TASK ctx kept running; a
	// pause cancels the task ctx (ectx) instead. Checking workCtx.Err() AFTER
	// unregister would always be true (unregister cancels it) → every completed
	// work would be wrongly marked stopped.
	workCause := context.Cause(workCtx)
	killed := workCtx.Err() != nil && ectx.Err() == nil
	action, completeWork := e.detachWork(intent.ID)
	// A caller may stop waiting and release its in-memory reservation before the
	// agent honors cancellation. The named context cause remains authoritative and
	// still settles the stopped run into a recoverable state.
	if action == "" {
		switch {
		case errors.Is(workCause, agent.AbortWorkPausedByUser):
			action = "pause"
		case errors.Is(workCause, agent.AbortWorkCancelledByUser):
			action = "cancel"
		}
	}
	var controlErr error
	defer func() { completeWork(controlErr) }()
	if action == "pause" {
		controlErr = transitionIntentState(t.Store, intent.ID, "running", "paused")
		if controlErr != nil {
			log.Printf("[worker %s] task %s 탐색 의도 #%d 일시 중지된 상태에서 라이브러리를 삭제하지 못했습니다.: %v", name, t.ID, intent.ID, controlErr)
			return true
		}
		log.Printf("[worker %s] task %s 탐색 의도 #%d 일시 중지됨", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	if action == "cancel" {
		// Park the stopped run in paused before handing cleanup to the API. If the
		// request disconnects after cancellation, the intent remains recoverable and
		// a later cancel can finish cleanup instead of leaving a phantom running row.
		controlErr = transitionIntentState(t.Store, intent.ID, "running", "paused")
		if controlErr != nil {
			log.Printf("[worker %s] task %s 탐색 의도 #%d 펜스 드롭인을 취소하지 못했습니다.: %v", name, t.ID, intent.ID, controlErr)
			return true
		}
		log.Printf("[worker %s] task %s 탐색 의도 #%d 중지되었습니다. 정리 취소를 기다리는 중입니다.", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	// if a pause cancelled this run mid-flight, return the intent to the frontier
	// so it is re-claimed on resume — the worker will resume the prior LLM
	// conversation from its transcript instead of restarting from scratch.
	if ectx.Err() != nil && taskExecutionPaused(context.Cause(ectx)) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "open"); err != nil {
			log.Printf("[worker %s] task %s 탐색 의도 #%d 작업 일시 중지 및 롤백 실패: %v", name, t.ID, intent.ID, err)
		}
		return true
	}
	// cancel(pause 아님, kill 아님)로 끝나는 작업 시간 초과의 확실한 결론은 이 run를 취소 → exhausted(종료됨)로 분류되며,
	// blocked 라벨을 잘못 붙이지 마십시오. 이때 worker는 일반적으로 settlement 단계에서 결과를 다시 작성했습니다.
	if ectx.Err() != nil && e.isSettling(t.ID) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "exhausted"); err != nil {
			log.Printf("[worker %s] task %s 탐색 의도 #%d 시간 초과 종료 상태를 라이브러리에 추가하지 못했습니다.: %v", name, t.ID, intent.ID, err)
		}
		log.Printf("[worker %s] task %s 탐색 의도 #%d 작업 시간 초과로 인해 종료됨(exhausted)，답장하다 %s", name, t.ID, intent.ID, wrote)
		e.touch(t.ID)
		return true
	}
	// 작업이 완료된 것으로 판단되었습니다(done via 일반 경로)→ 위의 cancelExec가 이 run를 취소했습니다. 의도한 결과는 의미가 없으며,
	// stopped(blocked 아님)를 표시하고 완료된 작업의 의도 상태를 오염시키지 마세요.
	if ectx.Err() != nil && isTerminalStatus(e.m.TaskStatus(t.ID)) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "stopped"); err != nil {
			log.Printf("[worker %s] task %s 탐색 의도 #%d 최종 상태 중지 및 드롭아웃 실패: %v", name, t.ID, intent.ID, err)
		}
		log.Printf("[worker %s] task %s 탐색 의도 #%d 작업이 완료되어 취소되었습니다.(stopped)", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	// killed by the planner: mark stopped (don't write back results, don't auto-reclaim).
	if killed {
		if err := transitionIntentState(t.Store, intent.ID, "running", "stopped"); err != nil {
			log.Printf("[worker %s] task %s 탐색 의도 #%d planner 라이브러리 드롭아웃을 중지하지 못했습니다.: %v", name, t.ID, intent.ID, err)
		}
		log.Printf("[worker %s] task %s 탐색 의도 #%d 종료됨(stopped)", name, t.ID, intent.ID)
		e.touch(t.ID)
		t.Notify()
		return true
	}
	if err != nil {
		log.Printf("[worker %s] intent %d: %v", name, intent.ID, err)
	}
	// 터미널 전환: 단계 상한에 도달 ≠ 완료되었습니다. max_turns→exhausted (기획자는 이를 바탕으로 이 방향을 알고 있습니다.
	// 시도했지만 실제로 완료되지 않은 경우 덮힌 것으로 처리하고 영구적으로 건너뛰는 대신 각도를 변경해야 합니다.) 오류 → blocked; 일반 → done.
	state := "done"
	switch {
	case err != nil:
		state = "blocked"
	case reason == harness.ReasonMaxTurns:
		state = "exhausted"
		log.Printf("[worker %s] intent %d 걸음 수 상한에 도달(exhausted)，이번엔 답장해 %s", name, intent.ID, wrote)
	case reason == harness.ReasonTimeout:
		state = "exhausted"
		log.Printf("[worker %s] intent %d 실행 시간 초과(exhausted)，마친 후 다시 쓰기 %s", name, intent.ID, wrote)
	}
	if state == "blocked" && isTaskLLMChainExhausted(err) {
		_ = t.Store.SetIntentBlockedReason(intent.ID, db.IntentBlockedLLMQuota)
	} else {
		if stateErr := transitionIntentState(t.Store, intent.ID, "running", state); stateErr != nil {
			log.Printf("[worker %s] task %s 탐색 의도 #%d 최종 상태 %s 창고에 반입하지 ​​못했습니다.: %v", name, t.ID, intent.ID, state, stateErr)
		}
	}
	log.Printf("[worker %s] task %s 탐색 의도 #%d 마치다: %s (답장하다 %s)", name, t.ID, intent.ID, state, wrote)
	e.touch(t.ID)
	t.NotifyDone(intent.ID) // results changed the graph -> wake the planner (with the just-finished intent id)
	return true
}

// runDetachedIntent runs one paused intent OUTSIDE the worker pool in its own
// goroutine — the human-message path. It transitions the intent paused->running
// itself (never through 'open'), so the pool, which only claims 'open', can never
// race it; the "at most one run per intent" invariant still holds because winning
// the CAS is the sole entry and work[intentID] was cleared when the pause settled.
// Because it does not compete for a frontier slot, a user message continues the
// worker immediately even when all pool slots are busy (mirroring how the
// main-agent chat handler starts its run directly). The spawned goroutine owns one
// task-operation admission for the whole run and roots its context at ctx (pass the
// server root, never the HTTP request, so a disconnect cannot strand the run while
// task pause/delete/shutdown still stops it). Returns an error if the run could not
// be started; the intent is left untouched in that case.
func (e *Engine) runDetachedIntent(ctx context.Context, t *Task, intentID int64, requestID, message, agentMessage string) error {
	if !e.beginTaskOperation(t.ID) {
		return fmt.Errorf("task is being deleted")
	}
	release := true
	defer func() {
		if release {
			e.decInflight(t.ID)
		}
	}()
	_, worker := e.snapshotFor(t)
	if worker == nil {
		return fmt.Errorf("worker는 아직 준비되지 않았습니다")
	}
	node, err := t.Store.GetNode(intentID)
	if err != nil {
		return err
	}
	if node == nil || node.Kind != db.KindIntent {
		return fmt.Errorf("intent not found")
	}
	changed, err := t.Store.CompareAndSetIntentState(intentID, "paused", "running")
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("%w: 의도가 더 이상 paused 상태에 있지 않습니다.", db.ErrIntentStateConflict)
	}
	node.State, node.Owner = "running", "chat"
	// Record the human turn as a visible activity BEFORE the run starts, so it is
	// ordered ahead of any worker step and never appears without the run happening.
	// Keep the UI copy concise; ExecuteWithMessage writes the server-resolved
	// reference snapshot into the intent transcript as the LLM input.
	uid := intentID
	e.emitActivity(t, db.Activity{NodeID: &uid, Worker: "user", Kind: "user", Summary: message, Detail: message})
	release = false // ownership of the admission passes to the goroutine
	go func() {
		defer e.decInflight(t.ID)
		e.runIntent(ctx, t, "chat", worker, node, requestID, agentMessage)
	}()
	return nil
}

func taskExecutionPaused(cause error) bool {
	var abort *agent.AbortCause
	if !errors.As(cause, &abort) {
		return false
	}
	switch abort.Code {
	case "paused_by_user", "paused_by_orchestrator", "paused_on_reload", "paused_race_guard",
		"queued_for_admission", "llm_unavailable_queued", "task_deleted":
		return true
	default:
		return false
	}
}

func sleepCtx(ctx context.Context, d time.Duration) (done bool) {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func (e *Engine) claimNext(t *Task, name string) *db.Node {
	fr, _ := t.Store.Frontier(20)
	for _, in := range fr {
		if ok, _ := t.Store.ClaimIntent(in.ID, name); ok {
			return in
		}
	}
	return nil
}
