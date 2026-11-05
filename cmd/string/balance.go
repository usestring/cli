package main

import "encoding/json"

// balanceCommand prints GET /account/balance as the API sends it. Unlike
// string status, it fails when the API can't answer, so a script can check
// the exit status before reading the balance.
func balanceCommand(list []string, rt runtime) error {
	if done, err := noOptions(list, "balance", rt); done {
		return err
	}
	key, err := apiKey(rt.env)
	if err != nil {
		return &failure{exit: exitAuth, code: "invalid_key", message: err.Error()}
	}
	if key == "" {
		return &failure{exit: exitAuth, code: "key_required", message: "string balance needs a key.", helpURL: helpURL, nextStep: needsKey}
	}
	base, err := apiBaseURL(rt.env)
	if err != nil {
		return usage(err)
	}
	caller := apiClient{rt: rt, base: base, key: key}
	op := &operation{method: "GET", path: "/account/balance"}
	data, err := caller.call(op)
	if err != nil {
		return err
	}
	if json.Unmarshal(data, &accountBalance{}) != nil {
		return &failure{exit: exitFailure, code: "unexpected_response", message: "The API's answer was not a balance."}
	}
	return caller.emit(op, data)
}
