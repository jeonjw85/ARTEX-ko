package notify

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// 이 문서는 일반 Webhook 템플릿의 **기능 경계**를 잠급니다.
//
// 이는 "사용자 제공 문자열이 코드로 평가되는" 패키지의 유일한 위치이므로 수행할 수 있는 작업에 대해 명확하게 설명합니다.
// 수행할 수 없는 작업 및 테스트를 사용하여 이러한 속성을 수정합니다. 그렇지 않으면 나중에 누군가 편리하게 템플릿 컨텍스트를 추가할 것입니다.
// 방법을 사용하거나 readFile를 FuncMap에 추가하면 기능이 자동으로 확장되고 diff는
// 단지 무해한 작은 기능일 뿐입니다.

// TestTemplateContextHasNoMethods가 가장 중요한 것입니다.
//
// text/template는 내보내기 메서드를 호출합니다({{.Foo}}는 필드를 가져오고 메서드를 조정할 수 있음). 따라서 템플릿 컨텍스트
// 내보낸 메서드가 있는 **모든** 유형에 액세스할 수 있는 한 해당 메서드는 템플릿 작성자에게 노출됩니다.
// 이 기능의 컨텍스트는 의도적으로 모든 순수 데이터입니다(내보낸 필드만, 메서드 없음).
//
// 이것이 실패하는 경우: 누군가가 webhookTemplateData / webhookItem에 메소드를 추가했음을 의미합니다.
// 릴리스하기로 결정하기 전에 먼저 노출하고 싶지 않은 항목을 읽기 위해 템플릿에서 해당 메서드를 사용할 수 있는지 생각해 보세요.
func TestTemplateContextHasNoMethods(t *testing.T) {
	for _, v := range []any{webhookTemplateData{}, webhookItem{}} {
		typ := reflect.TypeOf(v)
		if n := typ.NumMethod(); n != 0 {
			var names []string
			for i := 0; i < n; i++ {
				names = append(names, typ.Method(i).Name)
			}
			t.Fatalf("%s는 %d 메서드(%s)를 노출합니다. text/template는 이를 호출할 수 있습니다."+
				"이는 템플릿 작성자에게 이러한 방법의 기능을 공개하는 것과 같습니다.", typ.Name(), n, strings.Join(names, ", "))
		}
	}
}

// TestTemplateFuncsAreMinimal 템플릿에 노출된 기능 세트를 잠급니다.
//
// FuncMap의 각 추가 기능에는 추가 기능이 있습니다. 현재 값을 직렬화하는 데 사용되는 json / jsons만 있습니다.
// JSON 조각으로 - 파일을 읽을 수 없고, 요청을 보낼 수 없으며, 명령을 실행할 수 없습니다.
func TestTemplateFuncsAreMinimal(t *testing.T) {
	var got []string
	for name := range webhookTemplateFuncs {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"json", "jsons"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("템플릿 기능 세트가 변경되었습니다. %v를 얻었고 %v가 필요했습니다. 새로운 기능을 추가하기 전에 기능이 확장되지 않는지 확인하십시오."+
			"(파일을 읽고 쓸 수 없고, 네트워크 요청을 시작할 수 없으며, 명령을 실행할 수 없습니다)", got, want)
	}
}

// TestTemplateCannotReachUnknownData는 템플릿의 범위를 벗어난 액세스를 다룹니다.
// 존재하지 않는 것에 액세스하는 것은 무언가를 에코하는 것이 아니라 실패해야 합니다. 실패 메시지는 내부 데이터를 가져오면 안 됩니다.
func TestTemplateCannotReachUnknownData(t *testing.T) {
	_, err := renderWebhookBody(`{"x": {{.Environment}}, "y": {{.Env}}}`, singleMsg())
	if err == nil {
		t.Fatal("존재하지 않는 필드에 액세스하면 오류가 발생합니다.")
	}
	// 템플릿 컨텍스트(취약점 제목/요약)의 실제 콘텐츠는 오류에 나타날 수 없습니다.
	for _, leak := range []string{"SQL 주입", "매개변수 id"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("템플릿 오류로 인해 메시지 내용이 드러납니다. %q: %v", leak, err)
		}
	}
}

// TestTemplateRenderFailsPermanently 잘못 작성된 템플릿은 구성 오류이며 다시 시도해도 저절로 복구되지 않습니다.
// 재시도가 가능하다고 판단되면 잘못된 템플릿으로 인해 각 배달에서 세 번의 회피가 헛되이 실행됩니다.
func TestTemplateRenderFailsPermanently(t *testing.T) {
	cfg := map[string]any{
		"url":           "https://example.com/hook",
		"body_template": `{{.Items.`,
	}
	if err := (webhookChannel{}).Validate(cfg); err == nil {
		t.Fatal("저장 시 템플릿 구문 오류가 차단되어야 합니다.")
	}
	// 검증을 우회하여 직접 전달하더라도 반복적인 재시도 대신 영구 실패를 판단해야 합니다.
	_, err := (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("잘못된 템플릿은 영구실패로 판단하여 %v를 받아야 합니다.", err)
	}
}

// TestTemplateCanOnlyProduceJSON는 "템플릿 렌더링 결과가 적법한 JSON"라는 제약 조건을 무시합니다.
// 우연히 "템플릿을 사용하여 다른 프로토콜을 트리거하는 일반 텍스트를 생성하는" 사용을 차단합니다.
func TestTemplateCanOnlyProduceJSON(t *testing.T) {
	// 법적 템플릿은 통과할 수 있습니다.
	ok := map[string]any{"url": "https://example.com/hook", "body_template": `{"t":{{json .Title}}}`}
	if err := (webhookChannel{}).Validate(ok); err != nil {
		t.Fatalf("법적 템플릿은 확인을 통과해야 합니다: %v", err)
	}
	// JSON가 아닌 렌더링은 거부되어야 합니다(있는 그대로 내보내는 대신).
	bad := map[string]any{"url": "http://127.0.0.1:1/hook", "body_template": `not json {{.Count}}`}
	_, err := (webhookChannel{}).Send(context.Background(), bad, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("JSON가 아닌 렌더링은 영구 실패로 판단하여 %v를 받아야 합니다.", err)
	}
	if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("오류 메시지에는 문제가 JSON라고 나와야 합니다. %v를 얻으세요.", err)
	}
}
