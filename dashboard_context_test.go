package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func dashboardBody(t *testing.T) string {
	t.Helper()
	raw, err := handleManagement(mustJSON(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/editor"}))
	if err != nil {
		t.Fatal(err)
	}
	var got envelope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	var response pluginapi.ManagementResponse
	if err := json.Unmarshal(got.Result, &response); err != nil {
		t.Fatal(err)
	}
	return string(response.Body)
}

// dashboardScript returns the single inline <script> block of the editor.
func dashboardScript(t *testing.T, html string) string {
	t.Helper()
	const open, close = "<script>", "</script>"
	if got := strings.Count(html, open); got != 1 {
		t.Fatalf("script blocks = %d, want exactly 1", got)
	}
	start := strings.Index(html, open) + len(open)
	end := strings.Index(html[start:], close)
	if end < 0 {
		t.Fatal("script block is not closed")
	}
	return html[start : start+end]
}

// TestDashboardInlineScriptBalances is the Go-side syntax gate for the
// dashboard: the page ships as one inline script, so a single stray brace
// silently kills every control on it (this regression happened in review and
// was only caught by node --check). The scanner checks bracket balance while
// honoring string, template and regex literals and comments; scripts/build.sh
// additionally runs node --check over the same extraction as the authoritative
// parser gate.
func TestDashboardInlineScriptBalances(t *testing.T) {
	script := dashboardScript(t, dashboardBody(t))
	if err := checkJSStructure(script); err != nil {
		t.Fatalf("dashboard inline script is broken: %v", err)
	}
}

const (
	stCode = iota
	stString
	stTemplate
	stLine
	stBlock
	stRegex
)

// templateMarker sits on the bracket stack for "${" inside a template
// literal; the matching "}" returns to template mode.
const templateMarker = byte(0)

// checkJSStructure verifies that (), [] and {} stay balanced outside literals
// and comments. Regex literals are recognised by the preceding significant
// token (same heuristic a JS lexer needs, limited to the two positions that
// occur in this dashboard: after a statement separator/punctuation or after
// keywords such as return).
func checkJSStructure(script string) error {
	var stack []byte
	line := 1
	state := stCode
	var quote byte
	for i := 0; i < len(script); i++ {
		c := script[i]
		if c == '\n' {
			line++
			switch state {
			case stLine:
				state = stCode
			case stString, stRegex:
				return fmt.Errorf("unterminated literal ending line %d", line)
			}
		}
		switch state {
		case stLine:
			continue
		case stBlock:
			if c == '/' && i > 0 && script[i-1] == '*' {
				state = stCode
			}
			continue
		case stString:
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				state = stCode
			}
			continue
		case stRegex:
			if c == '\\' {
				i++
				continue
			}
			if c == '[' {
				i++
				for ; i < len(script); i++ {
					if script[i] == '\n' {
						return fmt.Errorf("unterminated character class at line %d", line)
					}
					if script[i] == '\\' {
						i++
						continue
					}
					if script[i] == ']' {
						break
					}
				}
				if i >= len(script) {
					return fmt.Errorf("unterminated character class at line %d", line)
				}
				continue
			}
			if c == '/' {
				state = stCode
			}
			continue
		case stTemplate:
			if c == '\\' {
				i++
				continue
			}
			if c == '`' {
				state = stCode
				continue
			}
			if c == '$' && i+1 < len(script) && script[i+1] == '{' {
				stack = append(stack, templateMarker)
				i++
				state = stCode
			}
			continue
		}
		switch {
		case c == '/' && i+1 < len(script) && script[i+1] == '/':
			state = stLine
			i++
		case c == '/' && i+1 < len(script) && script[i+1] == '*':
			state = stBlock
			i++
		case c == '/' && regexStartsHere(script, i):
			state = stRegex
		case c == '\'' || c == '"':
			state = stString
			quote = c
		case c == '`':
			state = stTemplate
		case c == '(' || c == '[' || c == '{':
			stack = append(stack, c)
		case c == ')' || c == ']' || c == '}':
			if len(stack) == 0 {
				return fmt.Errorf("unexpected '%c' at line %d", c, line)
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if open == templateMarker {
				if c != '}' {
					return fmt.Errorf("template interpolation closed by '%c' at line %d", c, line)
				}
				state = stTemplate
				continue
			}
			if matchingBracket(open) != c {
				return fmt.Errorf("'%c' at line %d closes '%c'", c, line, open)
			}
		}
	}
	if state == stString || state == stRegex || state == stBlock || state == stTemplate {
		return fmt.Errorf("unterminated literal or comment at end of script")
	}
	if len(stack) != 0 {
		return fmt.Errorf("unclosed '%c' (stack depth %d at end)", stack[len(stack)-1], len(stack))
	}
	return nil
}

// regexStartsHere reports whether the '/' at idx can open a regex literal:
// the last significant token before it must not be a value.
func regexStartsHere(script string, idx int) bool {
	j := idx
	for j > 0 && isWordByte(script[j-1]) {
		j--
	}
	switch script[j:idx] {
	case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "case":
		return true
	}
	k := idx - 1
	for k >= 0 && (script[k] == ' ' || script[k] == '\t' || script[k] == '\r') {
		k--
	}
	if k < 0 {
		return true
	}
	if isWordByte(script[k]) {
		return false
	}
	switch script[k] {
	case '(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '~', '^', '<', '>', '\n':
		return true
	}
	return false
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func matchingBracket(open byte) byte {
	switch open {
	case '(':
		return ')'
	case '[':
		return ']'
	case '{':
		return '}'
	}
	return 0
}

// TestCheckJSStructureDetectsBreakage proves the gate itself: the exact
// brace doubling that the string-contains tests missed must fail the scanner.
func TestCheckJSStructureDetectsBreakage(t *testing.T) {
	broken := "function f(){return JSON.stringify({a:1})}}\nfunction g(){const s=/^\\/\\/;}"
	if err := checkJSStructure(broken); err == nil {
		t.Fatal("scanner accepted a stray closing brace")
	}
	ok := "function f(){return JSON.stringify({a:1})}\nfunction g(){const s=/^https?:\\/\\//.test('x')?s:'y';const t='}(';const u='`x${1}y`';if(f()&&/^a$/.test('a'))return}"
	if err := checkJSStructure(ok); err != nil {
		t.Fatalf("scanner rejected valid code: %v", err)
	}
}

func TestDashboardExposesContextLengthEditor(t *testing.T) {
	html := dashboardBody(t)
	for _, expected := range []string{
		"let config={},chains={},strategies={},contextLengths={}",
		"contextLengths=normalizeContextLengths(config.context_lengths,chains)",
		"function normalizeContextLengths(value,modelChains)",
		"function contextLengthsForSave()",
		"function addContextSettings()",
		"addStrategySettings();addContextSettings();",
		"'Context length in tokens (optional)'",
		"CONTEXT_LENGTH_MAX=2147483647",
		"lengthField.input.max=String(CONTEXT_LENGTH_MAX)",
		"num>CONTEXT_LENGTH_MAX",
		",context_lengths:contextLengthsForSave()})",
		",context_lengths:contextLengthsForSave()};",
	} {
		if !strings.Contains(html, expected) {
			t.Errorf("dashboard is missing %q", expected)
		}
	}
	// Regression: the editableState edit once produced one brace too many
	// (})}} ), a SyntaxError that silently killed the whole page.
	if strings.Contains(html, "context_lengths:contextLengthsForSave()})}}") {
		t.Error("editableState gained an extra closing brace")
	}
	// Unknown keys must survive saves through the spread of the loaded config.
	if !strings.Contains(html, "const body={...config,enabled:ui.enabled.checked") {
		t.Error("dashboard save body must preserve unknown config keys via spread")
	}
}

func TestDashboardRenameMovesContextLengthAndDeleteRemovesIt(t *testing.T) {
	html := dashboardBody(t)
	if !strings.Contains(html, "contextLengths[alias]=contextLengths[selected];delete contextLengths[selected]") {
		t.Error("renaming an alias must move its context length entry")
	}
	if !strings.Contains(html, "delete chains[selected];delete strategies[selected];delete contextLengths[selected]") {
		t.Error("deleting an alias must delete its context length entry")
	}
}

// codeToken is one significant code token outside literals and comments.
type codeToken struct {
	kind byte   // 'i' identifier, 'p' punctuation/other, 'k' keyword
	text string // identifier text or single punctuation char
	pos  int    // byte offset into the script
}

// tokenizeJSCode strips literals and comments and returns significant code
// tokens. It reuses the same literal recognition as checkJSStructure.
func tokenizeJSCode(script string) []codeToken {
	var tokens []codeToken
	state := stCode
	var quote byte
	i := 0
	for i < len(script) {
		c := script[i]
		switch state {
		case stLine:
			if c == '\n' {
				state = stCode
			}
			i++
			continue
		case stBlock:
			if c == '/' && i > 0 && script[i-1] == '*' {
				state = stCode
			}
			i++
			continue
		case stString:
			if c == '\\' {
				i += 2
				continue
			}
			if c == quote {
				state = stCode
			}
			i++
			continue
		case stRegex:
			if c == '\\' {
				i += 2
				continue
			}
			if c == '[' {
				i++
				for i < len(script) && script[i] != ']' {
					if script[i] == '\\' {
						i++
					}
					i++
				}
				i++
				continue
			}
			if c == '/' {
				state = stCode
			}
			i++
			continue
		case stTemplate:
			if c == '\\' {
				i += 2
				continue
			}
			if c == '`' {
				state = stCode
				i++
				continue
			}
			if c == '$' && i+1 < len(script) && script[i+1] == '{' {
				state = stCode
				tokens = append(tokens, codeToken{kind: 'p', text: "${", pos: i})
				i += 2
				continue
			}
			i++
			continue
		}
		switch {
		case c == '/' && i+1 < len(script) && script[i+1] == '/':
			state = stLine
			i += 2
		case c == '/' && i+1 < len(script) && script[i+1] == '*':
			state = stBlock
			i += 2
		case c == '/' && regexStartsHere(script, i):
			state = stRegex
			tokens = append(tokens, codeToken{kind: 'p', text: "/", pos: i})
			i++
		case c == '\'' || c == '"':
			state = stString
			quote = c
			tokens = append(tokens, codeToken{kind: 'p', text: "str", pos: i})
			i++
		case c == '`':
			state = stTemplate
			tokens = append(tokens, codeToken{kind: 'p', text: "`", pos: i})
			i++
		case c == '$' && i+1 < len(script) && script[i+1] == '{':
			state = stCode
			tokens = append(tokens, codeToken{kind: 'p', text: "${", pos: i})
			i += 2
		case isWordByte(c):
			start := i
			for i < len(script) && isWordByte(script[i]) {
				i++
			}
			word := script[start:i]
			switch word {
			case "const", "let", "var", "function", "return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "case":
				tokens = append(tokens, codeToken{kind: 'k', text: word, pos: start})
			default:
				tokens = append(tokens, codeToken{kind: 'i', text: word, pos: start})
			}
		default:
			tokens = append(tokens, codeToken{kind: 'p', text: string(c), pos: i})
			i++
		}
	}
	return tokens
}

// checkUndeclaredAssignments is the Go-side (docker-free) gate for the error
// class node --check cannot see: under the page's 'use strict' an assignment
// to an undeclared identifier is syntactically legal yet throws a
// ReferenceError at init, which kills the whole dashboard silently. This
// happened when a constant was appended to a const declaration line behind a
// comment as a bare assignment (CONTEXT_LENGTH_MAX=...). The scanner reports
// every `IDENT =` whose target is not declared anywhere in the file via
// const/let/var (including comma chains), a function or arrow parameter, or a
// for-loop binding, and that is not a member/index target.
func checkUndeclaredAssignments(script string) error {
	tokens := tokenizeJSCode(script)
	declared := map[string]bool{}
	depth := 0
	inDeclChain := false
	chainDepth := 0
	for idx := 0; idx < len(tokens); idx++ {
		tk := tokens[idx]
		switch tk.kind {
		case 'p':
			switch tk.text {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				depth--
				if inDeclChain && depth < chainDepth {
					inDeclChain = false
				}
			case ";":
				inDeclChain = false
			case ",":
				// a comma continues a declaration chain only at chain depth
			case "=":
			}
		case 'k':
			switch tk.text {
			case "const", "let", "var":
				inDeclChain = true
				chainDepth = depth
			case "function":
				if idx+1 < len(tokens) && tokens[idx+1].kind == 'i' {
					declared[tokens[idx+1].text] = true
					idx++
					if idx+1 < len(tokens) && tokens[idx+1].text == "(" {
						idx++
						for idx < len(tokens) && tokens[idx].text != ")" {
							if tokens[idx].kind == 'i' {
								declared[tokens[idx].text] = true
							}
							idx++
						}
					}
				}
			}
		case 'i':
			prev := func(o int) *codeToken {
				for j := idx - 1; j >= 0; j-- {
					if tokens[j].kind == 'p' && (tokens[j].text == " " || tokens[j].text == "\n") {
						continue
					}
					return &tokens[j]
				}
				return nil
			}(0)
			if prev != nil && prev.kind == 'k' {
				switch prev.text {
				case "const", "let", "var":
					declared[tk.text] = true
					continue
				}
			}
			if inDeclChain && depth == chainDepth && prev != nil && prev.text == "," {
				declared[tk.text] = true
				continue
			}
			// arrow parameters: (a,b)=> or a=> ; also default values a=1 inside
			if prev != nil && prev.text == "(" {
				// param list or grouped expr: mark every IDENT before , or )
				for j := idx; j < len(tokens) && tokens[j].text != ")" && tokens[j].text != "{"; j++ {
					if tokens[j].kind == 'i' {
						declared[tokens[j].text] = true
					}
				}
			}
			_ = prev
		}
	}
	lineAt := func(pos int) int {
		return 1 + strings.Count(script[:pos], "\n")
	}
	for idx, tk := range tokens {
		if tk.kind != 'i' || idx+1 >= len(tokens) {
			continue
		}
		eq := tokens[idx+1]
		if eq.kind != 'p' || eq.text != "=" {
			continue
		}
		// not a comparison, not =>
		if idx+2 < len(tokens) && tokens[idx+2].text == ">" {
			continue
		}
		// member/index target: . ] ) before the identifier
		if idx > 0 {
			p := tokens[idx-1]
			if p.text == "." || p.text == "]" {
				continue
			}
		}
		if !declared[tk.text] {
			return fmt.Errorf("bare assignment to undeclared identifier %q at line %d (ReferenceError under use strict)", tk.text, lineAt(tk.pos))
		}
	}
	return nil
}

func TestCheckUndeclaredAssignmentsDetectsBareAssign(t *testing.T) {
	bug := "(()=>{'use strict';\nconst PLUGIN='p',DEFAULT_ATTEMPTS=3;/* mirrors const in go */CONTEXT_LENGTH_MAX=2147483647;\nconst x=CONTEXT_LENGTH_MAX;})()"
	if err := checkUndeclaredAssignments(bug); err == nil {
		t.Fatal("bare assignment after comment in a const line must be rejected")
	} else if !strings.Contains(err.Error(), "CONTEXT_LENGTH_MAX") {
		t.Fatalf("error should name the identifier: %v", err)
	}
	fixed := "(()=>{'use strict';\nconst PLUGIN='p',DEFAULT_ATTEMPTS=3,\n// mirrors const in go\nCONTEXT_LENGTH_MAX=2147483647;\nfunction f(a,b=1){a=2;return b}const o={k:1};o.k=2;})()"
	if err := checkUndeclaredAssignments(fixed); err != nil {
		t.Fatalf("declared-in-chain const must pass: %v", err)
	}
}

func TestDashboardInlineScriptHasNoUndeclaredAssignments(t *testing.T) {
	script := dashboardScript(t, dashboardBody(t))
	if err := checkUndeclaredAssignments(script); err != nil {
		t.Fatalf("dashboard inline script: %v", err)
	}
}

// TestDashboardContextValueReachesInput asserts the load->render->input chain:
// a saved context_lengths value must appear as the input value of the alias
// panel (the whole point of the editor field), and blank aliases show an
// empty field. It drives the dashboard's own functions through the extracted
// script's pure parts.
func TestDashboardContextValueReachesInput(t *testing.T) {
	html := dashboardBody(t)
	for _, expected := range []string{
		// value flows: normalize on load, then String(...) into field(...)
		"const current=contextLengths[selected]===undefined||contextLengths[selected]===null?'':String(contextLengths[selected])",
		"field('Context length in tokens (optional)',current,'e.g. 524288','number')",
		// input handler writes back (empty = unset)
		"if(raw==='')delete contextLengths[selected];else contextLengths[selected]=raw",
	} {
		if !strings.Contains(html, expected) {
			t.Errorf("dashboard must wire the alias value into the input; missing %q", expected)
		}
	}
}
