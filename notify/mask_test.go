package notify

import (
	"errors"
	"strings"
	"testing"
)

func TestMaskedValueHidesBodyButKeepsTailHint(t *testing.T) {
	const secret = "https://oapi.dingtalk.com/robot/send?access_token=abcdef123456"
	got := MaskedValue(secret)
	if strings.Contains(got, "abcdef123456") {
		t.Fatalf("마스크 값은 전체 자격 증명을 나타냅니다: %q", got)
	}
	if strings.Contains(got, "oapi.dingtalk.com") {
		t.Fatalf("마스크 값은 주소 본문(%q)을 노출해서는 안 됩니다.", got)
	}
	// 마지막 6자리는 사용자가 어떤 로봇인지 식별할 수 있도록 남겨두어야 합니다.
	if !strings.HasSuffix(got, "123456") {
		t.Fatalf("마지막 6자리는 식별 팁으로 보관해야 합니다: %q", got)
	}
	if !IsMasked(got) {
		t.Fatalf("마스크 값은 IsMasked: %q에서 인식되어야 합니다.", got)
	}
}

func TestMaskedValueShortSecretGivesNoHint(t *testing.T) {
	// 짧은 크리덴셜의 마지막 6자리까지 노출된다면 크리덴셜 전체를 노출시키는 것과 같습니다.
	for _, s := range []string{"abc", "abcdef", ""} {
		got := MaskedValue(s)
		if got != MaskedPrefix {
			t.Fatalf("%d 길이의 자격 증명은 꼬리 힌트를 제공하지 않아야 하며 %q를 가져옵니다.", len(s), got)
		}
		if s != "" && strings.Contains(got, s) {
			t.Fatalf("마스크 값에는 원래 값인 %q가 포함됩니다.", got)
		}
	}
}

func TestMaskConfigMasksOnlySecrets(t *testing.T) {
	cfg := map[string]any{
		"webhook": "https://example.com/hook?token=SECRETVALUE",
		"secret":  "SECtest123456",
		"port":    float64(587),
		"host":    "smtp.example.com",
	}
	masked := MaskConfig(KindDingTalk, cfg)
	for _, k := range []string{"webhook", "secret"} {
		s, _ := masked[k].(string)
		if !IsMasked(s) {
			t.Errorf("%q를 얻으려면 %s를 마스크해야 합니다.", k, s)
		}
	}
	// 자격 증명이 아닌 필드는 그대로 두어야 합니다. 그렇지 않으면 UI를 표시할 수 없습니다.
	if masked["port"] != float64(587) {
		t.Errorf("비자격 증명 필드 port는 변경하면 안 됩니다: %v", masked["port"])
	}
}

func TestMaskConfigUnknownKindReturnsEmpty(t *testing.T) {
	// 채널 유형을 인식할 수 없는 경우 자격 증명이 포함될 수 있는 원본 콘텐츠를 다시 표시하는 것보다 UI가 빈 구성을 표시하도록 하는 것이 더 좋습니다.
	got := MaskConfig("nope", map[string]any{"webhook": "https://x/y?token=LEAK"})
	if len(got) != 0 {
		t.Fatalf("알 수 없는 채널 유형은 빈 구성을 반환해야 합니다. %v를 가져옵니다.", got)
	}
}

func TestMaskConfigDoesNotMutateInput(t *testing.T) {
	// 마스크는 표시 레이어 동작이므로 라이브러리의 실제 값을 변경할 수 없습니다.
	cfg := map[string]any{"webhook": "https://example.com/hook", "secret": "SECtest123456"}
	_ = MaskConfig(KindDingTalk, cfg)
	if IsMasked(cfg["secret"].(string)) {
		t.Fatal("MaskConfig는 입력 매개변수를 수정하여 실제 자격 증명을 마스크 값으로 덮어쓰게 됩니다.")
	}
}

func TestMergeConfigKeepsStoredOnMaskedIncoming(t *testing.T) {
	stored := map[string]any{"webhook": "https://real/hook", "secret": "REALSECRET", "method": "POST"}
	// 사용자는 method만 변경했고 브라우저는 마스크 값 + 새 method를 제출했습니다.
	incoming := map[string]any{
		"webhook": MaskedValue("https://real/hook"),
		"secret":  MaskedValue("REALSECRET"),
		"method":  "PUT",
	}
	got := MergeConfig(stored, incoming)
	if got["webhook"] != "https://real/hook" || got["secret"] != "REALSECRET" {
		t.Fatalf("마스크 필드는 라이브러리의 원래 값을 유지해야 하므로 %v가 발생합니다.", got)
	}
	if got["method"] != "PUT" {
		t.Fatalf("수정된 필드가 적용되고 %v를 받아야 합니다.", got["method"])
	}
}

func TestMergeConfigEmptyStringClears(t *testing.T) {
	stored := map[string]any{"webhook": "https://real/hook", "secret": "REALSECRET"}
	got := MergeConfig(stored, map[string]any{"secret": ""})
	if _, ok := got["secret"]; ok {
		t.Fatalf("빈 문자열은 이 필드를 지우고 %v를 가져와야 합니다.", got)
	}
	// 언급되지 않은 필드는 유지됩니다(로컬 업데이트 의미).
	if got["webhook"] != "https://real/hook" {
		t.Fatalf("언급되지 않은 필드는 유지되어야 하며 %v를 가져옵니다.", got)
	}
}

func TestMergeConfigKeepsUnmentionedStoredKeys(t *testing.T) {
	stored := map[string]any{"host": "smtp.example.com", "port": float64(587), "password": "pw"}
	got := MergeConfig(stored, map[string]any{"port": float64(465)})
	if got["host"] != "smtp.example.com" || got["password"] != "pw" {
		t.Fatalf("언급되지 않은 필드는 유지되어야 하며 %v를 가져옵니다.", got)
	}
	if got["port"] != float64(465) {
		t.Fatalf("언급된 필드를 업데이트하여 %v를 가져와야 합니다.", got["port"])
	}
}

// TestPrepareConfigUpdateBlocksDestinationSwap는 이 패키지의 가장 중요한 보안 불변입니다.
// **대상 주소를 변경할 때 기존 자격 증명을 가져올 수 없습니다**.
//
// 이러한 사용 사례는 정확히 동일한 공격 형태의 입력을 사용합니다(주소만 변경하고 자격 증명은 언급하지 않음).
// "방어 논리의 올바른 입력"보다는 - 후자만 테스트하면 방어가 적용되지 않더라도 방어는 여전히 모두 녹색입니다.
func TestPrepareConfigUpdateBlocksDestinationSwap(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		stored   map[string]any
		incoming map[string]any
		// wantMissing는 이름이 지정될 자격 증명 키입니다.
		wantMissing string
	}{
		{
			name: "일반 Webhook 주소를 변경하려면 Authorization 헤더를 사용하면 됩니다.",
			kind: KindWebhook,
			stored: map[string]any{
				"url":     "https://legit.example.com/hook",
				"headers": map[string]any{"Authorization": "Bearer REAL-TOKEN"},
			},
			incoming:    map[string]any{"url": "https://attacker.tld/c"},
			wantMissing: "headers",
		},
		{
			name:        "Telegram를 base_url로 변경하고 Bot Token를 자신의 엔드포인트로 보내려고 합니다.",
			kind:        KindTelegram,
			stored:      map[string]any{"bot_token": "123456:REAL", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming:    map[string]any{"base_url": "https://attacker.tld"},
			wantMissing: "bot_token",
		},
		{
			name:        "이메일을 SMTP로 변경하세요. 호스트가 비밀번호를 넘겨주려고 합니다.",
			kind:        KindEmail,
			stored:      map[string]any{"host": "smtp.corp.com", "port": 587, "password": "REALPW", "from": "a@b.c", "to": []any{"d@e.f"}},
			incoming:    map[string]any{"host": "smtp.attacker.tld"},
			wantMissing: "password",
		},
		{
			name:        "이메일이 닫혔습니다. TLS 비밀번호를 다시 입력해야 합니다.",
			kind:        KindEmail,
			stored:      map[string]any{"host": "smtp.corp.com", "port": 587, "tls": false, "password": "REALPW", "from": "a@b.c", "to": []any{"d@e.f"}},
			incoming:    map[string]any{"tls": true},
			wantMissing: "password",
		},
		{
			// 마스크 값 = "이전 자격 증명 상속". 주소 변경 시에도 거부되어야 합니다.
			name:        "패스백 마스크 자격 증명 + 새 주소",
			kind:        KindTelegram,
			stored:      map[string]any{"bot_token": "123456:REAL", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming:    map[string]any{"base_url": "https://attacker.tld", "bot_token": MaskedValue("123456:REAL")},
			wantMissing: "bot_token",
		},
		{
			name:        "DingTalk가 Webhook를 변경하고 서명 키를 계속 사용하려고 합니다.",
			kind:        KindDingTalk,
			stored:      map[string]any{"webhook": "https://oapi.dingtalk.com/robot/send?access_token=OLD", "secret": "REALSEC"},
			incoming:    map[string]any{"webhook": "https://attacker.tld/hook"},
			wantMissing: "secret",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := PrepareConfigUpdate(tc.kind, tc.stored, tc.incoming)
			if err == nil {
				t.Fatalf("자격 증명을 다시 제시하지 않고 주소를 변경하는 것은 거부되어야 합니다. %v 구성하기", merged)
			}
			var target *ErrDestinationChangedWithoutCredentials
			if !errors.As(err, &target) {
				t.Fatalf("인터페이스가 실행 가능한 프롬프트를 제공할 수 있도록 특수 오류 유형이 반환되어야 합니다. %T: %v를 가져옵니다.", err, err)
			}
			found := false
			for _, m := range target.Missing {
				if m == tc.wantMissing {
					found = true
				}
			}
			if !found {
				t.Fatalf("누락된 자격 증명 키의 이름을 %q로 지정하고 %v를 가져와야 합니다.", tc.wantMissing, target.Missing)
			}
			// 오류 메시지는 운영자에게 오류 수정 방법을 안내할 수 있어야 합니다.
			if !strings.Contains(err.Error(), tc.wantMissing) {
				t.Errorf("오류 메시지에는 %q: %v가 언급되어야 합니다.", tc.wantMissing, err)
			}
		})
	}
}

// TestPrepareConfigUpdateAllowsLegitimateEdits 반대 사용 사례: 실수로 일반 편집을 차단할 수 없습니다.
// 그렇지 않으면 이 보호 기능은 "너무 짜증나기" 때문에 우회되거나 삭제됩니다.
func TestPrepareConfigUpdateAllowsLegitimateEdits(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		stored   map[string]any
		incoming map[string]any
	}{
		{
			name:     "이름만 변경하세요. (구성은 그대로 반환됩니다.)",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://legit.example.com/hook", "headers": map[string]any{"Authorization": "Bearer REAL"}},
			incoming: map[string]any{"url": MaskedValue("https://legit.example.com/hook")},
		},
		{
			name:     "요청 방법만 변경되며 주소와 자격 증명은 변경되지 않습니다.",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://legit.example.com/hook", "method": "POST"},
			incoming: map[string]any{"method": "PUT"},
		},
		{
			name:     "주소를 변경하고 동시에 새 자격 증명을 제공하세요.",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://old.example.com/hook", "headers": map[string]any{"Authorization": "Bearer OLD"}},
			incoming: map[string]any{"url": "https://new.example.com/hook", "headers": map[string]any{"Authorization": "Bearer NEW"}},
		},
		{
			name:     "주소를 변경하고 자격 증명이 더 이상 필요하지 않음을 명시적으로 명시합니다.",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://old.example.com/hook", "headers": map[string]any{"Authorization": "Bearer OLD"}},
			incoming: map[string]any{"url": "https://new.example.com/hook", "headers": ""},
		},
		{
			name:     "Telegram가 chat_id로 변경됩니다(대상 아님).",
			kind:     KindTelegram,
			stored:   map[string]any{"bot_token": "t", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming: map[string]any{"chat_id": "-100200"},
		},
		{
			name:     "이메일 수신자 변경(대상 아님)",
			kind:     KindEmail,
			stored:   map[string]any{"host": "smtp.corp.com", "port": 587, "password": "PW", "from": "a@b.c", "to": []any{"x@y.z"}},
			incoming: map[string]any{"to": []any{"new@y.z"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := PrepareConfigUpdate(tc.kind, tc.stored, tc.incoming)
			if err != nil {
				t.Fatalf("실수로 차단된 합법적인 편집자: %v", err)
			}
			if merged == nil {
				t.Fatal("병합된 결과가 반환되어야 합니다.")
			}
		})
	}
}

// TestPrepareConfigUpdatePortTypeTolerance는 잘못 판단하기 쉬운 세부 사항을 다룹니다.
// 프런트 엔드에서 제출한 포트는 JSON number(float64)이고, 라이브러리에서 다시 읽은 포트도 float64입니다.
// 그러나 두 값의 유형은 다를 수 있습니다(예: int vs float64). ==와 비교하면 "변경되지 않음"이 "변경됨"으로 판단됩니다.
// 결과적으로 이름만 변경한 사용자에게는 "비밀번호를 다시 입력하십시오"라는 팝업이 표시됩니다. 잘못된 경보로 인해 사람들은 더 이상 이 보호 기능을 신뢰하지 않게 됩니다.
func TestPrepareConfigUpdatePortTypeTolerance(t *testing.T) {
	stored := map[string]any{"host": "smtp.corp.com", "port": float64(587), "password": "PW"}
	// 동일한 포트가 int 형식으로 제출됩니다.
	if _, err := PrepareConfigUpdate(KindEmail, stored, map[string]any{"port": 587}); err != nil {
		t.Fatalf("동일한 포트 값(타입만 다름)은 주소 변경으로 간주하면 안 됩니다: %v", err)
	}
	// 포트가 실제로 변경된 경우에는 차단해야 합니다.
	if _, err := PrepareConfigUpdate(KindEmail, stored, map[string]any{"port": 25}); err == nil {
		t.Fatal("포트 변경을 차단해야 합니다.")
	}
}

// TestPrepareConfigUpdateSurvivesRepeatedSaveWithBlankDestination 커버 "비워둘 수 있습니다.
// "대상 필드" 경로: Telegram의 base_url. 공식 API 주소를 사용하려면 공백으로 남겨두세요.
//
// 이는 채널이 두 번째 저장에서 영구적으로 저장되지 못하게 만드는 데 사용되었습니다.
//
//	새 경로를 생성할 때 base_url:""를 라이브러리에 저장합니다. (생성된 경로는 프런트 엔드에서 제출한 config를 직접 저장하고 MergeConfig를 사용하지 않습니다.)
//	→ 처음 저장할 때 MergeConfig는 빈 문자열을 명시적 클리어로 처리하고 delete는 키를 제거합니다.
//	→ 두 번째로 저장하면 incoming는 여전히 ""이지만 stored에는 더 이상 이 키가 없으며 "주소 변경됨"으로 판단됩니다.
//	→ bot_token는 마스크 에코 값 → 400 "대상 주소가 변경되었습니다. 동시에 자격 증명 필드를 다시 채워주십시오."
//
// 사용자는 아무것도 변경하지 않았지만 Bot Token를 다시 붙여넣지 않으면 더 이상 저장할 수 없습니다.
func TestPrepareConfigUpdateSurvivesRepeatedSaveWithBlankDestination(t *testing.T) {
	stored := map[string]any{"bot_token": "123:ABC", "chat_id": "-100", "base_url": ""}

	// 프런트엔드 buildConfig()는 이 채널의 각 필드 정의에 대한 값(Credential Backfill Mask, Credential Backfill Mask)을 제출합니다.
	// 빈 텍스트 상자는 빈 문자열을 제출합니다. 다음은 "변경된 키"를 제출하는 것이 아니라 출력을 완전히 재현한 것입니다.
	submit := func() map[string]any {
		return map[string]any{
			"bot_token": MaskedValue("123:ABC"),
			"chat_id":   "-100",
			"base_url":  "",
		}
	}

	// 첫 번째 저장: 채널 이름만 변경되고, config가 그대로 반환됩니다.
	merged, err := PrepareConfigUpdate(KindTelegram, stored, submit())
	if err != nil {
		t.Fatalf("첫 번째 저장이 실수로 차단되었습니다: %v", err)
	}
	if _, ok := merged["base_url"]; ok {
		t.Fatal("전제가 변경되었습니다. 빈 문자열은 MergeConfig에 의해 삭제되어야 합니다. 이 사용 사례에서 다루고자 하는 것은 ＂키가 사라진 후＂ 단계입니다.")
	}

	// 두 번째 저장: 제출된 콘텐츠는 지난번과 정확히 동일하며 사용자가 아무것도 변경하지 않았습니다.
	merged2, err := PrepareConfigUpdate(KindTelegram, merged, submit())
	if err != nil {
		t.Fatalf("두 번째 저장이 실수로 차단되었습니다(사용자가 아무것도 변경하지 않음): %v", err)
	}
	// 세 번째에서는 '한 번만 틀렸다'가 아니라 안정적이고 저장이 가능한 것으로 확인됐다.
	if _, err := PrepareConfigUpdate(KindTelegram, merged2, submit()); err != nil {
		t.Fatalf("세 번째 저장이 실수로 차단되었습니다: %v", err)
	}
	// 자격 증명은 항상 보존되어야 하며 빈 문자열 논리로 지워지지 않아야 합니다.
	if got := merged2["bot_token"]; got != "123:ABC" {
		t.Fatalf("Bot Token는 %v를 얻으려면 원래 값을 사용해야 합니다.", got)
	}
}

// TestPrepareConfigUpdateStillGuardsBlankDestinationChanges는 이전 사용 사례의 페어링입니다.
// 주장: 빈 문자열을 "키가 존재하지 않습니다"와 동일하게 취급하고 실제 주소 변경을 포기할 수 없습니다.
// 두 방향 모두 실제 자격 증명 나가는 경로입니다. Telegram 및 Token의 Bot는 URL 경로에 있습니다.
// base_url를 변경하는 것은 Token를 새 주소에 부여하는 것과 같습니다.
func TestPrepareConfigUpdateStillGuardsBlankDestinationChanges(t *testing.T) {
	// 방향 1: "비어 있는"(공식 주소)에서 자체 생성된 주소로 변경합니다.
	official := map[string]any{"bot_token": "123:ABC", "chat_id": "-100"}
	if _, err := PrepareConfigUpdate(KindTelegram, official, map[string]any{
		"bot_token": MaskedValue("123:ABC"),
		"base_url":  "https://tg-proxy.attacker.tld",
	}); err == nil {
		t.Fatal("공식 주소에서 자체 구축 주소로 변경 시 Token를 채워야 합니다.")
	}

	// 방향 2: 자체 생성된 주소를 지우고(= 공식 API로 다시 변경), 이것도 주소 변경입니다.
	proxied := map[string]any{"bot_token": "123:ABC", "base_url": "https://proxy.internal/bot"}
	if _, err := PrepareConfigUpdate(KindTelegram, proxied, map[string]any{
		"bot_token": MaskedValue("123:ABC"),
		"base_url":  "",
	}); err == nil {
		t.Fatal("자체 생성된 주소를 지우는 것(공식 API로 돌아가는 것)도 주소 변경이므로 Token를 다시 채워야 합니다.")
	}
}

func TestDestinationKeysDeclaredForEveryKind(t *testing.T) {
	// SecretKeys와 동일: 채널이 대상 키 선언을 잊어버린 경우 PrepareConfigUpdate는 채널을 보호할 수 없습니다.
	for kind, ch := range registry {
		if len(ch.DestinationKeys()) == 0 {
			t.Errorf("채널 %s는 대상 키를 선언하지 않으며 주소 변경 및 자격 증명 가져오기 보호가 유효하지 않습니다.", kind)
		}
		if len(ch.SecretKeys()) == 0 {
			t.Errorf("채널 %s 자격 증명 키가 선언되지 않았습니다.", kind)
		}
	}
}

func TestSecretKeysDeclaredForEveryKind(t *testing.T) {
	// 컴파일러는 각 채널이 SecretKeys를 구현하도록 강제했습니다. 또 다른 확인 사항은 다음과 같습니다. "마스크에 채널이 없습니다."
	// "Hand in 공백" - 빈 조각을 반환하는 채널은 해당 자격 증명이 브라우저에 일반 형식으로 표시된다는 의미입니다.
	expect := map[string]bool{
		KindDingTalk: true, KindFeishu: true, KindWeCom: true,
		KindWebhook: true, KindTelegram: true, KindEmail: true,
	}
	for kind, ch := range registry {
		if !expect[kind] {
			t.Errorf("채널 %s가 테스트에서 마스크 기대치를 등록하지 않았습니다.", kind)
			continue
		}
		if len(ch.SecretKeys()) == 0 {
			t.Errorf("채널 %s는 자격 증명 필드를 선언하지 않으며 해당 구성은 일반 텍스트로 표시됩니다.", kind)
		}
	}
}

// TestPrepareConfigUpdateRejectsMaskedInContainer 커버리지 감사에서 지적된 결함:
// 마스크된 센티넬을 **문자열이 아닌** 구조(예: webhook.headers가 객체임)에 넣을 때,
// MergeConfig는 "접두사가 있는 문자열"만 마스크로 인식하므로 리터럴 "__masked__"는 다음과 같이 간주됩니다.
// 실제 헤더 값은 데이터베이스에 저장됩니다. 후속 인증은 오류 없이 자동으로 실패합니다.
func TestPrepareConfigUpdateRejectsMaskedInContainer(t *testing.T) {
	stored := map[string]any{
		"url":     "https://legit.example.com/hook",
		"headers": map[string]any{"Authorization": "Bearer REAL"},
	}
	// 개체 내부 동반 마스크 보초.
	incoming := map[string]any{
		"headers": map[string]any{"Authorization": MaskedPrefix},
	}
	if _, err := PrepareConfigUpdate(KindWebhook, stored, incoming); err == nil {
		t.Fatal("구조 내부의 마스크된 센티널은 거부되어야 합니다(그렇지 않으면 리터럴이 라이브러리에 저장됩니다).")
	}
	// 전체 커밋 개체(실제 새 값)는 평소와 같이 허용됩니다.
	ok := map[string]any{"headers": map[string]any{"Authorization": "Bearer NEW"}}
	if _, err := PrepareConfigUpdate(KindWebhook, stored, ok); err != nil {
		t.Fatalf("새로운 요청 헤더의 정상적인 제출이 차단되어서는 안 됩니다: %v", err)
	}
}
