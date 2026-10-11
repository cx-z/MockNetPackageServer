package template

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// =============================================================================
// Parenthesized Syntax Tests
// =============================================================================

func TestRandomIntParenthesized(t *testing.T) {
	engine := New()

	tests := []struct {
		name     string
		template string
		min      int
		max      int
	}{
		{"basic range", "{{random.int(1, 100)}}", 1, 100},
		{"tight range", "{{random.int(5, 5)}}", 5, 5},
		{"zero range", "{{random.int(0, 0)}}", 0, 0},
		{"large range", "{{random.int(0, 1000000)}}", 0, 1000000},
		{"with spaces", "{{ random.int(1, 50) }}", 1, 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Process(tt.template, nil)
			if err != nil {
				t.Fatalf("Process() error = %v", err)
			}

			n, err := strconv.Atoi(result)
			if err != nil {
				t.Fatalf("result should be integer, got %q: %v", result, err)
			}
			if n < tt.min || n > tt.max {
				t.Errorf("result %d not in range [%d, %d]", n, tt.min, tt.max)
			}
		})
	}
}

func TestRandomFloatParenthesized(t *testing.T) {
	engine := New()

	t.Run("basic range", func(t *testing.T) {
		result, err := engine.Process("{{random.float(1.0, 10.0)}}", nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		f, err := strconv.ParseFloat(result, 64)
		if err != nil {
			t.Fatalf("result should be float, got %q: %v", result, err)
		}
		if f < 1.0 || f > 10.0 {
			t.Errorf("result %f not in range [1.0, 10.0]", f)
		}
	})

	t.Run("with precision", func(t *testing.T) {
		result, err := engine.Process("{{random.float(0.0, 100.0, 2)}}", nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		f, err := strconv.ParseFloat(result, 64)
		if err != nil {
			t.Fatalf("result should be float, got %q: %v", result, err)
		}
		if f < 0.0 || f > 100.0 {
			t.Errorf("result %f not in range [0.0, 100.0]", f)
		}
		// Check precision: should have at most 2 decimal places
		parts := strings.Split(result, ".")
		if len(parts) == 2 && len(parts[1]) > 2 {
			t.Errorf("expected at most 2 decimal places, got %q", result)
		}
	})

	t.Run("no args returns 0-1", func(t *testing.T) {
		result, err := engine.Process("{{random.float}}", nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		f, err := strconv.ParseFloat(result, 64)
		if err != nil {
			t.Fatalf("result should be float, got %q: %v", result, err)
		}
		if f < 0.0 || f >= 1.0 {
			t.Errorf("result %f not in range [0.0, 1.0)", f)
		}
	})
}

func TestParenthesizedUpperLower(t *testing.T) {
	engine := New()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{"upper parenthesized", `{{upper("hello")}}`, "HELLO"},
		{"lower parenthesized", `{{lower("WORLD")}}`, "world"},
		{"upper unquoted", `{{upper(hello)}}`, "HELLO"},
		{"lower unquoted", `{{lower(WORLD)}}`, "world"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Process(tt.template, nil)
			if err != nil {
				t.Fatalf("Process() error = %v", err)
			}
			if result != tt.expected {
				t.Errorf("Process() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestParenthesizedDefault(t *testing.T) {
	engine := New()

	t.Run("default with empty value", func(t *testing.T) {
		// Without context, request.query.missing resolves to ""
		result, err := engine.Process(`{{default(request.query.missing, "fallback")}}`, nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result != "fallback" {
			t.Errorf("Process() = %q, want %q", result, "fallback")
		}
	})
}

// =============================================================================
// Random String Tests
// =============================================================================

func TestRandomStringParenthesized(t *testing.T) {
	engine := New()

	t.Run("default length 10", func(t *testing.T) {
		result, err := engine.Process("{{random.string}}", nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if len(result) != 10 {
			t.Errorf("random.string should return 10 chars, got %d: %q", len(result), result)
		}
		matched, _ := regexp.MatchString(`^[a-zA-Z0-9]+$`, result)
		if !matched {
			t.Errorf("random.string should be alphanumeric, got %q", result)
		}
	})

	t.Run("custom length", func(t *testing.T) {
		result, err := engine.Process("{{random.string(20)}}", nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if len(result) != 20 {
			t.Errorf("random.string(20) should return 20 chars, got %d: %q", len(result), result)
		}
		matched, _ := regexp.MatchString(`^[a-zA-Z0-9]+$`, result)
		if !matched {
			t.Errorf("random.string(20) should be alphanumeric, got %q", result)
		}
	})

	t.Run("length 1", func(t *testing.T) {
		result, err := engine.Process("{{random.string(1)}}", nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if len(result) != 1 {
			t.Errorf("random.string(1) should return 1 char, got %d: %q", len(result), result)
		}
	})

	t.Run("large length", func(t *testing.T) {
		result, err := engine.Process("{{random.string(100)}}", nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if len(result) != 100 {
			t.Errorf("random.string(100) should return 100 chars, got %d", len(result))
		}
	})

	t.Run("with spaces", func(t *testing.T) {
		result, err := engine.Process("{{ random.string(5) }}", nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if len(result) != 5 {
			t.Errorf("random.string(5) with spaces should return 5 chars, got %d: %q", len(result), result)
		}
	})

	t.Run("produces different values", func(t *testing.T) {
		results := make(map[string]bool)
		for i := 0; i < 20; i++ {
			result, _ := engine.Process("{{random.string(10)}}", nil)
			results[result] = true
		}
		// With 62^10 possibilities, 20 calls should produce at least 2 unique values
		if len(results) < 2 {
			t.Error("random.string should produce different values across calls")
		}
	})
}

// =============================================================================
// Random Element Tests
// =============================================================================

func TestRandomElement(t *testing.T) {
	engine := New()

	tests := []struct {
		name     string
		template string
		checkFn  func(string) bool
	}{
		{"random.Element picks from list", `{{random.Element "active" "inactive" "pending"}}`,
			func(r string) bool { return r == "active" || r == "inactive" || r == "pending" }},
		{"random.element case insensitive", `{{random.element "a" "b" "c"}}`,
			func(r string) bool { return r == "a" || r == "b" || r == "c" }},
		{"random.Element single item", `{{random.Element "only"}}`,
			func(r string) bool { return r == "only" }},
		{"random.Element unquoted", `{{random.Element active inactive pending}}`,
			func(r string) bool { return r == "active" || r == "inactive" || r == "pending" }},
		{"RANDOM.ELEMENT allcaps", `{{RANDOM.ELEMENT "x" "y" "z"}}`,
			func(r string) bool { return r == "x" || r == "y" || r == "z" }},
		{"randomElement camelCase", `{{randomElement "foo" "bar"}}`,
			func(r string) bool { return r == "foo" || r == "bar" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Process(tt.template, nil)
			if err != nil {
				t.Fatalf("Process() error = %v", err)
			}
			if !tt.checkFn(result) {
				t.Errorf("Process(%q) = %q, not in expected set", tt.template, result)
			}
		})
	}

	t.Run("produces variation", func(t *testing.T) {
		seen := make(map[string]bool)
		for i := 0; i < 50; i++ {
			result, _ := engine.Process(`{{random.Element "a" "b" "c"}}`, nil)
			seen[result] = true
		}
		if len(seen) < 2 {
			t.Errorf("random.Element should produce variation across 50 calls, got %d unique values", len(seen))
		}
	})
}

func TestRandomElementParenthesized(t *testing.T) {
	engine := New()

	tests := []struct {
		name     string
		template string
		checkFn  func(string) bool
	}{
		{"parenthesized quoted", `{{random.element("active", "inactive", "pending")}}`,
			func(r string) bool { return r == "active" || r == "inactive" || r == "pending" }},
		{"parenthesized single", `{{random.Element("only")}}`,
			func(r string) bool { return r == "only" }},
		{"parenthesized unquoted", `{{randomElement(foo, bar, baz)}}`,
			func(r string) bool { return r == "foo" || r == "bar" || r == "baz" }},
		{"parenthesized mixed case", `{{Random.Element("x", "y")}}`,
			func(r string) bool { return r == "x" || r == "y" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Process(tt.template, nil)
			if err != nil {
				t.Fatalf("Process() error = %v", err)
			}
			if !tt.checkFn(result) {
				t.Errorf("Process(%q) = %q, not in expected set", tt.template, result)
			}
		})
	}
}

// =============================================================================
// Default Function Extended Tests
// =============================================================================

func TestDefaultWithPayloadFallback(t *testing.T) {
	engine := New()

	t.Run("default with missing payload uses fallback", func(t *testing.T) {
		ctx := NewContextFromMap(nil, nil)
		result, err := engine.Process(`{{default(request.body.missing, "N/A")}}`, ctx)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result != "N/A" {
			t.Errorf("Process() = %q, want %q", result, "N/A")
		}
	})

	t.Run("default with present payload uses value", func(t *testing.T) {
		payload := map[string]any{"temp": "72.5"}
		ctx := NewContextFromMap(payload, nil)
		result, err := engine.Process(`{{default(request.body.temp, "N/A")}}`, ctx)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result != "72.5" {
			t.Errorf("Process() = %q, want %q", result, "72.5")
		}
	})

	t.Run("default with missing header uses fallback parenthesized", func(t *testing.T) {
		result, err := engine.Process(`{{default(request.header.X-Custom, "fallback-value")}}`, nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result != "fallback-value" {
			t.Errorf("Process() = %q, want %q", result, "fallback-value")
		}
	})

}

// =============================================================================
// Sequence with Default Engine Tests
// =============================================================================

func TestSequenceWithDefaultEngine(t *testing.T) {
	t.Run("sequence works in HTTP context", func(t *testing.T) {
		engine := New()
		result1, _ := engine.Process(`{{sequence("http_counter")}}`, nil)
		result2, _ := engine.Process(`{{sequence("http_counter")}}`, nil)
		result3, _ := engine.Process(`{{sequence("http_counter")}}`, nil)
		if result1 != "1" || result2 != "2" || result3 != "3" {
			t.Errorf("sequence should auto-increment: got %q, %q, %q", result1, result2, result3)
		}
	})

	t.Run("sequence with custom start in HTTP context", func(t *testing.T) {
		engine := New()
		result1, _ := engine.Process(`{{sequence("counter", 100)}}`, nil)
		result2, _ := engine.Process(`{{sequence("counter", 100)}}`, nil)
		if result1 != "100" || result2 != "101" {
			t.Errorf("sequence with start=100 should give 100, 101: got %q, %q", result1, result2)
		}
	})
}

// =============================================================================
// Sequence Tests
// =============================================================================

func TestSequenceBasic(t *testing.T) {
	store := NewSequenceStore()
	engine := NewWithSequences(store)

	t.Run("auto-increment from 1", func(t *testing.T) {
		for i := int64(1); i <= 5; i++ {
			result, err := engine.Process(`{{sequence("counter")}}`, nil)
			if err != nil {
				t.Fatalf("Process() error = %v", err)
			}
			expected := strconv.FormatInt(i, 10)
			if result != expected {
				t.Errorf("iteration %d: got %q, want %q", i, result, expected)
			}
		}
	})

	t.Run("custom start value", func(t *testing.T) {
		result, err := engine.Process(`{{sequence("custom", 100)}}`, nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result != "100" {
			t.Errorf("first value should be 100, got %q", result)
		}

		result, err = engine.Process(`{{sequence("custom", 100)}}`, nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result != "101" {
			t.Errorf("second value should be 101, got %q", result)
		}
	})

	t.Run("independent sequences", func(t *testing.T) {
		store2 := NewSequenceStore()
		eng2 := NewWithSequences(store2)

		eng2.Process(`{{sequence("a")}}`, nil) // a=1
		eng2.Process(`{{sequence("a")}}`, nil) // a=2
		eng2.Process(`{{sequence("b")}}`, nil) // b=1

		resultA, _ := eng2.Process(`{{sequence("a")}}`, nil)
		resultB, _ := eng2.Process(`{{sequence("b")}}`, nil)

		if resultA != "3" {
			t.Errorf("sequence 'a' should be 3, got %q", resultA)
		}
		if resultB != "2" {
			t.Errorf("sequence 'b' should be 2, got %q", resultB)
		}
	})

	t.Run("default engine has sequences", func(t *testing.T) {
		eng := New()
		result, err := eng.Process(`{{sequence("test")}}`, nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result != "1" {
			t.Errorf("sequence with default engine should start at 1, got %q", result)
		}
		result, err = eng.Process(`{{sequence("test")}}`, nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result != "2" {
			t.Errorf("second call should return 2, got %q", result)
		}
	})
}

// =============================================================================
// SequenceStore Tests
// =============================================================================

func TestSequenceStoreNext(t *testing.T) {
	store := NewSequenceStore()

	// First call starts at the given start value
	if v := store.Next("test", 1); v != 1 {
		t.Errorf("first Next = %d, want 1", v)
	}
	if v := store.Next("test", 1); v != 2 {
		t.Errorf("second Next = %d, want 2", v)
	}
	if v := store.Next("test", 1); v != 3 {
		t.Errorf("third Next = %d, want 3", v)
	}
}

func TestSequenceStoreReset(t *testing.T) {
	store := NewSequenceStore()

	store.Next("reset-test", 10) // 10
	store.Next("reset-test", 10) // 11
	store.Reset("reset-test")

	// After reset, should start from the given start value again
	if v := store.Next("reset-test", 10); v != 10 {
		t.Errorf("after reset Next = %d, want 10", v)
	}
}

func TestSequenceStoreCurrent(t *testing.T) {
	store := NewSequenceStore()

	// Non-existent sequence returns 0
	if v := store.Current("missing"); v != 0 {
		t.Errorf("Current of missing = %d, want 0", v)
	}

	store.Next("exist", 5)
	// After Next(5), current should be 6 (value was 5, then incremented)
	if v := store.Current("exist"); v != 6 {
		t.Errorf("Current after Next(5) = %d, want 6", v)
	}
}

func TestSequenceStoreConcurrency(t *testing.T) {
	store := NewSequenceStore()
	var wg sync.WaitGroup
	goroutines := 100
	iterations := 100

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				store.Next("concurrent", 1)
			}
		}()
	}
	wg.Wait()

	// After 100 goroutines × 100 iterations, current should be 10001 (started at 1, incremented 10000 times)
	expected := int64(goroutines*iterations) + 1
	if v := store.Current("concurrent"); v != expected {
		t.Errorf("concurrent Current = %d, want %d", v, expected)
	}
}

// =============================================================================
// Faker Tests
// =============================================================================

func TestFakerVariables(t *testing.T) {
	engine := New()

	fakerTypes := []struct {
		name    string
		pattern string // regex to validate output format
	}{
		{"uuid", `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`},
		{"boolean", `^(true|false)$`},
		{"name", `.+`},
		{"firstName", `.+`},
		{"lastName", `.+`},
		{"email", `.+@.+\..+`},
		{"address", `.+`},
		{"phone", `^\+1-\d{3}-\d{3}-\d{4}$`},
		{"company", `.+`},
		{"word", `^\w+$`},
		{"sentence", `.+\.$`},
		{"city", `.+`},
		{"state", `.+`},
		{"zipCode", `^\d{5}$`},
		{"country", `.+`},
		{"username", `^[a-z]+_[a-z]+\d+$`},
		{"paragraph", `.+\..+\.`},
	}

	for _, ft := range fakerTypes {
		t.Run("faker."+ft.name, func(t *testing.T) {
			result, err := engine.Process("{{faker."+ft.name+"}}", nil)
			if err != nil {
				t.Fatalf("Process() error = %v", err)
			}
			if result == "" {
				t.Error("faker should produce non-empty output")
			}
			if matched, _ := regexp.MatchString(ft.pattern, result); !matched {
				t.Errorf("faker.%s = %q doesn't match pattern %q", ft.name, result, ft.pattern)
			}
		})
	}

	t.Run("faker.unknown returns empty", func(t *testing.T) {
		result, err := engine.Process("{{faker.nonexistent}}", nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result != "" {
			t.Errorf("unknown faker type should return empty, got %q", result)
		}
	})
}

// =============================================================================
// ProcessInterface Tests
// =============================================================================

func TestProcessInterface(t *testing.T) {
	engine := New()
	ctx := NewContextFromMap(map[string]any{"temp": 72.5}, nil)

	t.Run("string values are processed", func(t *testing.T) {
		data := "Temp is {{request.body.temp}}"
		result := engine.ProcessInterface(data, ctx)
		if result != "Temp is 72.5" {
			t.Errorf("ProcessInterface() = %q, want %q", result, "Temp is 72.5")
		}
	})

	t.Run("map values are recursively processed", func(t *testing.T) {
		data := map[string]interface{}{
			"temp": "{{request.body.temp}}",
			"num":  42,
		}
		result := engine.ProcessInterface(data, ctx).(map[string]interface{})
		if result["temp"] != "72.5" {
			t.Errorf("temp = %v, want %v", result["temp"], "72.5")
		}
		if result["num"] != 42 {
			t.Errorf("num = %v, want 42", result["num"])
		}
	})

	t.Run("slice values are recursively processed", func(t *testing.T) {
		data := []interface{}{"{{request.body.temp}}", "literal", 123}
		result := engine.ProcessInterface(data, ctx).([]interface{})
		if result[0] != "72.5" {
			t.Errorf("[0] = %v, want %v", result[0], "72.5")
		}
		if result[1] != "literal" {
			t.Errorf("[1] = %q, want %q", result[1], "literal")
		}
		if result[2] != 123 {
			t.Errorf("[2] = %v, want 123", result[2])
		}
	})

	t.Run("nil returns nil", func(t *testing.T) {
		result := engine.ProcessInterface(nil, ctx)
		if result != nil {
			t.Errorf("nil should return nil, got %v", result)
		}
	})

	t.Run("non-string non-collection returned unchanged", func(t *testing.T) {
		result := engine.ProcessInterface(42, ctx)
		if result != 42 {
			t.Errorf("int should be unchanged, got %v", result)
		}
	})
}

// =============================================================================
// formatValue Tests
// =============================================================================

func TestFormatValue(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected string
	}{
		{"string", "hello", "hello"},
		{"float64", 3.14, "3.14"},
		{"float64 whole", 42.0, "42"},
		{"int", 42, "42"},
		{"int64", int64(123), "123"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"other", []int{1, 2}, "[1 2]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatValue(tt.input)
			if result != tt.expected {
				t.Errorf("formatValue(%v) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// splitFuncArgs Tests
// =============================================================================

func TestSplitFuncArgs(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{"simple", "a, b, c", []string{"a", "b", "c"}},
		{"quoted commas", `"hello, world", b`, []string{`"hello, world"`, "b"}},
		{"single arg", "value", []string{"value"}},
		{"quoted strings", `"first", "second"`, []string{`"first"`, `"second"`}},
		{"mixed", `request.query.x, "fallback"`, []string{"request.query.x", `"fallback"`}},
		{"single quotes", `'hello, world', b`, []string{`'hello, world'`, "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitFuncArgs(tt.input)
			if len(result) != len(tt.expected) {
				t.Fatalf("splitFuncArgs(%q) = %v (len %d), want %v (len %d)",
					tt.input, result, len(result), tt.expected, len(tt.expected))
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("splitFuncArgs(%q)[%d] = %q, want %q", tt.input, i, result[i], tt.expected[i])
				}
			}
		})
	}
}

func TestBuiltinVariables(t *testing.T) {
	engine := New()

	builtins := []struct {
		name     string
		template string
		checkFn  func(string) bool
	}{
		{"now", "{{now}}", func(s string) bool { return strings.Contains(s, "T") }},
		{"uuid", "{{uuid}}", func(s string) bool { return len(s) == 36 && strings.Count(s, "-") == 4 }},
		{"uuid.short", "{{uuid.short}}", func(s string) bool { return len(s) == 8 }},
		{"timestamp", "{{timestamp}}", func(s string) bool { _, err := strconv.ParseInt(s, 10, 64); return err == nil }},
		{"random", "{{random}}", func(s string) bool { return len(s) == 8 }}, // hex encoded 4 bytes
	}

	for _, b := range builtins {
		t.Run(b.name, func(t *testing.T) {
			result, err := engine.Process(b.template, nil)
			if err != nil {
				t.Fatalf("Process() error = %v", err)
			}
			if !b.checkFn(result) {
				t.Errorf("check failed for %q, got %q", b.name, result)
			}
		})
	}
}

func TestUnknownExpression(t *testing.T) {
	engine := New()

	result, err := engine.Process("{{unknown.expression}}", nil)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if result != "" {
		t.Errorf("unknown expression should return empty, got %q", result)
	}
}

func TestMixedTemplate(t *testing.T) {
	engine := New()

	// Template with literal text + multiple expressions
	result, err := engine.Process("Hello {{uuid.short}}, today is {{now}}", nil)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if !strings.HasPrefix(result, "Hello ") {
		t.Errorf("result should start with 'Hello ', got %q", result)
	}
	if !strings.Contains(result, ", today is ") {
		t.Errorf("result should contain ', today is ', got %q", result)
	}
}

func TestEmptyTemplate(t *testing.T) {
	engine := New()

	result, err := engine.Process("", nil)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if result != "" {
		t.Errorf("empty template should return empty, got %q", result)
	}
}

func TestTemplateWithNoExpressions(t *testing.T) {
	engine := New()

	result, err := engine.Process("plain text with no expressions", nil)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if result != "plain text with no expressions" {
		t.Errorf("plain text should be unchanged, got %q", result)
	}
}

func TestWhitespaceInExpressions(t *testing.T) {
	engine := New()

	// Extra whitespace inside braces should be handled
	tests := []struct {
		name     string
		template string
	}{
		{"leading space", "{{ uuid.short }}"},
		{"extra spaces", "{{  uuid.short  }}"},
		{"tab", "{{\tuuid.short\t}}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Process(tt.template, nil)
			if err != nil {
				t.Fatalf("Process() error = %v", err)
			}
			if len(result) != 8 {
				t.Errorf("should still resolve uuid.short, got %q (len=%d)", result, len(result))
			}
		})
	}
}

func TestParseStringArg(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`"hello"`, "hello"},
		{`'hello'`, "hello"},
		{`hello`, "hello"},
		{`"  spaced  "`, "  spaced  "},
		{`""`, ""},
		{`''`, ""},
		{`a`, "a"},
		{`  "trimmed"  `, "trimmed"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseStringArg(tt.input)
			if result != tt.expected {
				t.Errorf("parseStringArg(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestCaseInsensitiveTemplates verifies that template expressions are
// case-insensitive for function names — {{random.Int}}, {{Random.INT}},
// {{FAKER.Name}}, {{UUID}}, {{NOW}} should all work.
func TestCaseInsensitiveTemplates(t *testing.T) {
	engine := New()

	tests := []struct {
		name     string
		template string
		validate func(t *testing.T, result string)
	}{
		// random.int — all case variants
		{"random.int lowercase", "{{random.int 1 50}}", func(t *testing.T, r string) {
			n, err := strconv.Atoi(r)
			if err != nil || n < 1 || n > 50 {
				t.Errorf("got %q, want int in [1,50]", r)
			}
		}},
		{"random.Int mixed", "{{random.Int 1 50}}", func(t *testing.T, r string) {
			n, err := strconv.Atoi(r)
			if err != nil || n < 1 || n > 50 {
				t.Errorf("got %q, want int in [1,50]", r)
			}
		}},
		{"Random.INT upper", "{{Random.INT 1 50}}", func(t *testing.T, r string) {
			n, err := strconv.Atoi(r)
			if err != nil || n < 1 || n > 50 {
				t.Errorf("got %q, want int in [1,50]", r)
			}
		}},
		{"randomInt camelCase", "{{randomInt 1 50}}", func(t *testing.T, r string) {
			n, err := strconv.Atoi(r)
			if err != nil || n < 1 || n > 50 {
				t.Errorf("got %q, want int in [1,50]", r)
			}
		}},
		{"RANDOMINT allcaps", "{{RANDOMINT 1 50}}", func(t *testing.T, r string) {
			n, err := strconv.Atoi(r)
			if err != nil || n < 1 || n > 50 {
				t.Errorf("got %q, want int in [1,50]", r)
			}
		}},

		// random.int parenthesized — case variants
		{"random.int(1,50) lower", "{{random.int(1, 50)}}", func(t *testing.T, r string) {
			n, err := strconv.Atoi(r)
			if err != nil || n < 1 || n > 50 {
				t.Errorf("got %q, want int in [1,50]", r)
			}
		}},
		{"Random.Int(1,50) mixed", "{{Random.Int(1, 50)}}", func(t *testing.T, r string) {
			n, err := strconv.Atoi(r)
			if err != nil || n < 1 || n > 50 {
				t.Errorf("got %q, want int in [1,50]", r)
			}
		}},

		// random.float — case variants
		{"random.float lower", "{{random.float}}", func(t *testing.T, r string) {
			f, err := strconv.ParseFloat(r, 64)
			if err != nil || f < 0 || f > 1 {
				t.Errorf("got %q, want float in [0,1]", r)
			}
		}},
		{"random.Float mixed", "{{random.Float}}", func(t *testing.T, r string) {
			f, err := strconv.ParseFloat(r, 64)
			if err != nil || f < 0 || f > 1 {
				t.Errorf("got %q, want float in [0,1]", r)
			}
		}},
		{"RANDOM.FLOAT upper", "{{RANDOM.FLOAT}}", func(t *testing.T, r string) {
			f, err := strconv.ParseFloat(r, 64)
			if err != nil || f < 0 || f > 1 {
				t.Errorf("got %q, want float in [0,1]", r)
			}
		}},
		{"randomFloat(1.0, 5.0) mixed", "{{RandomFloat(1.0, 5.0)}}", func(t *testing.T, r string) {
			f, err := strconv.ParseFloat(r, 64)
			if err != nil || f < 1.0 || f > 5.0 {
				t.Errorf("got %q, want float in [1,5]", r)
			}
		}},

		// random.string — case variants
		{"random.string lower", "{{random.string 8}}", func(t *testing.T, r string) {
			if len(r) != 8 {
				t.Errorf("got %q (len %d), want len 8", r, len(r))
			}
		}},
		{"random.String mixed", "{{random.String 8}}", func(t *testing.T, r string) {
			if len(r) != 8 {
				t.Errorf("got %q (len %d), want len 8", r, len(r))
			}
		}},
		{"RANDOM.STRING upper", "{{RANDOM.STRING 8}}", func(t *testing.T, r string) {
			if len(r) != 8 {
				t.Errorf("got %q (len %d), want len 8", r, len(r))
			}
		}},
		{"Random.String(12) parens", "{{Random.String(12)}}", func(t *testing.T, r string) {
			if len(r) != 12 {
				t.Errorf("got %q (len %d), want len 12", r, len(r))
			}
		}},

		// Built-in variables — case variants
		{"UUID upper", "{{UUID}}", func(t *testing.T, r string) {
			if len(r) != 36 || r[8] != '-' {
				t.Errorf("got %q, want UUID format", r)
			}
		}},
		{"Now mixed", "{{Now}}", func(t *testing.T, r string) {
			if len(r) < 10 {
				t.Errorf("got %q, want RFC3339 timestamp", r)
			}
		}},
		{"TIMESTAMP upper", "{{TIMESTAMP}}", func(t *testing.T, r string) {
			_, err := strconv.ParseInt(r, 10, 64)
			if err != nil {
				t.Errorf("got %q, want unix timestamp: %v", r, err)
			}
		}},

		// faker — case variants (faker.Name already works, test Faker. prefix)
		{"faker.name lower", "{{faker.name}}", func(t *testing.T, r string) {
			if r == "" {
				t.Error("got empty, want a name")
			}
		}},
		{"faker.Name camel", "{{faker.Name}}", func(t *testing.T, r string) {
			if r == "" {
				t.Error("got empty, want a name")
			}
		}},
		{"Faker.name prefix cap", "{{Faker.name}}", func(t *testing.T, r string) {
			if r == "" {
				t.Error("got empty, want a name")
			}
		}},
		{"FAKER.EMAIL allcaps", "{{FAKER.EMAIL}}", func(t *testing.T, r string) {
			if r == "" || !strings.Contains(r, "@") {
				t.Errorf("got %q, want email with @", r)
			}
		}},

		// upper/lower/default functions — case variants
		{"Upper(uuid) cap", "{{Upper(uuid)}}", func(t *testing.T, r string) {
			if r != strings.ToUpper(r) {
				t.Errorf("got %q, want uppercase", r)
			}
		}},
		{"DEFAULT cap", "{{Default(\"\", \"fallback\")}}", func(t *testing.T, r string) {
			if r != "fallback" {
				t.Errorf("got %q, want %q", r, "fallback")
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Process(tt.template, nil)
			if err != nil {
				t.Fatalf("Process(%q) error = %v", tt.template, err)
			}
			tt.validate(t, result)
		})
	}
}

// =============================================================================
// Bug Fix: upper/lower with faker and random in resolveValue
// =============================================================================

func TestUpperLowerWithFaker(t *testing.T) {
	engine := New()

	t.Run("upper(faker.name) resolves faker", func(t *testing.T) {
		result, err := engine.Process(`{{upper(faker.name)}}`, nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result == "" {
			t.Fatal("expected non-empty result")
		}
		if result != strings.ToUpper(result) {
			t.Errorf("expected all uppercase, got %q", result)
		}
		// Should NOT be literal "FAKER.NAME"
		if result == "FAKER.NAME" {
			t.Errorf("got literal %q, expected resolved faker name", result)
		}
	})

	t.Run("lower(faker.email) resolves faker", func(t *testing.T) {
		result, err := engine.Process(`{{lower(faker.email)}}`, nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result == "" {
			t.Fatal("expected non-empty result")
		}
		if result != strings.ToLower(result) {
			t.Errorf("expected all lowercase, got %q", result)
		}
		if !strings.Contains(result, "@") {
			t.Errorf("expected email with @, got %q", result)
		}
	})

	t.Run("upper(random.int) resolves random", func(t *testing.T) {
		result, err := engine.Process(`{{upper(random.int)}}`, nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result == "" {
			t.Fatal("expected non-empty result")
		}
		// Should be a number, not "RANDOM.INT"
		if result == "RANDOM.INT" {
			t.Errorf("got literal %q, expected resolved random int", result)
		}
		_, err = strconv.Atoi(result)
		if err != nil {
			t.Errorf("expected numeric result, got %q", result)
		}
	})

	t.Run("upper faker.name space-separated resolves faker", func(t *testing.T) {
		result, err := engine.Process(`{{upper faker.name}}`, nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		if result == "" {
			t.Fatal("expected non-empty result")
		}
		if result != strings.ToUpper(result) {
			t.Errorf("expected all uppercase, got %q", result)
		}
		if result == "FAKER.NAME" {
			t.Errorf("got literal %q, expected resolved faker name", result)
		}
	})

	t.Run("default with faker uses faker value", func(t *testing.T) {
		result, err := engine.Process(`{{default(faker.name, "fallback")}}`, nil)
		if err != nil {
			t.Fatalf("Process() error = %v", err)
		}
		// faker.name always returns a non-empty string, so fallback should not be used
		if result == "fallback" {
			t.Errorf("expected faker name, got fallback")
		}
		if result == "" {
			t.Error("expected non-empty result")
		}
	})
}

// =============================================================================
// Bug Fix: Array indexing in evaluateBodyField
// =============================================================================

func TestBodyFieldArrayIndexing(t *testing.T) {
	engine := New()

	body := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"name": "Alice", "id": "1"},
			map[string]interface{}{"name": "Bob", "id": "2"},
			map[string]interface{}{"name": "Charlie", "id": "3"},
		},
		"nested": map[string]interface{}{
			"list": []interface{}{"a", "b", "c"},
		},
	}

	ctx := &Context{
		Request: RequestContext{
			Body: body,
		},
	}

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{"first element name", "{{request.body.items.0.name}}", "Alice"},
		{"second element name", "{{request.body.items.1.name}}", "Bob"},
		{"third element id", "{{request.body.items.2.id}}", "3"},
		{"out of bounds", "{{request.body.items.99.name}}", ""},
		{"negative index", "{{request.body.items.-1.name}}", ""},
		{"non-numeric index", "{{request.body.items.abc.name}}", ""},
		{"nested array element", "{{request.body.nested.list.0}}", "a"},
		{"nested array element 2", "{{request.body.nested.list.2}}", "c"},
		{"nested array out of bounds", "{{request.body.nested.list.5}}", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Process(tt.template, ctx)
			if err != nil {
				t.Fatalf("Process() error = %v", err)
			}
			if result != tt.expected {
				t.Errorf("Process(%q) = %q, want %q", tt.template, result, tt.expected)
			}
		})
	}
}
