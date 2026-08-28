package workflow

import (
	"errors"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

const bundledAdapterVersion = "1.0.0"

type ProviderReadiness struct {
	Descriptor contractsv1.ProviderDescriptor `json:"descriptor"`
	Ready      bool                           `json:"ready"`
	Code       string                         `json:"code"`
	Missing    []string                       `json:"missing"`
}

var bundledProviders = []contractsv1.ProviderDescriptor{
	providerDescriptor(contractsv1.ProviderIDCodex, "Codex", "agent-workflow-codex", []string{"OPENAI_API_KEY"}, contractsv1.ProviderCapabilityStreaming, contractsv1.ProviderCapabilityPolling, contractsv1.ProviderCapabilityScopedCancel, contractsv1.ProviderCapabilityResume, contractsv1.ProviderCapabilityStructuredOutput, contractsv1.ProviderCapabilityToolAllowlist, contractsv1.ProviderCapabilityUsage, contractsv1.ProviderCapabilityEventCursor),
	providerDescriptor(contractsv1.ProviderIDClaudeCode, "Claude Code", "agent-workflow-claude-code", []string{"ANTHROPIC_API_KEY"}, contractsv1.ProviderCapabilityStreaming, contractsv1.ProviderCapabilityPolling, contractsv1.ProviderCapabilityScopedCancel, contractsv1.ProviderCapabilityResume, contractsv1.ProviderCapabilityStructuredOutput, contractsv1.ProviderCapabilityToolAllowlist, contractsv1.ProviderCapabilityUsage, contractsv1.ProviderCapabilityEventCursor),
	providerDescriptor(contractsv1.ProviderIDPi, "Pi", "agent-workflow-pi", nil, contractsv1.ProviderCapabilityStreaming, contractsv1.ProviderCapabilityPolling, contractsv1.ProviderCapabilityScopedCancel, contractsv1.ProviderCapabilityResume, contractsv1.ProviderCapabilityToolAllowlist, contractsv1.ProviderCapabilityUsage, contractsv1.ProviderCapabilityEventCursor),
	providerDescriptor(contractsv1.ProviderIDOpenclaw, "OpenClaw", "agent-workflow-openclaw", nil, contractsv1.ProviderCapabilityStreaming, contractsv1.ProviderCapabilityPolling, contractsv1.ProviderCapabilityScopedCancel, contractsv1.ProviderCapabilityResume, contractsv1.ProviderCapabilityToolAllowlist, contractsv1.ProviderCapabilityInteractiveApproval, contractsv1.ProviderCapabilityUsage, contractsv1.ProviderCapabilityEventCursor),
	providerDescriptor(contractsv1.ProviderIDHermesAgent, "Hermes Agent", "agent-workflow-hermes", nil, contractsv1.ProviderCapabilityStreaming, contractsv1.ProviderCapabilityPolling, contractsv1.ProviderCapabilityScopedCancel, contractsv1.ProviderCapabilityResume, contractsv1.ProviderCapabilityToolAllowlist, contractsv1.ProviderCapabilityInteractiveApproval, contractsv1.ProviderCapabilityUsage, contractsv1.ProviderCapabilityEventCursor),
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
	descriptor, err := ProviderDescriptor(id)
	if err != nil {
		return ProviderReadiness{}, err
	}
	missing := make([]string, 0, len(descriptor.AuthEnvironment)+1)
	if _, err := exec.LookPath(descriptor.Executable); err != nil {
		missing = append(missing, "binary:"+descriptor.Executable)
	}
	for _, name := range descriptor.AuthEnvironment {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, "env:"+name)
		}
	}
	sort.Strings(missing)
	readiness := ProviderReadiness{Descriptor: descriptor, Ready: len(missing) == 0, Missing: missing}
	if readiness.Ready {
		readiness.Code = "ready"
	} else {
		readiness.Code = "unavailable"
	}
	return readiness, nil
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
