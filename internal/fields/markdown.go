package fields

import (
	"fmt"
	"strings"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// Markdown renders the whole field table as documentation.
//
// docs/FIELDS.md is generated from this and checked in CI, so the label
// reference, the environment variables and the built-in defaults can never
// drift apart from what the parser actually does.
func Markdown() string {
	var sb strings.Builder
	sb.WriteString(`<!-- Generated from internal/fields; do not edit by hand. -->
<!-- Run "go test ./internal/fields -update" after changing the table. -->

# Field reference

Every property of a managed resource, with the label that sets it, the
environment variable that changes its default and the NPM/NPMplus API field it
ends up in.

* **Label** is the canonical spelling. Inside a name ` + "`.`" + `, ` + "`_`" + ` and ` + "`-`" + ` are
  interchangeable, so ` + "`ssl.hsts_subdomains`" + ` and ` + "`ssl-hsts-subdomains`" + ` are the
  same field.
* **Environment** sets the default for every container. The kind specific name
  is shown; the cross-kind ` + "`NPM_DEFAULT_<FIELD>`" + ` works as well, and every
  alias has its own variable (which is how ` + "`NPM_PROXY_SSL_FORCE`" + ` works).
* **Default** ` + "`auto`" + ` means the value is derived at runtime: the certificate
  from the certificate list, the port from the container's exposed ports and
  ` + "`ssl.forced`" + ` from whether a certificate was attached.
* **+** marks a field that only exists in NPMplus. Against upstream
  nginx-proxy-manager it is left out of the request and reported once.

`)

	for _, kind := range npm.Kinds {
		label := kind.Label()
		fmt.Fprintf(&sb, "## %s%ss (`%s`)\n\n", strings.ToUpper(label[:1]), label[1:], kind)
		sb.WriteString("| Label | Aliases | Type | Default | Environment | API field | |\n")
		sb.WriteString("|---|---|---|---|---|---|---|\n")
		for _, f := range List(kind) {
			plus := ""
			if f.Plus {
				plus = "+"
			}
			values := f.Type.String()
			if f.Type == TypeEnum {
				values = "`" + strings.Join(f.Enum, "`, `") + "`"
			}
			fmt.Fprintf(&sb, "| `%s` | %s | %s | %s | `%s` | `%s` | %s |\n",
				f.Name, aliasList(f), values, defaultValue(f), envName(envPrefix+envKind(kind)+"_", f.Name), f.APIField, plus)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Custom locations (`location.<n>.<field>`)\n\n")
	sb.WriteString("Location blocks belong to a proxy host and inherit every switch from it.\n\n")
	sb.WriteString("| Label | Aliases | Type | Default | API field | |\n")
	sb.WriteString("|---|---|---|---|---|---|\n")
	for _, f := range LocationFields {
		plus := ""
		if f.Plus {
			plus = "+"
		}
		values := f.Type.String()
		if f.Type == TypeEnum {
			values = "`" + strings.Join(f.Enum, "`, `") + "`"
		}
		fmt.Fprintf(&sb, "| `%s` | %s | %s | %s | `%s` | %s |\n",
			f.Name, aliasList(f), values, defaultValue(f), f.APIField, plus)
	}

	sb.WriteString(`
## Inverted switches

NPMplus spells three settings as "disable X". The labels are positive, and the
value is negated on the way into the API:

| Label | API field | ` + "`true`" + ` means |
|---|---|---|
| ` + "`crowdsec_appsec`" + ` | ` + "`npmplus_crowdsec_appsec`" + ` | AppSec is **active** (the API field is set to ` + "`false`" + `) |
| ` + "`request_buffering`" + ` | ` + "`npmplus_proxy_request_buffering`" + ` | buffering is **active** |
| ` + "`response_buffering`" + ` | ` + "`npmplus_proxy_response_buffering`" + ` | buffering is **active** |

The explicit spellings ` + "`disable_crowdsec_appsec`" + `, ` + "`disable_request_buffering`" + ` and
` + "`disable_response_buffering`" + ` are accepted too and mean the opposite.
`)
	return sb.String()
}

// aliasList renders the accepted alternative spellings.
func aliasList(f Field) string {
	if len(f.Aliases) == 0 {
		return "—"
	}
	quoted := make([]string, 0, len(f.Aliases))
	for _, alias := range f.Aliases {
		quoted = append(quoted, "`"+alias+"`")
	}
	return strings.Join(quoted, ", ")
}

// defaultValue renders the built-in default.
func defaultValue(f Field) string {
	if f.Default == "" {
		return "—"
	}
	return "`" + f.Default + "`"
}
