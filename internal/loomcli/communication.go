package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
)

func newCommunicationCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "communication",
		Short: "Inspect LOOM node communication state",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "health",
		Short: "Show communication backlog and presence counters",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.CommunicationHealth(ctx, correlationID)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect communication health.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderCommunicationHealth(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newMessagesCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "messages",
		Short: "List LOOM communication messages",
	}
	filter := communication.MessageFilter{Limit: 50}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List durable node communication messages",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListMessages(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list communication messages.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, message := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), message.CommunicationMessageID)
				}
				return nil
			}
			renderCommunicationMessageList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of messages to return")
	listCmd.Flags().StringVar(&filter.NodeRef, "node", "", "filter by node ID or key")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by message status")
	listCmd.Flags().StringVar(&filter.Kind, "kind", "", "filter by message kind")
	listCmd.Flags().StringVar(&filter.Direction, "direction", "", "filter by direction")
	listCmd.Flags().StringVar(&filter.CorrelationID, "correlation", "", "filter by correlation ID")
	listCmd.Flags().StringVar(&filter.IdempotencyKey, "idempotency-key", "", "filter by message idempotency key")
	cmd.AddCommand(listCmd)
	return cmd
}

func newMessageCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "message",
		Short: "Inspect and enqueue LOOM communication messages",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <message-ref>",
		Short: "Inspect a communication message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetMessage(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect communication message.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderCommunicationMessage(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	enqueueOpts := struct {
		nodeRef        string
		kind           string
		inputText      string
		inputFile      string
		idempotencyKey string
	}{kind: communication.KindMainPing}
	enqueueCmd := &cobra.Command{
		Use:   "enqueue",
		Short: "Enqueue a main-to-node communication message",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			payload, err := readCapabilityInput(enqueueOpts.inputText, enqueueOpts.inputFile)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("communication.payload_invalid", "communication", "payload", "Communication payload is invalid.", err))
			}
			client, idempotencyKey := withEffectIdempotency(client, enqueueOpts.idempotencyKey, "communication.message.enqueue."+enqueueOpts.nodeRef+"."+enqueueOpts.kind)
			envelope, err := client.EnqueueMessage(ctx, correlationID, communication.EnqueueInput{
				NodeRef:        enqueueOpts.nodeRef,
				Kind:           enqueueOpts.kind,
				PayloadJSON:    payload,
				IdempotencyKey: idempotencyKey,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not enqueue communication message.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.CommunicationMessageID)
				return nil
			}
			renderCommunicationMessage(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	enqueueCmd.Flags().StringVar(&enqueueOpts.nodeRef, "node", "", "target node ID or key")
	enqueueCmd.Flags().StringVar(&enqueueOpts.kind, "kind", communication.KindMainPing, "message kind")
	enqueueCmd.Flags().StringVar(&enqueueOpts.inputText, "input", "", "message payload JSON object")
	enqueueCmd.Flags().StringVar(&enqueueOpts.inputFile, "input-file", "", "path to message payload JSON object")
	enqueueCmd.Flags().StringVar(&enqueueOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	_ = enqueueCmd.MarkFlagRequired("node")
	cmd.AddCommand(enqueueCmd)
	return cmd
}

func renderCommunicationHealth(cmd *cobra.Command, health communication.Health) {
	fmt.Fprintf(cmd.OutOrStdout(), "Nodes: total=%d online=%d recently_seen=%d offline=%d\n", health.TotalNodes, health.OnlineNodes, health.RecentlySeenNodes, health.OfflineNodes)
	fmt.Fprintf(cmd.OutOrStdout(), "Messages: pending=%d failed=%d dead_letter=%d\n", health.PendingMessages, health.FailedMessages, health.DeadLetterMessages)
}

func renderCommunicationMessageList(cmd *cobra.Command, messages []communication.Message) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "MESSAGE ID\tNODE\tDIRECTION\tKIND\tSTATUS\tATTEMPTS\tCREATED")
	for _, message := range messages {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			message.CommunicationMessageID,
			message.NodeID,
			message.Direction,
			message.Kind,
			message.Status,
			message.AttemptCount,
			message.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderCommunicationMessage(cmd *cobra.Command, message communication.Message) {
	fmt.Fprintf(cmd.OutOrStdout(), "Message: %s\n", message.CommunicationMessageID)
	fmt.Fprintf(cmd.OutOrStdout(), "Node: %s\n", message.NodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Direction: %s\n", message.Direction)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s\n", message.Kind)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", message.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Idempotency: %s\n", ptrOrDash(message.IdempotencyKey))
	fmt.Fprintf(cmd.OutOrStdout(), "Correlation: %s\n", ptrOrDash(message.CorrelationID))
	fmt.Fprintf(cmd.OutOrStdout(), "Payload: %s\n", compactText(string(message.PayloadJSON), 240))
	fmt.Fprintf(cmd.OutOrStdout(), "Attempts: %d\n", message.AttemptCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Available: %s\n", message.AvailableAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", message.CreatedAt.UTC().Format(time.RFC3339))
}
