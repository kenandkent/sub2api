package service

// The Bun transport admits the pinned official free models plus any new
// official "-free" model whose profile is derived from the embedded audited
// templates (see opencode_free_bun_dynamic.go). Pinned models use immutable
// profile bytes and their SHA-256 values pin what deploy/opencode/profiles/
// ships; derived models get the exact same bytes gen_profiles.py would write.
// Everything else is rejected before any dispatch.
type openCodeFreeProfileRef struct {
	ID     string
	SHA256 string
}

type openCodeFreeModelProfiles struct {
	Text  openCodeFreeProfileRef
	Agent openCodeFreeProfileRef
}

const (
	openCodeFreeModeText  = "text"
	openCodeFreeModeAgent = "agent"
)

// openCodeFreeProfile is the account marker: an opt-in acknowledgement that
// stays pinned to the original big-pickle text profile so existing credentials
// and records stay valid. openCodeFreeProfileHash pins that same profile's
// bytes; per-request accounting records the profile actually dispatched.
const openCodeFreeProfile = "opencode-big-pickle-text-balanced-20261027-v2"
const openCodeFreeProfileHash = "da80fdbfd8112be5433856044945bbf09f926d06c1a4dc59b70624ab8bd9a0a9"

var openCodeFreeModels = map[string]openCodeFreeModelProfiles{
	"big-pickle": {
		Text:  openCodeFreeProfileRef{ID: "opencode-big-pickle-text-balanced-20261027-v2", SHA256: "da80fdbfd8112be5433856044945bbf09f926d06c1a4dc59b70624ab8bd9a0a9"},
		Agent: openCodeFreeProfileRef{ID: "opencode-big-pickle-agent-balanced-20261027-v1", SHA256: "23927e109355a85b84bc0e671feda37f7f492dc297427737f2f0f51b89b8f966"},
	},
	"glm-5.3-flash-free": {
		Text:  openCodeFreeProfileRef{ID: "opencode-glm-5.3-flash-free-text-balanced-20261027-v1", SHA256: "dac0af4c37cbe611d2dc9d797c82c7f91d0cf7a0c3d0503a7b26587360211d2b"},
		Agent: openCodeFreeProfileRef{ID: "opencode-glm-5.3-flash-free-agent-balanced-20261027-v1", SHA256: "c45b8b2876f50959f32b221a53f4016409d994067dd845925be35cf69018fb3d"},
	},
	"deepseek-v4.1-flash-free": {
		Text:  openCodeFreeProfileRef{ID: "opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1", SHA256: "d8173f3ab87eb5e559aeed02b196a0832c7c7b509c365f0adee4ed37f7784135"},
		Agent: openCodeFreeProfileRef{ID: "opencode-deepseek-v4.1-flash-free-agent-balanced-20261027-v1", SHA256: "650835e45c22d68862c91f19fbaf270dc35d71705f3affbd8e30d168e03bb18b"},
	},
	"beam-free": {
		Text:  openCodeFreeProfileRef{ID: "opencode-beam-free-text-balanced-20261027-v1", SHA256: "fc29629f303532831088b8eea5d341d6cbb52e7776551929ad207221d6fe3131"},
		Agent: openCodeFreeProfileRef{ID: "opencode-beam-free-agent-balanced-20261027-v1", SHA256: "01ea2cdc9c740d313e2aa4f226433c9b357bafc765af3233123370916275f4c7"},
	},
	"flint-40b-free": {
		Text:  openCodeFreeProfileRef{ID: "opencode-flint-40b-free-text-balanced-20261027-v1", SHA256: "f0a5bdc0a1a5cbc7aecfa077afd3699f319116102bd5b48976afe93ab3ea4b81"},
		Agent: openCodeFreeProfileRef{ID: "opencode-flint-40b-free-agent-balanced-20261027-v1", SHA256: "16b72bfff900fcebbd99dc978f5ee630c5bd606953fe884380bda767e0ec6a94"},
	},
	"mimo-v2.6-flash-free": {
		Text:  openCodeFreeProfileRef{ID: "opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1", SHA256: "5d8c67ddedd624f1837dd47503948069de6b03a29ff5b5252c0f81fd7b58cc52"},
		Agent: openCodeFreeProfileRef{ID: "opencode-mimo-v2.6-flash-free-agent-balanced-20261027-v1", SHA256: "86278247b87dc05c0a213a4789b24ec3b9d7d4f8cdd810139e37356f27f4c99f"},
	},
	"nemotron-3-ultra-free": {
		Text:  openCodeFreeProfileRef{ID: "opencode-nemotron-3-ultra-free-text-balanced-20261027-v1", SHA256: "3a49a35f0328cadaeb0045917f3d316f22e92b0213ae1f0d72daff48a08dc97e"},
		Agent: openCodeFreeProfileRef{ID: "opencode-nemotron-3-ultra-free-agent-balanced-20261027-v1", SHA256: "b4fd8481c30acbe4c3626243070d7092bb251d3d72d2d4fc916316ac265103e3"},
	},
	"nemotron-3.5-lightning-free": {
		Text:  openCodeFreeProfileRef{ID: "opencode-nemotron-3.5-lightning-free-text-balanced-20261027-v1", SHA256: "bc4da2490694fe2c61a28c00056a298d736d8a44941cf11f4784ef72aa9307b2"},
		Agent: openCodeFreeProfileRef{ID: "opencode-nemotron-3.5-lightning-free-agent-balanced-20261027-v1", SHA256: "4475acfb221fdc3a075e2082e4b1337be78d81b49349cafaf72934ca132e7b3d"},
	},
	"nemotron-3.5-super-vl-free": {
		Text:  openCodeFreeProfileRef{ID: "opencode-nemotron-3.5-super-vl-free-text-balanced-20261027-v1", SHA256: "fd0741e75a212749894fc4d155c24904291b5162b857f617be6f2bcea71343ae"},
		Agent: openCodeFreeProfileRef{ID: "opencode-nemotron-3.5-super-vl-free-agent-balanced-20261027-v1", SHA256: "21c601a7346f9f295bf4f0ae16430b9f057999ecef2ca8874631ee0aba0f23cd"},
	},
}

// openCodeFreeProfileFor returns the profile reference for a mapped upstream
// model. Pinned models always win — their audited bytes differ from derivation
// (big-pickle text v2, mimo's generation contract) — and admitted non-pinned
// models derive both their id and SHA-256 from the embedded templates.
func openCodeFreeProfileFor(model, mode string) (openCodeFreeProfileRef, bool) {
	if profiles, ok := openCodeFreeModels[model]; ok {
		switch mode {
		case openCodeFreeModeText:
			return profiles.Text, true
		case openCodeFreeModeAgent:
			return profiles.Agent, true
		default:
			return openCodeFreeProfileRef{}, false
		}
	}
	if !openCodeFreeAdmissible(model) {
		return openCodeFreeProfileRef{}, false
	}
	ref, _, ok := openCodeFreeDeriveProfile(model, mode)
	return ref, ok
}
