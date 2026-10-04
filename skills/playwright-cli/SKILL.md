---
name: playwright-cli
description: 브라우저 상호 작용을 자동화하고, 웹 페이지를 테스트하고, Playwright 테스트를 사용하세요.
allowed-tools: Bash(playwright-cli:*) Bash(npx:*) Bash(npm:*)
---

# playwright-cli를 사용한 브라우저 자동화

## 빠른 시작

```bash
# open new browser
playwright-cli open
# navigate to a page
playwright-cli goto https://playwright.dev
# interact with the page using refs from the snapshot
playwright-cli click e15
playwright-cli type "page.click"
playwright-cli press Enter
# take a screenshot (rarely used, as snapshot is more common)
playwright-cli screenshot
# close the browser
playwright-cli close
```

## 명령

### 핵심

```bash
playwright-cli open
# open and navigate right away
playwright-cli open https://example.com/
playwright-cli goto https://playwright.dev
playwright-cli type "search query"
playwright-cli click e3
playwright-cli dblclick e7
# --submit presses Enter after filling the element
playwright-cli fill e5 "user@example.com"  --submit
playwright-cli drag e2 e8
# drop files or data onto an element (from outside the page)
playwright-cli drop e4 --path=./image.png
playwright-cli drop e4 --data="text/plain=hello world"
playwright-cli hover e4
playwright-cli select e9 "option-value"
playwright-cli upload ./document.pdf
playwright-cli check e12
playwright-cli uncheck e12
playwright-cli snapshot
# search the snapshot for text or a regexp, returns matching nodes with surrounding context
playwright-cli find "Sign in"
playwright-cli find --regex "Sign (in|up)"
# wrap the regexp in slashes to add flags, e.g. /i for case-insensitive
playwright-cli find --regex "/sign (in|up)/i"
playwright-cli eval "document.title"
playwright-cli eval "el => el.textContent" e5
# get element id, class, or any attribute not visible in the snapshot
playwright-cli eval "el => el.id" e5
playwright-cli eval "el => el.getAttribute('data-testid')" e5
playwright-cli dialog-accept
playwright-cli dialog-accept "confirmation text"
playwright-cli dialog-dismiss
playwright-cli resize 1920 1080
playwright-cli close
```

### 항해

```bash
playwright-cli go-back
playwright-cli go-forward
playwright-cli reload
```

### 건반

```bash
playwright-cli press Enter
playwright-cli press ArrowDown
playwright-cli keydown Shift
playwright-cli keyup Shift
```

### 생쥐

```bash
playwright-cli mousemove 150 300
playwright-cli mousedown
playwright-cli mousedown right
playwright-cli mouseup
playwright-cli mouseup right
playwright-cli mousewheel 0 100
```

### 다른 이름으로 저장

```bash
playwright-cli screenshot
playwright-cli screenshot e5
playwright-cli screenshot --filename=page.png
playwright-cli screenshot --hires
playwright-cli pdf --filename=page.pdf
```

### 탭

```bash
playwright-cli tab-list
playwright-cli tab-new
playwright-cli tab-new https://example.com/page
playwright-cli tab-close
playwright-cli tab-close 2
playwright-cli tab-select 0
```

### 저장

```bash
playwright-cli state-save
playwright-cli state-save auth.json
playwright-cli state-load auth.json

# Cookies
playwright-cli cookie-list
playwright-cli cookie-list --domain=example.com
playwright-cli cookie-get session_id
playwright-cli cookie-set session_id abc123
playwright-cli cookie-set session_id abc123 --domain=example.com --httpOnly --secure
playwright-cli cookie-delete session_id
playwright-cli cookie-clear

# LocalStorage
playwright-cli localstorage-list
playwright-cli localstorage-get theme
playwright-cli localstorage-set theme dark
playwright-cli localstorage-delete theme
playwright-cli localstorage-clear

# SessionStorage
playwright-cli sessionstorage-list
playwright-cli sessionstorage-get step
playwright-cli sessionstorage-set step 3
playwright-cli sessionstorage-delete step
playwright-cli sessionstorage-clear
```

### 회로망

```bash
playwright-cli route "**/*.jpg" --status=404
playwright-cli route "https://api.example.com/**" --body='{"mock": true}'
playwright-cli route-list
playwright-cli unroute "**/*.jpg"
playwright-cli unroute
```

### 개발자 도구

```bash
playwright-cli console
playwright-cli console warning
playwright-cli requests
playwright-cli request 5
playwright-cli run-code "async page => await page.context().grantPermissions(['geolocation'])"
playwright-cli run-code --filename=script.js
playwright-cli tracing-start
playwright-cli tracing-stop
playwright-cli video-start video.webm
playwright-cli video-chapter "Chapter Title" --description="Details" --duration=2000
playwright-cli video-stop

# annotate each subsequent action (click, type, ...) with a callout naming the action and highlighting the target
playwright-cli video-show-actions --duration=600 --position=top-right
playwright-cli video-hide-actions

# launch the dashboard for UI review / design feedback — user annotates the page, you receive the annotated screenshot, snapshot, and notes
playwright-cli show --annotate

# generate a Playwright locator for an element from its ref or selector
playwright-cli generate-locator e5 --raw

# show a persistent highlight overlay for an element, optionally with a custom style
playwright-cli highlight e5
playwright-cli highlight e5 --style="outline: 3px dashed red"
# hide a single element highlight, or all page highlights when no target is given
playwright-cli highlight e5 --hide
playwright-cli highlight --hide
```

## 원시 출력

전역 `--raw` 옵션은 출력에서 ​​페이지 상태, 생성된 코드 및 스냅샷 섹션을 제거하고 결과 값만 반환합니다. 명령 출력을 다른 도구로 파이프하는 데 사용합니다. 출력을 생성하지 않는 명령은 아무것도 반환하지 않습니다.

```bash
playwright-cli --raw eval "JSON.stringify(performance.timing)" | jq '.loadEventEnd - .navigationStart'
playwright-cli --raw eval "JSON.stringify([...document.querySelectorAll('a')].map(a => a.href))" > links.json
playwright-cli --raw snapshot > before.yml
playwright-cli click e5
playwright-cli --raw snapshot > after.yml
diff before.yml after.yml
TOKEN=$(playwright-cli --raw cookie-get session_id)
playwright-cli --raw localstorage-get theme
```

모든 응답을 JSON으로 래핑하는 구조화된 출력의 경우 --json을 전달합니다.
```bash
playwright-cli list --json
```

## 매개변수 열기
```bash
# Use specific browser when creating session
playwright-cli open --browser=chrome
playwright-cli open --browser=firefox
playwright-cli open --browser=webkit
playwright-cli open --browser=msedge

# Emulate a generic mobile device (Pixel 10 for Chromium, iPhone 17 for WebKit).
# Prefer this when a mobile layout is acceptable: mobile pages are usually
# lighter, so snapshots are smaller and cheaper.
playwright-cli open --mobile
playwright-cli open --device="iPhone 15"

# Use persistent profile (by default profile is in-memory)
playwright-cli open --persistent
# Use persistent profile with custom directory
playwright-cli open --profile=/path/to/profile

# Connect to browser via Playwright Extension
playwright-cli attach --extension=chrome

# Connect to a running Chrome or Edge by channel name
playwright-cli attach --cdp=chrome
playwright-cli attach --cdp=msedge

# Connect to a running browser via CDP endpoint
playwright-cli attach --cdp=http://localhost:9222

# Start with config file
playwright-cli open --config=my-config.json

# Close the browser
playwright-cli close
# Detach from an attached browser (leaves the external browser running)
playwright-cli -s=msedge detach
# Delete user data for the default session
playwright-cli delete-data
```

## Windows에서 `&`가 포함된 URL

Windows에서 `cmd.exe` 및 PowerShell은 `&`를 명령 구분 기호로 처리하므로 `playwright-cli`가 실행되기 전에 여러 쿼리 매개 변수가 있는 URL이 잘립니다. `cmd.exe`에서 `^&`를 사용하여 `&`를 이스케이프하거나 PowerShell에서 `--%`를 사용합니다.

```batch
playwright-cli goto "https://example.com/?a=1^&b=2"
```

```powershell
playwright-cli --% goto "https://example.com/?a=1&b=2"
```

## 스냅샷

각 명령 후에 playwright-cli는 현재 브라우저 상태의 스냅샷을 제공합니다.

```bash
> playwright-cli goto https://example.com
### Page
- Page URL: https://example.com/
- Page Title: Example Domain
### Snapshot
[Snapshot](.playwright-cli/page-2026-02-14T19-22-42-679Z.yml)
```

`playwright-cli snapshot` 명령을 사용하여 요청 시 스냅샷을 찍을 수도 있습니다. 아래의 모든 옵션은 필요에 따라 결합될 수 있습니다.

```bash
# default - save to a file with timestamp-based name
playwright-cli snapshot

# save to file, use when snapshot is a part of the workflow result
playwright-cli snapshot --filename=after-click.yaml

# snapshot an element instead of the whole page
playwright-cli snapshot "#main"

# limit snapshot depth for efficiency, take a partial snapshot afterwards
playwright-cli snapshot --depth=4
playwright-cli snapshot e34

# include each element's bounding box as [box=x,y,width,height]
playwright-cli snapshot --boxes

# search a large snapshot instead of capturing it all — returns matching nodes
# with 3 lines of context around each match (like grep -C)
playwright-cli find "Add to cart"
playwright-cli find --regex "\\$[0-9]+\\.[0-9]{2}"
```

## 타겟팅 요소

기본적으로 스냅샷의 참조를 사용하여 페이지 요소와 상호 작용합니다.

```bash
# get snapshot with refs
playwright-cli snapshot

# interact using a ref
playwright-cli click e15
```

CSS 선택기나 Playwright 로케이터를 사용할 수도 있습니다.

```bash
# css selector
playwright-cli click "#main > button.submit"

# role locator
playwright-cli click "getByRole('button', { name: 'Submit' })"

# test id
playwright-cli click "getByTestId('submit-button')"
```

## 브라우저 세션

```bash
# create new browser session named "mysession" with persistent profile
playwright-cli -s=mysession open example.com --persistent
# same with manually specified profile directory (use when requested explicitly)
playwright-cli -s=mysession open example.com --profile=/path/to/profile
playwright-cli -s=mysession click e6
playwright-cli -s=mysession close  # stop a named browser
playwright-cli -s=mysession delete-data  # delete user data for persistent session

playwright-cli list
# Close all browsers
playwright-cli close-all
# Forcefully kill all browser processes
playwright-cli kill-all
```

## 설치

글로벌 `playwright-cli` 명령을 사용할 수 없는 경우 `npx playwright cli`를 통해 로컬 버전을 사용해 보십시오.

```bash
npx --no-install playwright --version
```

로컬 버전을 사용할 수 있는 경우 모든 명령에서 `npx playwright cli`를 사용하십시오. 그렇지 않으면 `playwright-cli`를 전역 명령으로 설치하십시오.

```bash
npm install -g @playwright/cli@latest
```

## 예: 양식 제출

```bash
playwright-cli open https://example.com/form
playwright-cli snapshot

playwright-cli fill e1 "user@example.com"
playwright-cli fill e2 "password123"
playwright-cli click e3
playwright-cli snapshot
playwright-cli close
```

## 예: 다중 탭 작업 흐름

```bash
playwright-cli open https://example.com
playwright-cli tab-new https://example.com/other
playwright-cli tab-list
playwright-cli tab-select 0
playwright-cli snapshot
playwright-cli close
```

## 예: DevTools를 사용한 디버깅

```bash
playwright-cli open https://example.com
playwright-cli click e4
playwright-cli fill e7 "test"
playwright-cli console
playwright-cli requests
playwright-cli close
```

```bash
playwright-cli open https://example.com
playwright-cli tracing-start
playwright-cli click e4
playwright-cli fill e7 "test"
playwright-cli tracing-stop
playwright-cli close
```

## 예: 대화형 세션

사용자에게 UI 검토 또는 디자인 피드백을 요청하세요. 사용자는 라이브 페이지에 상자를 그리고 댓글을 입력합니다. 주석이 달린 스크린샷, 표시된 영역의 스냅샷, 사용자 메모를 받게 됩니다. 사용자가 "UI 검토", "디자인 피드백"을 요청하거나 "사용자에게 생각/원하는/의미가 무엇인지 물어보기"를 요청할 때마다 이 방법을 사용하세요.

```bash
playwright-cli open https://example.com
playwright-cli show --annotate
```

## 특정 작업

* **Playwright 테스트 실행 및 디버깅** [references/playwright-tests.md](references/playwright-tests.md)
* **모의 요청** [references/request-mocking.md](references/request-mocking.md)
* **Playwright 코드 실행** [references/running-code.md](references/running-code.md)
* **브라우저 세션 관리** [references/session-management.md](references/session-management.md)
* **저장소 상태(쿠키, localStorage)** [references/storage-state.md](references/storage-state.md)
* **테스트 생성(계획/생성/치료)** [references/test-gener.md](references/test-generation.md)
* **추적** [references/tracing.md](references/tracing.md)
* **영상 녹화** [references/video-recording.md](references/video-recording.md)
* **요소 속성 검사 중** [references/element-attributes.md](references/element-attributes.md)
