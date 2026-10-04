# 비디오 녹화

디버깅, 문서화 또는 확인을 위해 브라우저 자동화 세션을 비디오로 캡처합니다. WebM(VP8/VP9 코덱)을 생성합니다.

## 기본녹화

```bash
# Open browser first
playwright-cli open

# Start recording
playwright-cli video-start demo.webm

# Add a chapter marker for section transitions
playwright-cli video-chapter "Getting Started" --description="Opening the homepage" --duration=2000

# Navigate and perform actions
playwright-cli goto https://example.com
playwright-cli snapshot
playwright-cli click e1

# Add another chapter
playwright-cli video-chapter "Filling Form" --description="Entering test data" --duration=2000
playwright-cli fill e2 "test input"

# Stop and save
playwright-cli video-stop
```

## 모범 사례

### 1. 설명적인 파일 이름을 사용하세요

```bash
# Include context in filename
playwright-cli video-start recordings/login-flow-2024-01-15.webm
playwright-cli video-start recordings/checkout-test-run-42.webm
```

### 2. 전체 히어로 스크립트를 녹음하세요.

사용자를 위해 또는 작업 증명으로 비디오를 녹화할 때 코드 조각을 생성하고 실행 코드로 실행하는 것이 가장 좋습니다.
작업 사이에 적절한 일시 중지를 삽입하고 비디오에 주석을 달 수 있습니다. 이를 위한 새로운 Playwright API가 있습니다.

1) CLI를 사용하여 시나리오를 수행하고 모든 로케이터와 작업을 기록합니다. 강조 표시를 위해 경계 상자를 요청하려면 해당 로케이터가 필요합니다.
2) 비디오용으로 의도한 스크립트를 사용하여 파일을 만듭니다(아래). 좋은 타이핑을 위해 지연과 함께 pressSequentially를 사용하고 적당히 일시 중지하세요.
3) playwright-cli run-code --filename your-script.js를 사용하세요.

**중요**: 오버레이는 `pointer-events: none`이므로 페이지 상호 작용을 방해하지 않습니다. 페이지를 클릭하거나 채우거나 작업을 수행하는 동안 고정 오버레이를 안전하게 표시할 수 있습니다.

```js
async page => {
  await page.screencast.start({ path: 'video.webm', size: { width: 1280, height: 800 } });
  await page.goto('https://demo.playwright.dev/todomvc');

  // Show a chapter card — blurs the page and shows a dialog.
  // Blocks until duration expires, then auto-removes.
  // Use this for simple use cases, but always feel free to hand-craft your own beautiful
  // overlay via await page.screencast.showOverlay().
  await page.screencast.showChapter('Adding Todo Items', {
    description: 'We will add several items to the todo list.',
    duration: 2000,
  });

  // Perform action
  await page.getByRole('textbox', { name: 'What needs to be done?' }).pressSequentially('Walk the dog', { delay: 60 });
  await page.getByRole('textbox', { name: 'What needs to be done?' }).press('Enter');
  await page.waitForTimeout(1000);

  // Show next chapter
  await page.screencast.showChapter('Verifying Results', {
    description: 'Checking the item appeared in the list.',
    duration: 2000,
  });

  // Add a sticky annotation that stays while you perform actions.
  // Overlays are pointer-events: none, so they won't block clicks.
  const annotation = await page.screencast.showOverlay(`
    <div style="position: absolute; top: 8px; right: 8px;
      padding: 6px 12px; background: rgba(0,0,0,0.7);
      border-radius: 8px; font-size: 13px; color: white;">
      ✓ Item added successfully
    </div>
  `);

  // Perform more actions while the annotation is visible
  await page.getByRole('textbox', { name: 'What needs to be done?' }).pressSequentially('Buy groceries', { delay: 60 });
  await page.getByRole('textbox', { name: 'What needs to be done?' }).press('Enter');
  await page.waitForTimeout(1500);

  // Remove the annotation when done
  await annotation.dispose();

  // You can also highlight relevant locators and provide contextual annotations.
  const bounds = await page.getByText('Walk the dog').boundingBox();
  await page.screencast.showOverlay(`
    <div style="position: absolute;
      top: ${bounds.y}px;
      left: ${bounds.x}px;
      width: ${bounds.width}px;
      height: ${bounds.height}px;
      border: 1px solid red;">
    </div>
    <div style="position: absolute;
      top: ${bounds.y + bounds.height + 5}px;
      left: ${bounds.x + bounds.width / 2}px;
      transform: translateX(-50%);
      padding: 6px;
      background: #808080;
      border-radius: 10px;
      font-size: 14px;
      color: white;">Check it out, it is right above this text
    </div>
  `, { duration: 2000 });

  await page.screencast.stop();
}
```

창의성을 포용하세요. 오버레이는 강력합니다.

### 오버레이 API 요약

| 방법 | 사용 사례 |
|--------|----------|
| `page.screencast.showChapter(title, { description?, duration?, styleSheet? })` | 배경이 흐릿한 전체 화면 챕터 카드 — 섹션 전환에 적합 |
| `page.screencast.showOverlay(html, { duration? })` | 맞춤 HTML 오버레이 — 콜아웃, 라벨, 하이라이트에 사용 |
| `disposable.dispose()` | 지속 시간 없이 추가된 고정 오버레이 제거 |
| `page.screencast.hideOverlays()` / `page.screencast.showOverlays()` | 모든 오버레이를 일시적으로 숨기기/표시 |

## 추적과 비디오

| 특징 | 동영상 | 트레이싱 |
|---------|-------|---------|
| 산출 | WebM 파일 | 추적 파일(Trace Viewer에서 볼 수 있음) |
| 쇼 | 영상녹화 | DOM 스냅샷, 네트워크, 콘솔, 작업 |
| 사용 사례 | 데모, 문서 | 디버깅, 분석 |
| 크기 | 더 크게 | 더 작게 |

## 제한 사항

- 녹음하면 자동화에 약간의 오버헤드가 추가됩니다.
- 대용량 녹화는 상당한 디스크 공간을 차지할 수 있습니다.
