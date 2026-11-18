package proxy

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	maxTemplateBytes = 16 << 20
	maxPlaceholders  = 32
	maxGroupRows     = 100000
)

type patternToken struct {
	literal string
	name    string
}

type compiledVariable struct {
	name   string
	kind   PlaceholderKind
	values []string
	set    map[string]bool
	random RandomSpec
	group  int
}

type prefixChoice struct {
	value string
	next  *prefixNode
}

type prefixNode struct {
	choices []prefixChoice
	byValue map[string]*prefixNode
}

type compiledGroup struct {
	id      string
	columns []string
	rows    [][]string
	root    *prefixNode
}

// CompiledTemplate owns immutable tokens, enum ranges, and prefix indexes.
// Callers can safely share it across concurrent bindings and retain old revisions.
type CompiledTemplate struct {
	endpoint               Endpoint
	tokens                 []patternToken
	variables              []compiledVariable
	byName                 map[string]int
	groups                 []compiledGroup
	normalized             Template
	maximumBytes           int
	combinationCount       int64
	combinationCountCapped bool
	hasRandom              bool
	groupRows              int
	warnings               []ValidationIssue
}

func compilePattern(pattern string, encoded bool) ([]patternToken, error) {
	if !utf8.ValidString(pattern) || hasControl(pattern) {
		return nil, invalid("username_pattern_invalid", "username_pattern", "username pattern contains invalid characters")
	}
	var tokens []patternToken
	for len(pattern) > 0 {
		open := strings.IndexAny(pattern, "{}")
		literal := pattern
		if open >= 0 {
			literal = pattern[:open]
		}
		if literal != "" {
			if encoded {
				var err error
				literal, err = url.PathUnescape(literal)
				if err != nil {
					return nil, invalid("credential_encoding_invalid", "username_pattern", "username literal encoding is invalid")
				}
			}
			if !utf8.ValidString(literal) || hasControl(literal) {
				return nil, invalid("username_pattern_invalid", "username_pattern", "username literal contains invalid characters")
			}
			tokens = append(tokens, patternToken{literal: literal})
		}
		if open < 0 {
			break
		}
		if pattern[open] != '{' {
			return nil, invalid("placeholder_invalid", "username_pattern", "username pattern contains an unmatched brace")
		}
		close := strings.IndexByte(pattern[open+1:], '}')
		if close < 0 {
			return nil, invalid("placeholder_invalid", "username_pattern", "username pattern contains an unmatched brace")
		}
		close += open + 1
		name := pattern[open+1 : close]
		if !placeholderNamePattern.MatchString(name) {
			return nil, invalid("placeholder_name_invalid", "username_pattern", "username pattern contains an invalid placeholder name")
		}
		tokens = append(tokens, patternToken{name: name})
		pattern = pattern[close+1:]
	}
	return tokens, nil
}

func enumValues(spec *EnumSpec, path string) ([]string, []ValidationIssue, error) {
	if spec == nil || !utf8.ValidString(spec.Separator) || utf8.RuneCountInString(spec.Separator) < 1 || utf8.RuneCountInString(spec.Separator) > 8 {
		return nil, nil, invalid("enum_separator_invalid", path+".separator", "enum separator must contain 1..8 Unicode characters")
	}
	if spec.Input != "" && len(spec.Values) > 0 {
		return nil, nil, invalid("enum_ranges_conflict", path, "enum input and explicit values cannot both be configured")
	}
	input := spec.Values
	if len(input) == 0 {
		input = strings.Split(spec.Input, spec.Separator)
	}
	values := make([]string, 0, len(input))
	seen := make(map[string]bool, len(input))
	var warnings []ValidationIssue
	for index, value := range input {
		if !utf8.ValidString(value) || hasControl(value) {
			return nil, nil, invalid("enum_value_invalid", fmt.Sprintf("%s.values[%d]", path, index), "enum value contains invalid characters")
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, nil, invalid("enum_value_empty", fmt.Sprintf("%s.values[%d]", path, index), "enum values cannot be empty")
		}
		if seen[value] {
			warnings = append(warnings, ValidationIssue{Code: "enum_value_duplicate", Path: fmt.Sprintf("%s.values[%d]", path, index), Message: "duplicate enum value was removed"})
			continue
		}
		seen[value] = true
		values = append(values, value)
	}
	return values, warnings, nil
}

func CompileTemplate(template Template) (*CompiledTemplate, error) {
	if len(template.Placeholders) > maxPlaceholders {
		return nil, invalid("placeholder_limit", "placeholders", "template supports at most 32 placeholders")
	}
	rowCount := 0
	for _, group := range template.ValueGroups {
		rowCount += len(group.Rows)
		if rowCount > maxGroupRows {
			return nil, invalid("group_row_limit", "value_groups", "template supports at most 100000 relation rows")
		}
	}
	body, err := json.Marshal(template)
	if err != nil || len(body) > maxTemplateBytes {
		return nil, invalid("template_too_large", "template", "template exceeds the 16 MiB configuration size limit")
	}
	if template.UsernamePatternEncoding != "" && template.UsernamePatternEncoding != "literal" && template.UsernamePatternEncoding != "url" {
		return nil, invalid("pattern_encoding_invalid", "username_pattern_encoding", "username pattern encoding is invalid")
	}
	if template.UpdatePolicy != "" && template.UpdatePolicy != TemplateUpdateKeepBound && template.UpdatePolicy != TemplateUpdateRerenderBound {
		return nil, invalid("update_policy_invalid", "update_policy", "template update policy is invalid")
	}
	tokens, err := compilePattern(template.UsernamePattern, template.UsernamePatternEncoding == "url")
	if err != nil {
		return nil, err
	}
	var stub strings.Builder
	counts := make(map[string]int)
	literalBytes := 0
	for _, token := range tokens {
		if token.name == "" {
			literalBytes += len(token.literal)
			stub.WriteString(token.literal)
		} else {
			counts[token.name]++
			stub.WriteByte('x')
		}
	}
	endpoint, err := (Endpoint{
		Protocol: template.Protocol, Host: template.Host, Port: template.Port,
		Username: stub.String(), Password: template.Password,
		HasUser:     template.HasUser || template.UsernamePattern != "" || template.HasPassword,
		HasPassword: template.HasPassword,
	}).Normalize()
	if err != nil {
		return nil, err
	}
	compiled := &CompiledTemplate{
		endpoint: endpoint, tokens: tokens, byName: make(map[string]int),
		normalized: CloneTemplate(template), maximumBytes: literalBytes, combinationCount: 1,
	}
	compiled.normalized.Protocol = endpoint.Protocol
	compiled.normalized.Host = endpoint.Host
	compiled.normalized.HasUser = endpoint.HasUser
	for index, placeholder := range template.Placeholders {
		path := fmt.Sprintf("placeholders[%d]", index)
		if !placeholderNamePattern.MatchString(placeholder.Name) {
			return nil, invalid("placeholder_name_invalid", path+".name", "placeholder name is invalid")
		}
		if _, exists := compiled.byName[placeholder.Name]; exists {
			return nil, invalid("placeholder_duplicate", path+".name", "placeholder is defined more than once")
		}
		if counts[placeholder.Name] == 0 {
			return nil, invalid("placeholder_unused", path+".name", "placeholder is not used in the username pattern")
		}
		variable := compiledVariable{name: placeholder.Name, kind: placeholder.Kind, group: -1}
		switch placeholder.Kind {
		case PlaceholderEnum:
			if placeholder.Enum == nil || placeholder.Random != nil {
				return nil, invalid("placeholder_config_invalid", path, "enum placeholders require only an enum configuration")
			}
			if !utf8.ValidString(placeholder.Enum.Separator) || utf8.RuneCountInString(placeholder.Enum.Separator) < 1 || utf8.RuneCountInString(placeholder.Enum.Separator) > 8 {
				return nil, invalid("enum_separator_invalid", path+".enum.separator", "enum separator must contain 1..8 Unicode characters")
			}
			if placeholder.GroupID != "" {
				if placeholder.Enum.Input != "" || len(placeholder.Enum.Values) > 0 {
					return nil, invalid("group_enum_range", path+".enum", "grouped enum ranges must come from the relation table")
				}
			} else {
				var warnings []ValidationIssue
				variable.values, warnings, err = enumValues(placeholder.Enum, path+".enum")
				if err != nil {
					return nil, err
				}
				compiled.warnings = append(compiled.warnings, warnings...)
				variable.set = make(map[string]bool, len(variable.values))
				maximum := 0
				for valueIndex, value := range variable.values {
					if len(value) > credentialLimit(endpoint.Protocol) {
						return nil, invalid("enum_value_too_long", fmt.Sprintf("%s.enum.values[%d]", path, valueIndex), "enum value exceeds the protocol byte limit")
					}
					variable.set[value] = true
					maximum = max(maximum, len(value))
				}
				compiled.maximumBytes += counts[placeholder.Name] * maximum
				compiled.multiplyCombinations(len(variable.values))
				normalized := compiled.normalized.Placeholders[index].Enum
				if len(placeholder.Enum.Values) > 0 {
					normalized.Values = append([]string(nil), variable.values...)
				} else {
					normalized.Input = strings.Join(variable.values, normalized.Separator)
				}
			}
		case PlaceholderRandom:
			if placeholder.Random == nil || placeholder.Enum != nil || placeholder.GroupID != "" {
				return nil, invalid("placeholder_config_invalid", path, "random placeholders require only a random configuration and cannot belong to a group")
			}
			if placeholder.Random.Length < 1 || placeholder.Random.Length > 128 {
				return nil, invalid("random_length_invalid", path+".random.length", "random length must be 1..128")
			}
			variable.random = *placeholder.Random
			compiled.maximumBytes += counts[placeholder.Name] * variable.random.Length
			compiled.hasRandom = true
		case PlaceholderReserved:
			if template.Kind != TemplateResin || placeholder.Name != ReservedAccountPlaceholder {
				return nil, invalid("reserved_placeholder_invalid", path, "only the resin account placeholder may be reserved")
			}
			if placeholder.Enum != nil || placeholder.Random != nil || placeholder.GroupID != "" {
				return nil, invalid("placeholder_config_invalid", path, "reserved placeholders do not accept enum, random, or group configuration")
			}
			// resinAccountBase caps the auth-derived value at 48 bytes.
			compiled.maximumBytes += counts[placeholder.Name] * 48
		default:
			return nil, invalid("placeholder_kind_invalid", path+".kind", "placeholder kind must be enum, random, or reserved")
		}
		compiled.byName[placeholder.Name] = len(compiled.variables)
		compiled.variables = append(compiled.variables, variable)
	}
	for name := range counts {
		if _, exists := compiled.byName[name]; !exists {
			return nil, invalid("placeholder_undefined", "username_pattern", "username pattern contains an undefined placeholder")
		}
	}
	groupIDs := make(map[string]bool)
	for groupIndex, group := range template.ValueGroups {
		path := fmt.Sprintf("value_groups[%d]", groupIndex)
		if group.ID == "" || hasControl(group.ID) || !utf8.ValidString(group.ID) || len(group.Columns) < 2 {
			return nil, invalid("group_invalid", path, "relation groups require an ID and at least two columns")
		}
		if groupIDs[group.ID] {
			return nil, invalid("group_duplicate", path+".id", "relation group ID is defined more than once")
		}
		groupIDs[group.ID] = true
		seenColumns := make(map[string]bool)
		for columnIndex, column := range group.Columns {
			columnPath := fmt.Sprintf("%s.columns[%d]", path, columnIndex)
			variableIndex, exists := compiled.byName[column]
			if !exists || compiled.variables[variableIndex].kind != PlaceholderEnum || template.Placeholders[variableIndex].GroupID != group.ID {
				return nil, invalid("group_member_invalid", columnPath, "relation columns must reference member enum placeholders")
			}
			if seenColumns[column] || compiled.variables[variableIndex].group >= 0 {
				return nil, invalid("group_overlap", columnPath, "relation group columns cannot repeat or overlap")
			}
			seenColumns[column] = true
			compiled.variables[variableIndex].group = groupIndex
		}
		if len(group.Rows) == 0 {
			return nil, invalid("group_empty", path+".rows", "relation groups cannot be empty")
		}
		relation := compiledGroup{id: group.ID, columns: append([]string(nil), group.Columns...), root: &prefixNode{}}
		seenRows := make(map[string]bool)
		maximum := 0
		for rowIndex, row := range group.Rows {
			if len(row) != len(group.Columns) {
				return nil, invalid("group_row_incomplete", fmt.Sprintf("%s.rows[%d]", path, rowIndex), "relation row has the wrong number of columns")
			}
			normalizedRow := make([]string, len(row))
			contribution := 0
			for columnIndex, value := range row {
				if !utf8.ValidString(value) || hasControl(value) {
					return nil, invalid("group_value_invalid", fmt.Sprintf("%s.rows[%d][%d]", path, rowIndex, columnIndex), "relation value contains invalid characters")
				}
				value = strings.TrimSpace(value)
				if value == "" {
					return nil, invalid("group_value_empty", fmt.Sprintf("%s.rows[%d][%d]", path, rowIndex, columnIndex), "relation values cannot be empty")
				}
				if len(value) > credentialLimit(endpoint.Protocol) {
					return nil, invalid("group_value_too_long", fmt.Sprintf("%s.rows[%d][%d]", path, rowIndex, columnIndex), "relation value exceeds the protocol byte limit")
				}
				normalizedRow[columnIndex] = value
				contribution += counts[group.Columns[columnIndex]] * len(value)
			}
			key := strings.Join(normalizedRow, "\x00")
			if seenRows[key] {
				compiled.warnings = append(compiled.warnings, ValidationIssue{Code: "group_row_duplicate", Path: fmt.Sprintf("%s.rows[%d]", path, rowIndex), Message: "duplicate relation row was removed"})
				continue
			}
			seenRows[key] = true
			relation.rows = append(relation.rows, normalizedRow)
			addPrefixRow(relation.root, normalizedRow)
			maximum = max(maximum, contribution)
		}
		compiled.maximumBytes += maximum
		compiled.multiplyCombinations(len(relation.rows))
		compiled.groupRows += len(relation.rows)
		compiled.normalized.ValueGroups[groupIndex].Rows = relation.rows
		compiled.groups = append(compiled.groups, relation)
	}
	for index, placeholder := range template.Placeholders {
		if placeholder.GroupID != "" && compiled.variables[index].group < 0 {
			return nil, invalid("group_member_missing", fmt.Sprintf("placeholders[%d].group_id", index), "placeholder references a missing group or is not listed as a member")
		}
	}
	if compiled.maximumBytes > credentialLimit(endpoint.Protocol) {
		return nil, invalid("username_too_long", "username_pattern", "maximum generated username exceeds the protocol byte limit")
	}
	return compiled, nil
}

func addPrefixRow(root *prefixNode, row []string) {
	node := root
	for _, value := range row {
		next := prefixChild(node, value)
		if next == nil {
			next = &prefixNode{}
			// Most prefixes of a wide relation have only one continuation.
			// Allocate a lookup map only when a second distinct branch appears.
			if node.byValue != nil {
				node.byValue[value] = next
			} else if len(node.choices) == 1 {
				first := node.choices[0]
				node.byValue = map[string]*prefixNode{first.value: first.next, value: next}
			}
			node.choices = append(node.choices, prefixChoice{value: value, next: next})
		}
		node = next
	}
}

func prefixChild(node *prefixNode, value string) *prefixNode {
	if node.byValue != nil {
		return node.byValue[value]
	}
	if len(node.choices) == 1 && node.choices[0].value == value {
		return node.choices[0].next
	}
	return nil
}

func boundedProduct(value int64, factor int) int64 {
	if factor > 0 && value > math.MaxInt64/int64(factor) {
		return math.MaxInt64
	}
	return value * int64(factor)
}

func (c *CompiledTemplate) multiplyCombinations(factor int) {
	if factor > 0 && c.combinationCount > math.MaxInt64/int64(factor) {
		c.combinationCountCapped = true
	}
	c.combinationCount = boundedProduct(c.combinationCount, factor)
}

func (c *CompiledTemplate) CombinationCountCapped() bool { return c.combinationCountCapped }

// CombinationCountExact preserves a readable count beyond both int64 and the
// precision of JSON consumers' floating-point number representations.
func (c *CompiledTemplate) CombinationCountExact() string {
	var total big.Int
	total.SetInt64(1)
	var factor big.Int
	for _, variable := range c.variables {
		if variable.kind == PlaceholderEnum && variable.group < 0 {
			factor.SetInt64(int64(len(variable.values)))
			total.Mul(&total, &factor)
		}
	}
	for _, group := range c.groups {
		factor.SetInt64(int64(len(group.rows)))
		total.Mul(&total, &factor)
	}
	return total.String()
}

func (c *CompiledTemplate) MaximumUsernameBytes() int { return c.maximumBytes }
func (c *CompiledTemplate) CombinationCount() int64   { return c.combinationCount }
func (c *CompiledTemplate) HasRandom() bool           { return c.hasRandom }
func (c *CompiledTemplate) PlaceholderCount() int     { return len(c.variables) }
func (c *CompiledTemplate) GroupRowCount() int        { return c.groupRows }
func (c *CompiledTemplate) NormalizedTemplate() Template {
	return CloneTemplate(c.normalized)
}
func (c *CompiledTemplate) Warnings() []ValidationIssue {
	return append([]ValidationIssue(nil), c.warnings...)
}

func (c *CompiledTemplate) Generate(ctx context.Context, request GenerateRequest) (RenderedProxy, error) {
	if err := ctx.Err(); err != nil {
		return RenderedProxy{}, err
	}
	if request.AvoidEndpointHash != "" {
		return c.RenderAvoiding(nil, request.AvoidEndpointHash)
	}
	return c.RenderWithReader(nil, nil)
}

func (c *CompiledTemplate) RenderWithReader(reader io.Reader, fixed map[string]string) (RenderedProxy, error) {
	if reader == nil {
		reader = rand.Reader
	}
	values := make(map[string]string, len(c.variables))
	for name, value := range fixed {
		variableIndex, exists := c.byName[name]
		if !exists {
			return RenderedProxy{}, invalid("assignment_unknown", "assignments", "assignment references an unknown placeholder")
		}
		if !utf8.ValidString(value) || hasControl(value) || len(value) > credentialLimit(c.endpoint.Protocol) {
			return RenderedProxy{}, invalid("assignment_invalid", "assignments."+name, "assignment contains invalid characters or is too long")
		}
		variable := c.variables[variableIndex]
		if variable.group < 0 {
			if variable.kind == PlaceholderEnum && !variable.set[value] {
				return RenderedProxy{}, invalid("assignment_invalid", "assignments."+name, "assignment is outside the enum range")
			}
			if variable.kind == PlaceholderRandom && !validRandomValue(value, variable.random) {
				return RenderedProxy{}, invalid("assignment_invalid", "assignments."+name, "assignment does not match the random configuration")
			}
		}
		values[name] = value
	}
	for _, group := range c.groups {
		if err := group.resolve(reader, values); err != nil {
			return RenderedProxy{}, err
		}
	}
	for _, variable := range c.variables {
		if _, exists := values[variable.name]; exists {
			continue
		}
		var value string
		var err error
		switch variable.kind {
		case PlaceholderEnum:
			value, err = randomChoice(reader, variable.values)
		case PlaceholderRandom:
			value, err = randomString(reader, variable.random.Length, variable.random.CaseSensitive)
		case PlaceholderReserved:
			return RenderedProxy{}, invalid("reserved_value_required", "assignments."+variable.name, "reserved placeholder requires a supplied value")
		}
		if err != nil {
			return RenderedProxy{}, err
		}
		values[variable.name] = value
	}
	return c.renderValues(values)
}

func validRandomValue(value string, spec RandomSpec) bool {
	if len(value) != spec.Length {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || spec.CaseSensitive && character >= 'A' && character <= 'Z' {
			continue
		}
		return false
	}
	return true
}

func (group compiledGroup) resolve(reader io.Reader, values map[string]string) error {
	needsFilter := false
	missing := false
	for _, column := range group.columns {
		_, assigned := values[column]
		if !assigned {
			missing = true
		} else if missing {
			needsFilter = true
		}
	}
	var viable map[*prefixNode]bool
	if needsFilter {
		viable = make(map[*prefixNode]bool)
		var matches func(*prefixNode, int) bool
		matches = func(node *prefixNode, depth int) bool {
			if cached, exists := viable[node]; exists {
				return cached
			}
			if depth == len(group.columns) {
				viable[node] = true
				return true
			}
			allowed := false
			if fixed, assigned := values[group.columns[depth]]; assigned {
				if child := prefixChild(node, fixed); child != nil {
					allowed = matches(child, depth+1)
				}
			} else {
				for _, choice := range node.choices {
					if matches(choice.next, depth+1) {
						allowed = true
					}
				}
			}
			viable[node] = allowed
			return allowed
		}
		if !matches(group.root, 0) {
			return invalid("assignment_constraints", "value_groups."+group.id, "fixed assignments do not satisfy the relation table")
		}
	}
	node := group.root
	for _, column := range group.columns {
		if value, assigned := values[column]; assigned {
			child := prefixChild(node, value)
			if child == nil || needsFilter && !viable[child] {
				return invalid("assignment_constraints", "value_groups."+group.id, "fixed assignments do not satisfy the relation table")
			}
			node = child
			continue
		}
		count := len(node.choices)
		if needsFilter {
			count = 0
			for _, choice := range node.choices {
				if viable[choice.next] {
					count++
				}
			}
		}
		index, err := randomIndex(reader, count)
		if err != nil {
			return err
		}
		choice := prefixChoice{}
		if !needsFilter {
			choice = node.choices[index]
		} else {
			for _, candidate := range node.choices {
				if !viable[candidate.next] {
					continue
				}
				if index == 0 {
					choice = candidate
					break
				}
				index--
			}
		}
		values[column] = choice.value
		node = choice.next
	}
	return nil
}

// RenderResolved serializes trusted already-resolved values, such as persisted
// operation targets or Resin's system lease identity. User preview assignments
// must use RenderWithReader, which enforces enum and relation membership.
func (c *CompiledTemplate) RenderResolved(resolved map[string]string) (RenderedProxy, error) {
	if len(resolved) != len(c.variables) {
		return RenderedProxy{}, invalid("assignment_incomplete", "values", "resolved placeholder values are incomplete")
	}
	values := make(map[string]string, len(resolved))
	for name, value := range resolved {
		if _, exists := c.byName[name]; !exists {
			return RenderedProxy{}, invalid("assignment_unknown", "values", "resolved values reference an unknown placeholder")
		}
		if !utf8.ValidString(value) || hasControl(value) {
			return RenderedProxy{}, invalid("assignment_invalid", "values."+name, "resolved value contains invalid characters")
		}
		values[name] = value
	}
	return c.renderValues(values)
}

func (c *CompiledTemplate) renderValues(values map[string]string) (RenderedProxy, error) {
	var username strings.Builder
	username.Grow(c.maximumBytes)
	for _, token := range c.tokens {
		if token.name == "" {
			username.WriteString(token.literal)
		} else {
			username.WriteString(values[token.name])
		}
	}
	endpoint := c.endpoint
	endpoint.Username = username.String()
	if len(endpoint.Username) > credentialLimit(endpoint.Protocol) {
		return RenderedProxy{}, invalid("username_too_long", "username", "generated username exceeds the protocol byte limit")
	}
	if endpoint.HasPassword && endpoint.Username == "" {
		return RenderedProxy{}, invalid("username_required", "username", "proxy password requires a username")
	}
	return RenderedProxy{
		Endpoint: endpoint, URL: endpoint.canonicalURL(), MaskedURL: endpoint.maskedURL(),
		Values: values, VariantKey: variantKey(values), UsernameLength: len(endpoint.Username),
	}, nil
}

// RenderAvoiding retains ordinary random generation, with a bounded search of
// finite enum alternatives if random retries repeatedly return the old endpoint.
func (c *CompiledTemplate) RenderAvoiding(reader io.Reader, avoidHash string) (RenderedProxy, error) {
	if avoidHash == "" {
		return c.RenderWithReader(reader, nil)
	}
	var baseline RenderedProxy
	for attempt := 0; attempt < 16; attempt++ {
		rendered, err := c.RenderWithReader(reader, nil)
		if err != nil {
			return RenderedProxy{}, err
		}
		if rendered.Endpoint.Fingerprint() != avoidHash {
			return rendered, nil
		}
		baseline = rendered
	}
	copyValues := func() map[string]string {
		values := make(map[string]string, len(baseline.Values))
		for key, value := range baseline.Values {
			values[key] = value
		}
		return values
	}
	for _, variable := range c.variables {
		if variable.kind != PlaceholderEnum || variable.group >= 0 {
			continue
		}
		for _, choice := range variable.values {
			if choice == baseline.Values[variable.name] {
				continue
			}
			values := copyValues()
			values[variable.name] = choice
			rendered, err := c.renderValues(values)
			if err != nil {
				return RenderedProxy{}, err
			}
			if rendered.Endpoint.Fingerprint() != avoidHash {
				return rendered, nil
			}
		}
	}
	for _, group := range c.groups {
		for _, row := range group.rows {
			values := copyValues()
			for index, column := range group.columns {
				values[column] = row[index]
			}
			rendered, err := c.renderValues(values)
			if err != nil {
				return RenderedProxy{}, err
			}
			if rendered.Endpoint.Fingerprint() != avoidHash {
				return rendered, nil
			}
		}
	}
	return RenderedProxy{}, invalid("rotation_unavailable", "source", "template could not generate a different endpoint")
}

func (c *CompiledTemplate) Preview(count int) []TemplateSample {
	if count < 1 {
		count = 3
	}
	count = min(count, 20)
	samples := make([]TemplateSample, 0, count)
	for index := 0; index < count; index++ {
		rendered, err := c.RenderWithReader(nil, nil)
		if err != nil {
			break
		}
		samples = append(samples, TemplateSample{
			MaskedURL:  rendered.MaskedURL,
			DisplayURL: rendered.Endpoint.MaskedPasswordURL(),
			Values:     rendered.Values,
			VariantKey: rendered.VariantKey,
		})
	}
	return samples
}
