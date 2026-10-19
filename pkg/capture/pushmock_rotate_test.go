package capture

import (
	"strings"
	"testing"
)

func TestRotateFakePlaceholders_NoPlaceholderPassthrough(t *testing.T) {
	in := `{"msgid":123,"content":"hi"}`
	got := RotateFakePlaceholders(in)
	if got != in {
		t.Fatalf("无占位符应原样返回，got=%s", got)
	}
}

func TestRotateFakePlaceholders_EmptyPassthrough(t *testing.T) {
	if got := RotateFakePlaceholders(""); got != "" {
		t.Fatalf("空串应原样返回，got=%q", got)
	}
}

func TestRotateFakePlaceholders_ReplacesMsgID(t *testing.T) {
	in := `{"msgid":"` + FakePlaceholderMsgID + `","time":"` + FakePlaceholderTime + `"}`
	got := RotateFakePlaceholders(in)
	// 带引号占位符应被替换成裸数字（无引号）
	if strings.Contains(got, FakePlaceholderMsgID) {
		t.Fatalf("占位符应被替换，got=%s", got)
	}
	if strings.Contains(got, `"fake_msgid"`) {
		t.Fatalf("不应保留字符串形态，got=%s", got)
	}
	// 应是合法 JSON：{"msgid":<number>,"time":<number>}
	if !strings.HasPrefix(got, `{"msgid":`) || !strings.Contains(got, `,"time":`) {
		t.Fatalf("替换后形态异常，got=%s", got)
	}
	// 两次调用应产出不同 msgid（每次 send 独立轮换）
	again := RotateFakePlaceholders(in)
	// 提取两次 msgid 值比较
	m1 := between(got, `{"msgid":`, ",")
	m2 := between(again, `{"msgid":`, ",")
	if m1 == m2 {
		t.Fatalf("两次轮换 msgid 应不同，m1=%s m2=%s", m1, m2)
	}
}

func TestRotateFakePlaceholders_SessionID(t *testing.T) {
	in := `{"session_id":"` + FakePlaceholderSessionID + `"}`
	got := RotateFakePlaceholders(in)
	if strings.Contains(got, FakePlaceholderSessionID) {
		t.Fatalf("session_id 占位符应被替换，got=%s", got)
	}
	if !strings.HasPrefix(got, `{"session_id":`) {
		t.Fatalf("应替换为裸数字，got=%s", got)
	}
}

func TestRotateFakePlaceholders_LeavesNonPlaceholderUntouched(t *testing.T) {
	in := `{"msgid":999,"content":"keep me"}`
	got := RotateFakePlaceholders(in)
	if got != in {
		t.Fatalf("无占位符时应原样，got=%s", got)
	}
}

func TestRotateFakePlaceholders_BareFormReplaced(t *testing.T) {
	// 不带引号的裸占位符也应替换成裸数字。
	in := `{"ct":` + FakePlaceholderTime + `,"msgid":` + FakePlaceholderMsgID + `}`
	got := RotateFakePlaceholders(in)
	if strings.Contains(got, "fake_time") || strings.Contains(got, "fake_msgid") {
		t.Fatalf("裸占位符应被替换，got=%s", got)
	}
	// 应是合法 JSON：{"ct":<number>,"msgid":<number>}
	if !strings.HasPrefix(got, `{"ct":`) || !strings.Contains(got, `,"msgid":`) {
		t.Fatalf("裸替换后形态异常，got=%s", got)
	}
}

func TestRotateFakePlaceholders_MixedQuotedAndBare(t *testing.T) {
	// 同一次调用里同时有带引号和裸占位符，都应替换成同一个数字。
	in := `{"msgid":"` + FakePlaceholderMsgID + `","ct":` + FakePlaceholderTime + `}`
	got := RotateFakePlaceholders(in)
	if strings.Contains(got, "fake_msgid") || strings.Contains(got, "fake_time") {
		t.Fatalf("混合占位符都应被替换，got=%s", got)
	}
	// 两次调用 msgid 不同（独立轮换）。
	again := RotateFakePlaceholders(in)
	m1 := between(got, `{"msgid":`, ",")
	m2 := between(again, `{"msgid":`, ",")
	if m1 == m2 {
		t.Fatalf("两次轮换 msgid 应不同，m1=%s m2=%s", m1, m2)
	}
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i+len(start):]
	j := strings.Index(s, end)
	if j < 0 {
		return s
	}
	return s[:j]
}
