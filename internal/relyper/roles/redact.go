// Relyper-owned: configuration without secrets for read-only users.
package roles

import (
	"net/url"
	"regexp"
	"strings"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Redacted replaces secret values.
const Redacted = "[REDACTED]"

var secretField = regexp.MustCompile(`(?i)(password|passwd|priv|token|secret|webhook|url|api_key|apikey|command)`)

// RedactConfig returns a copy of cfg without secrets: repository passwords,
// environment values, passwords in URIs and flags, password hashes, the
// multihost identity and every string inside hooks (URLs, tokens and
// scripts may contain credentials).
func RedactConfig(cfg *v1.Config) *v1.Config {
	if cfg == nil {
		return nil
	}
	c := proto.Clone(cfg).(*v1.Config)
	for _, r := range c.GetRepos() {
		if r.Password != "" {
			r.Password = Redacted
		}
		for i, e := range r.Env {
			if name, _, ok := strings.Cut(e, "="); ok {
				r.Env[i] = name + "=" + Redacted
			}
		}
		r.Uri = redactURI(r.Uri)
		for i, f := range r.Flags {
			if secretField.MatchString(f) {
				r.Flags[i] = Redacted
			}
		}
		for _, h := range r.GetHooks() {
			redactStrings(h.ProtoReflect())
		}
	}
	for _, p := range c.GetPlans() {
		for _, h := range p.GetHooks() {
			redactStrings(h.ProtoReflect())
		}
	}
	if a := c.GetAuth(); a != nil {
		for _, u := range a.GetUsers() {
			if u.GetPasswordBcrypt() != "" {
				u.Password = &v1.User_PasswordBcrypt{PasswordBcrypt: Redacted}
			}
		}
	}
	if m := c.GetMultihost(); m != nil {
		m.Identity = nil
	}
	// Anything else that looks like a secret by its field name.
	redactSecretFields(c.ProtoReflect())
	return c
}

func redactURI(uri string) string {
	scheme, rest, ok := strings.Cut(uri, ":")
	if !ok {
		return uri
	}
	u, err := url.Parse(rest)
	if err != nil || u.User == nil {
		return uri
	}
	if p, set := u.User.Password(); set && p != "" {
		return scheme + ":" + strings.Replace(rest, ":"+p+"@", ":"+Redacted+"@", 1)
	}
	return uri
}

// redactStrings replaces every non-empty string in m.
func redactStrings(m protoreflect.Message) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			mp := v.Map()
			mp.Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
				if fd.MapValue().Kind() == protoreflect.StringKind {
					mp.Set(k, protoreflect.ValueOfString(Redacted))
				}
				return true
			})
		case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				redactStrings(l.Get(i).Message())
			}
		case fd.IsList() && fd.Kind() == protoreflect.StringKind:
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				l.Set(i, protoreflect.ValueOfString(Redacted))
			}
		case fd.Kind() == protoreflect.MessageKind:
			redactStrings(v.Message())
		case fd.Kind() == protoreflect.StringKind && v.String() != "":
			m.Set(fd, protoreflect.ValueOfString(Redacted))
		}
		return true
	})
}

// redactSecretFields replaces string fields whose names look secret.
func redactSecretFields(m protoreflect.Message) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
		case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				redactSecretFields(l.Get(i).Message())
			}
		case fd.IsList():
		case fd.Kind() == protoreflect.MessageKind:
			redactSecretFields(v.Message())
		case fd.Kind() == protoreflect.StringKind && v.String() != "" && fd.Name() != "uri" &&
			fd.Name() != "id" && secretField.MatchString(string(fd.Name())):
			m.Set(fd, protoreflect.ValueOfString(Redacted))
		}
		return true
	})
}
