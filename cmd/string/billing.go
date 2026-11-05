package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// checkoutRequest is the portal's answer to an agent asking for credit. No
// money moves until a person signed in to String opens approvalUrl and pays,
// within the hour before expiresAt.
type checkoutRequest struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	Kind        string  `json:"kind"`
	AmountUSD   float64 `json:"amountUsd"`
	ApprovalURL string  `json:"approvalUrl"`
	ExpiresAt   string  `json:"expiresAt"`
}

const checkoutPoll = 5 * time.Second

var checkoutKinds = map[string]string{"credit_pack": "credit pack", "top_up": "top-up"}

func topupCommand(list []string, rt runtime) error {
	checking := len(list) > 0 && list[0] == "status"
	if checking {
		list = list[1:]
	}
	parsed, err := parse(list, spec{switches: []string{"wait"}, fields: map[string]kind{"packPlanId": integer}})
	if err != nil {
		return usage(err)
	}
	if parsed.help {
		return write(rt.stdout, []byte(help))
	}
	if _, ok := parsed.value("input"); ok {
		return usage(errors.New("--input does not apply to topup"))
	}
	wait := parsed.switches["wait"]
	if checking {
		if err := expectPositionals(parsed.positionals, 1, "string topup status ID [--wait]"); err != nil {
			return usage(err)
		}
		if len(parsed.fields) > 0 {
			return usage(errors.New("--packPlanId applies only to string topup"))
		}
	} else {
		if err := expectPositionals(parsed.positionals, 0, "string topup [--packPlanId ID]"); err != nil {
			return usage(err)
		}
		if wait {
			return usage(errors.New("--wait applies to string topup status ID, once the link is with the person who pays"))
		}
	}
	key, err := apiKey(rt.env)
	if err != nil {
		return &failure{exit: exitAuth, code: "invalid_key", message: err.Error()}
	}
	if key == "" {
		return &failure{exit: exitAuth, code: "key_required", message: "string topup needs a key.", helpURL: helpURL, nextStep: needsKey}
	}
	caller := apiClient{rt: rt, base: portalURL, key: key}
	op := &operation{method: "POST", path: "/api/agent/checkout", body: parsed.fields}
	if checking {
		op = &operation{method: "GET", path: "/api/agent/checkout/" + encodeURIComponent(parsed.positionals[0])}
	}
	data, checkout, err := checkoutCall(caller, op)
	if err != nil {
		return err
	}
	var ended error
	if wait {
		data, checkout, ended = waitForCheckout(caller, data, checkout)
		if data == nil {
			return ended
		}
	}
	if rt.json {
		if err := caller.emit(op, data); err != nil {
			return err
		}
	} else if ended == nil {
		if err := write(rt.stdout, []byte(checkoutText(checkout, rt.clock()))); err != nil {
			return err
		}
	}
	return ended
}

// checkoutCall sends op to the portal and reads its answer as a checkout
// request, so a sign-in page or any other stray 200 fails instead of printing.
func checkoutCall(caller apiClient, op *operation) ([]byte, checkoutRequest, error) {
	var checkout checkoutRequest
	data, err := caller.call(op)
	if err != nil {
		return nil, checkout, err
	}
	if json.Unmarshal(data, &checkout) != nil || checkout.ID == "" || checkout.Status == "" {
		return nil, checkout, &failure{exit: exitFailure, code: "unexpected_response", message: "The portal's answer named no checkout request."}
	}
	return data, checkout, nil
}

// waitForCheckout polls the request until it is paid or expires, keeping a
// progress line on a terminal. It returns the last answer.
func waitForCheckout(caller apiClient, data []byte, checkout checkoutRequest) ([]byte, checkoutRequest, error) {
	poll := &operation{method: "GET", path: "/api/agent/checkout/" + encodeURIComponent(checkout.ID)}
	shown := false
	defer func() {
		if shown {
			fmt.Fprintln(caller.rt.stderr)
		}
	}()
	for {
		switch checkout.Status {
		case "completed":
			return data, checkout, nil
		case "expired":
			return data, checkout, &failure{exit: exitFailure, code: "checkout_expired", message: fmt.Sprintf("The request for %s expired unpaid.", checkoutWhat(checkout)), nextStep: "run `string topup` for a new link"}
		}
		if caller.rt.progress {
			progress := "Waiting for approval until " + checkoutExpiry(checkout, caller.rt.clock())
			if checkout.Status == "processing" {
				progress = "Paid; waiting for the payment to clear"
			}
			fmt.Fprintf(caller.rt.stderr, "\r\x1b[K%s", progress)
			shown = true
		}
		caller.rt.pause(checkoutPoll)
		var err error
		if data, checkout, err = checkoutCall(caller, poll); err != nil {
			return nil, checkout, err
		}
	}
}

func checkoutWhat(c checkoutRequest) string {
	kind := checkoutKinds[c.Kind]
	if kind == "" {
		kind = "credit purchase"
	}
	return fmt.Sprintf("a $%.2f %s", c.AmountUSD, kind)
}

func checkoutExpiry(c checkoutRequest, now time.Time) string {
	at, err := time.Parse(time.RFC3339, c.ExpiresAt)
	if err != nil {
		return c.ExpiresAt
	}
	return clockTime(at, now)
}

// checkoutText says what the request buys and what is left to do.
func checkoutText(c checkoutRequest, now time.Time) string {
	switch c.Status {
	case "completed":
		return fmt.Sprintf("Paid: %s.\n", checkoutWhat(c))
	case "processing":
		return fmt.Sprintf("Paid for %s; the payment is still clearing.\n", checkoutWhat(c))
	case "expired":
		return fmt.Sprintf("The request for %s expired unpaid. string topup asks for a new link.\n", checkoutWhat(c))
	}
	return fmt.Sprintf("To add %s, open this link signed in to String, then approve and pay:\n%s\nIt expires at %s. string topup status %s --wait waits for the payment.\n",
		checkoutWhat(c), c.ApprovalURL, checkoutExpiry(c, now), c.ID)
}

// upgradePlans are the plans the portal's Usage & Plans page can preselect.
var upgradePlans = []string{"starter", "growth", "scale", "volume"}

func upgradeCommand(list []string, rt runtime) error {
	parsed, err := parseArgs(list)
	if err != nil {
		return usage(err)
	}
	if parsed.help {
		return write(rt.stdout, []byte(help))
	}
	if _, ok := parsed.value("input"); ok {
		return usage(errors.New("--input does not apply to upgrade"))
	}
	if len(parsed.positionals) > 1 {
		return usage(errors.New("Usage: string upgrade [PLAN]"))
	}
	link := portalURL + "/usage"
	if len(parsed.positionals) == 1 {
		plan := strings.ToLower(parsed.positionals[0])
		if !slices.Contains(upgradePlans, plan) {
			return usage(fmt.Errorf("Unknown plan %q. Choose one of: %s.", parsed.positionals[0], strings.Join(upgradePlans, ", ")))
		}
		link += "?plan=" + plan
	}
	if rt.json {
		encoded, err := json.Marshal(map[string]string{"url": link})
		if err != nil {
			return err
		}
		return write(rt.stdout, append(encoded, '\n'))
	}
	return write(rt.stdout, []byte(link+"\n"))
}
