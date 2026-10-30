package service

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

// The two embedded copies under opencode_free_bun_templates/ are the audited
// model-neutral templates (deploy/opencode/balanced-text-profile.v2.json and
// deploy/opencode/profiles/big-pickle-agent-balanced.v1.json). A test keeps the
// bytes in sync with deploy/. New official free models are admitted by naming
// pattern and their profiles are derived here with the exact byte substitution
// deploy/opencode/gen_profiles.py uses, so the gateway and the worker agree on
// profile ids and SHA-256 values without any registry update.

//go:embed opencode_free_bun_templates/text.json
var openCodeFreeTextTemplate []byte

//go:embed opencode_free_bun_templates/agent.json
var openCodeFreeAgentTemplate []byte

const (
	openCodeFreeTextTemplateID  = "opencode-big-pickle-text-balanced-20261027-v2"
	openCodeFreeAgentTemplateID = "opencode-big-pickle-agent-balanced-20261027-v1"
	openCodeFreeDynamicStatus   = "candidate-unvalidated"
	openCodeFreeMaxModelLength  = 64
)

// openCodeFreeDynamicModel matches the official free model naming scheme
// ("<provider>-<family>-free"). Uppercase, separators outside [a-z0-9.-] and
// non "-free" names never match; pinned models keep working regardless.
var openCodeFreeDynamicModel = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*-free$`)

// openCodeFreeAdmissible reports whether a mapped upstream model may use the
// Bun transport: the pinned registry first, then any "-free" model that is
// also usable on the standard chat completions endpoint.
func openCodeFreeAdmissible(model string) bool {
	if _, ok := openCodeFreeModels[model]; ok {
		return true
	}
	if model == "" || len(model) > openCodeFreeMaxModelLength || !openCodeFreeDynamicModel.MatchString(model) {
		return false
	}
	return !IsOpenCodeUnsupportedModel(model)
}

// openCodeFreeDeriveProfile returns the profile reference (and raw bytes) for
// an admitted non-pinned model. The substitution mirrors gen_profiles.py: the
// quoted template id is replaced once, the status value becomes
// "candidate-unvalidated", and exactly two "big-pickle" model fields take the
// model name. Pinned models must never reach this function; their bytes are
// audited and differ from derivation (big-pickle v2, mimo's contract).
func openCodeFreeDeriveProfile(model, mode string) (openCodeFreeProfileRef, []byte, bool) {
	var raw []byte
	var templateID, id string
	switch mode {
	case openCodeFreeModeText:
		raw, templateID = openCodeFreeTextTemplate, openCodeFreeTextTemplateID
		id = "opencode-" + model + "-text-balanced-20261027-v1"
	case openCodeFreeModeAgent:
		raw, templateID = openCodeFreeAgentTemplate, openCodeFreeAgentTemplateID
		id = "opencode-" + model + "-agent-balanced-20261027-v1"
	default:
		return openCodeFreeProfileRef{}, nil, false
	}
	derived, err := openCodeFreeSubstituteTemplate(raw, templateID, id, model, openCodeFreeDynamicStatus)
	if err != nil {
		return openCodeFreeProfileRef{}, nil, false
	}
	sum := sha256.Sum256(derived)
	return openCodeFreeProfileRef{ID: id, SHA256: hex.EncodeToString(sum[:])}, derived, true
}

func openCodeFreeSubstituteTemplate(raw []byte, templateID, id, model, status string) ([]byte, error) {
	text := string(raw)
	quoted := `"` + templateID + `"`
	if strings.Count(text, quoted) != 1 {
		return nil, errors.New("template id not unique")
	}
	text = strings.Replace(text, quoted, `"`+id+`"`, 1)
	const statusKey = `"status": `
	if strings.Count(text, statusKey) != 1 {
		return nil, errors.New("template status not unique")
	}
	head, tail, _ := strings.Cut(text, statusKey)
	if len(tail) < 2 {
		return nil, errors.New("template status value missing")
	}
	closing := strings.IndexByte(tail[1:], '"')
	if closing < 0 {
		return nil, errors.New("template status value missing")
	}
	text = head + statusKey + `"` + status + `"` + tail[closing+2:]
	if strings.Count(text, "big-pickle") != 2 {
		return nil, errors.New("unexpected big-pickle occurrences")
	}
	text = strings.ReplaceAll(text, "big-pickle", model)
	return []byte(text), nil
}
