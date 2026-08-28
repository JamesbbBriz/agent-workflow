package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

const bundledAdapterVersion = "1.0.0"

type ProviderReadiness = contractsv1.ProviderReadiness

var bundledProviders = []contractsv1.ProviderDescriptor{
	providerDescriptor(contractsv1.ProviderIDCodex, "Codex", "agent-workflow-codex", []string{"OPENAI_API_KEY"}, contractsv1.ProviderCapabilityPolling, contractsv1.ProviderCapabilityScopedCancel, contractsv1.ProviderCapabilityStructuredOutput, contractsv1.ProviderCapabilityToolAllowlist, contractsv1.ProviderCapabilityEventCursor),
	providerDescriptor(contractsv1.ProviderIDClaudeCode, "Claude Code", "agent-workflow-claude-code", []string{"ANTHROPIC_API_KEY"}, contractsv1.ProviderCapabilityPolling, contractsv1.ProviderCapabilityScopedCancel, contractsv1.ProviderCapabilityStructuredOutput, contractsv1.ProviderCapabilityToolAllowlist, contractsv1.ProviderCapabilityEventCursor),
	providerDescriptor(contractsv1.ProviderIDPi, "Pi", "agent-workflow-pi", []string{"AGENT_WORKFLOW_PROVIDER_TOKEN"}, contractsv1.ProviderCapabilityPolling, contractsv1.ProviderCapabilityScopedCancel, contractsv1.ProviderCapabilityStructuredOutput, contractsv1.ProviderCapabilityToolAllowlist, contractsv1.ProviderCapabilityEventCursor),
	providerDescriptor(contractsv1.ProviderIDOpenclaw, "OpenClaw", "agent-workflow-openclaw", []string{"AGENT_WORKFLOW_PROVIDER_TOKEN"}, contractsv1.ProviderCapabilityPolling, contractsv1.ProviderCapabilityScopedCancel, contractsv1.ProviderCapabilityStructuredOutput, contractsv1.ProviderCapabilityToolAllowlist, contractsv1.ProviderCapabilityEventCursor),
	providerDescriptor(contractsv1.ProviderIDHermesAgent, "Hermes Agent", "agent-workflow-hermes", []string{"AGENT_WORKFLOW_PROVIDER_TOKEN"}, contractsv1.ProviderCapabilityPolling, contractsv1.ProviderCapabilityScopedCancel, contractsv1.ProviderCapabilityStructuredOutput, contractsv1.ProviderCapabilityToolAllowlist, contractsv1.ProviderCapabilityEventCursor),
}

var bundledUpstreams = map[contractsv1.ProviderID]string{
	contractsv1.ProviderIDCodex:       "codex",
	contractsv1.ProviderIDClaudeCode:  "claude",
	contractsv1.ProviderIDPi:          "pi",
	contractsv1.ProviderIDOpenclaw:    "openclaw",
	contractsv1.ProviderIDHermesAgent: "hermes",
}

func providerDescriptor(id contractsv1.ProviderID, name, executable string, auth []string, capabilities ...contractsv1.ProviderCapability) contractsv1.ProviderDescriptor {
	return contractsv1.ProviderDescriptor{
		Kind: contractsv1.ProviderDescriptorKindProviderDescriptor, SchemaVersion: 1,
		Id: id, DisplayName: name, AdapterVersion: bundledAdapterVersion, ProtocolVersion: 1,
		Executable: executable, Capabilities: capabilities, AuthEnvironment: auth,
	}
}

func BundledProviderDescriptors() []contractsv1.ProviderDescriptor {
	result := make([]contractsv1.ProviderDescriptor, len(bundledProviders))
	for i, descriptor := range bundledProviders {
		descriptor.Capabilities = append([]contractsv1.ProviderCapability{}, descriptor.Capabilities...)
		descriptor.AuthEnvironment = append([]string{}, descriptor.AuthEnvironment...)
		result[i] = descriptor
	}
	return result
}

func ProviderDescriptor(id contractsv1.ProviderID) (contractsv1.ProviderDescriptor, error) {
	for _, descriptor := range bundledProviders {
		if descriptor.Id == id {
			return BundledProviderDescriptors()[providerIndex(id)], nil
		}
	}
	return contractsv1.ProviderDescriptor{}, errors.New("unknown bundled provider")
}

func providerIndex(id contractsv1.ProviderID) int {
	for i := range bundledProviders {
		if bundledProviders[i].Id == id {
			return i
		}
	}
	return -1
}

func InspectProviderReadiness(id contractsv1.ProviderID) (ProviderReadiness, error) {
	return InspectProviderReadinessAt(id, "", "")
}

func InspectProviderReadinessAt(id contractsv1.ProviderID, stagedRoot, configRef string) (ProviderReadiness, error) {
	descriptor, err := ProviderDescriptor(id)
	if err != nil {
		return ProviderReadiness{}, err
	}
	missing := make([]string, 0, len(descriptor.AuthEnvironment)+1)
	if _, err := exec.LookPath(descriptor.Executable); err != nil {
		missing = append(missing, "binary:"+descriptor.Executable)
	}
	if upstream := bundledUpstreams[id]; upstream != "" {
		if _, err := ResolveBundledProviderUpstream(id); err != nil {
			missing = append(missing, "upstream:"+upstream)
		}
	}
	if _, err := sandboxDriver(); err != nil {
		missing = append(missing, "isolation:staged_subprocess")
	}
	if id == contractsv1.ProviderIDOpenclaw {
		if err := validateOpenClawProfile(stagedRoot, configRef); err != nil {
			missing = append(missing, "config:openclaw-agent-profile")
		}
	}
	for _, name := range descriptor.AuthEnvironment {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, "env:"+name)
		}
	}
	sort.Strings(missing)
	readiness := ProviderReadiness{Descriptor: descriptor, Ready: len(missing) == 0, Missing: missing}
	if readiness.Ready {
		readiness.Code = contractsv1.ProviderReadinessCodeReady
	} else {
		readiness.Code = contractsv1.ProviderReadinessCodeUnavailable
	}
	return readiness, nil
}

func ValidateBundledProviderProfile(profile contractsv1.ExecutorProfile, stagedRoot string) error {
	if len(profile.ToolAllowlist) != 1 || profile.ToolAllowlist[0] != "read-evidence" {
		return errors.New("bundled bridges support only the read-evidence tool authority")
	}
	if profile.ProviderId == contractsv1.ProviderIDOpenclaw {
		return validateOpenClawProfile(stagedRoot, profile.ConfigRef)
	}
	return nil
}

func validateOpenClawProfile(stagedRoot, configRef string) error {
	if stagedRoot == "" || filepath.Base(configRef) != configRef || configRef == "" || configRef == "." || configRef == ".." {
		return errors.New("OpenClaw isolated agent profile reference is invalid")
	}
	body, err := os.ReadFile(filepath.Join(stagedRoot, "input", "providers", configRef+".json"))
	if err != nil {
		return errors.New("OpenClaw isolated agent profile is unavailable")
	}
	var document struct {
		Agents struct {
			Entries map[string]struct {
				Workspace string `json:"workspace"`
				Tools     struct {
					Allow []string `json:"allow"`
				} `json:"tools"`
			} `json:"entries"`
		} `json:"agents"`
	}
	if json.Unmarshal(body, &document) != nil {
		return errors.New("OpenClaw isolated agent profile is invalid")
	}
	agent, ok := document.Agents.Entries[configRef]
	if !ok || agent.Workspace != "/workspace" || len(agent.Tools.Allow) != 1 || agent.Tools.Allow[0] != "read" {
		return errors.New("OpenClaw isolated agent profile does not enforce read-evidence")
	}
	return nil
}

// ResolveBundledProviderUpstream uses exactly the paths visible in the Linux
// Bubblewrap sandbox. User-local PATH entries are intentionally not advertised
// as runnable because the sandbox does not expose their runtime closure.
func ResolveBundledProviderUpstream(id contractsv1.ProviderID) (string, error) {
	name := bundledUpstreams[id]
	if name == "" {
		return "", errors.New("unknown bundled provider")
	}
	return resolveSystemExecutable(name)
}

func resolveSystemExecutable(name string) (string, error) {
	for _, root := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
		path := filepath.Join(root, name)
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || (!strings.HasPrefix(resolved, "/usr/") && !strings.HasPrefix(resolved, "/bin/")) {
			continue
		}
		if info, err := os.Stat(resolved); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return resolved, nil
		}
	}
	return "", errors.New("system executable is unavailable")
}

func SealExecutorProfile(profile contractsv1.ExecutorProfile) (contractsv1.ExecutorProfile, error) {
	profile.Capabilities = append([]contractsv1.ProviderCapability{}, profile.Capabilities...)
	profile.ToolAllowlist = append([]string{}, profile.ToolAllowlist...)
	profile.SecretRefs = append([]string{}, profile.SecretRefs...)
	sort.Slice(profile.Capabilities, func(i, j int) bool { return profile.Capabilities[i] < profile.Capabilities[j] })
	sort.Strings(profile.ToolAllowlist)
	sort.Strings(profile.SecretRefs)
	profile.ConfigHash = contractsv1.SHA256("sha256:" + strings.Repeat("0", 64))
	if err := contract.ValidateDefinition("ExecutorProfile", profile); err != nil {
		return contractsv1.ExecutorProfile{}, err
	}
	descriptor, err := ProviderDescriptor(profile.ProviderId)
	if err != nil {
		return contractsv1.ExecutorProfile{}, err
	}
	if profile.AdapterVersion != descriptor.AdapterVersion || profile.IsolationProfile != contractsv1.ProviderIsolationProfileStagedSubprocess {
		return contractsv1.ExecutorProfile{}, errors.New("executor profile does not match the bundled adapter")
	}
	allowed := make(map[contractsv1.ProviderCapability]bool, len(descriptor.Capabilities))
	for _, capability := range descriptor.Capabilities {
		allowed[capability] = true
	}
	for _, capability := range profile.Capabilities {
		if !allowed[capability] {
			return contractsv1.ExecutorProfile{}, errors.New("executor profile requests an unsupported capability")
		}
	}
	expectedSecrets := make([]string, len(descriptor.AuthEnvironment))
	for i, name := range descriptor.AuthEnvironment {
		expectedSecrets[i] = "env:" + name
	}
	sort.Strings(expectedSecrets)
	actualSecrets := append([]string{}, profile.SecretRefs...)
	sort.Strings(actualSecrets)
	if strings.Join(expectedSecrets, "\x00") != strings.Join(actualSecrets, "\x00") {
		return contractsv1.ExecutorProfile{}, errors.New("executor profile secret references do not match the bundled adapter")
	}
	hash, err := Digest(profile)
	if err != nil {
		return contractsv1.ExecutorProfile{}, err
	}
	profile.ConfigHash = contractsv1.SHA256(hash)
	if err := contract.ValidateDefinition("ExecutorProfile", profile); err != nil {
		return contractsv1.ExecutorProfile{}, err
	}
	return profile, nil
}

func VerifyExecutorProfile(profile contractsv1.ExecutorProfile) error {
	expected, err := SealExecutorProfile(profile)
	if err != nil {
		return err
	}
	if expected.ConfigHash != profile.ConfigHash {
		return errors.New("executor profile configuration hash is invalid")
	}
	return nil
}
