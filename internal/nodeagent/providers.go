package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/version"
)

func newProvidersCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "Inspect local providers exposed by this node-agent",
	}
	var providerKey string
	localCmd := &cobra.Command{
		Use:   "local",
		Short: "Print the local provider advertisement payload",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			advertisement, err := buildProviderAdvertisement(config, state, providerKey)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), advertisement))
			}
			provider := advertisement.Provider
			_, err = fmt.Fprintf(opts.out, "provider=%s capabilities=%d node_id=%s\n", provider.CompactAddress, len(provider.Capabilities), emptyPlaceholder(state.NodeID))
			return err
		},
	}
	localCmd.Flags().StringVar(&providerKey, "provider", "system", "Provider key to inspect: system or filesystem")
	cmd.AddCommand(localCmd)
	return cmd
}

func newAdvertiseCommand(opts *rootOptions) *cobra.Command {
	var once bool
	var providerKey string
	cmd := &cobra.Command{
		Use:   "advertise",
		Short: "Advertise local providers to main for owner review",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !once {
				return errors.New("advertise currently requires --once")
			}
			envelope, err := opts.advertiseOnce(cmd.Context(), providerKey)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, envelope)
			}
			providerID := "-"
			providerAddress := "-"
			if envelope.Data.Provider != nil {
				providerID = envelope.Data.Provider.ProviderID
				providerAddress = envelope.Data.Provider.CompactAddress
			}
			_, err = fmt.Fprintf(opts.out, "advertisement=%s status=%s provider=%s address=%s\n", envelope.Data.Advertisement.ProviderAdvertisementID, envelope.Data.Advertisement.Status, providerID, providerAddress)
			return err
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Send one provider advertisement and exit")
	cmd.Flags().StringVar(&providerKey, "provider", "system", "Provider key to advertise: system or filesystem")
	return cmd
}

func (opts *rootOptions) advertiseOnce(ctx context.Context, providerKey string) (response.Envelope[capabilities.ProviderAdvertisementInspection], error) {
	store, config, state, err := opts.loadAll()
	if err != nil {
		return response.Envelope[capabilities.ProviderAdvertisementInspection]{}, err
	}
	if strings.TrimSpace(state.NodeID) == "" {
		return response.Envelope[capabilities.ProviderAdvertisementInspection]{}, errors.New("node credential is not imported: missing node_id")
	}
	if strings.TrimSpace(state.CredentialToken) == "" {
		return response.Envelope[capabilities.ProviderAdvertisementInspection]{}, errors.New("node credential is not imported: missing credential_token")
	}
	client, err := NewClient(config.MainURL)
	if err != nil {
		return response.Envelope[capabilities.ProviderAdvertisementInspection]{}, err
	}

	input, err := buildProviderAdvertisement(config, state, providerKey)
	if err != nil {
		return response.Envelope[capabilities.ProviderAdvertisementInspection]{}, err
	}
	input.CredentialToken = state.CredentialToken
	input.IdempotencyKey = "node-agent.provider-advertise." + state.NodeID + "." + input.Provider.ProviderKey
	envelope, err := client.AdvertiseProvider(ctx, correlation.Normalize(opts.correlationID), input.IdempotencyKey, input)
	if err != nil {
		return response.Envelope[capabilities.ProviderAdvertisementInspection]{}, err
	}

	providerID := ""
	providerAddress := input.Provider.CompactAddress
	if envelope.Data.Provider != nil {
		providerID = envelope.Data.Provider.ProviderID
		providerAddress = envelope.Data.Provider.CompactAddress
	}
	state.LastAdvertisement = &AdvertisementState{
		ProviderAdvertisementID: envelope.Data.Advertisement.ProviderAdvertisementID,
		Status:                  envelope.Data.Advertisement.Status,
		ProviderID:              providerID,
		ProviderAddress:         providerAddress,
		AdvertisedAt:            time.Now().UTC(),
	}
	if err := store.SaveState(state); err != nil {
		return response.Envelope[capabilities.ProviderAdvertisementInspection]{}, err
	}
	return envelope, nil
}

func buildLocalProviderAdvertisement(config Config, state State) capabilities.ProviderAdvertisementInput {
	return buildSystemProviderAdvertisement(config, state)
}

func buildProviderAdvertisement(config Config, state State, providerKey string) (capabilities.ProviderAdvertisementInput, error) {
	switch strings.ToLower(strings.TrimSpace(providerKey)) {
	case "", "system":
		return buildSystemProviderAdvertisement(config, state), nil
	case filesystemconnector.ProviderKey:
		nodeSegment := addressSegment(config.NodeKey)
		nodeRef := advertisementNodeRef(config, state)
		provider, err := filesystemconnector.BuildProvider(filesystemconnector.ProviderManifestInput{
			ScopeSegment: nodeSegment,
			NodeKey:      config.NodeKey,
			NodeID:       state.NodeID,
			DisplayName:  config.DisplayName,
			RuntimeClass: config.RuntimeClass,
			Version:      version.Current().Version,
			Config:       config.Filesystem,
		})
		if err != nil {
			return capabilities.ProviderAdvertisementInput{}, err
		}
		return capabilities.ProviderAdvertisementInput{
			NodeRef:  nodeRef,
			Provider: provider,
			Metadata: objectJSON(map[string]any{
				"source":    "loom-node-agent",
				"node_key":  config.NodeKey,
				"node_id":   state.NodeID,
				"slice":     "13_part_1",
				"connector": filesystemconnector.ProviderKey,
			}),
		}, nil
	default:
		return capabilities.ProviderAdvertisementInput{}, fmt.Errorf("unsupported provider %q; expected system or filesystem", providerKey)
	}
}

func buildSystemProviderAdvertisement(config Config, state State) capabilities.ProviderAdvertisementInput {
	nodeSegment := addressSegment(config.NodeKey)
	providerAddress := capabilities.NodeSystemProviderAddress(nodeSegment)
	echoAddress := providerAddress + ".echo"
	statusAddress := providerAddress + ".status.read"
	quiescenceAddress := providerAddress + ".project.archive.quiesce"
	nodeRef := advertisementNodeRef(config, state)
	return capabilities.ProviderAdvertisementInput{
		NodeRef: nodeRef,
		Provider: capabilities.AdvertisedProvider{
			ProviderKey:    capabilities.NodeSystemProviderKey(nodeSegment),
			CompactAddress: providerAddress,
			DisplayName:    config.DisplayName + " System Provider",
			Description:    "Local system provider exposed by loom-node-agent for node status and safe diagnostic echo calls.",
			ProviderType:   capabilities.ProviderTypeSystem,
			Version:        version.Current().Version,
			RuntimeProfileJSON: objectJSON(map[string]any{
				"agent":                    "loom-node-agent",
				"runtime_class":            config.RuntimeClass,
				"remote_execution_enabled": true,
			}),
			DocumentationRefsJSON: json.RawMessage(`[]`),
			Health: capabilities.ProviderHealthInput{
				HealthStatus:       capabilities.HealthStatusOK,
				AvailabilityStatus: capabilities.AvailabilityStatusAvailable,
				Message:            "local provider advertisement generated by loom-node-agent",
				DetailsJSON: objectJSON(map[string]any{
					"node_key": config.NodeKey,
					"node_id":  state.NodeID,
				}),
			},
			Capabilities: append([]capabilities.AdvertisedCapability{
				{
					EndpointName:                "echo",
					CompactAddress:              echoAddress,
					ClassNamespace:              "system",
					ClassName:                   "echo",
					DisplayName:                 "Echo",
					Description:                 "Return a caller-provided text payload from this node through the remote node-agent dispatch path.",
					Form:                        capabilities.CapabilityFormCommand,
					InputSchemaJSON:             json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"text":{"type":"string"}},"required":["text"]}`),
					OutputSchemaJSON:            json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"},"node_id":{"type":"string"}}}`),
					RiskLevel:                   capabilities.RiskLevelLow,
					ExecutionAuthorizationLevel: 1,
					SideEffectsJSON:             json.RawMessage(`{"side_effects":[]}`),
					VersionLabel:                version.Current().Version,
					ManifestJSON: objectJSON(map[string]any{
						"source":        "loom-node-agent",
						"handler":       "system.echo",
						"execution":     "remote_node",
						"slice_enabled": true,
					}),
					UsageDocuments: []capabilities.AdvertisedUsageDocument{
						{
							Title: "Use echo to verify a node-specific system provider",
							Body:  "Use this capability when you need to confirm that a workspace node advertised its local system provider and can execute a simple remote dispatch/result cycle through main.",
						},
					},
				},
				{
					EndpointName:                "status.read",
					CompactAddress:              statusAddress,
					ClassNamespace:              "system",
					ClassName:                   "status.read",
					DisplayName:                 "Read Node Status",
					Description:                 "Read basic status from this node-agent provider through the remote node-agent dispatch path.",
					Form:                        capabilities.CapabilityFormQuery,
					InputSchemaJSON:             json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
					OutputSchemaJSON:            json.RawMessage(`{"type":"object","properties":{"node_id":{"type":"string"},"node_key":{"type":"string"},"runtime_class":{"type":"string"},"version":{"type":"string"},"current_time":{"type":"string"},"queue_counts":{"type":"object"},"last_heartbeat":{"type":"object"}}}`),
					RiskLevel:                   capabilities.RiskLevelLow,
					ExecutionAuthorizationLevel: 1,
					SideEffectsJSON:             json.RawMessage(`{"side_effects":[]}`),
					VersionLabel:                version.Current().Version,
					ManifestJSON: objectJSON(map[string]any{
						"source":        "loom-node-agent",
						"handler":       "system.status.read",
						"execution":     "remote_node",
						"slice_enabled": true,
					}),
					UsageDocuments: []capabilities.AdvertisedUsageDocument{
						{
							Title: "Use status.read to inspect advertised workspace node status",
							Body:  "Use this capability when an agent or workflow needs a node-scoped status read from the workspace node that advertised the provider, including runtime version, current node-agent time, queue counts, and last heartbeat summary.",
						},
					},
				},
				{
					EndpointName:                "project.archive.quiesce",
					CompactAddress:              quiescenceAddress,
					ClassNamespace:              "system",
					ClassName:                   "project.archive.quiesce",
					DisplayName:                 "Quiesce Project Runtime",
					Description:                 "Stop and durably fence exact reviewed project runtime targets on this owner node.",
					Form:                        capabilities.CapabilityFormCommand,
					InputSchemaJSON:             projectArchiveQuiescenceInputSchema(),
					OutputSchemaJSON:            projectArchiveQuiescenceOutputSchema(),
					RiskLevel:                   capabilities.RiskLevelHigh,
					ExecutionAuthorizationLevel: 5,
					SideEffectsJSON:             json.RawMessage(`{"side_effects":["watched_root.disable","service.stop","project_archive.fence.write","project_archive.receipt.write"]}`),
					VersionLabel:                version.Current().Version,
					ManifestJSON: objectJSON(map[string]any{
						"source":                 "loom-node-agent",
						"handler":                projectArchiveQuiescenceHandler,
						"execution":              "remote_node",
						"explicit_authorization": true,
						"slice_enabled":          true,
					}),
					UsageDocuments: []capabilities.AdvertisedUsageDocument{{
						Title: "Quiesce exact project runtime targets before custody moves",
						Body:  "Use only for an authenticated, reviewed physical-project archive operation. The request must bind this node and every exact watched-root or service target.",
					}},
				},
			}, applicationCapabilities(providerAddress)...),
			UsageDocuments: []capabilities.AdvertisedUsageDocument{
				{
					Title: "Workspace node system provider",
					Body:  "This provider represents the local system surface of a workspace node. It is advertised to main for owner review, then becomes discoverable as node-specific capabilities after approval.",
				},
			},
			Metadata: objectJSON(map[string]any{
				"source":       "loom-node-agent",
				"node_key":     config.NodeKey,
				"node_id":      state.NodeID,
				"slice":        "11_part_2",
				"execution":    "remote_node_agent",
				"remote_calls": true,
			}),
		},
		Metadata: objectJSON(map[string]any{
			"source":   "loom-node-agent",
			"node_key": config.NodeKey,
			"node_id":  state.NodeID,
			"slice":    "11_part_2",
		}),
	}
}

func advertisementNodeRef(config Config, state State) string {
	nodeRef := state.NodeID
	if strings.TrimSpace(nodeRef) == "" {
		nodeRef = config.NodeKey
	}
	return nodeRef
}

func addressSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	out := strings.Trim(builder.String(), "-_")
	if out == "" {
		out = "node"
	}
	if out[0] < 'a' || out[0] > 'z' {
		out = "node-" + out
	}
	return out
}
