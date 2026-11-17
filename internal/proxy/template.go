package proxy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/big"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type TemplateKind string

const (
	TemplateGeneric TemplateKind = "generic"
	TemplateResin   TemplateKind = "resin"
)

type TemplateUpdatePolicy string

const (
	TemplateUpdateKeepBound     TemplateUpdatePolicy = "keep_bound"
	TemplateUpdateRerenderBound TemplateUpdatePolicy = "rerender_bound"
)

type PlaceholderKind string

const (
	PlaceholderEnum   PlaceholderKind = "enum"
	PlaceholderRandom PlaceholderKind = "random"
)

type EnumSpec struct {
	Separator string   `json:"separator"`
	Input     string   `json:"input,omitempty"`
	Values    []string `json:"values,omitempty"`
}

type RandomSpec struct {
	Length        int  `json:"length"`
	CaseSensitive bool `json:"case_sensitive"`
}

type Placeholder struct {
	Name    string          `json:"name"`
	Kind    PlaceholderKind `json:"kind"`
	Enum    *EnumSpec       `json:"enum,omitempty"`
	Random  *RandomSpec     `json:"random,omitempty"`
	GroupID string          `json:"group_id,omitempty"`
}

type ValueGroup struct {
	ID      string     `json:"id"`
	Name    string     `json:"name,omitempty"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

type Template struct {
	ID                      string               `json:"id"`
	Name                    string               `json:"name"`
	Kind                    TemplateKind         `json:"kind"`
	Protocol                Protocol             `json:"protocol"`
	Host                    string               `json:"host"`
	Port                    int                  `json:"port"`
	UsernamePattern         string               `json:"username_pattern"`
	UsernamePatternEncoding string               `json:"username_pattern_encoding,omitempty"`
	HasUser                 bool                 `json:"has_user,omitempty"`
	Password                string               `json:"password,omitempty"`
	HasPassword             bool                 `json:"has_password"`
	Placeholders            []Placeholder        `json:"placeholders"`
	ValueGroups             []ValueGroup         `json:"value_groups,omitempty"`
	Enabled                 bool                 `json:"enabled"`
	Priority                int                  `json:"priority"`
	MaxBindings             int                  `json:"max_bindings"`
	UpdatePolicy            TemplateUpdatePolicy `json:"update_policy"`
	ConfigVersion           uint64               `json:"config_version,omitempty"`
	Revision                uint64               `json:"revision,omitempty"`
	ValidatedAt             time.Time            `json:"validated_at,omitempty"`
	CreatedAt               time.Time            `json:"created_at,omitempty"`
	UpdatedAt               time.Time            `json:"updated_at,omitempty"`
}

type RenderedProxy struct {
	Endpoint       Endpoint          `json:"-"`
	Cleanup        *LeaseRef         `json:"-"`
	URL            string            `json:"url"`
	MaskedURL      string            `json:"masked_url"`
	Values         map[string]string `json:"values,omitempty"`
	VariantKey     string            `json:"variant_key,omitempty"`
	UsernameLength int               `json:"username_length,omitempty"`
}

type TemplateSample struct {
	MaskedURL  string            `json:"masked_url"`
	Values     map[string]string `json:"values,omitempty"`
	VariantKey string            `json:"variant_key,omitempty"`
}

var placeholderNamePattern = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_.-]{0,63}$`)

func ParseTemplateURL(raw string) (Template, error) {
	raw = strings.TrimSpace(raw)
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" {
		return Template{}, ErrMissingScheme
	}
	if len(raw) > maxTemplateBytes {
		return Template{}, invalid("template_too_large", "url", "template exceeds the configuration size limit")
	}
	authority := rest
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	at := strings.LastIndexByte(authority, '@')
	if at < 0 {
		if strings.ContainsAny(scheme+rest, "{}") {
			return Template{}, invalid("placeholder_outside_username", "url", "placeholders are only allowed in the username")
		}
		endpoint, err := ParseEndpoint(raw, ParseOptions{})
		if err != nil {
			return Template{}, err
		}
		return templateFromEndpoint(endpoint, "", ""), nil
	}
	userinfo := authority[:at]
	usernamePattern, rawPassword, hasPassword := strings.Cut(userinfo, ":")
	if strings.ContainsAny(scheme+rawPassword+rest[at+1:], "{}") {
		return Template{}, invalid("placeholder_outside_username", "url", "placeholders are only allowed in the username")
	}
	tokens, err := compilePattern(usernamePattern, true)
	if err != nil {
		return Template{}, err
	}
	var username strings.Builder
	var placeholders []Placeholder
	seen := make(map[string]bool)
	for _, token := range tokens {
		if token.name == "" {
			username.WriteString(token.literal)
			continue
		}
		username.WriteByte('x')
		if !seen[token.name] {
			seen[token.name] = true
			placeholders = append(placeholders, Placeholder{Name: token.name, Kind: PlaceholderEnum})
		}
	}
	safeUser := url.User(username.String()).String()
	if hasPassword {
		safeUser += ":" + rawPassword
	}
	endpoint, err := ParseEndpoint(scheme+"://"+safeUser+"@"+rest[at+1:], ParseOptions{})
	if err != nil {
		return Template{}, err
	}
	template := templateFromEndpoint(endpoint, usernamePattern, "url")
	template.Placeholders = placeholders
	return template, nil
}

func templateFromEndpoint(endpoint Endpoint, pattern, encoding string) Template {
	return Template{
		Protocol: endpoint.Protocol, Host: endpoint.Host, Port: endpoint.Port,
		UsernamePattern: pattern, UsernamePatternEncoding: encoding, HasUser: endpoint.HasUser,
		Password: endpoint.Password, HasPassword: endpoint.HasPassword,
		UpdatePolicy: TemplateUpdateKeepBound, Enabled: true,
	}
}

func (t Template) URLForDisplay() string {
	return t.displayURL(false)
}

// URLForReveal returns the same URL shape as URLForDisplay with the literal
// password included. Callers expose this only through an explicit reveal action.
func (t Template) URLForReveal() string {
	return t.displayURL(true)
}

func (t Template) displayURL(revealPassword bool) string {
	masked := (Endpoint{Protocol: t.Protocol, Host: t.Host, Port: t.Port}).MaskedURL()
	if !t.HasUser && t.UsernamePattern == "" && !t.HasPassword {
		return masked
	}
	tokens, err := compilePattern(t.UsernamePattern, t.UsernamePatternEncoding == "url")
	if err != nil {
		return masked
	}
	var username strings.Builder
	for _, token := range tokens {
		if token.name == "" {
			username.WriteString(url.User(token.literal).String())
		} else {
			username.WriteByte('{')
			username.WriteString(token.name)
			username.WriteByte('}')
		}
	}
	userinfo := username.String()
	if t.HasPassword {
		password := "******"
		if revealPassword {
			_, encoded, _ := strings.Cut(url.UserPassword("u", t.Password).String(), ":")
			password = encoded
		}
		userinfo += ":" + password
	}
	return string(t.Protocol) + "://" + userinfo + "@" + net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

func (t Template) UsernamePlaceholders() []string {
	tokens, err := compilePattern(t.UsernamePattern, t.UsernamePatternEncoding == "url")
	if err != nil {
		return nil
	}
	var names []string
	for _, token := range tokens {
		if token.name != "" {
			names = append(names, token.name)
		}
	}
	return names
}

func (p Placeholder) ValueList() []string {
	if p.Enum == nil || p.Enum.Separator == "" {
		return nil
	}
	values, _, err := enumValues(p.Enum, "enum")
	if err != nil {
		return nil
	}
	return values
}

func (t Template) Validate() error {
	_, err := CompileTemplate(t)
	return err
}

// ValidateStructure performs the same complete validation as saving a template.
func (t Template) ValidateStructure() error {
	return t.Validate()
}

func (t Template) MaximumUsernameBytes() int {
	compiled, err := CompileTemplate(t)
	if err != nil {
		return 0
	}
	return compiled.MaximumUsernameBytes()
}

// RenderWithReader is a convenience entry point. Repeated generation should use
// the immutable CompiledTemplate cached by source ID and generation revision.
func (t Template) RenderWithReader(reader io.Reader, fixed map[string]string) (RenderedProxy, error) {
	compiled, err := CompileTemplate(t)
	if err != nil {
		return RenderedProxy{}, err
	}
	return compiled.RenderWithReader(reader, fixed)
}

func (t Template) Preview(count int) []TemplateSample {
	compiled, err := CompileTemplate(t)
	if err != nil {
		return nil
	}
	return compiled.Preview(count)
}

func randomChoice(reader io.Reader, values []string) (string, error) {
	index, err := randomIndex(reader, len(values))
	if err != nil {
		return "", err
	}
	return values[index], nil
}

func randomIndex(reader io.Reader, count int) (int, error) {
	if count < 1 {
		return 0, invalid("enum_empty", "placeholders", "no values to choose")
	}
	if count == 1 {
		return 0, nil
	}
	index, err := rand.Int(reader, big.NewInt(int64(count)))
	if err != nil {
		return 0, err
	}
	return int(index.Int64()), nil
}

func randomString(reader io.Reader, length int, caseSensitive bool) (string, error) {
	if length < 1 || length > 128 {
		return "", invalid("random_length_invalid", "random.length", "random length must be 1..128")
	}
	alphabet := "abcdefghijklmnopqrstuvwxyz0123456789"
	if caseSensitive {
		alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	}
	// Reject the incomplete final bucket; batching avoids one big.Int per byte.
	limit := 256 - 256%len(alphabet)
	result := make([]byte, 0, length)
	var buffer [128]byte
	for len(result) < length {
		read := buffer[:length-len(result)]
		if _, err := io.ReadFull(reader, read); err != nil {
			return "", err
		}
		for _, value := range read {
			if int(value) < limit {
				result = append(result, alphabet[int(value)%len(alphabet)])
			}
		}
	}
	return string(result), nil
}

func variantKey(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(values[key])
		builder.WriteByte(0)
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:16])
}
