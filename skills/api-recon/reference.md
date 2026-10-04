# api-recon — 참조 매뉴얼

Grep 레시피, `config.json` 템플릿 및 문제 해결. 모든 grep는 `js/` 디렉터리에 대해 실행됩니다. bundle의 단일 라인의 경우 `js-beautify` 또는 `sed 's/}/}\n/g'`를 먼저 사용할 수 있으며 일반적으로 컨텍스트 창이 있는 raw grep이면 충분합니다.

## 스크립트 설명

`scripts/`의 모든 파일은 **참조 템플릿**이므로 실행하기 전에 대상 사이트에 따라 조정되어야 합니다. 일반적인 변경사항:

| 스크립트 | 일반적으로 필요한 조정 |
|---|---|
| `harvest_static.py` | endpoint 일반, webpack/Vite manifest 구문 분석, 마이크로 프런트 엔드 publicPath, 재시도/동시성 |
| `runtime_harvest.js` | neutralize 필드 이름 및 성공 값, stub 일치 규칙 및 body 구조, routes 소스, WS 녹음, `waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, L3, `recordDetail`, `observe.*`, `neutralizeVueRouter` 활성화 여부 |
| `spider_mpa.py` | `--exclude` 파괴 링크, cookie, depth/max, 동일 도메인 필터링 |
| `extract_route_map.py` | `routeMap` / `routeLink` 일반, KEY 명명 패턴 |
| `build_perm_tree.py` | `userRouteAuth` 구문 분석, `ROOTS`/`PREFIX_PARENT` 계층적 휴리스틱, stub 외부 필드 이름 |
| `config.json` | 위의 모든 사이트의 독점 매개변수에 대한 통합 입구 |

조정된 파일을 작업 작업 디렉터리(예: `recon/`)에 배치하는 것이 좋으며 참조 스크립트와 관련된 특정 변경 사항이 보고서에 기록됩니다.

---

## A. 3개의 역방향 문

### A1. 렌더링 게이트 — "로그인되었는지 어떻게 확인하나요?"

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

`isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))` 링크를 찾아 **저장 키**, **컨테이너**(Cookie vs localStorage), **인코딩**을 확인합니다.

| 코딩 | config 위조방법 |
|---|---|
| 일반 텍스트 문자열 / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | 서명 없음/`alg:none` JWT 또는 bundle 내부 키 서명 |
| 암호화(SM2/AES/RSA) | 하드코드된 키를 찾으세요. 렌더 게이트는 디코딩 가능해야 합니다. blob 언제 할 수 있나요? forge；그렇지 않으면 정적 커버 |

→ `cookies` / `localStorage`를 씁니다.

### A2. 인터셉터 게이트 — "/login 점프를 유발하는 것은 무엇입니까?"

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(로그인되지 않음|다시 로그인해주세요|로그인이 만료되었습니다|unauthorized|로그인이 유효하지 않습니다|승인하다|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

결정: **필드 이름**, **성공 값**(일반적으로 `0` 또는 `200`), **점프를 트리거하는 실패 값**. junk session로 확인:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ `neutralize.fields` + `neutralize.success`를 씁니다.

### A3. Content Gate — "메뉴/권한은 어디에서 오는가?"

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**두 개의 데이터 계층**(공통 엔터프라이즈 백엔드):

| API | 일반적인 payload | 소비자 측 |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | 경로 가드, 버튼 레벨 ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | 사이드바 메뉴 렌더링 |
| `userRouteAuth` 내의 bundle | `{ CODE: { url, name? } }` | code → 프런트 엔드 path |
| `routeMap` 내의 bundle | `{ KEY: { name, link } }` | 별칭 분해능(webpack `o.DASHBOARD`) |

소비자 코드 확인 읽기: `getResultTree(tree, permissions)` 필터링 방법, `v-if` / `hasAuth(code)` 확인할 필드.

**수동 forge**(소규모 사이트): permissive payload → `stubs`를 빌드합니다.

**전체 권한 트리 복원**(대형 사이트, 사이드바/하위 모듈이 여전히 비어 있음): **I**를 참조하세요.

---

## B. config.json 템플릿

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

필드 설명:
- `runtimeMode`：`depth`(인형사)、`coverage`(브라우저 MCP)、`both`
- `cookies[].value` 접두사: `b64json:` → base64(JSON); `json:` → 원본 JSON; 접두사 없음 → 리터럴
- `forward: true` 실제 요청을 전달하고 코드 필드를 다시 작성합니다.；`false` 완전히 오프라인 stub
- `mockTier`: coverage 모드 preload 활성화 수준(예: `L1+L2`, `L1+L2+L3`)
- `routes`는 `routes.txt`에서 유래되었습니다. harness는 forge 메뉴 뒤에 `<a href>`를 자동으로 추가합니다.
- `captureResponses` / `recordWs` depth 모드만 유효합니다.
- `waitUntil`: `networkidle2` 행잉을 방지하려면 대형 SPA의 경우 `domcontentloaded`를 사용하세요.
- `routeTimeout`: 단일 경로 `page.goto` 시간 초과(밀리초)
- `proxy`: Puppeteer `--proxy-server`; `HTTP_PROXY` / `HTTPS_PROXY`도 설정 가능

### B1. 듀얼 stub 템플릿(role_permissions + permissions/all)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

외부 필드 이름(`response_code` / `code` / `data`)은 A2 인터셉터 게이트와 일치해야 합니다. `permissions`는 tree의 모든 leaf code를 포함해야 합니다.

---

## C. coverage 모드: preload 구성

`scripts/preload.js` 상위 `CONFIG` 객체를 편집하거나 주입 전에 CDP로 교체합니다.

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* 같은 config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

확인: `window.__API_RECON_PRELOAD__ === true` 및 pathname는 안정적입니다.

녹음 결과 내보내기:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime Hook 기능

preload(coverage) 및 runtime_harvest(depth) 내장 브라우저 Hook 기능 및 적용 범위:

| Hook 기능 | API에 대해 찾은 값 | 씌우다 |
|---|---|---|
| 후크 페치 / XHR.open | 녹음 요청 URL/ 방법 | ✅ `recordDetail` + `__API_RECON_LOG__` |
| 후크 XHR.setRequestHeader | Authorization 및 기타 헤더를 ​​찾았습니다. | ✅ `observe.xhrHeaders` |
| Hook localStorage/cookie 읽기 | 세션 키 이름 확인 | ⚠️ 옵션 `observe.storageReads/cookieReads` |
| Vue 경로 가져오기 | frontendRoutes를 완료하세요 | ✅ `__API_RECON_ROUTES__` (경로 로드됨) |
| Vue 라우팅 가드 무력화/로그인 점프 차단 | 개방형 모듈 트리거 API | ✅ `neutralizeVueRouter` + 기본 점프 무력화 |
| React 경로 가져오기 | 보충 노선 | ⚠️ 정적 + 클릭; 전용 Hook 없음 |
| 페이지 점프 차단(로그인 path) | 탈퇴 페이지 분석 | ⚠️ 비즈니스 탐색 차단을 방지하려면 로그인 path만 차단하세요. |
| Hook 암호화 라이브러리(CryptoJS/SM 등) | 암호화 매개변수 → 일반 텍스트 API body | ❌ 암호화 기능의 매개변수는 수동으로 입력해야 합니다. Hook; 결론은 config로 작성되었습니다. |
| 디버깅 방지 bypass | 그렇지 않으면 runtime는 API를 기록할 수 없습니다. | ❌ 수동으로 처리해야 합니다. 아직 정적 사용 가능 |

---

## E. Endpoint 일반 규칙 추출(정적이 너무 적은 경우)

`harvest_static.py`의 `extract_endpoints`에서 휴식을 취하거나 수동으로 휴식을 취하세요.

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. 문제 해결

| 현상 | 원인 → 해결방법 |
|---|---|
| 정적 API 드물게 | endpoint 방언 불일치 → 규칙성 완화(D 섹션) |
| chunk 수 ≪ manifest | CSS-only 또는 chunk가 배포되지 않았습니다. 404 재시도됨 |
| runtime에 여전히 로그인 페이지가 표시됩니다. | 렌더 게이트 오류 → A1 검토: 키 이름, 컨테이너, 인코딩, domain |
| 쉘에 들어가지만 모듈이 비어 있습니다. | 콘텐츠 도어 → forge 메뉴(A3); `routes` path가 틀렸을 수도 있습니다 |
| 각 경로에는 bootstrap/locale만 있습니다. | 불완전한 권한 코드 → I 섹션 권한 트리 복원; `role_permissions` + `permissions/all` 더블 stub 확인 |
| 사이드바에 항목이 있지만 하위 페이지가 비어 있습니다. | tree에 intermediate 노드가 없거나 code가 `userRouteAuth`와 일치하지 않습니다. |
| 각 API는 로그인으로 이동합니다. | 인터셉터 게이트 → `neutralize` 확인; 중첩된 필드는 walk 로직으로 확장되어야 합니다. |
| WS 프레임은 0입니다. | 사용자 상호 작용 subscribe가 필요합니다. 길어진 `perRouteMs` |
| 응답 본문이 비어 있습니다. | `forward: true`일 때만 실제 응답이 있습니다. |
| Chromium 누락 | chromium 설치 또는 `config.chromium` / `CHROMIUM` 설정 |
| Mock 여전히 많은 사람들이 로그인하고 있습니다 | Hook가 너무 늦거나 누락됨 `location.href` setter → document-start + preload |
| 목록이 완전히 비어 있습니다. | L3 빈 배열은 정상입니다. 계속해서 Tab/ 설정/세부정보를 클릭하세요. |
| Redux action를 경로로 착각함 | get/set/change/clear/toggle/upload를 포함하는 필터 내부 path |
| Vue가 여전히 로그인을 건너뜁니다. | preload는 document-start가 아닙니다 → 분사 타이밍을 변경하십시오. 또는 `neutralizeVueRouter: false`일 때 수동으로 가드를 제거하십시오. |
| 응답에 URL가 있는데 log가 입력되지 않았습니다. | `extractUrlsFromResponse`를 엽니다. 또는 `__API_RECON_DETAIL__`에서 수동으로 추출 |
| Authorization 이름을 모르겠어요 | 요청 헤더를 보려면 `observe.xhrHeaders` 또는 DevTools를 엽니다. |
| runtime 매우 느림/시간 초과 | `waitUntil: domcontentloaded`로 변경합니다. `routeTimeout`로 줄입니다. `networkidle2`를 사용하지 마십시오 |
| 에이전트 연결 실패 | `proxy` / 환경 변수를 확인하세요. Puppeteer 및 curl 프록시 포트는 일관성이 있습니다. |

---

## G. hardened 타겟

서버가 점차적으로 세션을 확인하면(서명 cookie는 forge가 될 수 없고 서버는 메뉴를 렌더링하며 메뉴는 stub가 될 수 없음) runtime는 shell에서 멈추게 됩니다. 예상되는 동작:

- **endpoint 열거를 수행하기에 충분한 정적** — 코드의 모듈 path
- 승인이 허용되면 **실제 세션**을 사용하여 동일한 harness: `forward: true`를 실행하고 neutralize 없이 실제 methods/params/responses를 캡처합니다.

---

## H. 단일 작업 목록

1. 승인 범위 확인
2. **읽기** `scripts/harvest_static.py` → 목표에 맞게 조정 → 실행 → `api_static.txt`, `routes.txt` 검토
3. **Phase 1b**: path 앵커 창 확장 + 바인딩 레이어 → `param_candidates.json`(J 섹션)
4. 역 A1/A2/A3 → 쓰기 사이트 전용 `config.json`
5. **읽고 조정** `runtime_harvest.js` / `preload.js` 실행 전
6. `runtimeMode=depth`:`npm install` → 조정된 harvest 스크립트 실행
7. `runtimeMode=coverage/both`: document-start는 조정된 preload → browser MCP 동적 열거 + **매개변수 트리거 매트릭스**를 삽입합니다.
8. 모듈이 렌더링되지 않음 → **I 섹션 권한 트리 복원** → patch stubs → 재실행
9. 파라미터 다중 샘플 diff + 오류 반전 → `params_merged.json`
10. 병합 → `site_map.json` + `api_merged.txt`, 적용 범위, 공백 및 스크립트 변경 사항을 솔직하게 표시합니다.

---

## I. 권한 트리 복원(Phase 4 심화)

forge 단순 `menus: [{ path, show: true }]`가 유효하지 않고 하위 모듈이 여전히 mount가 아닐 때 사용됩니다.

### I1. auth 모듈 포지셔닝

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

레코드: **권한 API path**, **응답 필드 이름**, **소비 chunk 파일 이름**.

### I2. routeMap 추출

```bash
python3 scripts/extract_route_map.py recon/js recon/
# 산출 recon/route_map.json
```

`[!] no routeMap pattern found`인 경우: `extract_route_map.py`의 일반 규칙을 완화하거나 수동으로 grep를 수행합니다.

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. 빌드 권한 트리 + stub

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

스크립트 논리:
1. 분석 `userRouteAuth={MONITOR:{url:...},...}`(webpack 별칭 `He=o.DASHBOARD` 포함)
2. `route_map.json`를 사용하여 alias → 실제 path를 구문 분석합니다.
3. 접두사 code에 따라 parent를 추론합니다(`MONITOR_ALERT` → `MONITOR`).
4. 출력 `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json`
5. `--config`는 자동으로 `stubs` 및 `config.json`의 확장자 `routes`를 작성합니다.

**대상별 조정**(스크립트 상단):
- `DEFAULT_ROOTS`: 상위 모듈 code 목록
- `DEFAULT_PREFIX_PARENT`: `PREFIX_` → parent 매핑
- `DEFAULT_EXTRA_PARENT`: 접두사가 아닌 관계가 있는 orphan 노드

### I4. stub의 일관성 확인

```bash
# permissions 수량은 ≒ userRouteAuth 항목 수와 같아야 합니다.
wc -l recon/perm_codes_all.txt
# routes는 route_map 전체 link를 커버해야 합니다.
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. runtime를 다시 실행하고 비교하십시오.

```bash
node recon/runtime_harvest.js recon/config.json
# forge 전후의 runtime_api.json 수를 비교하십시오. API 모듈이 /attack, /asset 등에 나타나는지 확인하십시오.
```

| forge 전 | forge 이후(성공) |
|---|---|
| 각 경로에는 동일한 3–5 bootstrap가 있습니다. | 다른 경로는 다른 module API를 트리거합니다. |
| `/api/locale/language` 전용 | 나타나다 `/api/web/...` 기준 치수 endpoint |
| `routes.txt` 1자리 라우팅 | route_map의 `routes` 80–110+ |

### I6. 여전히 실패

- **coverage 모드**: 사이드바 + Tab를 클릭하면 상호 작용 후 gating 권한이 요청될 수 있습니다.
- **stub 필드**: 실제 API(curl + 실제 session)를 stub의 nesting와 비교합니다.
- **추가 가드**: grep `hasPermission|checkRole|func.` 버튼 수준 검사를 기다리고 확장합니다. `role_permissions.permissions`
- **정적 결론**: 모듈 API path는 여전히 `api_static.txt`에 있으며, runtime는 METHOD/body만 보완합니다. 매개변수는 유지됩니다. `param_candidates.json` + 기록된 샘플

---

## J. 매개변수 반전(Phase 1b/5b/5c)

**방법론, 비범용 스크립트. ** path를 찾으려면 일반 규칙을 사용하십시오. 앵커 창 확장을 사용하여 매개변수 + UI 바인딩 체인 + 다중 샘플 diff + 오류 반전을 찾습니다.

### J1. 앵커 포인트 확장 창 - path에서 그룹 개체를 찾습니다.

```bash
# Phase 1 알려진 path를 앵커로 사용
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. 포장층 및 투과 형태

```bash
# axios / 통합 request
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# 경로 매개변수
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. 게이트 확인 — 필수 / 형식 / 열거

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. 바인딩 레이어 - 양식 → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

runtime 보완: DevTools → Network → 요청 → **초기자**(call stack)는 `fetch`/`send`에서 패키지 기능을 추적합니다.

### J5. 암호화 매개변수

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**암호문의 필드를 추측하지 마십시오** — Hook 암호화 기능 **입력 매개변수**, 암호화 전에 plaintext payload를 기록합니다. 결론 `config.json` / `param_candidates.json`를 작성합니다.

### J6. 매개변수 트리거 매트릭스(Phase 3이 해야 함)

각 모듈은 작업당 한 번씩 기록되며, diff는 body/query를 요청합니다.

| 작동하다 | 집중하다 |
|---|---|
| 목록 첫 화면 | 페이지 매김 기본값 |
| 검색 | 키워드、필터 |
| 고급 필터링 | optional 필드 |
| 신규/수정 | entity를 완료하세요 |
| 배치/내보내기 | `ids[]`、`exportType` |
| 정렬/페이지 | `sortField`、`order` |

출력 `param_samples.json`: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. 신뢰 규칙

| 신뢰 | 상태 |
|---|---|
| **높은** | 정적 callsite + runtime ≥2 샘플 일관성 |
| **가운데** | 정적만 또는 1회만 runtime |
| **낮은** | 응답/오류 반전, 2차 검증 없음 |
| **발동 예정** | 알려진 정적 필드, UI/ 권한에 도달하지 않았습니다. |

### J8. 장면 빠른 매칭

| 장면 | 주문하다 |
|---|---|
| REST 목록 페이지 | J1 그룹 패키지 객체 → J6 4배 diff → J3 rules |
| 양식 생성/수정 | J3 Form name → J4 submit 체인 → runtime 제출 + 의도적으로 공백으로 남겨두기 400 |
| GraphQL | J2 variables 문 → runtime 각 operation 레코드 variables |
| 암호화 body | J5 Hook 입력 매개변수 → 암호화 전 필드가 실제입니다 params |

### J9. 및 api-recon 스테이지 매핑

| API 정찰 | 매개변수 recon |
|---|---|
| Phase 1 정적 | J1 앵커 포인트 확장 창 |
| Phase 2 A2 인터셉터 | 글로벌 주입 필드(tenantId, sign) |
| 3단계 런타임 | J6 트리거 매트릭스 + `param_samples.json` |
| Phase 4 권한 트리 | 모듈마다 양식이 다르므로 권한이 충분한 경우에만 모든 필드를 트리거합니다. |
| Phase 5 병합 | `params_merged.json` + 신뢰 수준; 단일 샘플을 지정하지 마십시오. 필수의 |

### J10. 문제 해결

| 현상 | 다루다 |
|---|---|
| 정적 필드 이름 runtime가 나타나지 않습니다. | "트리거됨"을 표시하십시오. 권한 트리 추가 / 고급 필터 클릭 / select 각 option 연결 |
| path와 동일 body 모양이 다름 | 일반 - `action`에 따라 레코드를 분리하고 schema를 강제로 병합하지 않습니다. |
| stub가 거짓으로 응답했지만 params를 보고 싶었습니다. | **outbound 요청 참조** body/headers, stub 응답에서 푸시백하지 않음 |
| 400 신문 nested field | 외부 포장 `data`/`bizData`/`variables`에 주의하세요. |
| GraphQL operation라는 이름만 보입니다. | `variables` JSON를 확장합니다. 정적으로 `$var: Type` 찾기 |

---
