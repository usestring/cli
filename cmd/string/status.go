package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// keylessState is what the last keyless call from this machine said was left
// of the day's allowance, so string status can show it without spending any.
type keylessState struct {
	RequestsRemaining *int64    `json:"requestsRemaining,omitempty"`
	CreditsRemaining  *int64    `json:"creditsRemaining,omitempty"`
	At                time.Time `json:"at"`
}

func keylessStatePath(env func(string) (string, bool)) (string, bool) {
	home, err := homeDir(env)
	if err != nil {
		return "", false
	}
	return filepath.Join(underConfig("string")(home, env), "keyless.json"), true
}

// recordKeyless keeps a keyless response's allowance headers for string
// status. It is a convenience, so a failure to write is ignored.
func recordKeyless(rt runtime, header http.Header) {
	state := keylessState{
		RequestsRemaining: headerCount(header, "X-Keyless-Requests-Remaining"),
		CreditsRemaining:  headerCount(header, "X-Keyless-Credits-Remaining"),
		At:                rt.clock().UTC(),
	}
	if state.RequestsRemaining == nil && state.CreditsRemaining == nil {
		return
	}
	if path, ok := keylessStatePath(rt.env); ok {
		_ = writePrivate(path, state)
	}
}

func headerCount(header http.Header, name string) *int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(header.Get(name)), 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// accountBalance is GET /account/balance: dollars as JSON numbers.
type accountBalance struct {
	Remaining float64 `json:"remaining"`
	Held      float64 `json:"held"`
	Exhausted bool    `json:"exhausted"`
	AsOf      string  `json:"asOf"`
	Plan      *struct {
		Tier string  `json:"tier"`
		Name *string `json:"name"`
	} `json:"plan"`
	AutoTopUp *struct {
		Enabled                bool     `json:"enabled"`
		AmountUSD              *float64 `json:"amountUsd"`
		RemainingThisPeriodUSD *float64 `json:"remainingThisPeriodUsd"`
	} `json:"autoTopUp"`
}

type statusReport struct {
	Version string `json:"version"`
	Key     struct {
		Source  string `json:"source"`
		Email   string `json:"email,omitempty"`
		Refused int    `json:"refusedStatus,omitempty"`
	} `json:"key"`
	Balance           json.RawMessage `json:"balance"`
	RequestsPerSecond *float64        `json:"requestsPerSecond"`
	Keyless           *keylessState   `json:"keyless"`
}

func statusCommand(list []string, rt runtime) error {
	if done, err := noOptions(list, "status", rt); done {
		return err
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
	var report statusReport
	report.Version = version
	report.Balance = json.RawMessage("null")
	if value, _ := rt.env("STRING_API_KEY"); value != "" {
		report.Key.Source = "environment"
	} else if path, ok := credentialsPath(rt.env); ok && key != "" {
		report.Key.Source = "saved"
		if saved, err := loadCredentials(path); err == nil {
			report.Key.Email = saved.Email
		}
	} else {
		report.Key.Source = "none"
	}
	var balance *accountBalance
	var balanceProblem string
	if key != "" {
		data, err := caller.call(&operation{method: "GET", path: "/account/balance"})
		var refusal *failure
		switch {
		case err == nil:
			balance = &accountBalance{}
			if json.Unmarshal(data, balance) == nil {
				report.Balance = json.RawMessage(data)
			} else {
				balance, balanceProblem = nil, "the API's answer was not JSON"
			}
		case errors.As(err, &refusal) && (refusal.status == http.StatusUnauthorized || refusal.status == http.StatusForbidden):
			report.Key.Refused = refusal.status
		case errors.As(err, &refusal):
			balanceProblem = fmt.Sprintf("HTTP %d", refusal.status)
		default:
			balanceProblem = err.Error()
		}
		if balance != nil && balance.Plan != nil {
			report.RequestsPerSecond = planRate(caller, balance.Plan.Tier)
		}
	}
	if path, ok := keylessStatePath(rt.env); ok && key == "" {
		if data, err := os.ReadFile(path); err == nil {
			var state keylessState
			if json.Unmarshal(data, &state) == nil {
				report.Keyless = &state
			}
		}
	}
	if rt.json {
		encoded, err := json.Marshal(report)
		if err != nil {
			return err
		}
		if err := write(rt.stdout, append(encoded, '\n')); err != nil {
			return err
		}
	} else if err := write(rt.stdout, []byte(statusText(report, balance, balanceProblem, rt.clock()))); err != nil {
		return err
	}
	if report.Key.Refused != 0 {
		return &failure{exit: exitAuth, status: report.Key.Refused, code: "key_refused", message: fmt.Sprintf("The API refused the key (HTTP %d).", report.Key.Refused), nextStep: "check STRING_API_KEY, or run `string login`"}
	}
	return nil
}

// planRate is the plan's request rate from GET /pricing, or nil for a tier the
// price list doesn't carry, such as enterprise.
func planRate(caller apiClient, tier string) *float64 {
	data, err := caller.call(&operation{method: "GET", path: "/pricing"})
	if err != nil {
		return nil
	}
	var pricing struct {
		Plans []struct {
			ID                string  `json:"id"`
			RequestsPerSecond float64 `json:"requestsPerSecond"`
		} `json:"plans"`
		Free struct {
			RequestsPerSecond float64 `json:"requestsPerSecond"`
		} `json:"free"`
	}
	if json.Unmarshal(data, &pricing) != nil {
		return nil
	}
	if tier == "free" && pricing.Free.RequestsPerSecond > 0 {
		return &pricing.Free.RequestsPerSecond
	}
	for _, plan := range pricing.Plans {
		if plan.ID == tier {
			return &plan.RequestsPerSecond
		}
	}
	return nil
}

func statusText(report statusReport, balance *accountBalance, balanceProblem string, now time.Time) string {
	var out strings.Builder
	line := func(label, format string, values ...any) {
		fmt.Fprintf(&out, "%-9s %s\n", label, fmt.Sprintf(format, values...))
	}
	fmt.Fprintf(&out, "string %s\n", report.Version)
	switch report.Key.Source {
	case "environment":
		line("Key", "STRING_API_KEY from the environment")
	case "saved":
		if report.Key.Email != "" {
			line("Key", "saved by string login, as %s", report.Key.Email)
		} else {
			line("Key", "saved by string login")
		}
	default:
		line("Key", "none, so fetch and search run keyless")
	}
	if report.Key.Refused != 0 {
		line("", "refused by the API (HTTP %d)", report.Key.Refused)
	}
	switch {
	case balance != nil:
		left := fmt.Sprintf("$%.2f left", balance.Remaining)
		if balance.Exhausted {
			left = "$0.00 left: add funds at https://portal.usestring.ai/usage"
		}
		if at, err := time.Parse(time.RFC3339, balance.AsOf); err == nil {
			left += ", with usage counted to " + clockTime(at, now)
		}
		line("Balance", "%s", left)
		if balance.Held > 0 {
			line("Held", "$%.2f for crawls still running", balance.Held)
		}
		if balance.Plan != nil {
			name := balance.Plan.Tier
			if balance.Plan.Name != nil && *balance.Plan.Name != "" {
				name = *balance.Plan.Name
			}
			if report.RequestsPerSecond != nil {
				line("Plan", "%s, %s requests a second", name, strconv.FormatFloat(*report.RequestsPerSecond, 'f', -1, 64))
			} else {
				line("Plan", "%s", name)
			}
		}
		if top := balance.AutoTopUp; top != nil && balance.Plan != nil && balance.Plan.Tier != "free" {
			switch {
			case !top.Enabled:
				line("Top-up", "off")
			case top.AmountUSD != nil && top.RemainingThisPeriodUSD != nil:
				line("Top-up", "on, $%.2f at a time, $%.2f left this period", *top.AmountUSD, *top.RemainingThisPeriodUSD)
			default:
				line("Top-up", "on")
			}
		}
	case balanceProblem != "":
		line("Balance", "unavailable (%s); see https://portal.usestring.ai/usage", balanceProblem)
	}
	if report.Key.Source == "none" {
		state := report.Keyless
		if state == nil || state.At.UTC().Format(time.DateOnly) != now.UTC().Format(time.DateOnly) {
			line("Keyless", "no keyless call from this machine today; the allowance resets at 00:00 UTC")
		} else {
			var parts []string
			if state.RequestsRemaining != nil {
				parts = append(parts, fmt.Sprintf("%d requests", *state.RequestsRemaining))
			}
			if state.CreditsRemaining != nil {
				parts = append(parts, fmt.Sprintf("%d credits", *state.CreditsRemaining))
			}
			line("Keyless", "%s left today, as of this machine's last keyless call at %s", strings.Join(parts, " and "), clockTime(state.At, now))
		}
	}
	return out.String()
}

// clockTime shows a UTC time, with the date only when it isn't today.
func clockTime(at, now time.Time) string {
	at = at.UTC()
	if at.Format(time.DateOnly) == now.UTC().Format(time.DateOnly) {
		return at.Format("15:04 UTC")
	}
	return at.Format("Jan 2 15:04 UTC")
}
