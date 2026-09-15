package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/response"
)

func newRealtimeCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "realtime",
		Short: "Inspect and operate LOOM realtime primitives",
	}
	cmd.AddCommand(newRealtimeTopicsCommand(opts))
	cmd.AddCommand(newRealtimeTopicCommand(opts))
	cmd.AddCommand(newRealtimeSubscriptionsCommand(opts))
	cmd.AddCommand(newRealtimeSubscriptionCommand(opts))
	cmd.AddCommand(newRealtimePresenceCommand(opts))
	cmd.AddCommand(newRealtimeNotificationsCommand(opts))
	cmd.AddCommand(newRealtimeNotificationCommand(opts))
	cmd.AddCommand(newRealtimeProgressCommand(opts))
	cmd.AddCommand(newRealtimeLeasesCommand(opts))
	cmd.AddCommand(newRealtimeLeaseCommand(opts))
	return cmd
}

func newRealtimeTopicsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "topics",
		Short: "List realtime topics",
	}
	filter := realtime.TopicFilter{Limit: 50}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List realtime topics",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListRealtimeTopics(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list realtime topics.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, topic := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), topic.TopicID)
				}
				return nil
			}
			renderRealtimeTopicList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of topics to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by topic status")
	listCmd.Flags().StringVar(&filter.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	cmd.AddCommand(listCmd)
	return cmd
}

func newRealtimeTopicCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "topic",
		Short: "Inspect, create, and publish realtime topics",
	}
	createInput := realtime.CreateTopicInput{}
	var createIdempotencyKey string
	createCmd := &cobra.Command{
		Use:   "create <topic-path>",
		Short: "Create a realtime topic",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			input := createInput
			input.TopicPath = args[0]
			input.RetentionMode = strings.TrimSpace(input.RetentionMode)
			input.DeliveryClass = strings.TrimSpace(input.DeliveryClass)
			if err := validateRealtimeTopicCreateInput(input); err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client, _ := withEffectIdempotency(commandCtx.Client, createIdempotencyKey, "realtime.topic.create."+args[0])
			envelope, err := client.CreateRealtimeTopic(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not create realtime topic.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.TopicID)
				return nil
			}
			renderRealtimeTopic(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	createCmd.Flags().StringVar(&createInput.DisplayName, "display-name", "", "topic display name")
	createCmd.Flags().StringVar(&createInput.ScopeRef, "scope", "", "scope ID, key, or slug")
	createCmd.Flags().StringVar(&createInput.RetentionMode, "retention", "", "retention mode: retain_latest, retain_bounded, durable_event_only")
	createCmd.Flags().StringVar(&createInput.DeliveryClass, "delivery-class", "", "delivery class: polling, actor_inbox, main_outbox")
	createCmd.Flags().StringVar(&createIdempotencyKey, "idempotency-key", "", "retry key for this topic creation")

	cmd.AddCommand(createCmd)
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <topic>",
		Short: "Inspect a realtime topic",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetRealtimeTopic(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect realtime topic.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRealtimeTopic(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	publishInput := realtime.PublishInput{}
	var publishPayload string
	var publishPayloadFile string
	var publishIdempotencyKey string
	publishCmd := &cobra.Command{
		Use:   "publish <topic>",
		Short: "Publish a retained realtime topic message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			payload, err := readCapabilityInput(publishPayload, publishPayloadFile)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("realtime.payload_invalid", "realtime", "payload", "Realtime publish payload is invalid.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			input := publishInput
			input.Payload = payload
			client, _ := withEffectIdempotency(commandCtx.Client, publishIdempotencyKey, "realtime.topic.publish."+args[0])
			envelope, err := client.PublishRealtimeTopic(ctx, commandCtx.CorrelationID, args[0], input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not publish realtime topic.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.TopicPublicationID)
				return nil
			}
			renderRealtimePublication(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	publishCmd.Flags().StringVar(&publishInput.MessageType, "type", "", "message type")
	publishCmd.Flags().StringVar(&publishPayload, "input", "", "publication payload JSON object")
	publishCmd.Flags().StringVar(&publishPayloadFile, "input-file", "", "path to publication payload JSON object")
	publishCmd.Flags().StringVar(&publishInput.PayloadSchemaRef, "payload-schema", "", "optional payload schema reference")
	publishCmd.Flags().StringVar(&publishInput.CausationRef, "causation", "", "optional causation reference")
	publishCmd.Flags().BoolVar(&publishInput.MeaningfulEvent, "event", false, "also emit a durable lifecycle event for this publication")
	publishCmd.Flags().StringVar(&publishIdempotencyKey, "idempotency-key", "", "retry key for this publication")
	cmd.AddCommand(publishCmd)
	return cmd
}

func validateRealtimeTopicCreateInput(input realtime.CreateTopicInput) error {
	if input.RetentionMode != "" && !realtime.ValidRetentionMode(input.RetentionMode) {
		return loomerrors.New(
			"realtime.retention_mode_invalid",
			"realtime",
			"retention",
			"Invalid realtime topic retention mode. Allowed values: retain_latest, retain_bounded, durable_event_only.",
		)
	}
	if input.DeliveryClass != "" && !realtime.ValidDeliveryClass(input.DeliveryClass) {
		return loomerrors.New(
			"realtime.delivery_class_invalid",
			"realtime",
			"delivery_class",
			"Invalid realtime topic delivery class. Allowed values: polling, actor_inbox, main_outbox.",
		)
	}
	return nil
}

func newRealtimeSubscriptionsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subscriptions",
		Short: "List realtime subscriptions",
	}
	filter := realtime.SubscriptionFilter{Limit: 50}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List realtime subscriptions",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListRealtimeSubscriptions(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list realtime subscriptions.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, subscription := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), subscription.SubscriptionID)
				}
				return nil
			}
			renderRealtimeSubscriptionList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of subscriptions to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by subscription status")
	listCmd.Flags().StringVar(&filter.TopicRef, "topic", "", "filter by topic ID or path")
	cmd.AddCommand(listCmd)
	return cmd
}

func newRealtimeSubscriptionCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subscription",
		Short: "Inspect and operate realtime subscriptions",
	}
	createInput := realtime.CreateSubscriptionInput{}
	var createIdempotencyKey string
	createCmd := &cobra.Command{
		Use:   "create <topic>",
		Short: "Create a polling subscription to a realtime topic",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			input := createInput
			input.TopicRef = args[0]
			client, _ := withEffectIdempotency(commandCtx.Client, createIdempotencyKey, "realtime.subscription.create."+args[0])
			envelope, err := client.CreateRealtimeSubscription(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not create realtime subscription.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.SubscriptionID)
				return nil
			}
			renderRealtimeSubscription(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	createCmd.Flags().StringVar(&createInput.CursorMode, "cursor", "", "cursor mode: from_now or from_start")
	createCmd.Flags().StringVar(&createInput.DeliveryTargetRef, "delivery-target", "", "optional delivery target reference")
	createCmd.Flags().StringVar(&createIdempotencyKey, "idempotency-key", "", "retry key for this subscription creation")
	cmd.AddCommand(createCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <subscription>",
		Short: "Inspect a realtime subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetRealtimeSubscription(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect realtime subscription.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRealtimeSubscription(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	pollInput := realtime.PollSubscriptionInput{Limit: 50}
	pollCmd := &cobra.Command{
		Use:   "poll <subscription>",
		Short: "Poll retained publications for a realtime subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.PollRealtimeSubscription(ctx, commandCtx.CorrelationID, args[0], pollInput)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not poll realtime subscription.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, publication := range envelope.Data.Publications {
					fmt.Fprintln(cmd.OutOrStdout(), publication.TopicPublicationID)
				}
				return nil
			}
			renderRealtimePollResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	pollCmd.Flags().IntVar(&pollInput.Limit, "limit", 50, "maximum number of publications to return")
	cmd.AddCommand(pollCmd)

	var ackInput realtime.AcknowledgeSubscriptionInput
	var ackIdempotencyKey string
	ackCmd := &cobra.Command{
		Use:   "ack <subscription>",
		Short: "Acknowledge a realtime subscription sequence",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client, _ := withEffectIdempotency(commandCtx.Client, ackIdempotencyKey, "realtime.subscription.ack."+args[0])
			envelope, err := client.AcknowledgeRealtimeSubscription(ctx, commandCtx.CorrelationID, args[0], ackInput)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not acknowledge realtime subscription.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.CursorSequence)
				return nil
			}
			renderRealtimeSubscription(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	ackCmd.Flags().Int64Var(&ackInput.Sequence, "sequence", 0, "publication sequence to acknowledge")
	ackCmd.Flags().StringVar(&ackIdempotencyKey, "idempotency-key", "", "retry key for this acknowledgement")
	_ = ackCmd.MarkFlagRequired("sequence")
	cmd.AddCommand(ackCmd)

	var cancelIdempotencyKey string
	cancelCmd := &cobra.Command{
		Use:   "cancel <subscription>",
		Short: "Cancel a realtime subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client, _ := withEffectIdempotency(commandCtx.Client, cancelIdempotencyKey, "realtime.subscription.cancel."+args[0])
			envelope, err := client.CancelRealtimeSubscription(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not cancel realtime subscription.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
				return nil
			}
			renderRealtimeSubscription(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cancelCmd.Flags().StringVar(&cancelIdempotencyKey, "idempotency-key", "", "retry key for this cancellation")
	cmd.AddCommand(cancelCmd)
	return cmd
}

func newRealtimePresenceCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "presence",
		Short: "Inspect realtime presence",
	}
	filter := realtime.PresenceFilter{Limit: 50}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List realtime presence records",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListRealtimePresence(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list realtime presence.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, presence := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), presence.PresenceID)
				}
				return nil
			}
			renderRealtimePresenceList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of presence records to return")
	listCmd.Flags().StringVar(&filter.SubjectKind, "subject-kind", "", "filter by subject kind")
	listCmd.Flags().StringVar(&filter.State, "state", "", "filter by presence state")
	cmd.AddCommand(listCmd)
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <presence-or-subject>",
		Short: "Inspect a realtime presence record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetRealtimePresence(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect realtime presence.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRealtimePresence(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newRealtimeNotificationsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notifications",
		Short: "List realtime notifications",
	}
	filter := realtime.NotificationFilter{Limit: 50}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List realtime notifications",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListRealtimeNotifications(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list realtime notifications.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, notification := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), notification.NotificationID)
				}
				return nil
			}
			renderRealtimeNotificationList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of notifications to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by notification status")
	listCmd.Flags().StringVar(&filter.Category, "category", "", "filter by category")
	listCmd.Flags().StringVar(&filter.TargetKind, "target-kind", "", "filter by target kind")
	listCmd.Flags().StringVar(&filter.TargetRef, "target-ref", "", "filter by target ref")
	listCmd.Flags().StringVar(&filter.ApprovalRef, "approval", "", "filter by approval ID or key")
	cmd.AddCommand(listCmd)
	return cmd
}

func newRealtimeNotificationCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notification",
		Short: "Inspect and update realtime notifications",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <notification>",
		Short: "Inspect a realtime notification",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetRealtimeNotification(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect realtime notification.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRealtimeNotification(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	addNotificationTransitionCommand(cmd, opts, "ack")
	addNotificationTransitionCommand(cmd, opts, "dismiss")
	return cmd
}

func addNotificationTransitionCommand(parent *cobra.Command, opts *options, action string) {
	var idempotencyKey string
	cmd := &cobra.Command{
		Use:   action + " <notification>",
		Short: action + " a realtime notification",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client, _ := withEffectIdempotency(commandCtx.Client, idempotencyKey, "realtime.notification."+action+"."+args[0])
			var notification realtime.Notification
			var meta response.Meta
			if action == "ack" {
				envelope, err := client.AcknowledgeRealtimeNotification(ctx, commandCtx.CorrelationID, args[0])
				if err == nil {
					notification = envelope.Data
					meta = envelope.Meta
				}
			} else {
				envelope, err := client.DismissRealtimeNotification(ctx, commandCtx.CorrelationID, args[0])
				if err == nil {
					notification = envelope.Data
					meta = envelope.Meta
				}
			}
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not update realtime notification.", err))
			}
			if opts.jsonOutput {
				envelope := response.Envelope[realtime.Notification]{OK: true, Data: notification, Meta: meta}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), notification.Status)
				return nil
			}
			renderRealtimeNotification(cmd, notification)
			renderResponseMeta(cmd, opts, meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "retry key for this notification update")
	parent.AddCommand(cmd)
}

func newRealtimeProgressCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "progress",
		Short: "Inspect realtime progress feeds",
	}
	var limit int
	inspectCmd := &cobra.Command{
		Use:   "inspect <source>",
		Short: "Inspect a realtime progress source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetRealtimeProgress(ctx, commandCtx.CorrelationID, args[0], limit)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect realtime progress.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRealtimeProgress(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	inspectCmd.Flags().IntVar(&limit, "limit", 50, "maximum number of progress updates to return")
	cmd.AddCommand(inspectCmd)
	return cmd
}

func newRealtimeLeasesCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "leases",
		Short: "List realtime leases",
	}
	filter := realtime.LeaseFilter{Limit: 50}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List realtime leases",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListRealtimeLeases(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list realtime leases.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, lease := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), lease.LeaseID)
				}
				return nil
			}
			renderRealtimeLeaseList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of leases to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by lease status")
	listCmd.Flags().StringVar(&filter.Resource, "resource", "", "filter by resource kind:ref")
	listCmd.Flags().StringVar(&filter.HolderKind, "holder-kind", "", "filter by holder kind")
	listCmd.Flags().StringVar(&filter.HolderRef, "holder-ref", "", "filter by holder ref")
	cmd.AddCommand(listCmd)
	return cmd
}

func newRealtimeLeaseCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lease",
		Short: "Inspect and operate realtime leases",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <lease>",
		Short: "Inspect a realtime lease",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetRealtimeLease(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect realtime lease.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRealtimeLease(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	requestInput := realtime.RequestLeaseInput{}
	var durationRaw string
	var requestIdempotencyKey string
	requestCmd := &cobra.Command{
		Use:   "request",
		Short: "Request a realtime lease",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			input := requestInput
			if durationRaw != "" {
				duration, err := time.ParseDuration(durationRaw)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("realtime.lease_duration_invalid", "realtime", "duration", "Lease duration must be a Go duration such as 30s or 5m.", err))
				}
				input.DurationSeconds = int(duration.Seconds())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client, _ := withEffectIdempotency(commandCtx.Client, requestIdempotencyKey, "realtime.lease.request."+input.Resource)
			envelope, err := client.RequestRealtimeLease(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not request realtime lease.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.LeaseID)
				return nil
			}
			renderRealtimeLease(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	requestCmd.Flags().StringVar(&requestInput.Resource, "resource", "", "resource kind:ref to lease")
	requestCmd.Flags().StringVar(&requestInput.LeaseMode, "mode", "exclusive", "lease mode: read, shared, write, control, or exclusive")
	requestCmd.Flags().StringVar(&durationRaw, "duration", "5m", "lease duration such as 30s or 5m")
	requestCmd.Flags().StringVar(&requestInput.HolderKind, "holder-kind", "", "holder kind")
	requestCmd.Flags().StringVar(&requestInput.HolderRef, "holder-ref", "", "holder ref")
	requestCmd.Flags().StringVar(&requestInput.ScopeRef, "scope", "", "scope ID, key, or slug")
	requestCmd.Flags().StringVar(&requestIdempotencyKey, "idempotency-key", "", "retry key for this lease request")
	_ = requestCmd.MarkFlagRequired("resource")
	cmd.AddCommand(requestCmd)

	var releaseInput realtime.ReleaseLeaseInput
	var releaseIdempotencyKey string
	releaseCmd := &cobra.Command{
		Use:   "release <lease>",
		Short: "Release a realtime lease",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client, _ := withEffectIdempotency(commandCtx.Client, releaseIdempotencyKey, "realtime.lease.release."+args[0])
			envelope, err := client.ReleaseRealtimeLease(ctx, commandCtx.CorrelationID, args[0], releaseInput)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not release realtime lease.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
				return nil
			}
			renderRealtimeLease(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	releaseCmd.Flags().StringVar(&releaseInput.ReleaseReason, "reason", "", "release reason")
	releaseCmd.Flags().StringVar(&releaseIdempotencyKey, "idempotency-key", "", "retry key for this lease release")
	cmd.AddCommand(releaseCmd)
	return cmd
}

func renderRealtimeTopicList(cmd *cobra.Command, topics []realtime.Topic) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "TOPIC\tSTATUS\tRETENTION\tDELIVERY\tSEQUENCE\tUPDATED")
	for _, topic := range topics {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\t%s\n",
			topic.TopicPath,
			topic.Status,
			topic.RetentionMode,
			topic.DeliveryClass,
			topic.LastSequence,
			topic.UpdatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderRealtimeTopic(cmd *cobra.Command, topic realtime.Topic) {
	fmt.Fprintf(cmd.OutOrStdout(), "Topic: %s\n", topic.TopicPath)
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", topic.TopicID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", topic.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Scope: %s\n", ptrOrDash(topic.ScopeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Retention: %s\n", topic.RetentionMode)
	fmt.Fprintf(cmd.OutOrStdout(), "Delivery: %s\n", topic.DeliveryClass)
	fmt.Fprintf(cmd.OutOrStdout(), "Sequence: %d\n", topic.LastSequence)
	fmt.Fprintf(cmd.OutOrStdout(), "Updated: %s\n", topic.UpdatedAt.UTC().Format(time.RFC3339))
}

func renderRealtimePublication(cmd *cobra.Command, publication realtime.TopicPublication) {
	fmt.Fprintf(cmd.OutOrStdout(), "Publication: %s\n", publication.TopicPublicationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Topic: %s\n", publication.TopicID)
	fmt.Fprintf(cmd.OutOrStdout(), "Sequence: %d\n", publication.Sequence)
	fmt.Fprintf(cmd.OutOrStdout(), "Type: %s\n", publication.MessageType)
	fmt.Fprintf(cmd.OutOrStdout(), "Durability: %s\n", publication.DurabilityMode)
	fmt.Fprintf(cmd.OutOrStdout(), "Payload: %s\n", compactText(string(publication.PayloadJSON), 240))
}

func renderRealtimeSubscriptionList(cmd *cobra.Command, subscriptions []realtime.Subscription) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SUBSCRIPTION\tSTATUS\tTOPIC\tCURSOR\tACK\tUPDATED")
	for _, subscription := range subscriptions {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%d\t%s\n",
			subscription.SubscriptionID,
			subscription.Status,
			subscription.TopicID,
			subscription.CursorSequence,
			subscription.LastAcknowledgedSequence,
			subscription.UpdatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderRealtimeSubscription(cmd *cobra.Command, subscription realtime.Subscription) {
	fmt.Fprintf(cmd.OutOrStdout(), "Subscription: %s\n", subscription.SubscriptionID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", subscription.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Topic: %s\n", subscription.TopicID)
	fmt.Fprintf(cmd.OutOrStdout(), "Cursor: %d\n", subscription.CursorSequence)
	fmt.Fprintf(cmd.OutOrStdout(), "Acknowledged: %d\n", subscription.LastAcknowledgedSequence)
	fmt.Fprintf(cmd.OutOrStdout(), "Delivery: %s/%s\n", subscription.DeliveryMode, subscription.DeliveryTargetKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Updated: %s\n", subscription.UpdatedAt.UTC().Format(time.RFC3339))
}

func renderRealtimePollResult(cmd *cobra.Command, result realtime.SubscriptionPollResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Subscription: %s\n", result.Subscription.SubscriptionID)
	fmt.Fprintf(cmd.OutOrStdout(), "Cursor: %d\n", result.Subscription.CursorSequence)
	fmt.Fprintf(cmd.OutOrStdout(), "Publications: %d\n", len(result.Publications))
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SEQUENCE\tPUBLICATION\tTYPE\tCREATED")
	for _, publication := range result.Publications {
		fmt.Fprintf(writer, "%d\t%s\t%s\t%s\n",
			publication.Sequence,
			publication.TopicPublicationID,
			publication.MessageType,
			publication.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderRealtimePresenceList(cmd *cobra.Command, records []realtime.Presence) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SUBJECT\tSTATE\tNODE\tLAST SEEN\tEXPIRES")
	for _, presence := range records {
		fmt.Fprintf(writer, "%s:%s\t%s\t%s\t%s\t%s\n",
			presence.SubjectKind,
			presence.SubjectRef,
			presence.State,
			ptrOrDash(presence.NodeID),
			presence.LastSeenAt.UTC().Format(time.RFC3339),
			presence.ExpiresAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderRealtimePresence(cmd *cobra.Command, presence realtime.Presence) {
	fmt.Fprintf(cmd.OutOrStdout(), "Presence: %s\n", presence.PresenceID)
	fmt.Fprintf(cmd.OutOrStdout(), "Subject: %s:%s\n", presence.SubjectKind, presence.SubjectRef)
	fmt.Fprintf(cmd.OutOrStdout(), "Node: %s\n", ptrOrDash(presence.NodeID))
	fmt.Fprintf(cmd.OutOrStdout(), "State: %s\n", presence.State)
	fmt.Fprintf(cmd.OutOrStdout(), "Source: %s:%s\n", presence.SourceKind, presence.SourceRef)
	fmt.Fprintf(cmd.OutOrStdout(), "Last seen: %s\n", presence.LastSeenAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Expires: %s\n", presence.ExpiresAt.UTC().Format(time.RFC3339))
}

func renderRealtimeNotificationList(cmd *cobra.Command, notifications []realtime.Notification) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "NOTIFICATION\tSTATUS\tCATEGORY\tPRIORITY\tTARGET\tSUMMARY")
	for _, notification := range notifications {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s:%s\t%s\n",
			notification.NotificationID,
			notification.Status,
			notification.Category,
			notification.Priority,
			notification.TargetKind,
			notification.TargetRef,
			compactText(notification.Summary, 80),
		)
	}
	_ = writer.Flush()
}

func renderRealtimeNotification(cmd *cobra.Command, notification realtime.Notification) {
	fmt.Fprintf(cmd.OutOrStdout(), "Notification: %s\n", notification.NotificationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", notification.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Category: %s\n", notification.Category)
	fmt.Fprintf(cmd.OutOrStdout(), "Priority: %s\n", notification.Priority)
	fmt.Fprintf(cmd.OutOrStdout(), "Target: %s:%s\n", notification.TargetKind, notification.TargetRef)
	fmt.Fprintf(cmd.OutOrStdout(), "Source: %s:%s\n", notification.SourceKind, notification.SourceRef)
	fmt.Fprintf(cmd.OutOrStdout(), "Approval: %s\n", ptrOrDash(notification.ApprovalID))
	fmt.Fprintf(cmd.OutOrStdout(), "Route: %s\n", ptrOrDash(notification.RouteID))
	fmt.Fprintf(cmd.OutOrStdout(), "Capability call: %s\n", ptrOrDash(notification.CapabilityCallID))
	fmt.Fprintf(cmd.OutOrStdout(), "Summary: %s\n", notification.Summary)
	fmt.Fprintf(cmd.OutOrStdout(), "Payload: %s\n", compactText(string(notification.PayloadJSON), 240))
	fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", notification.CreatedAt.UTC().Format(time.RFC3339))
}

func renderRealtimeProgress(cmd *cobra.Command, detail realtime.ProgressDetail) {
	feed := detail.Feed
	fmt.Fprintf(cmd.OutOrStdout(), "Progress: %s:%s\n", feed.SourceKind, feed.SourceRef)
	fmt.Fprintf(cmd.OutOrStdout(), "Feed: %s\n", feed.ProgressFeedID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", feed.CurrentStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Stage: %s\n", feed.CurrentStage)
	fmt.Fprintf(cmd.OutOrStdout(), "Message: %s\n", feed.CurrentMessage)
	fmt.Fprintf(cmd.OutOrStdout(), "Sequence: %d\n", feed.LastSequence)
	fmt.Fprintf(cmd.OutOrStdout(), "Updated: %s\n", feed.UpdatedAt.UTC().Format(time.RFC3339))
	if len(detail.Updates) == 0 {
		return
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SEQ\tSTATUS\tSTAGE\tSEVERITY\tMESSAGE\tCREATED")
	for _, update := range detail.Updates {
		fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\t%s\n",
			update.Sequence,
			update.Status,
			update.Stage,
			update.Severity,
			compactText(update.Message, 80),
			update.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderRealtimeLeaseList(cmd *cobra.Command, leases []realtime.Lease) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "LEASE\tSTATUS\tMODE\tRESOURCE\tHOLDER\tEXPIRES")
	for _, lease := range leases {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s:%s\t%s:%s\t%s\n",
			lease.LeaseID,
			lease.Status,
			lease.LeaseMode,
			lease.ResourceKind,
			lease.ResourceRef,
			lease.HolderKind,
			lease.HolderRef,
			lease.ExpiresAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderRealtimeLease(cmd *cobra.Command, lease realtime.Lease) {
	fmt.Fprintf(cmd.OutOrStdout(), "Lease: %s\n", lease.LeaseID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", lease.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Mode: %s\n", lease.LeaseMode)
	fmt.Fprintf(cmd.OutOrStdout(), "Resource: %s:%s\n", lease.ResourceKind, lease.ResourceRef)
	fmt.Fprintf(cmd.OutOrStdout(), "Holder: %s:%s\n", lease.HolderKind, lease.HolderRef)
	fmt.Fprintf(cmd.OutOrStdout(), "Scope: %s\n", ptrOrDash(lease.ScopeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Starts: %s\n", lease.StartsAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Expires: %s\n", lease.ExpiresAt.UTC().Format(time.RFC3339))
	if lease.ReleasedAt != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Released: %s\n", lease.ReleasedAt.UTC().Format(time.RFC3339))
	}
	if lease.ReleaseReason != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Release reason: %s\n", lease.ReleaseReason)
	}
}
