package agent

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

// 이 파일은 "내장 도구"를 순수 코드에서 DB로 덮어쓸 수 있는 열거 가능한 디렉터리로 변경합니다.
//   - BuiltinToolSeeds(): agent를 실행하는 세 가지 기본 제공 도구 세트를 seed 레코드(key +
//     설명 + 매개변수 schema + 기본 바인딩 agent), 서버 측에서 tools 테이블에 대한 멱등성 시딩을 시작합니다.
//   - ToolResolve 후크: 실행 시 DB에서 tools 라인을 눌러 설치된 도구를 agent +로 필터링합니다.
//     설명/schema + 주입 매개변수 기본값"을 재정의합니다. key/handler는 여전히 코드 수준에 있고 DB는 "산문 및 기본값"만 변경합니다.
// handler(Call 동작)은 항상 코드에서 발생합니다. DB는 변경할 수 없으며 모델에 표시되는 설명 및 기본 입력 매개변수만 변경할 수 있습니다.

// ToolSeed는 내장 도구의 시드 가능한 스냅샷입니다. key는 CoreTool.Name()입니다(handler에 연결됨,
// UI는 읽기 전용), Desc/Schema는 코드의 도구 정의에서 가져오고, Agents는 기본적으로 agent에 제공하는 코드입니다.
type ToolSeed struct {
	Key    string         // = CoreTool.Name(), 기본 키는 변경할 수 없습니다.
	Desc   string         // 최상위 설명(UI에서 재정의 가능)
	Schema map[string]any // 매개변수 JSON-Schema(구조 읽기 전용, description/default는 UI에서 변경 가능)
	Agents []string       // 기본 바운드 agent key(worker/planner/mainagent)
}

// builtinToolsByAgent는 "읽기 전용 빈 셸" ToolSet(nil stores)를 사용하여 각 실행 agent를 구성합니다.
// 도메인 도구 세트. 도구 생성자는 Spec에만 클로저를 삽입하고 생성 중에 store를 역참조하지 않으므로 nil는 안전합니다.
// 이러한 도구는 여기서 Name()/Description()/InputSchema()를 읽는 데만 사용되며 Call는 읽지 않습니다.
//
// 의도적으로 [포함되지 않음] SDK 일반 도구 actool.DefaultTools() (Read/Write/Edit/MultiEdit/LS/Glob/
// Grep/Bash): 각각의 agent는 고정적으로 소유되어 있으며 "누구에게 바인딩되어 있는지"를 선택할 수 없으며 지침은 대부분 Prompt()에 있습니다.
// (이 표에는 Description()만 포함되어 있어 절반만 적용하면 오해의 소지가 있습니다.) 아니요 seed → 아니요 DB 확인 → ToolResolve
// 덮어쓰지 않고 있는 그대로 해제하고 이전처럼 작동합니다. artex의 자체 도메인 도구만 테이블에서 관리할 수 있습니다.
func builtinToolsByAgent() map[string][]actool.CoreTool {
	ts := NewToolSet(nil, "")
	return map[string][]actool.CoreTool{
		"mainagent": ts.MainAgentTools(),
		"planner":   ts.PlannerTools(),
		"worker":    ts.WorkerTools(),
		// goals(대상 디스어셈블러)는 기본적으로 set_goals + set_constraints에 바인딩됩니다. 대상을 분해하려면 이를 사용합니다.
		// 추출된 작업 제약 조건이 라이브러리에 기록됩니다. mainagent와 동일한 관리 도구를 공유하면 web 측은 설명/schema를 변경하고 agent를 선택할 수 있습니다.
		"goals": {ts.setGoals(), ts.setConstraints()},
		// auto는 기본적으로 취약점 보고 + 자산 관리 도구와 함께 번들로 제공됩니다. UI에서 필요에 따라 다른 도메인 도구를 확인할 수 있습니다.
		// 새 라이브러리는 seed에 의해 작성되었습니다. 이전 라이브러리는 seedAutoDefaultBindings에 의해 마이그레이션되었습니다.
		"auto": {ts.addFinding(), ts.insertAssets(), ts.addCompanyScope(), ts.listAssets(), ts.listCompanies()},
		// pentest(독립 침투 agent)는 기본적으로 자산 확인/자산 삽입/취약점 보고/취약점 확인/기업 확인에 바인딩됩니다.
		// 새 라이브러리는 seed에 의해 작성되었습니다. 이전 라이브러리는 seedPentestDefaultBindings에 의해 마이그레이션되었습니다.
		"pentest": {ts.listAssets(), ts.insertAssets(), ts.addFinding(), ts.listFindings(), ts.listCompanies()},
	}
}

// defaultUnbound: 이러한 system 도구는 평소와 같이 디렉토리에 입력되지만(web 측에 표시되며 agent를 눌러 수동으로 확인할 수 있음)
// 기본값은 [agent를 바인딩하지 않음] - ToolResolve입니다. 아무 것도 바인딩하지 않는 도구는 모든 agent를 삭제하고 opt-in를 명시적으로 지정해야 합니다.
// 특정 agent(예: PlannerTools의 goal_met)의 base 도구 세트에 남아 있는 이유: 먼저 seed를
// desc/schema를 얻으려면 구성하십시오. 둘째, 사용자가 수동으로 다시 바인딩하고 실행하면 base 및 ToolResolve가 유지될 수 있습니다.
//
// goal_met: prove_goal를 하나씩 우회하여 전체적인 상황에서 [전체 작업이 완료되었습니다]를 직접적으로 알리는 것은 무게가 무겁고 오판의 위험성이 있으며, 이와도 관련이 있습니다.
// "prove_goal는 마지막 대상을 표시 → 자동 종료"가 반복되므로 기본적으로 agent가 주어지지 않으며, 필요한 경우 수동으로 바인딩할 수 있습니다.
var defaultUnbound = map[string]bool{"goal_met": true}

// BuiltinToolSeeds는 각 agent의 내장 도구 세트를 중복 제거하고 seed 목록에 병합합니다. 동일한 이름을 가진 도구(예: list_assets)
// 여러 개의 agent가 하나로 결합되고 Agents가 결합을 취합니다. defaultUnbound의 도구는 바인딩을 비워 둡니다.
func BuiltinToolSeeds() []ToolSeed {
	byAgent := builtinToolsByAgent()
	order := []string{"mainagent", "goals", "planner", "worker", "auto", "pentest"}

	type acc struct {
		tool   actool.CoreTool
		agents []string
	}
	m := map[string]*acc{}
	var keys []string
	for _, ak := range order {
		for _, t := range byAgent[ak] {
			a, ok := m[t.Name()]
			if !ok {
				a = &acc{tool: t}
				m[t.Name()] = a
				keys = append(keys, t.Name())
			}
			a.agents = append(a.agents, ak)
		}
	}

	out := make([]ToolSeed, 0, len(keys))
	for _, k := range keys {
		a := m[k]
		agents := a.agents
		if defaultUnbound[k] {
			agents = []string{} // 디렉토리를 입력하고 수동으로 바인딩할 수 있지만 기본적으로 agent는 제공되지 않습니다(다른 도구와 일치하도록 null 대신 [] 저장).
		}
		out = append(out, ToolSeed{
			Key:    k,
			Desc:   a.tool.Description(),
			Schema: a.tool.InputSchema(),
			Agents: agents,
		})
	}
	return out
}

// ToolResolve, if set, post-processes an agent's fully-assembled tool list against
// the DB tools table: it drops tools not bound to this agent (or globally disabled)
// and wraps the rest so the model sees the DB-overridden description/schema and
// 기본 입력 매개변수 get injected. Tools with no matching DB row (MCP/skill/host tools like
// traffic) pass through untouched. nil = tools unchanged. Wired in server/assembly.go.
var ToolResolve func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool

// DecorateTool wraps t so Description()/InputSchema() report the DB overrides and
// Call() injects scalar parameter defaults (from schema's "default" props) whenever
// the model omitted them. Name/Prompt/permission/scheduler flags delegate to t, so
// the tool's identity and handler are unchanged. Empty desc/schema fall back to t's.
func DecorateTool(t actool.CoreTool, desc string, schema map[string]any) actool.CoreTool {
	if desc == "" {
		desc = t.Description()
	}
	if len(schema) == 0 {
		schema = t.InputSchema()
	}
	return &overriddenTool{CoreTool: t, desc: desc, schema: schema}
}

// overriddenTool is a CoreTool decorator: it embeds the original (so all behavioral
// methods — Prompt/IsReadOnly/IsConcurrencySafe/CheckPermissions/Name — delegate)
// and overrides only the model-facing description/schema plus default injection.
type overriddenTool struct {
	actool.CoreTool
	desc   string
	schema map[string]any
}

func (o *overriddenTool) Description() string         { return o.desc }
func (o *overriddenTool) InputSchema() map[string]any { return o.schema }

func (o *overriddenTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	return o.CoreTool.Call(ctx, injectDefaults(in, o.schema), tc)
}

// injectDefaults fills scalar parameter defaults declared in the (possibly edited)
// schema into the input JSON whenever the model omitted the field or left it empty/
// null. Structure (names/types/required) is untouched — 전용기본값 are merged in.
func injectDefaults(in json.RawMessage, schema map[string]any) json.RawMessage {
	defs := scalarDefaults(schema)
	if len(defs) == 0 {
		return in
	}
	m := map[string]json.RawMessage{}
	if len(in) > 0 {
		if err := json.Unmarshal(in, &m); err != nil {
			return in // non-object input: don't touch it
		}
	}
	changed := false
	for k, dv := range defs {
		if cur, ok := m[k]; !ok || isEmptyJSON(cur) {
			m[k] = dv
			changed = true
		}
	}
	if !changed {
		return in
	}
	b, err := json.Marshal(m)
	if err != nil {
		return in
	}
	return b
}

// scalarDefaults extracts properties[k]["default"] for scalar params (string/
// integer/number/boolean). Array/object defaults are skipped: merging them is
// ambiguous and not worth the surprise.
func scalarDefaults(schema map[string]any) map[string]json.RawMessage {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for name, raw := range props {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		dv, ok := p["default"]
		if !ok || dv == nil {
			continue
		}
		switch p["type"] {
		case "string", "integer", "number", "boolean":
			if b, err := json.Marshal(dv); err == nil {
				out[name] = b
			}
		}
	}
	return out
}

func isEmptyJSON(raw json.RawMessage) bool {
	s := string(raw)
	return s == "null" || s == `""`
}
