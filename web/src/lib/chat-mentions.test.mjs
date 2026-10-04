import assert from "node:assert/strict";
import test from "node:test";
import { activeMention, mentionSearch, mentionToken, selectedMentions } from "./chat-mentions.ts";

test("한국어 참조와 커서 위치를 지원하고 이메일은 참조로 처리하지 않는다", () => {
  assert.equal(activeMention("user@example.com", 16), null);
  assert.equal(activeMention("@[취약점 #1 X] 선택됨", 12), null);
  assert.deepEqual(activeMention("확인@취약점 뒤의 텍스트", 6), { start: 2, end: 6, query: "취약점" });
  assert.equal(activeMention("@취약점\n다음 줄", 8), null);
});

test("한국어 분류·별칭과 IP·키워드 검색", () => {
  assert.equal(mentionSearch("").categories.length, 9);
  assert.equal(mentionSearch("취").categories[0].kind, "finding");
  assert.equal(mentionSearch("취약점").kind, "finding");
  assert.equal(mentionSearch("취약점SQL 주입").query, "SQL 주입");
  assert.equal(mentionSearch("ip 192.0.2.1").kind, "ip");
  assert.equal(mentionSearch("엔드포인트 GET /api").query, "GET /api");
  assert.equal(mentionSearch("acme.com").kind, "");
});

test("tokens roundtrip labels and removing one reference preserves its neighbors", () => {
  const first = mentionToken({ kind: "finding", id: 12, label: "제목[1]\n설명" });
  const second = mentionToken({ kind: "ip", id: 13, label: "192.0.2.1" });
  const value = `${first} 및 ${second} 분석`;
  const selected = selectedMentions(value);
  assert.equal(selected.length, 2);
  assert.equal(selected[0].label, "취약점 #12 · 제목（1） 설명");
  const next = value.slice(0, selected[0].start) + value.slice(selected[0].start + selected[0].token.length);
  assert.equal(selectedMentions(next)[0].token, second);
});
