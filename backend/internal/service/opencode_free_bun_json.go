package service

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Reject duplicate members before interpreting or forwarding an event. Different
// JSON consumers otherwise disagree about which value wins.
func validateOpenCodeFreeJSON(payload string) error {
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 100 {
			return errors.New("JSON nesting too deep")
		}
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]struct{}{}
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := k.(string)
				if !ok {
					return errors.New("invalid object key")
				}
				if _, exists := keys[name]; exists {
					return errors.New("duplicate JSON member")
				}
				keys[name] = struct{}{}
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("invalid object")
			}
		case '[':
			for dec.More() {
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("invalid array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
