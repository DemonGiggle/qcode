package llm

import "testing"

func TestInstanceIdentityPreservesProviderCapabilities(t *testing.T) {
	for _, kind := range Names() {
		t.Run(kind, func(t *testing.T) {
			cfg := Config{}
			if kind == OllamaCloudKind {
				cfg.APIKey = "fixture-key"
			}
			original, err := New(kind, cfg)
			if err != nil {
				t.Fatal(err)
			}
			cfg.InstanceName = "second-" + kind
			instance, err := New(kind, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if instance.Name() != "second-"+kind || Kind(instance) != kind || original.Name() != kind {
				t.Fatal("instance identity or kind lost")
			}
			_, wantModels := original.(ModelLister)
			_, gotModels := instance.(ModelLister)
			_, wantThinking := original.(ThinkingProvider)
			_, gotThinking := instance.(ThinkingProvider)
			_, wantContext := original.(ContextWindowProvider)
			_, gotContext := instance.(ContextWindowProvider)
			_, wantValidation := original.(ModelValidator)
			_, gotValidation := instance.(ModelValidator)
			if wantModels != gotModels || wantThinking != gotThinking || wantContext != gotContext || wantValidation != gotValidation {
				t.Fatal("instance changed optional capabilities")
			}
		})
	}
}
