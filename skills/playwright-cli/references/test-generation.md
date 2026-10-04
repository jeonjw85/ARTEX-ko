# 테스트 생성(계획 → 생성 → 치유)

`playwright-cli`를 사용하여 Playwright 테스트를 작성하고 유지 관리하기 위한 엔드투엔드 워크플로입니다. 모든 `playwright-cli` 작업은 동등한 Playwright TypeScript를 내보내며 생성된 코드는 모든 테스트의 원시 자료입니다. 아래 섹션은 독립적으로 사용할 수 있습니다.

- **생성 작동 방식** — 다른 모든 것이 의존하는 핵심 메커니즘: 액션이 TypeScript가 되고 어설션을 추가하는 방법도 있습니다.
- **계획** — 앱을 탐색하고 테스트할 내용을 설명하는 사양 파일을 생성합니다.
- **생성** — 사양을 Playwright 테스트 파일로 변환합니다. 사양이 모호하거나 오래된 경우 사양을 업데이트하세요.
- **치유** — 실패한 테스트를 진단하고, 코드를 수정하고, 사양을 현실에 맞게 조정합니다.

동일한 메커니즘을 사용하여 계획/생성/치료를 수행합니다. 백그라운드에서 `npx playwright test --debug=cli`를 실행한 다음 `playwright-cli attach tw-XXXX`를 실행하여 일시 중지된 페이지를 대화식으로 구동합니다. 디버그/연결 메커니즘은 [playwright-tests.md](playwright-tests.md)를 참조하세요.

---

## 0. 생성이 이루어지는 방식

`playwright-cli`로 수행하는 모든 작업은 해당 Playwright TypeScript 코드를 생성합니다. 이 코드는 출력에 나타나며 테스트 파일에 직접 복사할 수 있습니다.

```bash
# Start a session
playwright-cli open https://example.com/login

# Take a snapshot to see elements
playwright-cli snapshot
# Output shows: e1 [textbox "Email"], e2 [textbox "Password"], e3 [button "Sign In"]

# Fill form fields - generates code automatically
playwright-cli fill e1 "user@example.com"
# Ran Playwright code:
# await page.getByRole('textbox', { name: 'Email' }).fill('user@example.com');

playwright-cli fill e2 "password123"
# Ran Playwright code:
# await page.getByRole('textbox', { name: 'Password' }).fill('password123');

playwright-cli click e3
# Ran Playwright code:
# await page.getByRole('button', { name: 'Sign In' }).click();
```

### 테스트 파일 빌드

생성된 코드를 Playwright 테스트로 수집합니다.

```typescript
import { test, expect } from '@playwright/test';

test('login flow', async ({ page }) => {
  // Generated code from playwright-cli session:
  await page.goto('https://example.com/login');
  await page.getByRole('textbox', { name: 'Email' }).fill('user@example.com');
  await page.getByRole('textbox', { name: 'Password' }).fill('password123');
  await page.getByRole('button', { name: 'Sign In' }).click();

  // Add assertions
  await expect(page).toHaveURL(/.*dashboard/);
});
```

### 의미 로케이터 사용

생성된 코드는 가능한 경우 복원력이 더 뛰어난 역할 기반 로케이터를 사용합니다.

```typescript
// Generated (good - semantic)
await page.getByRole('button', { name: 'Submit' }).click();

// Avoid (fragile - CSS selectors)
await page.locator('#submit-btn').click();
```

### 녹음하기 전에 살펴보세요

작업을 기록하기 전에 페이지 구조를 이해하기 위해 스냅샷을 찍습니다.

```bash
playwright-cli open https://example.com
playwright-cli snapshot
# Review the element structure
playwright-cli click e5
```

### 수동으로 어설션 추가

생성된 코드는 작업을 캡처하지만 어설션은 캡처하지 않습니다. 권장 일치자 중 하나를 사용하여 테스트에 기대치를 추가합니다.

- `toBeVisible()` — 요소가 렌더링되어 표시됩니다.
- `toHaveText(text)` — 요소 텍스트 내용이 일치합니다.
- `toHaveValue(value) / toBeEmpty()` — 입력/선택 값이 일치합니다.
- `toBeChecked() / toBeUnchecked()` — 체크박스 상태가 일치합니다.
- `toMatchAriaSnapshot(snapshot)` — 페이지(또는 로케이터)가 부분 접근성 스냅샷과 일치합니다.

`playwright-cli generate-locator <target>`를 사용하여 어설션에 대한 로케이터 표현식을 생성하고 snapshot/eval 명령을 사용하여 예상 값을 캡처합니다.

텍스트 콘텐츠를 주장할 때 생성된 로케이터에 요소 자체의 텍스트가 포함되어 있지 않은지 확인하세요. `getByTestId()` 또는 `getByLabel()`는 일반적으로 텍스트를 주장하는 데 적합합니다. 로케이터가 텍스트 기반인 경우 대신 `toBeVisible()`를 선호합니다.

일치시킬 스냅샷에 모든 정보가 포함될 필요는 없습니다. 어설션에 필요한 정보만 캡처하세요. 불안정한 값에는 정규식을 사용할 수 있습니다.

```bash
# Get a stable locator for an element ref to use in the assertion
playwright-cli --raw generate-locator e5
# getByRole('button', { name: 'Submit' })

# Capture expected text content for toHaveText
playwright-cli --raw eval "el => el.textContent" e5

# Capture expected input value for toHaveValue/toBeEmpty
playwright-cli --raw eval "el => el.value" e5

# Capture expected aria snapshot for toMatchAriaSnapshot/toBeChecked
# (whole page, or use a ref to scope to a region)
playwright-cli --raw snapshot
playwright-cli --raw snapshot e5
```

```typescript
// Generated action
await page.getByRole('button', { name: 'Submit' }).click();

// Manual assertions using the outputs above:
await expect(page.getByRole('alert', { name: 'Success' })).toBeVisible();
await expect(page.getByTestId('main-header')).toHaveText('Welcome, user');
await expect(page.getByRole('textbox', { name: 'Email' })).toHaveValue('user@example.com');
await expect(page.getByRole('checkbox', { name: 'Enable notifications' })).toBeChecked();

// toMatchAriaSnapshot on the whole page, finds a matching region
await expect(page).toMatchAriaSnapshot(`
  - heading "Welcome, user"
  - link /\\d+ new messages?/
  - button "Sign out"
`);

// toMatchAriaSnapshot scoped to a region
await expect(page.getByRole('navigation')).toMatchAriaSnapshot(`
  - link "Home"
  - link /\\d+ new messages?/
  - link "Profile"
`);
```

---

## 1. 기획

목표: 테스트할 시나리오를 열거하는 사양 파일(예: `specs/<feature>.plan.md`)을 생성합니다. **항상** 사양을 파일에 씁니다.

### 1.1 전제조건: 작업공간

다른 것보다 먼저 작업 공간에 Playwright가 설치되어 있는지 확인하십시오.

```bash
# Either of these confirms a workspace:
test -f playwright.config.ts || test -f playwright.config.js
npx --no-install playwright --version
```

Playwright 설치가 없으면 하나를 부트스트랩하고 사용자가 기본값을 선택하도록 합니다.

```bash
npm init playwright@latest
```

### 1.2 전제 조건: 종자 테스트

**시드 테스트**는 모든 시나리오가 시작되는 상태(앱 탐색, 필수 로그인, 기능 플래그 등)에 페이지를 배치하는 최소 테스트입니다. 시나리오는 시드 *후* 새로운 시작을 가정합니다. `--debug=cli`는 이 테스트 *내부*에서 일시 중지되므로 시드는 모든 계획 및 생성 세션이 시작되는 곳입니다.

최소 실행 가능한 종자:

```ts
// tests/seed.spec.ts
import { test } from '@playwright/test';

test('seed', async ({ page }) => {
  await page.goto('https://example.com/');
});
```

선호 — 시나리오 테스트에서 재사용할 수 있도록 탐색 기능을 픽스처에 푸시합니다.

```ts
// tests/fixtures.ts
import { test as baseTest } from '@playwright/test';
export { expect } from '@playwright/test';

export const test = baseTest.extend({
  page: async ({ page }, use) => {
    await page.goto('https://example.com/');
    await use(page);
  },
});
```

```ts
// tests/seed.spec.ts
import { test } from './fixtures';

test('seed', async ({ page }) => {
  // Fixture already navigates. This empty body tells agents where to start.
});
```

시드가 없으면 최소한 앱으로 이동하는 시드를 만듭니다.

### 1.3 앱 탐색

백그라운드에서 시드를 통해 앱을 실행하고 다음을 연결합니다.

```bash
PLAYWRIGHT_HTML_OPEN=never npx playwright test tests/seed.spec.ts --debug=cli
# wait for "Debugging Instructions" and the session name tw-XXXX
playwright-cli attach tw-XXXX
```

시드가 실행되도록 재개한 후 앱을 조사합니다.

```bash
playwright-cli resume                   # resume so that seed test runs fully
playwright-cli snapshot                 # inventory of interactive elements
playwright-cli click e5                 # follow a flow
playwright-cli eval "location.href"     # read URL / state
playwright-cli show --annotate          # ask the user to point at something
```

지도 작성:

- 대화형 표면(양식, 버튼, 목록, 필터, 모달)
- 기본 사용자 여정은 처음부터 끝까지 진행됩니다.
- 엣지 케이스: 빈 상태, 유효성 검사 오류, 매우 긴 입력, 경계 값.
- 지속성: 다시 로드, 로컬/세션 저장소, URL 조각.
- 탐색: URL 변경, 뒤로/앞으로 동작을 제어합니다.

**중요**: playwright-cli를 사용하여 앱 URL을 열지 말고 항상 테스트를 진행하여 거기서 수행된 사용자 정의 설정을 캡처하세요.
**중요**: 탐색이 끝나면 백그라운드 테스트를 중지하세요.

### 1.4 스펙 파일 작성

`specs/<feature>.plan.md` 아래에 저장합니다. 다음 구조를 사용하십시오.

```markdown
# <Feature> Test Plan

## Application Overview

<One paragraph describing what the feature does and why it matters.>

## Test Scenarios

### 1. <Group Name>

**Seed:** `tests/seed.spec.ts`

#### 1.1. <kebab-case-scenario-name>

**File:** `tests/<group>/<kebab-case-scenario-name>.spec.ts`

**Steps:**
  1. <Concrete user step>
    - expect: <observable outcome>
    - expect: <another observable outcome>
  2. <Next step>
    - expect: <outcome>

#### 1.2. <next-scenario>
...

### 2. <Next Group>

**Seed:** `tests/seed.spec.ts`
...
```

지침:

- 각 시나리오는 독립적이며 시드의 새로운 상태에서 시작됩니다. 결코 연쇄 시나리오가 아닙니다.
- 시나리오 이름은 kebab-case이며 테스트 파일 이름과 일치합니다(`should-add-single-todo` → `should-add-single-todo.spec.ts`).
- 행복한 경로, 엣지 케이스, 검증, 부정적인 흐름, 지속성을 다룹니다.
- API 수준("`fill` 호출")이 아닌 사용자 수준("입력에 '우유 구매' 입력")에서 단계를 작성하세요.
- `- expect:` 글머리 기호에 관찰 가능한 결과를 넣으세요. 각각은 생성 중에 어설션이 됩니다.

---

## 2. 생성

목표: 사양 파일을 가져와 Playwright 테스트 파일을 생성합니다. 사양이 변동된 경우 선택적으로 사양을 업데이트합니다.

### 2.1 입력

- **사양 파일**(예: `specs/basic-operations.plan.md`.
- **대상**: 단일 시나리오(예: `1.2`), 전체 그룹(`1`) 또는 모두.
- **시드 파일**, 시나리오 그룹의 `**Seed:**` 라인에서 읽습니다.

### 2.2 하나의 시나리오 생성

각 대상 시나리오에 대해 순서대로(병렬이 아님 - 시나리오가 시드 세션을 공유함):

```bash
PLAYWRIGHT_HTML_OPEN=never npx playwright test <seed-file> --debug=cli   # background
playwright-cli attach tw-XXXX
# resume
```

**금지** 단지 playwright-cli를 사용하여 앱 URL을 열지 마십시오. 항상 테스트를 통해 거기에서 수행된 사용자 정의 설정을 캡처하십시오.

`playwright-cli`를 사용하여 시나리오의 `Steps:`를 하나씩 살펴보고 사양을 계획으로, 라이브 앱을 진실의 소스로 취급합니다. 단계가 모호하거나("버튼을 클릭하세요" - 어떤 버튼입니까?) 더 이상 존재하지 않는 요소를 참조하거나 앱의 실제 동작과 모순되는 경우 판단을 사용하세요. 앱이 실제로 수행하는 기능과 일치하도록 사양을 업데이트한 다음 계속 진행하세요. 사양 중간 세대 편집이 예상됩니다.

모든 작업은 동등한 Playwright TypeScript를 인쇄합니다([생성 작동 방식](#0-how-generation-works) 참조).

```bash
playwright-cli snapshot                         # find refs
playwright-cli fill e3 "John Doe"               # -> page.getByRole('textbox', {...}).fill(...)
playwright-cli press Enter
playwright-cli click e7
```

각 `- expect:` 글머리 기호에 대해 명시적인 어설션을 추가합니다. 자세한 내용은 [생성 작동 방식](#0-how-generation-works)을 참조하세요.

생성된 코드를 수집하고 사양에 지정된 경로에 테스트 파일을 작성합니다.

```ts
// spec: specs/basic-operations.plan.md
// seed: tests/seed.spec.ts
import { test, expect } from './fixtures';   // or '@playwright/test' if no fixtures file

test.describe('Signing in and out', () => {
  test('should sign in', async ({ page }) => {
    // 1. Navigate to the application
    // (handled by the seed fixture)

    // 2. Type 'John Doe' into the username field
    await page.getByRole('textbox', { name: 'username' }).fill('John Doe');

    // 3. Type password
    await page.getByRole('textbox', { name: 'password' }).fill('TestPassword');

    // 4. Press Enter to submit
    await page.getByRole('textbox', { name: 'password' }).press('Enter');

    await expect(page.getByRole('heading')).toContainText('Welcome, John Doe!');
  });
});
```

규칙:

- **파일당 하나의 테스트.** 파일 경로, 설명 이름 및 테스트 이름은 사양에서 그대로 가져옵니다(서수 제외).
- 작업 앞에 `// N. <step text>` 주석을 각 번호 단계 앞에 붙입니다.
- 사양에서 설명 그룹 이름을 그대로 사용합니다(`1.` 서수 없음).
- Import from `./fixtures` if the project has one; otherwise `@playwright/test`.
- **중요**: 다음 시나리오로 이동하기 전에 CLI 세션을 닫고 백그라운드 테스트를 중지하세요.

### 2.3 여러 시나리오 생성

한 번에 하나씩 대상 시나리오에 대해 2.2를 반복하고 각 시나리오 사이의 시드를 다시 시작하여 모든 테스트가 클린 페이지에서 시작되도록 합니다. 고유하게 생성된 세션 이름으로 인해 병렬화하는 것이 안전합니다. 각 테스트 실행이 중지되었는지 확인하세요.

### 2.4 생성된 테스트 실행

생성 후 새 테스트를 한 번 실행합니다.

```bash
PLAYWRIGHT_HTML_OPEN=never npx playwright test tests/<group>/<scenario>.spec.ts
```

실패하면 섹션 3으로 이동합니다.

---

## 3. 힐링

목표: 실패한 테스트를 수정하고 앱의 의도된 동작이 변경된 경우 사양을 업데이트합니다.

### 3.1 실패한 테스트 찾기

```bash
PLAYWRIGHT_HTML_OPEN=never npx playwright test
```

실패한 `<file>:<line>` 항목 목록을 기록하고 한 번에 하나씩 처리하십시오. 병렬 수정을 시도하지 마십시오. 공유 상태와 단일 CLI 세션으로 인해 문제가 발생하기 쉽습니다.

### 3.2 한 번의 실패 디버그

백그라운드에서 디버그 모드로 단일 실패 테스트를 실행한 후 다음을 연결합니다.

```bash
PLAYWRIGHT_HTML_OPEN=never npx playwright test tests/<group>/<scenario>.spec.ts:<line> --debug=cli
# wait for "Debugging Instructions" and the tw-XXXX session name
playwright-cli attach tw-XXXX
```

테스트가 시작될 때 일시 중지됩니다. 실패한 작업이나 주장 직전까지 앞으로 나아가거나 실행한 후 다음을 진단합니다.

```bash
playwright-cli snapshot                # did the element change / move / rename?
playwright-cli console                 # app-side errors?
playwright-cli requests                # failed request? wrong payload?
playwright-cli show --annotate         # ask the user to point somewhere
```

일반적인 원인: 선택기 드리프트, 새 래퍼 요소, 레이블/ARIA 이름 바꾸기, 타이밍(전환, 비동기 로드), 앱에서 업데이트된 어설션 텍스트, 실행 간에 테스트 데이터 누출.

`playwright-cli`와의 수정된 상호 작용을 연습합니다. 출력에 생성된 코드는 테스트에 다시 붙여넣는 코드입니다.

### 3.3 수정사항 적용

테스트 파일 편집: 수정된 동작과 일치하도록 로케이터, 어설션, 단계 순서 또는 입력을 업데이트합니다. 백그라운드 디버그 실행을 중지합니다. 단일 테스트를 다시 실행하여 녹색을 확인합니다.

절대로 후크를 건너뛰거나 수정 사항으로 수면을 추가하지 마세요. `networkidle`를 사용하지 마십시오.

### 3.4 사양과의 조화

테스트 파일의 `// spec:` 헤더에서 참조하는 사양을 열고 테스트와 일치하는 시나리오를 찾습니다.

- **수정은 순전히 기술적인 것**(로케이터 드리프트, 더 나은 어설션 형태)이었고 사양의 사용자 수준 동작은 여전히 ​​앱과 일치합니다. → 사양을 그대로 둡니다.
- **사양에서 설명하는 변경된 사용자 표시 단계, 입력, 순서 또는 예상 결과 수정** → 현실에 맞게 사양을 업데이트합니다. 시나리오 ID와 파일 경로를 안정적으로 유지하세요. 단계/예상 행만 변경됩니다.
- **앱 변경이 의도적인 것인지**(사양이 오래됨) **또는 회귀**(테스트가 옳았음, 앱이 틀림)인지 불분명 → **중단하고 사용자에게 물어보세요**. 제공하다:
  - 시나리오 ID(예: `2.3`),
  - 더 이상 일치하지 않는 사양 라인,
  - 관찰된 앱 동작(스냅샷 발췌 또는 구체적인 결과 인용)

사용자가 답변한 후에만 사양을 업데이트하거나(의도적인 변경) 버그를 다루는 테스트를 신고/플래그합니다(회귀).

### 3.5 반복과 포기

- 한 번에 하나씩 오류를 수정하세요. 각각 후에 다시 실행하세요.
- 철저하게 조사한 후 테스트가 정확하다고 확신하지만 앱이 잘못되었으며* 사용자가 버그라고 확인한 경우: 사용자의 결정 또는 문제 링크를 가리키는 주석으로 테스트 `test.fixme(...)`를 표시합니다. 절대로 자동으로 건너뛰지 마세요.

---

## 상호 참조

| 을 위한... | 보다 |
|---|---|
| `--debug=cli` / 부착 장치 | [극작가-tests.md](playwright-tests.md) |
| 탐색/생성 중 요청 모의 | [요청-mocking.md](request-mocking.md) |
| CLI 브라우저 세션 관리 | [세션 관리.md](session-management.md) |
