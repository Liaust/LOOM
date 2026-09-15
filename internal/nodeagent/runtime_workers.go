package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/nodeagent/macmount"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/version"
)

type pollOptions struct {
	maxMessages int
	flush       bool
}

type OutboxFlushRun struct {
	Submitted     int                       `json:"submitted"`
	Done          int                       `json:"done"`
	Failed        int                       `json:"failed"`
	ManualAction  int                       `json:"manual_action"`
	RequeuedStale int                       `json:"requeued_stale"`
	Items         []OutboxFlushItem         `json:"items"`
	Summary       noderuntime.OutboxSummary `json:"summary"`
}

type OutboxFlushItem struct {
	LocalOutboxID    string                       `json:"local_outbox_id"`
	Kind             string                       `json:"kind"`
	Status           string                       `json:"status"`
	PreviousStatus   string                       `json:"previous_status"`
	LegacyPath       string                       `json:"legacy_path,omitempty"`
	ErrorCode        string                       `json:"error_code,omitempty"`
	ErrorMessage     string                       `json:"error_message,omitempty"`
	Ack              *communication.AckResult     `json:"ack,omitempty"`
	CapabilityResult *routing.RemoteResultOutcome `json:"capability_result,omitempty"`
}

type heartbeatRuntime struct{}

func (heartbeatRuntime) Kind() string {
	return noderuntime.KindHeartbeat
}

func (heartbeatRuntime) RunOnce(ctx context.Context, runtimeStore noderuntime.Store, env noderuntime.Env, instance noderuntime.WorkerInstance) (noderuntime.RunResult, error) {
	store, config, state, err := loadRuntimeNodeAgentState(env)
	if err != nil {
		return nodeRuntimeFailure(err), nil
	}
	envelope, err := sendHeartbeatCore(ctx, store, config, state, runtimeStore, correlation.Normalize(env.CorrelationID), heartbeatRequestOptions{
		reportedStatus: "ok",
		runtimeVersion: version.Current().Version,
	})
	if err != nil {
		return nodeRuntimeFailure(err), nil
	}
	return noderuntime.RunResult{
		Status:       noderuntime.RunStatusSucceeded,
		HealthStatus: noderuntime.WorkerStatusHealthy,
		Message:      "heartbeat sent",
		ResultJSON:   mustMarshalJSON(envelope.Data),
	}, nil
}

type pollRuntime struct {
	options pollOptions
}

func (pollRuntime) Kind() string {
	return noderuntime.KindPoll
}

func (r pollRuntime) RunOnce(ctx context.Context, runtimeStore noderuntime.Store, env noderuntime.Env, instance noderuntime.WorkerInstance) (noderuntime.RunResult, error) {
	result, err := pollOnceCore(ctx, env, runtimeStore, r.options)
	if err != nil {
		return nodeRuntimeFailure(err), nil
	}
	return noderuntime.RunResult{
		Status:       noderuntime.RunStatusSucceeded,
		HealthStatus: noderuntime.WorkerStatusHealthy,
		Message:      "poll completed",
		ResultJSON:   mustMarshalJSON(result),
	}, nil
}

type outboxFlusherRuntime struct {
	maxItems int
}

func (outboxFlusherRuntime) Kind() string {
	return noderuntime.KindOutboxFlusher
}

func (r outboxFlusherRuntime) RunOnce(ctx context.Context, runtimeStore noderuntime.Store, env noderuntime.Env, instance noderuntime.WorkerInstance) (noderuntime.RunResult, error) {
	run, err := flushDueOutbox(ctx, env, runtimeStore, r.maxItems)
	if err != nil {
		return nodeRuntimeFailure(err), nil
	}
	health := noderuntime.WorkerStatusHealthy
	message := "outbox flush completed"
	if run.ManualAction > 0 {
		health = noderuntime.WorkerStatusRequiresManualAction
		message = "outbox flush requires manual action"
	} else if run.Failed > 0 {
		health = noderuntime.WorkerStatusOfflineQueueing
		message = "outbox flush left retryable failures"
	}
	return noderuntime.RunResult{
		Status:       noderuntime.RunStatusSucceeded,
		HealthStatus: health,
		Message:      message,
		ResultJSON:   mustMarshalJSON(run),
	}, nil
}

type placeholderRuntime struct {
	kind string
}

func (r placeholderRuntime) Kind() string {
	return r.kind
}

func (r placeholderRuntime) RunOnce(ctx context.Context, store noderuntime.Store, env noderuntime.Env, instance noderuntime.WorkerInstance) (noderuntime.RunResult, error) {
	return noderuntime.RunResult{
		Status:       noderuntime.RunStatusSkipped,
		HealthStatus: noderuntime.WorkerStatusDegraded,
		Message:      "worker runtime will be implemented in Slice 08 Part 3",
		ResultJSON: mustMarshalJSON(map[string]any{
			"implemented": false,
			"kind":        r.kind,
		}),
	}, nil
}

func nodeAgentRuntimeRegistry() noderuntime.Registry {
	return noderuntime.NewRegistry(
		noderuntime.SelfcheckRunner{},
		noderuntime.QueueReporterRunner{},
		applicationCapacityRuntime{},
		heartbeatRuntime{},
		pollRuntime{options: pollOptions{maxMessages: 10, flush: false}},
		outboxFlusherRuntime{maxItems: 50},
		watchedRootRuntime{},
		storageMountRuntime{},
		laneHousekeepingRuntime{},
	)
}

type laneHousekeepingRuntime struct{}

type laneHousekeepingRuntimeConfig struct {
	BoxRootPath      string `json:"box_root_path,omitempty"`
	RuntimeStateRoot string `json:"runtime_state_root,omitempty"`
	StateRelPath     string `json:"state_rel_path,omitempty"`
}

func (laneHousekeepingRuntime) Kind() string {
	return noderuntime.KindLaneHousekeeping
}

func (laneHousekeepingRuntime) RunOnce(ctx context.Context, runtimeStore noderuntime.Store, env noderuntime.Env, instance noderuntime.WorkerInstance) (noderuntime.RunResult, error) {
	select {
	case <-ctx.Done():
		return noderuntime.RunResult{}, ctx.Err()
	default:
	}
	workerConfig := laneHousekeepingRuntimeConfig{}
	if len(instance.ConfigJSON) > 0 {
		if err := json.Unmarshal(instance.ConfigJSON, &workerConfig); err != nil {
			return nodeRuntimeFailure(fmt.Errorf("decode Lane housekeeping worker config: %w", err)), nil
		}
	}
	rootPath := strings.TrimSpace(workerConfig.BoxRootPath)
	runtimeStateRoot := strings.TrimSpace(workerConfig.RuntimeStateRoot)
	nodeID := ""
	nodeRole := ""
	if rootPath == "" || runtimeStateRoot == "" {
		_, config, state, err := loadRuntimeNodeAgentState(env)
		if err != nil {
			if rootPath == "" {
				return nodeRuntimeFailure(err), nil
			}
		} else {
			if rootPath == "" {
				rootPath = config.BoxRootPath
			}
			if runtimeStateRoot == "" && filepath.Clean(strings.TrimSpace(config.BoxRootPath)) == filepath.Clean(rootPath) {
				runtimeStateRoot = config.BoxStateRoot
			}
			if filepath.Clean(strings.TrimSpace(config.BoxRootPath)) == filepath.Clean(rootPath) {
				nodeID = firstNonEmptyNodeValue(state.NodeID, config.NodeKey)
				nodeRole = config.NodeRole
			}
		}
	}
	if rootPath != "" && runtimeStateRoot == "" {
		runtimeStateRoot = filepath.Join(filepath.Dir(rootPath), ".loom-box-state")
	}
	resolved, err := box.Resolve(box.ResolveInput{
		ConfiguredPath:   rootPath,
		RuntimeStateRoot: runtimeStateRoot,
		NodeID:           nodeID,
		NodeRole:         nodeRole,
	})
	if err != nil {
		return nodeRuntimeFailure(err), nil
	}
	rootPath = resolved.RootPath
	stateResolution, err := box.ResolveRuntimeState(resolved)
	if err != nil {
		return nodeRuntimeFailure(err), nil
	}
	runtimeStateRoot = stateResolution.WriteRoot
	result, err := lane.Housekeep(lane.HousekeepingInput{
		RootPath:     rootPath,
		StateRelPath: workerConfig.StateRelPath,
		StatePath:    filepath.Join(runtimeStateRoot, "lane"),
	})
	if err != nil {
		return nodeRuntimeFailure(err), nil
	}
	runStatus := noderuntime.RunStatusSucceeded
	message := "Lane recovery housekeeping completed"
	if result.Status == "noop" {
		runStatus = noderuntime.RunStatusSkipped
		message = "Lane recovery housekeeping found no due work"
	}
	return noderuntime.RunResult{
		Status:           runStatus,
		HealthStatus:     noderuntime.WorkerStatusHealthy,
		Message:          message,
		ResultJSON:       mustMarshalJSON(result),
		QueueSummaryJSON: mustMarshalJSON(result.RecoveryStorage),
	}, nil
}

func firstNonEmptyNodeValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type storageMountRuntime struct{}

func (storageMountRuntime) Kind() string {
	return noderuntime.KindStorageMount
}

func (storageMountRuntime) RunOnce(ctx context.Context, runtimeStore noderuntime.Store, env noderuntime.Env, instance noderuntime.WorkerInstance) (noderuntime.RunResult, error) {
	manager, err := macmount.NewManager(env.DataDir)
	if err != nil {
		return nodeRuntimeFailure(err), nil
	}
	status, err := manager.RepairOnce(ctx, macmount.RepairOptions{})
	if err != nil {
		return nodeRuntimeFailure(err), nil
	}

	health := noderuntime.WorkerStatusHealthy
	message := "storage mount policy is disabled"
	desiredMounts := 0
	mountedMounts := 0
	if status.DesiredState == macmount.DesiredMounted {
		desiredMounts++
		if status.ActualState == macmount.ActualMounted {
			mountedMounts++
			message = "LOOM Main storage is mounted"
		} else {
			message = "LOOM Main storage is not mounted"
			health = noderuntime.WorkerStatusDegraded
			switch status.LastErrorCategory {
			case "smb_keychain":
				health = noderuntime.WorkerStatusRequiresManualAction
				message = "SMB credentials are missing from Keychain"
			case "smb_auth":
				health = noderuntime.WorkerStatusRequiresManualAction
				message = "SMB credentials were rejected; reconnect in Finder and save the password in Keychain"
			case "host_resolution", "wireguard_route", "tcp_445":
				health = noderuntime.WorkerStatusOfflineQueueing
			case "platform", "mount_smbfs", "security", "mount_path":
				health = noderuntime.WorkerStatusBlocked
			}
			if strings.TrimSpace(status.LastErrorMessage) != "" {
				message = status.LastErrorMessage
			}
		}
	}
	if status.CloudStorage != nil && status.CloudStorage.DesiredState == macmount.DesiredMounted {
		desiredMounts++
		if status.CloudStorage.ActualState == macmount.ActualMounted {
			mountedMounts++
			if status.DesiredState != macmount.DesiredMounted {
				message = "LOOM Cloud storage is mounted"
			}
		} else {
			cloudMessage := "LOOM Cloud storage is not mounted"
			if strings.TrimSpace(status.CloudStorage.LastErrorMessage) != "" {
				cloudMessage = status.CloudStorage.LastErrorMessage
			}
			message = cloudMessage
			switch status.CloudStorage.LastErrorCategory {
			case "finder_auth", "smb_keychain":
				health = noderuntime.WorkerStatusRequiresManualAction
			case "host_resolution", "tcp_445":
				if health == noderuntime.WorkerStatusHealthy {
					health = noderuntime.WorkerStatusOfflineQueueing
				}
			case "platform", "open", "mount_path", "cloud_link":
				health = noderuntime.WorkerStatusBlocked
			default:
				if health == noderuntime.WorkerStatusHealthy {
					health = noderuntime.WorkerStatusDegraded
				}
			}
		}
	}
	if desiredMounts > 1 && mountedMounts == desiredMounts {
		message = "LOOM Main storage and LOOM Cloud storage are mounted"
	}

	return noderuntime.RunResult{
		Status:       noderuntime.RunStatusSucceeded,
		HealthStatus: health,
		Message:      message,
		ResultJSON:   mustMarshalJSON(status),
	}, nil
}

func loadRuntimeNodeAgentState(env noderuntime.Env) (Store, Config, State, error) {
	store := Store{
		ConfigPath: env.ConfigPath,
		StatePath:  env.StatePath,
		DataDir:    env.DataDir,
	}
	config, err := store.LoadConfig()
	if err != nil {
		return Store{}, Config{}, State{}, err
	}
	state, err := store.LoadState()
	if err != nil {
		return Store{}, Config{}, State{}, err
	}
	return store, config, state, nil
}

func sendHeartbeatCore(ctx context.Context, store Store, config Config, state State, runtimeStore noderuntime.Store, correlationID string, heartbeatOpts heartbeatRequestOptions) (response.Envelope[nodes.Heartbeat], error) {
	if strings.TrimSpace(state.NodeID) == "" {
		return response.Envelope[nodes.Heartbeat]{}, errors.New("node credential is not imported: missing node_id")
	}
	if strings.TrimSpace(state.CredentialToken) == "" {
		return response.Envelope[nodes.Heartbeat]{}, errors.New("node credential is not imported: missing credential_token")
	}
	client, err := NewClient(config.MainURL)
	if err != nil {
		return response.Envelope[nodes.Heartbeat]{}, err
	}
	summary, summaryErr := runtimeStore.BuildLocalSummary()
	inboxBacklog := heartbeatOpts.inboxBacklog
	outboxBacklog := heartbeatOpts.outboxBacklog
	storageStatus := map[string]any{
		"status":   "ok",
		"data_dir": store.DataDir,
	}
	errorSummary := map[string]any{
		"status": "none",
	}
	metadata := map[string]any{
		"source":   "loom-node-agent",
		"node_key": config.NodeKey,
		"slice":    "08_part_2",
	}
	if summaryErr == nil {
		_ = runtimeStore.SaveLatestSummary(summary)
		if inboxBacklog == 0 {
			inboxBacklog = summary.Queues.InboxPending
		}
		if outboxBacklog == 0 {
			outboxBacklog = summary.Queues.OutboxPending + summary.Queues.OutboxFailed + summary.Queues.OutboxManualAction
		}
		storageStatus["runtime"] = map[string]any{
			"highest_status": summary.HighestStatus,
			"queues":         summary.Queues,
			"workers":        summary.Workers,
		}
		metadata["runtime"] = summary
	} else {
		errorSummary = map[string]any{
			"status":  "runtime_summary_failed",
			"message": summaryErr.Error(),
		}
	}
	reportedAt := time.Now().UTC()
	input := nodes.HeartbeatInput{
		NodeRef:           state.NodeID,
		CredentialToken:   state.CredentialToken,
		RuntimeVersion:    strings.TrimSpace(heartbeatOpts.runtimeVersion),
		ReportedStatus:    strings.TrimSpace(heartbeatOpts.reportedStatus),
		InboxBacklog:      inboxBacklog,
		OutboxBacklog:     outboxBacklog,
		ReportedAt:        &reportedAt,
		StorageStatusJSON: objectJSON(storageStatus),
		ErrorSummaryJSON:  objectJSON(errorSummary),
		Metadata:          objectJSON(metadata),
	}
	envelope, err := client.Heartbeat(ctx, correlation.Normalize(correlationID), input)
	if err != nil {
		return response.Envelope[nodes.Heartbeat]{}, err
	}
	state.LastHeartbeat = &HeartbeatState{
		NodeHeartbeatID: envelope.Data.NodeHeartbeatID,
		PresenceState:   envelope.Data.PresenceState,
		ReportedStatus:  envelope.Data.ReportedStatus,
		ReceivedAt:      envelope.Data.ReceivedAt,
	}
	if err := store.SaveState(state); err != nil {
		return response.Envelope[nodes.Heartbeat]{}, err
	}
	return envelope, nil
}

func pollOnceCore(ctx context.Context, env noderuntime.Env, runtimeStore noderuntime.Store, options pollOptions) (PollRunResult, error) {
	store, config, state, err := loadRuntimeNodeAgentState(env)
	if err != nil {
		return PollRunResult{}, err
	}
	if strings.TrimSpace(state.NodeID) == "" {
		return PollRunResult{}, errors.New("node credential is not imported: missing node_id")
	}
	if strings.TrimSpace(state.CredentialToken) == "" {
		return PollRunResult{}, errors.New("node credential is not imported: missing credential_token")
	}
	client, err := NewClient(config.MainURL)
	if err != nil {
		return PollRunResult{}, err
	}
	maxMessages := options.maxMessages
	if maxMessages <= 0 {
		maxMessages = 10
	}
	pollEnvelope, err := client.Poll(ctx, correlation.Normalize(env.CorrelationID), communication.PollInput{
		NodeRef:         state.NodeID,
		CredentialToken: state.CredentialToken,
		MaxMessages:     maxMessages,
		Metadata: objectJSON(map[string]any{
			"source":   "loom-node-agent",
			"node_key": config.NodeKey,
			"slice":    "08_part_2",
		}),
	})
	if err != nil {
		return PollRunResult{}, err
	}
	result := PollRunResult{
		Poll:      pollEnvelope.Data,
		Processed: []ProcessedMessage{},
	}
	for _, message := range pollEnvelope.Data.Messages {
		processed, err := processPolledMessage(ctx, client, store, runtimeStore, config, state, env.CorrelationID, message, options.flush)
		if err != nil {
			return PollRunResult{}, err
		}
		result.Processed = append(result.Processed, processed)
	}
	lastMessageID := ""
	if len(pollEnvelope.Data.Messages) > 0 {
		lastMessageID = pollEnvelope.Data.Messages[len(pollEnvelope.Data.Messages)-1].CommunicationMessageID
	}
	state.LastPoll = &PollState{
		PollCompletedAt: time.Now().UTC(),
		MessagesClaimed: len(pollEnvelope.Data.Messages),
		MessagesAcked:   countFlushedAcks(result.Processed),
		LastMessageID:   lastMessageID,
	}
	if err := store.SaveState(state); err != nil {
		return PollRunResult{}, err
	}
	return result, nil
}

func processPolledMessage(ctx context.Context, client Client, store Store, runtimeStore noderuntime.Store, config Config, state State, fallbackCorrelationID string, message communication.Message, flush bool) (ProcessedMessage, error) {
	messageRaw := mustMarshalJSON(message)
	inboxItem := noderuntime.InboxItem{
		LocalInboxID: message.CommunicationMessageID,
		MessageID:    message.CommunicationMessageID,
		Kind:         message.Kind,
		Status:       noderuntime.OutboxStatusPending,
		PayloadJSON:  messageRaw,
	}
	if _, err := runtimeStore.SaveInboxItem(inboxItem); err != nil {
		return ProcessedMessage{}, err
	}
	inboxPath, err := store.SaveInboxMessage(message)
	if err != nil {
		return ProcessedMessage{}, err
	}

	ackInput := buildAckInput(state, message)
	if message.Kind == communication.KindProtectedFolderPreflight {
		ackInput = buildProtectedFolderPreflightAck(config, state, store, message)
	}
	if message.Kind == communication.KindProtectedFolderReconcile {
		ackInput = buildProtectedFolderReconcileAck(config, state, store, message)
	}
	if message.Kind == communication.KindProjectWatchReconcile {
		ackInput = BuildProjectWatchReconcileAck(ctx, store, state, message)
	}
	var resultInput *routing.RemoteResultInput
	if message.Kind == communication.KindCapabilityDispatch {
		remoteResultInput, err := buildCapabilityResultInput(ctx, config, state, store, message)
		if err != nil {
			ackInput.AckStatus = communication.AckStatusRejected
			ackInput.ResultJSON = objectJSON(map[string]any{})
			ackInput.ErrorJSON = objectJSON(map[string]any{
				"code":    "capability_dispatch.invalid",
				"summary": err.Error(),
				"kind":    message.Kind,
			})
		} else {
			resultInput = &remoteResultInput
		}
	}

	var applicationOutbox *noderuntime.OutboxItem
	var applicationDispatch routing.RemoteDispatchPayload
	if message.Kind == communication.KindCapabilityDispatch && json.Unmarshal(message.PayloadJSON, &applicationDispatch) == nil && applicationDispatchOperation(config, applicationDispatch) != "" {
		// Never ACK a failed durability barrier. Main must redeliver the dispatch.
		if resultInput == nil {
			return ProcessedMessage{}, errors.New("application result was not durably frozen")
		}
		queued, e := queueApplicationResult(runtimeStore, *resultInput)
		if e != nil {
			return ProcessedMessage{}, e
		}
		applicationOutbox = &queued
	}

	ackOutbox, err := runtimeStore.QueueOutbox(noderuntime.OutboxItem{
		Kind:            noderuntime.OutboxKindMessageAck,
		Status:          noderuntime.OutboxStatusPending,
		IdempotencyKey:  ackInput.IdempotencyKey,
		CorrelationID:   nodeAgentMessageCorrelationID(fallbackCorrelationID, message),
		PayloadJSON:     sanitizedJSON(ackInput),
		SourceWorkerKey: noderuntime.WorkerKeyPoll,
		SourceMessageID: message.CommunicationMessageID,
	})
	if err != nil {
		return ProcessedMessage{}, err
	}

	messageCorrelationID := nodeAgentMessageCorrelationID(fallbackCorrelationID, message)
	processed := ProcessedMessage{
		CommunicationMessageID: message.CommunicationMessageID,
		Kind:                   message.Kind,
		AckStatus:              ackInput.AckStatus,
		InboxPath:              inboxPath,
		RuntimeAckOutboxID:     ackOutbox.LocalOutboxID,
		RuntimeAckOutboxStatus: ackOutbox.Status,
	}
	if flush {
		flushResult := flushOutboxItem(ctx, client, store, runtimeStore, state, ackOutbox)
		processed.RuntimeAckOutboxStatus = flushResult.Status
		processed.RuntimeFlushError = flushResult.ErrorMessage
		processed.OutboxPath = flushResult.LegacyPath
		if flushResult.Ack != nil {
			processed.Ack = *flushResult.Ack
		}
	}
	if resultInput != nil {
		var resultOutbox noderuntime.OutboxItem
		if applicationOutbox != nil {
			resultOutbox = *applicationOutbox
		} else {
			resultOutbox, err = runtimeStore.QueueOutbox(noderuntime.OutboxItem{
				Kind:            noderuntime.OutboxKindCapabilityResult,
				Status:          noderuntime.OutboxStatusPending,
				IdempotencyKey:  resultInput.IdempotencyKey,
				CorrelationID:   messageCorrelationID,
				PayloadJSON:     sanitizedJSON(*resultInput),
				SourceWorkerKey: noderuntime.WorkerKeyPoll,
				SourceMessageID: message.CommunicationMessageID,
			})
		}
		if err != nil {
			return ProcessedMessage{}, err
		}
		processed.RuntimeCapabilityResultOutboxID = resultOutbox.LocalOutboxID
		processed.RuntimeCapabilityResultOutboxStatus = resultOutbox.Status
		if flush {
			flushResult := flushOutboxItem(ctx, client, store, runtimeStore, state, resultOutbox)
			processed.RuntimeCapabilityResultOutboxStatus = flushResult.Status
			if flushResult.ErrorMessage != "" {
				processed.RuntimeFlushError = flushResult.ErrorMessage
			}
			processed.CapabilityResultOutboxPath = flushResult.LegacyPath
			processed.CapabilityResult = flushResult.CapabilityResult
		}
	}
	if _, err := runtimeStore.MoveInboxItem(inboxItem, noderuntime.OutboxStatusPending, noderuntime.OutboxStatusDone); err != nil {
		return ProcessedMessage{}, err
	}
	return processed, nil
}

func countFlushedAcks(processed []ProcessedMessage) int {
	count := 0
	for _, item := range processed {
		if item.RuntimeAckOutboxStatus == noderuntime.OutboxStatusDone {
			count++
		}
	}
	return count
}

func flushDueOutbox(ctx context.Context, env noderuntime.Env, runtimeStore noderuntime.Store, maxItems int) (OutboxFlushRun, error) {
	store, config, state, err := loadRuntimeNodeAgentState(env)
	if err != nil {
		return OutboxFlushRun{}, err
	}
	if strings.TrimSpace(state.NodeID) == "" {
		return OutboxFlushRun{}, errors.New("node credential is not imported: missing node_id")
	}
	if strings.TrimSpace(state.CredentialToken) == "" {
		return OutboxFlushRun{}, errors.New("node credential is not imported: missing credential_token")
	}
	client, err := NewClient(config.MainURL)
	if err != nil {
		return OutboxFlushRun{}, err
	}
	if err := runtimeStore.RestoreApplicationResults(128); err != nil {
		return OutboxFlushRun{}, err
	}
	requeued, err := runtimeStore.RequeueStaleInflight(5 * time.Minute)
	if err != nil {
		return OutboxFlushRun{}, err
	}
	if maxItems <= 0 {
		maxItems = 50
	}
	due, err := runtimeStore.ListDueOutbox(time.Now().UTC(), maxItems)
	if err != nil {
		return OutboxFlushRun{}, err
	}
	run := OutboxFlushRun{
		Submitted:     len(due),
		RequeuedStale: requeued,
		Items:         []OutboxFlushItem{},
	}
	for _, item := range due {
		result := flushOutboxItem(ctx, client, store, runtimeStore, state, item)
		run.Items = append(run.Items, result)
		switch result.Status {
		case noderuntime.OutboxStatusDone:
			run.Done++
		case noderuntime.OutboxStatusManualAction:
			run.ManualAction++
		case noderuntime.OutboxStatusFailed:
			run.Failed++
		}
	}
	summary, err := runtimeStore.OutboxSummary()
	if err != nil {
		return OutboxFlushRun{}, err
	}
	run.Summary = summary
	return run, nil
}

func flushOutboxItem(ctx context.Context, client Client, store Store, runtimeStore noderuntime.Store, state State, item noderuntime.OutboxItem) OutboxFlushItem {
	result := OutboxFlushItem{
		LocalOutboxID:  item.LocalOutboxID,
		Kind:           item.Kind,
		PreviousStatus: item.Status,
	}
	inflight, _, err := runtimeStore.MarkOutboxInflight(item)
	if err != nil {
		result.Status = item.Status
		result.ErrorCode = "node_agent.outbox_mark_inflight_failed"
		result.ErrorMessage = err.Error()
		return result
	}
	switch inflight.Kind {
	case noderuntime.OutboxKindMessageAck:
		result = sendAckOutboxItem(ctx, client, store, runtimeStore, state, inflight, result)
	case noderuntime.OutboxKindCapabilityResult:
		result = sendCapabilityResultOutboxItem(ctx, client, store, runtimeStore, state, inflight, result)
	case noderuntime.OutboxKindWorkerSummary:
		done, _, err := runtimeStore.MarkOutboxDone(inflight, mustMarshalJSON(map[string]any{"local_only": true}))
		result.Status = done.Status
		if err != nil {
			result.ErrorCode = "node_agent.outbox_done_failed"
			result.ErrorMessage = err.Error()
		}
	default:
		manual, _, err := runtimeStore.MarkOutboxManualAction(inflight, "node_agent.outbox_unsupported_kind", fmt.Sprintf("unsupported outbox kind %s", inflight.Kind))
		result.Status = manual.Status
		result.ErrorCode = manual.LastErrorCode
		result.ErrorMessage = manual.LastErrorMessage
		if err != nil {
			result.ErrorMessage = err.Error()
		}
	}
	return result
}

func sendAckOutboxItem(ctx context.Context, client Client, store Store, runtimeStore noderuntime.Store, state State, item noderuntime.OutboxItem, result OutboxFlushItem) OutboxFlushItem {
	var input communication.AckInput
	if err := json.Unmarshal(item.PayloadJSON, &input); err != nil {
		return markOutboxManualAction(runtimeStore, item, result, "node_agent.outbox_payload_invalid", err)
	}
	input.NodeRef = state.NodeID
	input.CredentialToken = state.CredentialToken
	envelope, err := client.Ack(ctx, item.CorrelationID, input.IdempotencyKey, input)
	if err != nil {
		return markOutboxSendFailure(runtimeStore, item, result, err)
	}
	legacyPath, err := store.SaveOutboxAck(envelope.Data)
	if err != nil {
		return markOutboxSendFailure(runtimeStore, item, result, err)
	}
	done, _, err := runtimeStore.MarkOutboxDone(item, mustMarshalJSON(envelope.Data))
	result.Status = done.Status
	result.LegacyPath = legacyPath
	result.Ack = &envelope.Data
	if err != nil {
		result.ErrorCode = "node_agent.outbox_done_failed"
		result.ErrorMessage = err.Error()
	}
	return result
}

func sendCapabilityResultOutboxItem(ctx context.Context, client Client, store Store, runtimeStore noderuntime.Store, state State, item noderuntime.OutboxItem, result OutboxFlushItem) OutboxFlushItem {
	var input routing.RemoteResultInput
	if err := json.Unmarshal(item.PayloadJSON, &input); err != nil {
		return markOutboxManualAction(runtimeStore, item, result, "node_agent.outbox_payload_invalid", err)
	}
	input.NodeRef = state.NodeID
	input.CredentialToken = state.CredentialToken
	envelope, err := client.CapabilityResult(ctx, item.CorrelationID, input.IdempotencyKey, input)
	if err != nil {
		return markOutboxSendFailure(runtimeStore, item, result, err)
	}
	legacyPath, err := store.SaveOutboxCapabilityResult(envelope)
	if err != nil {
		return markOutboxSendFailure(runtimeStore, item, result, err)
	}
	done, _, err := runtimeStore.MarkOutboxDone(item, mustMarshalJSON(envelope.Data))
	result.Status = done.Status
	result.LegacyPath = legacyPath
	result.CapabilityResult = &envelope.Data
	if err != nil {
		result.ErrorCode = "node_agent.outbox_done_failed"
		result.ErrorMessage = err.Error()
	}
	return result
}

func markOutboxSendFailure(runtimeStore noderuntime.Store, item noderuntime.OutboxItem, result OutboxFlushItem, err error) OutboxFlushItem {
	code, retryable := outboxErrorCodeAndRetryable(err)
	if !retryable || item.AttemptCount >= 10 {
		manual, _, markErr := runtimeStore.MarkOutboxManualAction(item, code, err.Error())
		result.Status = manual.Status
		result.ErrorCode = manual.LastErrorCode
		result.ErrorMessage = manual.LastErrorMessage
		if markErr != nil {
			result.ErrorMessage = markErr.Error()
		}
		return result
	}
	next := time.Now().UTC().Add(noderuntime.ComputeBackoff(item.AttemptCount))
	failed, _, markErr := runtimeStore.MarkOutboxFailed(item, code, err.Error(), &next)
	result.Status = failed.Status
	result.ErrorCode = failed.LastErrorCode
	result.ErrorMessage = failed.LastErrorMessage
	if markErr != nil {
		result.ErrorMessage = markErr.Error()
	}
	return result
}

func markOutboxManualAction(runtimeStore noderuntime.Store, item noderuntime.OutboxItem, result OutboxFlushItem, code string, err error) OutboxFlushItem {
	manual, _, markErr := runtimeStore.MarkOutboxManualAction(item, code, err.Error())
	result.Status = manual.Status
	result.ErrorCode = manual.LastErrorCode
	result.ErrorMessage = manual.LastErrorMessage
	if markErr != nil {
		result.ErrorMessage = markErr.Error()
	}
	return result
}

func outboxErrorCodeAndRetryable(err error) (string, bool) {
	var remoteErr RemoteRequestError
	if errors.As(err, &remoteErr) {
		code := remoteErr.Envelope.Error.Code
		if strings.TrimSpace(code) == "" {
			code = fmt.Sprintf("http_%d", remoteErr.StatusCode)
		}
		switch {
		case remoteErr.StatusCode == 429 || remoteErr.StatusCode >= 500:
			return code, true
		default:
			return code, false
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return "network_error", true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return "network_error", true
	}
	return "node_agent.outbox_send_failed", true
}

func nodeRuntimeFailure(err error) noderuntime.RunResult {
	return noderuntime.RunResult{
		Status:       noderuntime.RunStatusFailed,
		HealthStatus: classifyNodeRuntimeError(err),
		Message:      err.Error(),
		ResultJSON: mustMarshalJSON(map[string]any{
			"error": err.Error(),
		}),
	}
}

func classifyNodeRuntimeError(err error) string {
	if err == nil {
		return noderuntime.WorkerStatusHealthy
	}
	var remoteErr RemoteRequestError
	if errors.As(err, &remoteErr) {
		switch {
		case remoteErr.StatusCode == 401 || remoteErr.StatusCode == 403:
			return noderuntime.WorkerStatusBlocked
		case remoteErr.StatusCode >= 500 || remoteErr.StatusCode == 429:
			return noderuntime.WorkerStatusOfflineQueueing
		default:
			return noderuntime.WorkerStatusDegraded
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return noderuntime.WorkerStatusOfflineQueueing
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return noderuntime.WorkerStatusOfflineQueueing
	}
	if strings.Contains(err.Error(), "missing credential") || strings.Contains(err.Error(), "credential is not imported") {
		return noderuntime.WorkerStatusBlocked
	}
	return noderuntime.WorkerStatusDegraded
}

func sanitizedJSON(value any) json.RawMessage {
	raw := mustMarshalJSON(value)
	return noderuntime.RedactRawJSON(raw)
}

func mustMarshalJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
