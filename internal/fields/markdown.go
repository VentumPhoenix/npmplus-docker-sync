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

Every label this tool understands, what it does, the environment variable that
changes its default and the NPM/NPMplus API field it ends up in. This page is
generated from the field table in ` + "`internal/fields`" + `, which is the same table the
parser reads - it cannot be out of date. [LABELS.md](LABELS.md) explains the
grammar around these names.

A label is written ` + "`<prefix>.<kind>[.<index>].<field>`" + `, so the ` + "`websockets`" + ` row
of the proxy table is the label ` + "`npm.proxy.websockets`" + `.

* **Label** is the canonical spelling; **Aliases** are accepted just as well.
  Inside a name ` + "`.`" + `, ` + "`_`" + ` and ` + "`-`" + ` are interchangeable, so
  ` + "`ssl.hsts_subdomains`" + ` and ` + "`ssl-hsts-subdomains`" + ` are the same field.
* **Type** is the value domain: ` + "`bool`" + ` takes ` + "`true/false`" + `, ` + "`1/0`" + `, ` + "`yes/no`" + ` or
  ` + "`on/off`" + `; ` + "`domains`" + ` and ` + "`list`" + ` are comma, semicolon or whitespace
  separated; ` + "`port`" + ` is ` + "`1-65535`" + `; an enum lists its values.
* **Default** is the built-in one. ` + "`auto`" + ` means it is derived at runtime: the
  certificate from the certificate list, the port from the container's exposed
  ports and ` + "`ssl.forced`" + ` from whether a certificate was attached. ` + "`—`" + ` means
  unset.
* **Environment** sets the default for every container. The kind specific name
  is shown; the cross-kind ` + "`NPM_DEFAULT_<FIELD>`" + ` works as well, and every
  alias has its own variable (which is how ` + "`NPM_PROXY_SSL_FORCE`" + ` works). A
  label on a container always wins.
* **+** after the label marks a field that only exists in NPMplus. Against
  upstream nginx-proxy-manager it is left out of the request and reported once,
  rather than failing the write.

## Resource kinds

` + "`<kind>`" + ` selects which of NPM's four collections the labels describe. It may be
left out, in which case it is ` + "`proxy`" + `.

| Kind | Spellings | NPM calls it |
|---|---|---|
| ` + "`proxy`" + ` | ` + "`proxy`" + `, ` + "`proxies`" + ` | Proxy Host |
| ` + "`redirect`" + ` | ` + "`redirect`" + `, ` + "`redirection`" + ` | Redirection Host |
| ` + "`stream`" + ` | ` + "`stream`" + ` | Stream |
| ` + "`dead`" + ` | ` + "`404`" + `, ` + "`dead`" + ` | 404 Host |

## Labels that are not fields

These stand outside the per-resource tables below: they take no resource kind
and do not end up in any API field.

| Label | What it does |
|---|---|
| ` + "`npm.enable`" + ` | Whether the container is looked at at all. With ` + "`NPM_EXPOSED_BY_DEFAULT`" + ` (the default) every container carrying one label of the namespace is managed and ` + "`false`" + ` opts it out; with the flag off, ` + "`true`" + ` is required. |
| ` + "`npm.<index>.enable`" + ` | Switches one index off entirely, labels and all - nothing is created for it and nothing is deleted. Note the difference to ` + "`npm.<kind>.enabled`" + ` in the tables below, which *does* create the resource and leaves it disabled in NPM. |

`)

	for _, kind := range npm.Kinds {
		label := kind.Label()
		fmt.Fprintf(&sb, "## %s%ss (`%s`)\n\n", strings.ToUpper(label[:1]), label[1:], kind)
		sb.WriteString("| Label | Type | Default | What it does | Aliases | Environment | API field |\n")
		sb.WriteString("|---|---|---|---|---|---|---|\n")
		for _, f := range List(kind) {
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | `%s` | `%s` |\n",
				labelCell(f), typeCell(f), defaultValue(f), docCell(f), aliasList(f),
				envName(envPrefix+envKind(kind)+"_", f.Name), f.APIField)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Custom locations (`location.<n>.<field>`)\n\n")
	sb.WriteString("Location blocks belong to a proxy host and inherit every switch from it.\n\n")
	sb.WriteString("| Label | Type | Default | What it does | Aliases | API field |\n")
	sb.WriteString("|---|---|---|---|---|---|\n")
	for _, f := range LocationFields {
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | `%s` |\n",
			labelCell(f), typeCell(f), defaultValue(f), docCell(f), aliasList(f), f.APIField)
	}

	sb.WriteString(`
## NPMplus-only fields

These exist only in NPMplus. Against upstream nginx-proxy-manager they are left
out of the request and reported once per resource - but only when a label set
them, never because of a default.

| Kind | Label | API field | What it does |
|---|---|---|---|
`)
	for _, kind := range npm.Kinds {
		for _, f := range List(kind) {
			if !f.Plus {
				continue
			}
			fmt.Fprintf(&sb, "| `%s` | `%s` | `%s` | %s |\n", kind, f.Name, f.APIField, docCell(f))
		}
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

// labelCell renders the canonical name plus the NPMplus marker, so the one
// piece of information that decides whether a label works at all is next to
// the label instead of in the last column.
func labelCell(f Field) string {
	if f.Plus {
		return "`" + f.Name + "` **+**"
	}
	return "`" + f.Name + "`"
}

// typeCell renders the value domain; an enum is spelled out, because "enum"
// alone tells a reader nothing.
func typeCell(f Field) string {
	if f.Type == TypeEnum {
		return "`" + strings.Join(f.Enum, "`, `") + "`"
	}
	return f.Type.String()
}

// docCell renders the one line description. Every field has one - a field
// without it would leave a hole in the reference, so say so loudly.
func docCell(f Field) string {
	if f.Doc == "" {
		return "**TODO: undocumented field**"
	}
	return f.Doc
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
