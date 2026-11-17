package proxy

import (
	"sort"
	"strings"
)

type templateEquivalenceToken struct {
	literal  string
	variable string
}

// TemplateDefinitionsEqual reports whether two valid templates generate the
// same proxy endpoint and value space. Placeholder names and other presentation
// details do not affect equality. Fixed single-value enums are treated as
// literals, and a one-character enum covering the random alphabet is treated as
// the equivalent random placeholder.
func TemplateDefinitionsEqual(left, right Template) bool {
	if left.Kind != right.Kind {
		return false
	}
	leftCompiled, err := CompileTemplate(left)
	if err != nil {
		return false
	}
	rightCompiled, err := CompileTemplate(right)
	if err != nil {
		return false
	}
	if !compiledEndpointBaseEqual(leftCompiled, rightCompiled) {
		return false
	}
	leftTokens, ok := canonicalTemplateTokens(leftCompiled)
	if !ok {
		return false
	}
	rightTokens, ok := canonicalTemplateTokens(rightCompiled)
	if !ok {
		return false
	}
	mapping, ok := templateEquivalenceTokensEqual(leftTokens, rightTokens, leftCompiled, rightCompiled)
	if !ok {
		return false
	}
	return compiledGroupsEqual(leftCompiled.groups, rightCompiled.groups, mapping)
}

func compiledEndpointBaseEqual(left, right *CompiledTemplate) bool {
	a, b := left.endpoint, right.endpoint
	return a.Protocol == b.Protocol && a.Host == b.Host && a.Port == b.Port &&
		a.HasUser == b.HasUser && a.Password == b.Password && a.HasPassword == b.HasPassword
}

func canonicalTemplateTokens(compiled *CompiledTemplate) ([]templateEquivalenceToken, bool) {
	tokens := make([]templateEquivalenceToken, 0, len(compiled.tokens))
	appendLiteral := func(literal string) {
		if literal == "" {
			return
		}
		if len(tokens) > 0 && tokens[len(tokens)-1].variable == "" {
			tokens[len(tokens)-1].literal += literal
			return
		}
		tokens = append(tokens, templateEquivalenceToken{literal: literal})
	}
	for _, token := range compiled.tokens {
		if token.name == "" {
			appendLiteral(token.literal)
			continue
		}
		index, exists := compiled.byName[token.name]
		if !exists {
			return nil, false
		}
		variable := compiled.variables[index]
		if variable.kind == PlaceholderEnum && variable.group < 0 && len(variable.values) == 1 {
			appendLiteral(variable.values[0])
			continue
		}
		tokens = append(tokens, templateEquivalenceToken{variable: token.name})
	}
	return tokens, true
}

func templateEquivalenceTokensEqual(left, right []templateEquivalenceToken, leftCompiled, rightCompiled *CompiledTemplate) (map[string]string, bool) {
	if len(left) != len(right) {
		return nil, false
	}
	mapping := make(map[string]string)
	reverse := make(map[string]string)
	for index := range left {
		leftToken, rightToken := left[index], right[index]
		leftLiteral := leftToken.variable == ""
		rightLiteral := rightToken.variable == ""
		if leftLiteral != rightLiteral {
			return nil, false
		}
		if leftLiteral {
			if leftToken.literal != rightToken.literal {
				return nil, false
			}
			continue
		}
		leftVariable, ok := compiledVariableNamed(leftCompiled, leftToken.variable)
		if !ok {
			return nil, false
		}
		rightVariable, ok := compiledVariableNamed(rightCompiled, rightToken.variable)
		if !ok {
			return nil, false
		}
		if mapped, exists := mapping[leftToken.variable]; exists {
			if mapped != rightToken.variable {
				return nil, false
			}
		} else {
			if _, exists := reverse[rightToken.variable]; exists {
				return nil, false
			}
			mapping[leftToken.variable] = rightToken.variable
			reverse[rightToken.variable] = leftToken.variable
		}
		if !templateVariablesEquivalent(leftVariable, rightVariable) {
			return nil, false
		}
	}
	return mapping, true
}

func compiledVariableNamed(compiled *CompiledTemplate, name string) (compiledVariable, bool) {
	index, exists := compiled.byName[name]
	if !exists {
		return compiledVariable{}, false
	}
	return compiled.variables[index], true
}

func templateVariablesEquivalent(left, right compiledVariable) bool {
	if (left.group >= 0) != (right.group >= 0) {
		return false
	}
	if left.group >= 0 {
		return true
	}
	switch {
	case left.kind == PlaceholderEnum && right.kind == PlaceholderEnum:
		return sameEnumValueSpace(left, right)
	case left.kind == PlaceholderRandom && right.kind == PlaceholderRandom:
		return left.random == right.random
	case left.kind == PlaceholderEnum && right.kind == PlaceholderRandom:
		return enumMatchesRandomAlphabet(left.values, right.random)
	case left.kind == PlaceholderRandom && right.kind == PlaceholderEnum:
		return enumMatchesRandomAlphabet(right.values, left.random)
	case left.kind == PlaceholderReserved && right.kind == PlaceholderReserved:
		return left.name == right.name
	default:
		return false
	}
}

func sameEnumValueSpace(left, right compiledVariable) bool {
	if len(left.values) != len(right.values) {
		return false
	}
	for _, value := range left.values {
		if !right.set[value] {
			return false
		}
	}
	return true
}

func enumMatchesRandomAlphabet(values []string, random RandomSpec) bool {
	if random.Length != 1 {
		return false
	}
	alphabet := randomAlphabet(random.CaseSensitive)
	if len(values) != len(alphabet) {
		return false
	}
	for _, value := range values {
		if _, exists := alphabet[value]; !exists {
			return false
		}
	}
	return true
}

func randomAlphabet(caseSensitive bool) map[string]struct{} {
	characters := "abcdefghijklmnopqrstuvwxyz0123456789"
	if caseSensitive {
		characters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	}
	alphabet := make(map[string]struct{}, len(characters))
	for _, character := range characters {
		alphabet[string(character)] = struct{}{}
	}
	return alphabet
}

func compiledGroupsEqual(left, right []compiledGroup, mapping map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	used := make([]bool, len(right))
	for _, leftGroup := range left {
		matched := -1
		for index, rightGroup := range right {
			if used[index] || !sameGroupMembers(leftGroup, rightGroup, mapping) {
				continue
			}
			if sameGroupRows(leftGroup, rightGroup, mapping) {
				matched = index
				break
			}
		}
		if matched < 0 {
			return false
		}
		used[matched] = true
	}
	return true
}

func sameGroupMembers(left, right compiledGroup, mapping map[string]string) bool {
	if len(left.columns) != len(right.columns) {
		return false
	}
	mapped := make(map[string]bool, len(left.columns))
	for _, column := range left.columns {
		rightColumn, ok := mapping[column]
		if !ok {
			return false
		}
		mapped[rightColumn] = true
	}
	for _, column := range right.columns {
		if !mapped[column] {
			return false
		}
	}
	return true
}

func sameGroupRows(left, right compiledGroup, mapping map[string]string) bool {
	leftRows := groupRowKeys(left, mapping)
	rightRows := groupRowKeys(right, nil)
	if len(leftRows) != len(rightRows) {
		return false
	}
	for key := range leftRows {
		if !rightRows[key] {
			return false
		}
	}
	return true
}

func groupRowKeys(group compiledGroup, mapping map[string]string) map[string]bool {
	rows := make(map[string]bool, len(group.rows))
	for _, row := range group.rows {
		parts := make([]string, len(group.columns))
		for index, column := range group.columns {
			if mapping != nil {
				column = mapping[column]
			}
			parts[index] = column + "\x00" + row[index]
		}
		sort.Strings(parts)
		rows[strings.Join(parts, "\x01")] = true
	}
	return rows
}
