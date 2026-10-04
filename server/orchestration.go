package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// jsonResult marshals v to a JSON tool result.
func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// 이 문서는 P2 "교차 작업 오케스트레이션 도구 세트"(docs/ 벤치마크 오케스트레이션 §2 P2)를 구현합니다. host 도구는 다음과 같습니다. 필수
// Manager(모든 작업의 ​​Store), Engine(일시 중지)에 액세스하고 작업 프로세스를 빌드하여 server 레이어에 상주하도록 합니다.
// 읽기 도구는 대상 작업의 store에서 실행되도록 "기존 per-task 도구"를 리디렉션합니다(임시 ToolSet 생성).
// 그리고 Call 및 해당 도구)를 사용하여 정확히 동일한 논리를 재사용합니다. 컨트롤 클래스(spawn/pause)는 Manager/Engine를 직접 호출합니다.
// 그들은 교통 도구와 같습니다. seed는 tools 테이블에 입력되고 agent에 따라 바인딩됩니다(오케스트레이션 agent에 바인딩된 경우에만 표시됨).

// hostTools is the runtime host-tool provider fed to ToolAugment: traffic tools
// (gated by capture) + cross-task orchestration tools + user-defined custom tools.
// The second return is the names of custom tools flagged `deferred` (schema
// withheld, routed via SearchExtraTools/ExecuteExtraTool). Per-agent binding still
// decides who actually sees any of them.
//
//nolint:unused // used as the hostTools provider in wireAgentAugment
func (s *Server) hostTools() ([]actool.CoreTool, map[string][]string) {
	tools := append(s.m.HostTools(), s.orchestrationTools()...)
	tools = append(tools, s.findingRetestTools()...)
	tools = append(tools, s.platformTools()...) // 플랫폼 운영 도구(skill/ 도구/MCP 수정, Auto에서 사용)
	custom, err := s.customTools()
	if err != nil {
		log.Printf("[custom-tool] 불러오기 실패: %v", err)
		return tools, nil
	}
	tools = append(tools, custom...)
	// deferred custom tools → name -> its bound agent keys. ToolAugment turns a
	// name into a deferred entry only for agents it's actually bound to (so we don't
	// advertise a tool the per-agent binding will drop from the callable set).
	deferred := map[string][]string{}
	rows, _ := s.m.pg.ListCustomTools()
	for _, t := range rows {
		if t.Deferred && t.Enabled {
			deferred[t.Key] = t.Agents
		}
	}
	return tools, deferred
}

// orchestrationTools returns the cross-task tool set. Bound per-agent via the
// tools table (default: no binding — opt-in for orchestration agents).
func (s *Server) orchestrationTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolListTasks(),
		s.toolListLLMProfiles(),
		s.toolSpawnTask(),
		s.toolPauseTask(),
		s.toolGetTaskGraph(),
		s.toolListTaskFindings(),
		s.toolAddHint(),
		s.toolGetWorkerTrace(),
		s.toolListWorkerTraces(),
		s.toolSearchWorkerTraces(),
		s.toolGetTaskNodeDetail(),
		s.toolUpdateFindingReport(),
		s.toolGetFindingTraffic(),
		s.toolBindFindingTraffic(),
	}
}

// --- schema helpers ---

func strParam(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// parseProfileID reads an LLM profile id from a tool arg that may arrive as a JSON
// number (5) or a numeric string ("5"); returns 0 when absent/unparseable.
func parseProfileID(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(str), 10, 64)
		return v
	}
	return 0
}

func objSchema(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		m["required"] = req
	}
	return m
}

func roTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func wrTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// delegateToTask resolves the `task_id` in the input, builds a ToolSet bound to
// that task's store, strips task_id, and calls the chosen per-task tool — so the
// cross-task read reuses the exact in-task logic against another task.
func (s *Server) delegateToTask(ctx context.Context, in json.RawMessage, pick func(*agent.ToolSet) actool.CoreTool) (actool.Result, error) {
	var head struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(in, &head)
	if strings.TrimSpace(head.TaskID) == "" {
		return actool.Errorf("task_id가 필요합니다"), nil
	}
	t, ok := s.m.Task(head.TaskID)
	if !ok {
		return actool.Errorf("task가 존재하지 않습니다: " + head.TaskID), nil
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(in, &m)
	delete(m, "task_id")
	inner, _ := json.Marshal(m)
	tsx := agent.NewToolSet(t.Store, "orchestrator")
	if s.m.Assets() != nil {
		tsx.SetAssetStore(s.m.Assets(), s.m.Assets().Companies())
	}
	tsx.SetNotify(t.Notify)         // 범용 웨이크업(전용 콜백 없이 쓰기 작업이 수행됩니다. 읽기 도구는 no-op입니다.)
	tsx.SetNotifyHint(t.NotifyHint) // add_hint → "People added N strategy Tips:..."를 기억하고 planner를 깨우세요
	return pick(tsx).Call(ctx, inner, nil)
}

// --- tools ---

func (s *Server) toolListTasks() actool.CoreTool {
	return roTool("list_tasks",
		"모든 작업(id/ 설명/목표/상태/실행 시간/상위 작업/LLM 구성)을 나열하고, agent를 정리하여 전체적인 상황을 파악하고, 어떤 작업이 너무 오래 정체되어 있는지, 각각에 어떤 LLM를 사용해야 하는지 확인해보세요. 실행 시간: 실행 중 = 생성됨 → 현재, 최종 상태 = 생성됨 → 마지막 활동(초). llm_profile: 작업 planner/worker에서 사용되는 구성 이름, (활성화 구성) = 전역 활성화를 따릅니다.",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			lastAct, _ := s.m.PG().LastActivityAll()
			// id -> name to resolve each task's pinned LLM profile.
			profName := map[int64]string{}
			if profs, err := s.m.pg.ListProfiles(); err == nil {
				for _, p := range profs {
					profName[p.ID] = p.Name
				}
			}
			out := make([]map[string]any, 0)
			for _, t := range s.m.List() {
				status := s.deriveTaskStatus(t)
				end := lastAct[t.ExpID]
				if live := s.engine.LastActivity(t.ID); live > end {
					end = live
				}
				dur := int64(0)
				if status == "running" {
					dur = time.Now().Unix() - t.CreatedAt
				} else if end > t.CreatedAt {
					dur = end - t.CreatedAt
				}
				row := map[string]any{"id": t.ID, "description": t.Description, "goal": t.Goal, "status": status, "run_seconds": dur}
				if t.ParentRef != "" {
					row["parent_ref"] = t.ParentRef
				}
				llmState := t.llmStateSnapshot()
				if llmState.ProfileID == nil {
					row["llm_profile"] = "(구성 활성화)"
				} else if n, ok := profName[*llmState.ProfileID]; ok {
					row["llm_profile"] = n
				} else {
					row["llm_profile"] = fmt.Sprintf("#%d(삭제됨)", *llmState.ProfileID)
				}
				out = append(out, row)
			}
			return jsonResult(out)
		})
}

// toolListLLMProfiles lists the available LLM profiles (name/model/active) so an
// orchestration agent can pick one for spawn_task's llm_profile. Never leaks keys.
func (s *Server) toolListLLMProfiles() actool.CoreTool {
	return roTool("list_llm_profiles",
		"사용 가능한 LLM 구성(profile) 목록: id, 이름, 모델, 형식, 현재 활성 구성인지 여부. id를 사용하여 spawn_task의 llm_profile_id 매개변수에 대해 하위 작업별 LLM를 지정합니다(예: 정찰을 위한 저렴한 모델 및 활용을 위한 강력한 모델). API Key를 포함하지 않습니다.",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			profs, err := s.m.pg.ListProfiles()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := make([]map[string]any, 0, len(profs))
			for _, p := range profs {
				out = append(out, map[string]any{
					"id": p.ID, "name": p.Name, "model": p.Model, "format": p.Format, "is_active": p.IsDefault,
				})
			}
			return jsonResult(map[string]any{"profiles": out})
		})
}

func (s *Server) toolSpawnTask() actool.CoreTool {
	return wrTool("spawn_task",
		"새 하위 작업을 생성하고 탐색 엔진을 시작하여 task_id를 반환합니다. 하나의 항목(예: 질문/목표)을 독립적인 작업에 할당하는 데 사용됩니다. parent_ref 선택 사항: 현재 오케스트레이션 연결의 상위 작업을 입력합니다. id 상위-하위 연결을 만듭니다.",
		objSchema(map[string]any{
			"description":            strParam("작업 설명(짧은 제목)"),
			"goal":                   strParam("임무 목표(무엇을 달성할 것인가)"),
			"parent_ref":             strParam("선택 사항: 상위 작업 id(상위-하위 연결 만들기)"),
			"source_task_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": fmt.Sprintf("선택 사항: 읽기 전용 상속된 소스 작업 id 목록(최대 %d). 하위 작업은 이러한 작업의 입증된 자산/결론을 시작점으로 읽기 전용으로 참조할 수 있습니다. 이는 parent_ref의 순수 부모-자식 포인터와 달리 콘텐츠 상속입니다.", db.MaxTaskSourceCount)},
			"llm_profile_id":         map[string]any{"type": "integer", "description": "선택 사항: 이 하위 작업 planner/worker에서 사용되는 LLM 구성 id를 지정합니다(list_llm_profiles 참조). 상위 작업을 상속하고 전역 활성화 구성으로 롤백하려면 비워 두세요."},
			"timeout_seconds":        map[string]any{"type": "integer", "description": "선택 사항: 작업 수준 제한 시간(초). 지점에 도달한 후 우아한 엔딩이 트리거되고 timeout의 최종 상태로 들어갑니다. 비워두거나 0 = 시간 제한 없음"},
			"plan_heartbeat_seconds": map[string]any{"type": "integer", "description": "옵션: planner 하트비트 트리거 간격(초). 이 값은 마지막 계획 라운드 종료/임무 시작부터 도달하며 해당 기간 동안 트리거가 없습니다 → 계획 라운드 트리거(교착 상태 + 비행 worker를 감독하기 위한 깨우기). 비워두거나 0 = 기본값 600(10분);"},
			"seed_first_intent":      map[string]any{"type": "boolean", "description": "선택사항: 간단한 작업에 대해 활성화할 수 있습니다. 생성 시 worker가 planner의 첫 번째 라운드를 기다리지 않고 직접 테스트를 시작할 수 있도록 시드 의도(콘텐츠 = 설명 + 대상)를 직접 보냅니다. 기본값은 false입니다(먼저 표준 계획을 따른 후 실행)."},
		}, "description", "goal"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Description          string          `json:"description"`
				Goal                 string          `json:"goal"`
				ParentRef            string          `json:"parent_ref"`
				SourceTaskIDs        []string        `json:"source_task_ids"`
				LLMProfileID         json.RawMessage `json:"llm_profile_id"`
				TimeoutSeconds       int             `json:"timeout_seconds"`
				PlanHeartbeatSeconds int             `json:"plan_heartbeat_seconds"`
				SeedFirstIntent      bool            `json:"seed_first_intent"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Description) == "" {
				a.Description = "이름이 없는 작업"
			}
			if strings.TrimSpace(a.Goal) == "" {
				return actool.Errorf("goal가 필요합니다"), nil
			}
			if a.TimeoutSeconds < 0 {
				a.TimeoutSeconds = 0
			}
			// 읽기 전용 상속 소스 작업: 개수 상한 + 각 id는 유효/중복 제거/존재하며 확인 규칙은 HTTP 생성 작업과 일치합니다.
			if len(a.SourceTaskIDs) > db.MaxTaskSourceCount {
				return actool.Errorf(fmt.Sprintf("최대 %d 관련 작업을 선택할 수 있습니다.", db.MaxTaskSourceCount)), nil
			}
			sourceIDs := make([]int64, 0, len(a.SourceTaskIDs))
			seenSources := map[int64]bool{}
			for _, raw := range a.SourceTaskIDs {
				id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
				if err != nil || id <= 0 || seenSources[id] {
					return actool.Errorf("연결된 작업 id가 잘못되었거나 중복되었습니다."), nil
				}
				if _, ok := s.m.Task(strconv.FormatInt(id, 10)); !ok {
					return actool.Errorf(fmt.Sprintf("관련 작업 #%d가 존재하지 않습니다.", id)), nil
				}
				seenSources[id] = true
				sourceIDs = append(sourceIDs, id)
			}
			// LLM profile resolution: explicit id > inherit parent's pin > active(nil).
			var pin *int64
			if id := parseProfileID(a.LLMProfileID); id > 0 {
				if _, ok := s.loadProfileConfig(id); !ok {
					return actool.Errorf(fmt.Sprintf("LLM 구성 #%d가 존재하지 않거나 설정되지 않았습니다. API Key", id)), nil
				}
				pin = &id
			} else if a.ParentRef != "" {
				if pt, ok := s.m.Task(a.ParentRef); ok {
					pin = pt.LLMProfileID
				}
			}
			var llmIDs []int64
			if pin != nil {
				llmIDs = []int64{*pin}
			}
			t, err := s.m.CreateTaskWithOptions(a.Description, a.Goal, db.TaskCreateOptions{
				SourceTaskIDs:        sourceIDs,
				LLMProfileIDs:        llmIDs,
				TimeoutSeconds:       a.TimeoutSeconds,
				PlanHeartbeatSeconds: a.PlanHeartbeatSeconds,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if a.ParentRef != "" {
				t.ParentRef = a.ParentRef
				if id, e := strconv.ParseInt(t.ID, 10, 64); e == nil {
					_ = s.m.PG().SetParentRef(id, a.ParentRef)
				}
			}
			// 공유 사후 생성 프로세스는 HTTP 생성 작업(server.go createTask)과 동일한 세그먼트 launchTask를 재사용합니다.
			// seed + 백그라운드에서 목표 분해를 시각적으로 수행합니다(라운드 0/LLM 단계/목표를 하나씩) + engine.Run.
			// seed_first_intent는 기본적으로 false로 설정됩니다(표준이 먼저 계획된 후 실행됨). 간단한 작업을 통해 work 테스트를 직접 실행할 수 있습니다.
			s.launchTask(t, a.Description+" "+a.Goal, a.SeedFirstIntent)
			return actool.Text(fmt.Sprintf("task created: %s", t.ID)), nil
		})
}

func (s *Server) toolPauseTask() actool.CoreTool {
	return wrTool("pause_task", "지정된 작업을 일시 중지합니다(planner/worker 루프 중지).",
		objSchema(map[string]any{"task_id": strParam("일시 중지할 작업 id")}, "task_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID string `json:"task_id"`
			}
			_ = json.Unmarshal(in, &a)
			t, ok := s.m.Task(a.TaskID)
			if !ok {
				return actool.Errorf("task가 존재하지 않습니다: " + a.TaskID), nil
			}
			if _, err := s.applyTaskControlWithCause(t, "pause", agent.AbortPausedByOrchestrator); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("task paused: " + a.TaskID), nil
		})
}

func (s *Server) toolGetTaskGraph() actool.CoreTool {
	return roTool("get_task_graph", "지정된 작업의 탐색 그래프 개요(graph_overview: 자산 수/frontier/ 검색/커버리지 등)를 읽고 task_id를 사용하여 작업을 지정합니다.",
		objSchema(map[string]any{"task_id": strParam("작업 id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GraphOverviewTool)
		})
}

func (s *Server) toolListTaskFindings() actool.CoreTool {
	return roTool("list_task_findings", "지정된 작업의 확인 취약점 읽기(포함 flag/PoC；스트립 당 id/task_id/intent_id/vulnclass/severity/요약/상태)，사용 task_id 작업 지정。",
		objSchema(map[string]any{"task_id": strParam("작업 id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListFindingsTool)
		})
}

func (s *Server) toolAddHint() actool.CoreTool {
	return wrTool("add_task_hint", "지정된 작업에 전략적 힌트를 삽입합니다(작업의 planner는 다음 의도 생성 라운드에서 읽혀집니다). \n"+
		"★우선순위 배치: 여러 개의 프롬프트를 hints 배열에 넣고 한 번에 제출합니다(ids 배열을 반환하고 hints와 길이 및 순서가 동일하며 실패한 항목 id=0). 단일 프롬프트의 경우 hints가 생략되고 최상위 text로 직접 전송됩니다.",
		objSchema(map[string]any{
			"task_id":      strParam("작업 id"),
			"hints":        map[string]any{"type": "array", "description": "【이것을 먼저 사용하세요] 프롬프트 배열, 각 요소 필드는 최상위 수준과 동일합니다.（text/asset_ids/traffic_refs）。", "items": objSchema(map[string]any{"text": strParam("프롬프트 내용"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": agent.HintTrafficSchema()})},
			"text":         strParam("[하나의] 프롬프트 내용"),
			"traffic_refs": agent.HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "고정된 자산 id(선택 사항, 0/1/다중, 이 작업 내의 자산 id)"},
		}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).AddHintTool)
		})
}

func (s *Server) toolGetWorkerTrace() actool.CoreTool {
	return roTool("get_task_worker_trace",
		"지정된 작업에서 특정 work(의도)의 실행 프로세스를 살펴보세요. get_task_worker_trace(task_id, intent_id)는 단계 요약을 살펴봅니다. 그런 다음 step_ids=[...]을 추가하여 해당 단계의 전체 내용을 가져옵니다(한 번에 최대 5개, 다중 전송의 경우 처음 5개만 반환됨).",
		objSchema(map[string]any{
			"task_id":   strParam("작업 id"),
			"intent_id": map[string]any{"type": "integer", "description": "인텐트 id(이 작업에서는 work)"},
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "선택 사항: 전체 콘텐츠 id를 가져오는 단계(한 번에 최대 5개, 다중 전송을 위해 처음 5개만 반환되고 나머지는 omitted_step_ids에 나열됨)"},
		}, "task_id", "intent_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GetWorkerTraceTool)
		})
}

func (s *Server) toolListWorkerTraces() actool.CoreTool {
	return roTool("list_task_worker_traces", "지정된 작업에서 실행된 work(의도) + 각 단계 수를 나열하여 살펴볼 가치가 있는 work를 찾는 데 사용됩니다(get_task_worker_trace 재사용).",
		objSchema(map[string]any{"task_id": strParam("작업 id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListWorkerTracesTool)
		})
}

func (s *Server) toolSearchWorkerTraces() actool.CoreTool {
	return roTool("search_task_worker_traces", "지정된 작업에서 키워드로 모든 work의 실행 프로세스를 검색합니다(히트 단계 요약 반환 + intent_id).",
		objSchema(map[string]any{"task_id": strParam("작업 id"), "q": strParam("키워드 검색")}, "task_id", "q"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).SearchWorkerTracesTool)
		})
}

func (s *Server) toolGetTaskNodeDetail() actool.CoreTool {
	return roTool("get_task_node_detail",
		"지정된 작업(발견/사실/의도/목표: 요약 + 세부정보/증거/PoC)에서 탐색 그래프 노드의 전체 내용을 읽습니다. id는 탐색 노드 id입니다(예: 반환된 report_finding 또는 list_task_findings의 id). 취약점 보고서를 작성하기 전에 이를 사용하여 취약점에 대한 완전한 증거를 얻으십시오.",
		objSchema(map[string]any{
			"task_id": strParam("작업 id"),
			"id":      map[string]any{"type": "integer", "description": "탐색 그래프 노드 id(비자산 id)"},
		}, "task_id", "id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).NodeDetailTool)
		})
}

// toolUpdateFindingReport writes/overwrites a finding's detailed Markdown report.
// finding_id is the id report_finding returned ("finding recorded: <id>", the
// finding node id). The write (SetFindingReportByNodeID) is keyed by node_id and
// task-agnostic, so this host tool needs no task_id / exploration store.
func (s *Server) toolUpdateFindingReport() actool.CoreTool {
	return wrTool("update_finding_report",
		"등록된 취약점에 대한 [상세 보고서]를 작성/업데이트합니다(Markdown의 전체 텍스트, 전체 단락이 이전 내용을 다루고 있음). finding_id는 report_finding에서 반환된 id를 전달합니다(\"finding recorded의 숫자: <id>\"). 보고서 권장 사항에는 취약점 개요, 영향 및 피해, 재현 단계, 증거/PoC 및 수리 제안이 포함됩니다.",
		objSchema(map[string]any{
			"finding_id":       map[string]any{"type": "integer", "description": "대상 취약점 id(report_finding에서 반환된 id)"},
			"report":           strParam("상세 보고서 전문, Markdown 형식"),
			"evidence_version": map[string]any{"type": "integer", "description": "get_finding_traffic 반환된 증거 version; 보고서가 새로운 증거 변경 사항을 다루는 것을 방지하는 데 사용됩니다."},
		}, "finding_id", "report"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				EvidenceVersion *int64          `json:"evidence_version"`
				FindingID       json.RawMessage `json:"finding_id"`
				Report          string          `json:"report"`
			}
			_ = json.Unmarshal(in, &a)
			nodeID := parseProfileID(a.FindingID) // "숫자 또는 숫자 문자열" 구문 분석 재사용
			if nodeID <= 0 {
				return actool.Errorf("finding_id가 유효하지 않습니다"), nil
			}
			n, err := s.m.pg.SetFindingReportVersionByNodeID(ctx, nodeID, a.Report, a.EvidenceVersion)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == 0 {
				return actool.Errorf(fmt.Sprintf("finding_id=%d에 해당하는 취약점 레코드를 찾을 수 없습니다. (report_finding를 사용하여 먼저 등록하세요)", nodeID)), nil
			}
			return actool.Text(fmt.Sprintf("finding %d report updated (%d chars)", nodeID, len(a.Report))), nil
		})
}

// deriveTaskStatus mirrors listTasks' status derivation for the list_tasks tool.
func (s *Server) deriveTaskStatus(t *Task) string {
	lifecycle := t.lifecycleSnapshot()
	switch {
	case isTerminalStatus(lifecycle.Status):
		return lifecycle.Status
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		return "paused"
	case s.engine.ReadyFor(t) && s.engine.Started(t.ID):
		return "running"
	}
	return "created"
}

// orchestrationToolSeeds seeds the cross-task tools into the tools table so they
// are bindable per-agent (default: bound to nobody — opt-in for orchestration
// agents). First-insert only, like the traffic seeds.
func (s *Server) seedOrchestrationTools() {
	// task-op + platform tools default-bind to the built-in Auto agent(
	// 운영 플랫폼). SeedTool의 첫 번째 삽입이 적용됩니다. 이전 라이브러리의 seed 행은 seedAutoDefaultBindings로 보완됩니다.
	autoAgents, _ := json.Marshal([]string{"auto"})
	for _, t := range s.orchestrationTools() {
		schema, _ := json.Marshal(t.InputSchema())
		bindings := autoAgents
		if t.Name() == "bind_finding_traffic" {
			bindings = json.RawMessage(`["reporter"]`)
		}
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, bindings)
	}
	for _, t := range s.platformTools() {
		schema, _ := json.Marshal(t.InputSchema())
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, autoAgents)
	}
	s.refreshBuiltinToolSchemas()
	s.seedAutoDefaultBindings()
	s.seedPlannerDefaultBindings()
	s.seedPlannerListAssetsBinding()
	s.seedCompanyScopeRebind()
	s.seedWorkerReadToolsUnbind() // list_facts/list_companies/list_worker_traces는 기본적으로 worker에서 바인딩 해제됩니다(일회성).
	s.seedWorkerReadbackRebind()  // 이전 마이그레이션의 실수로 삭제된 문제 수정: search_all_worker_traces/get_worker_trace/node_detail를 worker에 다시 바인딩(일회성 사용)
	s.seedAutoReportFindingBinding()
	s.unbindGoalMetDefault()
	s.reseedGoalsPrompt()             // goals "펌프 작동 제약" 단계 프롬프트 단어 추가 → 새 기본값에 기존 라이브러리의 새 버전 추가(일회성)
	s.reseedMainAgentPrompt()         // mainagent 프롬프트 단어 "목표 달성 후 add_intent는 목표를 세울지 여부를 묻습니다."(1회)
	s.reseedPlannerPrompt()           // planner 프롬프트어: "의도 0" 정당한 사유 재작성 + 정량적 승인 확인 추가(1회)
	s.reseedWorkerPrompt()            // worker 프롬프트 단어: 부정적인 결론 증거 임계값 추가(1회)
	s.seedReporterAgent()             // 사전 설정된 "보고서 작성" agent + 도구 바인딩 + finding 트리거(1회)
	s.upgradeReporterTriggerMessage() // 이전 데이터베이스 마이그레이션: reporter가 evidence_version를 반환하도록 합니다(일회성).
	s.seedFindingTrafficTools()       // 선택적 증거 매개변수 및 읽기 전용 증거 도구를 추가하고 사용자 구성을 유지합니다.
	s.seedFindingWorkflowTools()
	// 참고: pentest의 기본 도구 바인딩은 마이그레이션할 필요가 없습니다. BuiltinToolSeeds는 마이그레이션됩니다.
	// list_assets/insert_assets/report_finding/list_findings/list_companies와 함께
	// pentest 및 seed가 준비되었습니다(프로젝트에 아직 이전 라이브러리가 없으므로 마이그레이션되지 않습니다).
}

// refreshBuiltinToolSchemas propagates code schema/description changes on the
// orchestration + platform tools into already-seeded rows ONCE per version flag —
// SeedTool is first-insert-only, so a new param (e.g. llm_profile of spawn_task) never
// reaches an old DB otherwise. Preserves each tool's agent binding + enabled flag.
// Bump the flag whenever these tools' schemas/descriptions change in code.
func (s *Server) refreshBuiltinToolSchemas() {
	const flag = "tool_schema_refresh_v7_list_facts_paging"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	tools := append(s.orchestrationTools(), s.platformTools()...)
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema())
		if err := s.m.pg.RefreshToolDefaults(t.Name(), t.Description(), schema); err != nil {
			log.Printf("[tools] refresh %s schema failed: %v", t.Name(), err)
		}
	}
	// 동시에 일부 내장 agent 도구는 코드 기본값으로 새로 고쳐집니다.
	//   - goal_met: 이전 라이브러리 seed에 대한 설명은 "이번 계획 라운드 종료"로 오해의 소지가 있습니다. 이로 인해 planner는 이를 다음과 같이 간주하게 됩니다.
	//     '빈 라운드 종료' 방식과 실행이 시작되자마자 전체 미션이 완료됐다고 오판하는 방식.
	//   - insert_assets: related 입력 매개변수 추가(자산이 현재 작업과 관련되어 있는지 표시하고 적용 범위에 들어갈지 여부를 결정),
	//     SeedTool만 먼저 삽입되고 이전 라이브러리에는 seed 및 schema가 있으며 그렇지 않으면 이 새 매개변수가 수신되지 않습니다.
	//   - list_facts: 페이징으로 변경하고 limit/before/q를 입력 매개변수로 추가합니다. 이전 라이브러리는 비어 있습니다. seed schema 그렇지 않으면
	//     공구 관리 페이지에 "매개변수 없음"이 표시되고 모델은 이러한 매개변수 설명을 얻을 수 없습니다.
	refreshBuiltin := map[string]bool{"goal_met": true, "insert_assets": true, "list_facts": true}
	for _, sd := range agent.BuiltinToolSeeds() {
		if !refreshBuiltin[sd.Key] {
			continue
		}
		schema, _ := json.Marshal(sd.Schema)
		if err := s.m.pg.RefreshToolDefaults(sd.Key, sd.Desc, schema); err != nil {
			log.Printf("[tools] refresh %s desc failed: %v", sd.Key, err)
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
	log.Printf("[tools] 새로 고침 orchestration/platform 도구 schema 코드 기본값으로(일회용의)")
}

// unbindGoalMetDefault removes goal_met's default "planner" binding ONCE (guarded by
// a settings flag), so existing DBs match the new default of NO agent. goal_met bypasses
// per-goal prove_goal to declare the whole task done — powerful/risky and redundant with
// the prove_goal→auto-complete path — so it ships unbound; users can re-bind it per agent
// in the UI. A user's own binding to another agent is untouched (we only strip planner).
func (s *Server) unbindGoalMetDefault() {
	const flag = "goal_met_unbind_default_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("planner", "goal_met"); err != nil {
		log.Printf("[tools] goal_met 풀다 planner 실패: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// reseedGoalsPrompt goals 대상 디스어셈블러의 프롬프트 단어를 [현재 코드 기본값]으로 새로 고칩니다. - 기본 텍스트가 추가되었기 때문입니다.
// "먼저 작업 제약 조건(set_constraints)을 추출한 다음 대상을 분할합니다" 단계에서 SeedPromptIfEmpty는 먼저 이전 데이터베이스에만 삽입됩니다.
// 기존 version 1은 이 단계를 수신할 수 없습니다. 여기에서 버전 관리를 사용하여 [새 버전 추가] 및 컷오버(ResetPromptToDefault),
// 이전 버전은 여전히 ​​기록에 유지됩니다. 사용자가 이를 사용자 정의한 경우 버전 기록에서 검색할 수 있습니다. settings flag 가드 → 한 번만 수행됩니다.
// 앞으로는 기본값이 bump 및 flag로 변경됩니다. 새 라이브러리는 처리할 필요가 없습니다(SeedPromptIfEmpty에는 seed의 최신 기본값이 있습니다).
func (s *Server) reseedGoalsPrompt() {
	const flag = "goals_prompt_constraint_step_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 성공하든 실패하든 한 번만 시도하세요.
	a, err := s.m.pg.GetAgentByKey("goals")
	if err != nil || a == nil {
		return // agent가 아직 구축되지 않은 새 라이브러리에서 실행되는 경우 seedPrompts는 이 마이그레이션 없이 seed를 최신 기본값으로 직접 사용합니다.
	}
	tmpl := agent.BuiltinPromptSeeds()["goals"]
	if tmpl == "" {
		return
	}
	// 새 라이브러리 seedPrompts는 최신 기본값인 seed였습니다 → 현재 버전은 코드 기본값과 동일하므로 중복 버전을 추가할 필요가 없습니다.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] goals 프롬프트 단어를 새 단어로 새로 고치는 것은 기본적으로 실패합니다.: %v", err)
		return
	}
	log.Printf("[prompts] goals 프롬프트 단어에 새로운 기본 버전이 추가되었습니다.(펌핑 동작 제약 단계 추가,일회용의)")
}

// reseedMainAgentPrompt 프롬프트 단어 mainagent를 [현재 코드 기본값]으로 새로 고칩니다. 기본 텍스트에 "Target All"이 추가되었습니다.
// add_intent의 직접투자의향을 달성한 후, 공식대상으로 등록할지 여부를 해당자에게 문의하시기 바랍니다." 이 지침에 따라 SeedPromptIfEmpty가 먼저 삽입됩니다.
// only, 이전 라이브러리의 기존 버전을 받을 수 없습니다. 버전 관리 [새 버전 추가]를 사용하여 (ResetPromptToDefault)로 전환하면 이전 버전이 여전히 유지됩니다.
// 역사 속에 남아있습니다. 사용자가 이를 사용자 정의한 경우 버전 기록에서 검색할 수 있습니다. settings flag 가드 → 이 작업은 한 번만 수행하십시오. 새로운 라이브러리를 처리할 필요가 없습니다.
// (SeedPromptIfEmpty는 seed의 최신 기본값입니다.) reseedGoalsPrompt와 완전히 동형입니다.
func (s *Server) reseedMainAgentPrompt() {
	const flag = "mainagent_prompt_goalless_intent_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 성공하든 실패하든 한 번만 시도하세요.
	a, err := s.m.pg.GetAgentByKey("mainagent")
	if err != nil || a == nil {
		return // agent가 아직 구축되지 않은 새 라이브러리에서 실행되는 경우 seedPrompts는 이 마이그레이션 없이 seed를 최신 기본값으로 직접 사용합니다.
	}
	tmpl := agent.BuiltinPromptSeeds()["mainagent"]
	if tmpl == "" {
		return
	}
	// 새 라이브러리 seedPrompts는 최신 기본값인 seed였습니다 → 현재 버전은 코드 기본값과 동일하므로 중복 버전을 추가할 필요가 없습니다.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] mainagent 프롬프트 단어를 새 단어로 새로 고치는 것은 기본적으로 실패합니다.: %v", err)
		return
	}
	log.Printf("[prompts] mainagent 프롬프트 단어에 새로운 기본 버전이 추가되었습니다.(목표가 달성된 후에는 목표를 세우기 위해 질문을 하세요.,일회용의)")
}

// reseedPlannerPrompt planner의 프롬프트 단어를 [현재 코드 기본값]으로 변경 - 기본 텍스트가 간소화되고 재구성되었으며 "제한"이 다음으로 다운그레이드되었습니다.
// 중복된 내용만 제거하고 "보도보다 깊이가 우선한다", "하드 바운더리: 목표가 달성되지 않고 실행할 의도가 없으면 목표를 생성해야 한다"고 부정적인 결론에 대한 검토에 한계를 추가합니다.
// 기본값이 크게 변경될 때마다 bump 아래의 flag(현재 v2)는 기존의 기존 데이터베이스를 새로 고칩니다. SeedPromptIfEmpty는 처음으로 삽입됩니다. 기존 라이브러리의 기존 버전을 받을 수 없으므로 버전 관리를 사용합니다.
// [새 버전 추가] 및 과거(ResetPromptToDefault)로 잘라냅니다. 이전 버전은 여전히 ​​기록에 유지됩니다. 사용자가 이를 사용자 정의한 경우 버전에서 기록할 수 있습니다.
// 검색하다. settings flag 가드 → 이 작업은 한 번만 수행하십시오. 새 라이브러리는 처리할 필요가 없습니다(SeedPromptIfEmpty에는 seed의 최신 기본값이 있습니다). 그리고
// reseedGoalsPrompt는 완전히 동형입니다.
func (s *Server) reseedPlannerPrompt() {
	const flag = "planner_prompt_compact_realistic_v2"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 성공하든 실패하든 한 번만 시도하세요.
	a, err := s.m.pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		return // agent가 아직 구축되지 않은 새 라이브러리에서 실행되는 경우 seedPrompts는 이 마이그레이션 없이 seed를 최신 기본값으로 직접 사용합니다.
	}
	tmpl := agent.BuiltinPromptSeeds()["planner"]
	if tmpl == "" {
		return
	}
	// 새 라이브러리 seedPrompts는 최신 기본값인 seed였습니다 → 현재 버전은 코드 기본값과 동일하므로 중복 버전을 추가할 필요가 없습니다.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] planner 프롬프트 단어를 새 단어로 새로 고치는 것은 기본적으로 실패합니다.: %v", err)
		return
	}
	log.Printf("[prompts] planner 프롬프트 단어에 새로운 기본 버전이 추가되었습니다.(간소화된 리팩토링+다운그레이드를 억제하고 중복 항목을 제거합니다.+깊이 우선+부정적인 리뷰 상한,일회용의)")
}

// reseedWorkerPrompt worker의 프롬프트 단어를 [현재 코드 기본값]으로 변경 - 기본 텍스트 단락 record_fact는 "부정적 결론"을 삭제합니다.
// "관찰 + 임시 판독"이라는 전체 문장을 작성하고 confidence(observed/inferred)를 "본래 의도와 수단이 소진되었는지 여부"에서 분리합니다(기획자를 쉽게 오도할 수 있음).
// 동시에 facts 어레이 스트라이핑은 "서로 완전히 독립적이고 병합할 수 없는" 몇 가지 예외로 강화되었습니다. bump flag ~ v3 기존의 이전 라이브러리를 다시 새로 고치도록 합니다.
// SeedPromptIfEmpty는 처음에만 삽입됩니다. 기존 라이브러리의 기존 버전을 받을 수 없으므로 버전 관리 [새 버전 추가]를 이용하여 전환하세요. 이전 버전은 기록에 계속 유지되며 검색할 수 있습니다.
// settings flag 가드 → 이 작업은 한 번만 수행하십시오. 새로운 라이브러리에는 처리가 필요하지 않습니다. reseedGoalsPrompt와 완전히 동형입니다.
func (s *Server) reseedWorkerPrompt() {
	const flag = "worker_prompt_compact_v4"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 성공하든 실패하든 한 번만 시도하세요.
	a, err := s.m.pg.GetAgentByKey("worker")
	if err != nil || a == nil {
		return // agent가 아직 구축되지 않은 새 라이브러리에서 실행되는 경우 seedPrompts는 이 마이그레이션 없이 seed를 최신 기본값으로 직접 사용합니다.
	}
	tmpl := agent.BuiltinPromptSeeds()["worker"]
	if tmpl == "" {
		return
	}
	// 새 라이브러리 seedPrompts는 최신 기본값인 seed였습니다 → 현재 버전은 코드 기본값과 동일하므로 중복 버전을 추가할 필요가 없습니다.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] worker 프롬프트 단어를 새 단어로 새로 고치는 것은 기본적으로 실패합니다.: %v", err)
		return
	}
	log.Printf("[prompts] worker 프롬프트 단어에 새로운 기본 버전이 추가되었습니다.(컨텍스트 세그먼트를 확인하면 다음과 같이 수렴됩니다. list_assets/list_findings,제거하다 list_facts/node_detail/asset_neighbors,일회용의)")
}

// reporterToolCallMessage는 보고서를 작성하기 전에 무조건 get_finding_traffic를 한 번 읽어야 합니다.
// 이 도구는 읽기 전용이며 "캡처 스위치에 의존하지 않습니다". 수동 바인딩의 증거는 자동 바인딩이 꺼져 있는지 여부에 관계없이 읽을 수 있습니다. 여기라면
// "자동 바인딩이 활성화된 경우에만 읽기"라고 씁니다. 기본 폐쇄 구성에서는 reporter가 evidence_version로 전송되지 않습니다.
// SetFindingReportVersionByNodeID는 legacy의 의미에 따라 -1로 작성되며, 취약점 세부정보는 Markdown로 내보내집니다.
// 이제부터 "증거가 변경되었습니다. 보고서를 업데이트해야 합니다."라는 메시지가 항상 있고, UI에는 이를 클리어할 입구가 없습니다.
const reporterToolCallMessage = "방금 report_finding에 의해 취약점이 등록되었습니다. JSON를 반환하는 finding_id(독립 취약성 레코드 ID) 및 finding_node_id(탐색 노드 ID)를 읽어 보시기 바랍니다." +
	"먼저 get_finding_traffic(finding_id)를 사용하여 현재 증거 목록과 해당 version를 읽습니다(빈 목록은 정상이며 평소대로 보고서를 작성합니다)." +
	"실행 가이드에서 자동 바인딩을 활성화한 경우 읽기 전에 이 취약점의 트래픽을 확인하고 상관관계를 확인하세요. 노드 세부사항은 finding_node_id를 사용합니다." +
	"마지막으로 update_finding_report(finding_id=finding_node_id, report, evidence_version=실제 읽기 버전)을 호출하여 저장하고," +
	"evidence_version를 보내야 합니다. 그렇지 않으면 보고서가 영구적으로 보류 중인 업데이트로 표시됩니다. 두 숫자를 혼합하지 마십시오."

// 레거시 트리거 메시지(0.3.8 이하). 여전히 단어 대 단어가 동일한 레코드만 마이그레이션으로 덮어쓰게 되며 사용자가 변경한 내용은 변경되지 않은 상태로 유지됩니다.
const reporterToolCallMessageV1 = "방금 report_finding에 의해 취약점이 등록되었습니다. 트리거 컨텍스트에서 finding_id를 제거하십시오." +
	"(도구는 \"finding recorded: <id>\"의 숫자를 반환합니다.) 및 작업 id, 귀하의 책임에 따라 취약성에 대한 자세한 보고서를 작성합니다." +
	"마지막으로 update_finding_report(finding_id, report)를 호출하여 저장합니다."

// upgradeReporterTriggerMessage 이전 라이브러리에 여전히 기본 복사본이 있는 reporter 트리거 메시지를 새 버전으로 새로 고칩니다.
// seedReporterAgent는 reporter_agent_seed_v1에 의해 보호되고 트리거는 agent가 생성될 때만 작성되므로
// 업그레이드된 라이브러리는 새로운 카피라이팅을 얻을 수 없습니다. schema 도구가 seedFindingTrafficTools에 의해 완료되었습니다.
// evidence_version이지만 reporter에게 이를 사용하라고 지시하는 것은 없습니다. 이는 일회성 작업이며 수정되지 않은 복사본에만 적용됩니다.
func (s *Server) upgradeReporterTriggerMessage() {
	const flag = "reporter_trigger_evidence_version_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 한번만 해보세요
	triggers, err := s.m.pg.ListTriggersFor("reporter")
	if err != nil {
		log.Printf("[reporter] 읽기 트리거 실패: %v", err)
		return
	}
	for _, t := range triggers {
		if !t.OnToolCall || t.ToolCallMessage != reporterToolCallMessageV1 {
			continue // 사용자가 변경했거나 finding 트리거가 아니어서 움직이지 않습니다.
		}
		t.ToolCallMessage = reporterToolCallMessage
		if err := s.m.pg.UpdateTrigger(t); err != nil {
			log.Printf("[reporter] 업그레이드 트리거 메시지가 실패했습니다.: %v", err)
			return
		}
		log.Printf("[reporter] 트리거 메시지가 읽고 다시 게시할 수 있도록 업그레이드되었습니다. evidence_version")
	}
}

// seedReporterAgent는 "보고서 작성" 사용자 정의 agent를 사전 설정합니다(builtin=false, UI에서 편집/삭제 가능).
// update_finding_report + 작업 쿼리 도구를 바인딩하고 "호출 시 report_finding가 트리거됩니다"를 중단합니다.
// 트리거 - 취약점이 등록될 때마다 자세한 보고서를 작성하도록 트리거됩니다. 일회성(settings flag 가드): 사용자가 삭제한 후에는 다시 빌드되지 않습니다.
// 의존하다：orchestration 도구가 이미 이 기능 위에 있습니다. SeedTool 창고에 들어가 묶일 수 있도록。
func (s *Server) seedReporterAgent() {
	const flag = "reporter_agent_seed_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 성공하든 실패하든 한 번만 시도하세요.

	if exist, _ := s.m.pg.GetAgentByKey("reporter"); exist != nil {
		return // key가 점유되었습니다(사용자가 수동으로 생성함) - 적용되지 않음
	}
	a, err := s.m.pg.CreateAgent("reporter", "보고서 작성",
		"상세 취약점 보고서 작성: 취약점 발견 시 자동으로 트리거되며, 증거 및 실행 프로세스를 확인한 후 Markdown 보고서를 작성하고 다시 작성합니다.")
	if err != nil {
		log.Printf("[reporter] 생성 agent 실패: %v", err)
		return
	}
	if err := s.m.pg.SeedPromptIfEmpty(a.ID, agent.ReporterDefaultPrompt); err != nil {
		log.Printf("[reporter] seed prompt 실패: %v", err)
	}
	// 트리거 실행 전략: parallel + none - 하나의 취약점이 보고되고 여러 개의 finding가 동시에 작성됩니다.
	// merge는 none여야 합니다. 그렇지 않으면(기본값 all) finding의 웨이브가 하나의 실행으로 병합되고 병렬화가 의미가 없게 됩니다.
	// maxParallel=5: 한 번에 너무 많은 LLM 호출을 피하기 위해 동시에 최대 5개의 보고 세션.
	if err := s.m.pg.SetAgentTriggerBehavior("reporter", "parallel", "none", 5); err != nil {
		log.Printf("[reporter] 트리거 실행 정책을 설정하지 못했습니다.: %v", err)
	}
	// 바인딩에 필요한 도구: 보고서 작성 + 증거/실행 프로세스/상황 읽기.
	if err := s.m.pg.AddAgentToToolBinding("reporter", []string{
		"update_finding_report", "get_task_node_detail", "list_task_findings",
		"get_task_worker_trace", "list_task_worker_traces", "search_task_worker_traces",
		"get_task_graph",
	}); err != nil {
		log.Printf("[reporter] 바인딩 도구 실패: %v", err)
	}
	// 트리거: 호출 시 report_finding가 트리거됩니다(도구는 finding_id와 함께 "finding recorded: <id>"를 반환합니다.
	// 작업 id도 트리거 메시지에 있습니다.
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey:        "reporter",
		Enabled:         true,
		OnToolCall:      true,
		ToolNames:       []string{"report_finding"},
		ToolCallMessage: reporterToolCallMessage,
	}); err != nil {
		log.Printf("[reporter] 트리거를 생성하지 못했습니다.: %v", err)
	}
	log.Printf("[reporter] 프리셋「보고서 작성」agent + finding 방아쇠")
}

// seedAutoReportFindingBinding adds "auto" to report_finding's binding ONCE so
// conversation-context agents can call it without requiring an intent_id.
func (s *Server) seedAutoReportFindingBinding() {
	const flag = "auto_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("auto", []string{"report_finding"}); err != nil {
		log.Printf("[auto] report_finding 기본 바인딩 실패: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerDefaultBindings adds "planner" to report_finding's binding ONCE
// (guarded by a settings flag), so existing DBs — whose report_finding row was
// seeded as worker-only — also let the planner record findings. Fresh DBs already
// get it via PlannerTools(); this only backfills without overriding a user unbind.
func (s *Server) seedPlannerDefaultBindings() {
	const flag = "planner_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"report_finding"}); err != nil {
		log.Printf("[planner] report_finding 기본 바인딩 실패: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerListAssetsBinding adds "planner" to list_assets's binding ONCE
// (guarded by a settings flag), so existing DBs — whose list_assets row was seeded
// as auto/pentest-only — also let the planner query the asset store by DSL. Fresh
// DBs already get it via PlannerTools(); this only backfills without overriding a
// user unbind.
func (s *Server) seedPlannerListAssetsBinding() {
	const flag = "planner_list_assets_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"list_assets"}); err != nil {
		log.Printf("[planner] list_assets 기본 바인딩 실패: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedCompanyScopeRebind changes add_company_scope's default binding ONCE on
// existing DBs (guarded by a settings flag): the tool moves off worker and onto
// planner — defining a company's asset scope is a planning/main/auto concern, not
// something a worker does mid-exploration. Fresh DBs already get planner via
// PlannerTools() and lack worker via WorkerTools(); this only backfills old rows.
// One-shot + flag-guarded so a user who later re-binds worker isn't overridden.
func (s *Server) seedCompanyScopeRebind() {
	const flag = "company_scope_rebind_v1" // worker→planner 기본 바인딩 전환
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"add_company_scope"}); err != nil {
		log.Printf("[planner] add_company_scope 기본 바인딩 실패: %v", err)
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("worker", "add_company_scope"); err != nil {
		log.Printf("[worker] add_company_scope 바인딩 해제 실패: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadToolsUnbind strips the read-context tools off worker's default
// binding ONCE on existing DBs (guarded by a settings flag): a worker executes one
// intent and writes back — reading facts/companies and listing all workers' traces is
// a planning/main concern, not the executor's. Fresh DBs already lack these via
// WorkerTools(); this only backfills old rows without overriding a user who
// deliberately re-binds worker. Each RemoveAgentFromTool is per-tool +
// membership-guarded, so planner/mainagent bindings of the same tool are untouched.
//
// NOTE: search_all_worker_traces / get_worker_trace / node_detail are intentionally NOT
// unbound — worker owns them for cross-work look-back + node drill-down (see WorkerTools).
// They used to be in this list back when worker lacked them; seedWorkerReadbackRebind
// repairs DBs whose old run stripped them.
func (s *Server) seedWorkerReadToolsUnbind() {
	const flag = "worker_readtools_unbind_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	for _, k := range []string{
		"list_facts", "list_companies", "list_worker_traces",
	} {
		if err := s.m.pg.RemoveAgentFromTool("worker", k); err != nil {
			log.Printf("[worker] %s ~에서 worker 바인딩 해제 실패: %v", k, err)
			return // 에러가 발생하면 flag에 떨어지지 않고 다음에 다시 시도합니다.
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadbackRebind re-binds the cross-work look-back / drill-down tools onto
// worker ONCE (guarded by a settings flag): an earlier seedWorkerReadToolsUnbind wrongly
// stripped search_all_worker_traces / get_worker_trace / node_detail from worker after
// they had been added to WorkerTools(), so any DB that ran that migration lost them.
// Fresh DBs already have them via WorkerTools() and this is a harmless no-op there.
// One-shot + flag-guarded so a user who later deliberately unbinds them isn't overridden.
func (s *Server) seedWorkerReadbackRebind() {
	const flag = "worker_readback_rebind_v2" // v2: 추가 node_detail
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("worker", []string{
		"search_all_worker_traces", "get_worker_trace", "node_detail",
	}); err != nil {
		log.Printf("[worker] 뒤를 돌아봐/세부정보 도구 리바인딩 실패: %v", err)
		return // 에러가 발생하면 flag에 떨어지지 않고 다음에 다시 시도합니다.
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedAutoDefaultBindings adds "auto" to the task-op + platform tools' bindings
// ONCE (guarded by a settings flag), so existing DBs whose tool rows were seeded
// before Auto existed still give Auto its default toolset — without re-adding it
// after a user deliberately unbinds.
func (s *Server) seedAutoDefaultBindings() {
	const flag = "auto_default_bindings_v3" // v3: 이전 자산 도구 이름을 바꾸고 insert_assets/add_company_scope를 추가합니다.
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	keys := make([]string, 0, len(platformToolKeys)+12)
	for _, t := range s.orchestrationTools() {
		keys = append(keys, t.Name())
	}
	keys = append(keys, platformToolKeys...)
	// 자산 도구: Auto 운영 플랫폼은 자산을 확인/등록하고 회사 범위를 관리해야 하는 경우가 많습니다.
	keys = append(keys, "insert_assets", "add_company_scope", "list_assets")
	if err := s.m.pg.AddAgentToToolBinding("auto", keys); err != nil {
		log.Printf("[auto] 기본 바인딩 실패: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}
