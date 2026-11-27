// relyper: Relyper Backup addition, not part of upstream Backrest.

package mcp

import (
	"encoding/json"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const redacted = "[REDACTED]"

// secretFieldName matches config field names whose values are secrets.
var secretFieldName = regexp.MustCompile(`(?i)(password|passwd|priv|token|secret|webhook_url|shoutrrr_url|api_key|apikey)`)

// secretParamName matches tool parameter names whose values are redacted in
// the audit log and in approval requests.
var secretParamName = regexp.MustCompile(`(?i)(password|passwd|secret|token|private|credential|env_values|^env$)`)

// collectSecrets returns every secret value in the configuration: repository
// passwords, environment variable values, hook URLs and tokens, password
// hashes and the multihost private key. Values shorter than 4 characters are
// skipped to avoid redacting ordinary words.
func collectSecrets(cfg *v1.Config) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if len(s) >= 4 {
			out = append(out, s)
		}
	}
	var walk func(m protoreflect.Message)
	walk = func(m protoreflect.Message) {
		m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			switch {
			case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
				l := v.List()
				for i := 0; i < l.Len(); i++ {
					walk(l.Get(i).Message())
				}
			case fd.IsMap():
			case fd.Kind() == protoreflect.MessageKind:
				walk(v.Message())
			case fd.Kind() == protoreflect.StringKind && !fd.IsList():
				if secretFieldName.MatchString(string(fd.Name())) {
					add(v.String())
				}
			}
			return true
		})
	}
	if cfg == nil {
		return nil
	}
	walk(cfg.ProtoReflect())
	for _, r := range cfg.GetRepos() {
		for _, e := range r.GetEnv() {
			if _, val, ok := strings.Cut(e, "="); ok {
				add(val)
			}
		}
		if p := uriPassword(r.GetUri()); p != "" {
			add(p)
		}
	}
	return out
}

// uriPassword extracts the password of a URI with user info (e.g.
// rest:https://user:pass@host/).
func uriPassword(uri string) string {
	_, rest, ok := strings.Cut(uri, ":")
	if !ok {
		return ""
	}
	u, err := url.Parse(rest)
	if err != nil || u.User == nil {
		return ""
	}
	p, _ := u.User.Password()
	return p
}

// redactURI removes the password from a URI's user info.
func redactURI(uri string) string {
	p := uriPassword(uri)
	if p == "" {
		return uri
	}
	return strings.Replace(uri, ":"+p+"@", ":"+redacted+"@", 1)
}

// scrubber replaces known secret values inside any string of a value. It is
// applied to every tool result as a last line of defence; the tools
// themselves never put secrets into results.
type scrubber struct{ secrets []string }

func (s scrubber) str(in string) string {
	for _, sec := range s.secrets {
		if strings.Contains(in, sec) {
			in = strings.ReplaceAll(in, sec, redacted)
		}
	}
	return in
}

// scrub walks v (a pointer to a struct, typically) and rewrites strings in
// place.
func (s scrubber) scrub(v any) {
	if len(s.secrets) == 0 || v == nil {
		return
	}
	s.walk(reflect.ValueOf(v))
}

func (s scrubber) walk(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			if v.Kind() == reflect.Interface {
				inner := v.Elem()
				// values inside interfaces are not addressable: copy, scrub, set
				if inner.Kind() == reflect.String && v.CanSet() {
					v.Set(reflect.ValueOf(s.str(inner.String())))
					return
				}
				if inner.Kind() == reflect.Map || inner.Kind() == reflect.Pointer || inner.Kind() == reflect.Slice {
					s.walk(inner)
					return
				}
				return
			}
			s.walk(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				s.walk(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			s.walk(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			e := v.MapIndex(k)
			if e.Kind() == reflect.Interface && !e.IsNil() {
				e = e.Elem()
			}
			if e.Kind() == reflect.String {
				v.SetMapIndex(k, reflect.ValueOf(s.str(e.String())))
			} else {
				s.walk(e)
			}
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(s.str(v.String()))
		}
	}
}

// redactParams returns the JSON of tool parameters with secret-looking
// fields replaced, for the audit log and approval display.
func redactParams(params any) string {
	b, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	var m any
	if err := json.Unmarshal(b, &m); err != nil {
		return string(b)
	}
	m = redactJSON(m, "")
	out, _ := json.Marshal(m)
	return string(out)
}

func redactJSON(v any, key string) any {
	if key != "" && secretParamName.MatchString(key) {
		switch x := v.(type) {
		case string:
			if x == "" {
				return x
			}
			return redacted
		case map[string]any:
			// env maps: keep the names, drop the values
			out := map[string]any{}
			for k := range x {
				out[k] = redacted
			}
			return out
		case nil, bool:
			return x
		default:
			return redacted
		}
	}
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = redactJSON(e, k)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = redactJSON(e, "")
		}
		return x
	case string:
		// URIs may carry a password in their user info
		if strings.Contains(x, "@") && strings.Contains(x, "://") {
			return redactURI(x)
		}
		return x
	default:
		return x
	}
}
