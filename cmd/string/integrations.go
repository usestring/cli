package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

const integrationsHelp = `Usage:
  string integrations [list]                     every site and its actions
  string integrations list SITE                  SITE's actions, each with its flags
  string integrations run SITE ACTION [--FIELD VALUE ...] [--input FILE] [--output FILE]

A site integration is a set of typed actions over one website, such as reddit
post. Each run flag is a field of the action's input, spelled as the field:
the post field is --post. A list field takes one item per flag, or a JSON
array. list needs no key; run needs one. Add --json to list for the JSON
catalog. https://portal.usestring.ai/docs/sites

Example:
  string integrations list reddit
  string integrations run reddit post --post https://www.reddit.com/r/technology/comments/1xapaw7/ --maxPages 2
`

// integrationSite is one site's entry in the catalog or its manifest. Only
// the manifest carries each action's input schema and examples.
type integrationSite struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Actions     []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Input       struct {
			Properties map[string]schemaProperty `json:"properties"`
			Required   []string                  `json:"required"`
		} `json:"input"`
		Examples []struct {
			Input map[string]any `json:"input"`
		} `json:"examples"`
	} `json:"actions"`
}

type schemaProperty struct {
	Type        json.RawMessage `json:"type"`
	Description string          `json:"description"`
	Enum        []any           `json:"enum"`
	Items       *schemaProperty `json:"items"`
}

// typeName is the property's JSON Schema type, reading a nullable
// ["null", "integer"] as integer.
func (p schemaProperty) typeName() string {
	var one string
	if json.Unmarshal(p.Type, &one) == nil {
		return one
	}
	var many []string
	_ = json.Unmarshal(p.Type, &many)
	for _, name := range many {
		if name != "null" {
			return name
		}
	}
	return ""
}

func (p schemaProperty) kind() kind {
	switch p.typeName() {
	case "string":
		return text
	case "integer":
		return integer
	case "number":
		return number
	case "boolean":
		return boolean
	case "array":
		if p.Items != nil {
			switch p.Items.typeName() {
			case "string":
				return textList
			case "integer":
				return integerList
			case "number":
				return numberList
			}
		}
		return object
	case "object":
		return object
	}
	return textOrObject
}

// hint is the value a flag takes, for the help.
func (p schemaProperty) hint() string {
	if len(p.Enum) > 0 {
		values := make([]string, len(p.Enum))
		for i, value := range p.Enum {
			values[i] = fmt.Sprint(value)
		}
		return " " + strings.Join(values, "|")
	}
	switch p.kind() {
	case boolean:
		return ""
	case integer, number:
		return " N"
	case textList:
		return " TEXT (repeatable)"
	case integerList, numberList:
		return " N (repeatable)"
	case object:
		return " JSON"
	}
	return " TEXT"
}

func integrationsCommand(list []string, rt runtime) error {
	if len(list) == 0 {
		list = []string{"list"}
	}
	if slices.Contains([]string{"--help", "-h", "help"}, list[0]) {
		return write(rt.stdout, []byte(integrationsHelp))
	}
	key, err := apiKey(rt.env)
	if err != nil {
		return &failure{exit: exitAuth, code: "invalid_key", message: err.Error()}
	}
	base, err := apiBaseURL(rt.env)
	if err != nil {
		return usage(err)
	}
	caller := apiClient{rt: rt, base: base, key: key}
	switch list[0] {
	case "list":
		return listIntegrations(caller, list[1:])
	case "run":
		return runIntegration(caller, list[1:])
	}
	return usage(fmt.Errorf("Unknown integrations command: %s. Use list or run.", list[0]))
}

func listIntegrations(caller apiClient, list []string) error {
	parsed, err := parseArgs(list)
	if err != nil {
		return usage(err)
	}
	if parsed.help {
		return write(caller.rt.stdout, []byte(integrationsHelp))
	}
	if len(parsed.positionals) > 1 {
		return usage(fmt.Errorf("Usage: string integrations list [SITE]"))
	}
	path := "/integrations"
	if len(parsed.positionals) == 1 {
		path += "/" + encodeURIComponent(parsed.positionals[0])
	}
	data, err := caller.call(&operation{method: "GET", path: path})
	if err != nil {
		return err
	}
	if caller.rt.json {
		return caller.emit(&operation{}, data)
	}
	var catalog struct {
		Sites []integrationSite `json:"sites"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return err
	}
	var out strings.Builder
	if len(parsed.positionals) == 0 {
		fmt.Fprintf(&out, "%d site integrations. string integrations list SITE shows an action's flags.\n", len(catalog.Sites))
		for _, site := range catalog.Sites {
			fmt.Fprintf(&out, "\n%-14s %s\n", site.Name, brief(site.Description))
			for _, action := range site.Actions {
				fmt.Fprintf(&out, "  %-12s %s\n", action.Name, brief(action.Description))
			}
		}
		return write(caller.rt.stdout, []byte(out.String()))
	}
	for _, site := range catalog.Sites {
		fmt.Fprintf(&out, "%s %s: %s\n", site.Name, site.Version, site.Description)
		for _, action := range site.Actions {
			names := flagOrder(action.Input.Properties, action.Input.Required)
			fmt.Fprintf(&out, "\nstring integrations run %s %s\n  %s\n", site.Name, action.Name, action.Description)
			for _, name := range names {
				property := action.Input.Properties[name]
				required := ""
				if slices.Contains(action.Input.Required, name) {
					required = "(required) "
				}
				fmt.Fprintf(&out, "  --%s%s\n      %s%s\n", name, property.hint(), required, property.Description)
			}
			if len(action.Examples) > 0 {
				fmt.Fprintf(&out, "  Example: string integrations run %s %s%s\n", site.Name, action.Name, exampleFlags(action.Examples[0].Input, names))
			}
		}
	}
	return write(caller.rt.stdout, []byte(out.String()))
}

func runIntegration(caller apiClient, list []string) error {
	if len(list) < 2 || strings.HasPrefix(list[0], "-") || strings.HasPrefix(list[1], "-") {
		if slices.Contains(list, "--help") {
			return write(caller.rt.stdout, []byte(integrationsHelp))
		}
		return usage(fmt.Errorf("Usage: string integrations run SITE ACTION [--FIELD VALUE ...]"))
	}
	siteName, actionName := list[0], list[1]
	if caller.key == "" {
		return &failure{
			exit:     exitAuth,
			code:     "key_required",
			message:  "string integrations run needs a key. string integrations list works without one.",
			helpURL:  helpURL,
			nextStep: needsKey,
		}
	}
	data, err := caller.call(&operation{method: "GET", path: "/integrations/" + encodeURIComponent(siteName)})
	if err != nil {
		return err
	}
	var manifest struct {
		Sites []integrationSite `json:"sites"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil || len(manifest.Sites) == 0 {
		return fmt.Errorf("The API sent no manifest for %s", siteName)
	}
	site := manifest.Sites[0]
	var names []string
	for _, action := range site.Actions {
		names = append(names, action.Name)
		if action.Name != actionName {
			continue
		}
		fields := map[string]kind{}
		for name, property := range action.Input.Properties {
			fields[name] = property.kind()
		}
		parsed, err := parse(list[2:], spec{options: []string{"output"}, fields: fields})
		if err != nil {
			return usage(err)
		}
		if parsed.help {
			return listIntegrations(caller, []string{siteName})
		}
		if len(parsed.positionals) > 0 {
			return usage(fmt.Errorf("Unexpected argument %q: every input is a flag, such as --%s", parsed.positionals[0], firstOr(flagOrder(action.Input.Properties, action.Input.Required), "FIELD")))
		}
		body, err := requestFields(parsed, caller.rt.stdin)
		if err != nil {
			return usage(err)
		}
		op := &operation{method: "POST", path: "/integrations/" + encodeURIComponent(siteName) + "/" + encodeURIComponent(actionName), body: body, output: parsed.values["output"]}
		response, err := caller.call(op)
		if err != nil {
			return err
		}
		return caller.emit(op, response)
	}
	return usage(fmt.Errorf("%s has no action %q. Its actions are %s.", siteName, actionName, strings.Join(names, ", ")))
}

// flagOrder lists an action's input fields, the required ones first.
func flagOrder(properties map[string]schemaProperty, required []string) []string {
	names := slices.Collect(maps.Keys(properties))
	slices.SortFunc(names, func(a, b string) int {
		ra, rb := slices.Index(required, a), slices.Index(required, b)
		switch {
		case ra >= 0 && rb >= 0:
			return ra - rb
		case ra >= 0:
			return -1
		case rb >= 0:
			return 1
		}
		return strings.Compare(a, b)
	})
	return names
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./:%+=,-]+$`)

// exampleFlags writes an example input as the flags that send it.
func exampleFlags(input map[string]any, order []string) string {
	var out strings.Builder
	for _, name := range order {
		value, ok := input[name]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case bool:
			if v {
				fmt.Fprintf(&out, " --%s", name)
			} else {
				fmt.Fprintf(&out, " --%s=false", name)
			}
		case []any:
			if slices.ContainsFunc(v, func(item any) bool { _, isObject := item.(map[string]any); return isObject }) {
				encoded, _ := json.Marshal(v)
				fmt.Fprintf(&out, " --%s %s", name, shellQuote(string(encoded)))
				continue
			}
			for _, item := range v {
				fmt.Fprintf(&out, " --%s %s", name, shellQuote(fmt.Sprint(item)))
			}
		case map[string]any:
			encoded, _ := json.Marshal(v)
			fmt.Fprintf(&out, " --%s %s", name, shellQuote(string(encoded)))
		default:
			fmt.Fprintf(&out, " --%s %s", name, shellQuote(fmt.Sprint(v)))
		}
	}
	return out.String()
}

func shellQuote(value string) string {
	if plainWord.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// brief is a description's first sentence, cut to fit one line.
func brief(description string) string {
	if end := strings.Index(description, ". "); end >= 0 {
		description = description[:end+1]
	}
	if len([]rune(description)) > 100 {
		description = string([]rune(description)[:99]) + "…"
	}
	return description
}

func firstOr(names []string, fallback string) string {
	if len(names) == 0 {
		return fallback
	}
	return names[0]
}
