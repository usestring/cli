package main

import (
	"fmt"
	"slices"
	"strings"
)

// With no key, fetch and search go to the API's keyless tier, which takes only
// these fields. Anything else needs a key, so the CLI refuses it here instead
// of spending a request on a 400.
var keylessFields = map[string][]string{
	"fetch":  {"url", "format", "executeJS"},
	"search": {"query", "language", "dateRange", "sortBy", "safeSearch", "autocorrect", "verbatim"},
}

// flagFor names the option that set a field, for the refusal message.
var flagFor = map[string]string{
	"searchCount": "--count",
	"engine":      "--engine",
	"country":     "--country",
	"location":    "--location",
}

// keylessOperation readies op to go without a key, or says why it can't.
func keylessOperation(op *operation) error {
	allowed, ok := keylessFields[op.command]
	if !ok {
		return &failure{
			exit:     exitAuth,
			code:     "key_required",
			message:  fmt.Sprintf("string %s needs a key.", op.command),
			helpURL:  helpURL,
			nextStep: needsKey,
		}
	}
	if op.command == "fetch" {
		// GET is the API's default, and keyless refuses the field.
		delete(op.body, "method")
	}
	var refused []string
	for name := range op.body {
		if !slices.Contains(allowed, name) {
			refused = append(refused, name)
		}
	}
	if len(refused) == 0 {
		return nil
	}
	slices.Sort(refused)
	for i, name := range refused {
		if flag, ok := flagFor[name]; ok {
			refused[i] = flag
		}
		refused[i] = "`" + refused[i] + "`"
	}
	return &failure{
		exit:     exitAuth,
		code:     "key_required",
		message:  fmt.Sprintf("%s needs a key. Without a key, string %s accepts only %s.", strings.Join(refused, ", "), op.command, strings.Join(allowed, ", ")),
		helpURL:  helpURL,
		nextStep: needsKey,
	}
}
