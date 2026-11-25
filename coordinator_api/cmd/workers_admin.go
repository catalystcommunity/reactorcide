package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient"
	"github.com/urfave/cli/v2"
)

// WorkersAdminCommand exposes the complete worker administration surface.
// The singular "worker" command runs a worker process. The plural command
// manages workers, pools, enrollment tokens, queues, and worker classes.
var WorkersAdminCommand = &cli.Command{
	Name:  "workers",
	Usage: "Manage workers and worker routing",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		workersListCommand(),
		workersStatusCommand(),
		workersDrainCommand(),
		workerPoolsCommand(),
		workerTokensCommand(),
		workerQueuesCommand(),
		workerClassesCommand(),
	},
}

func managementClient(ctx *cli.Context) (*csilapi.ReactorcideUiClient, error) {
	api, err := newAPIClient(ctx)
	if err != nil {
		return nil, err
	}
	transport := &workerclient.CSILRPCTransport{
		BaseURL:                api.apiURL,
		HTTPClient:             &http.Client{Timeout: 30 * time.Second},
		AllowInsecureTransport: api.allowInsecureTransport,
	}
	transport.SetSession(api.token)
	return csilapi.NewReactorcideUiClient(uiTransport{transport}), nil
}

// uiTransport carries the ReactorcideUi service over the worker transport.
// The envelope is the same; only the generated WireReply type differs.
type uiTransport struct {
	*workerclient.CSILRPCTransport
}

func (t uiTransport) CallReply(ctx context.Context, service, op string, req []byte) (csilapi.WireReply, error) {
	reply, err := t.CSILRPCTransport.CallReply(ctx, service, op, req)
	if err != nil {
		return csilapi.WireReply{}, err
	}
	return csilapi.WireReply{Variant: reply.Variant, Payload: reply.Payload}, nil
}

func workersListCommand() *cli.Command {
	return &cli.Command{Name: "list", Usage: "List enrolled workers", Flags: append(apiFlags(),
		&cli.StringFlag{Name: "pool", Usage: "Limit the result to one pool ID"},
		&cli.StringFlag{Name: "enrollment-token", Usage: "Only workers that last registered with this enrollment token ID"},
		&cli.BoolFlag{Name: "on-deactivated-token", Usage: "Only workers with an active session from a deactivated enrollment token"},
		formatFlag()), Action: func(ctx *cli.Context) error {
		client, err := managementClient(ctx)
		if err != nil {
			return err
		}
		req := csilapi.ListWorkersRequest{PoolId: optionalText(ctx.String("pool")), EnrollmentTokenId: optionalText(ctx.String("enrollment-token"))}
		if ctx.Bool("on-deactivated-token") {
			on := true
			req.OnDeactivatedToken = &on
		}
		resp, err := client.ListWorkers(ctx.Context, req)
		if err != nil {
			return err
		}
		return render(ctx.String("format"), resp.Workers, func(w *tabwriter.Writer) {
			fmt.Fprintln(w, "ID\tHOST\tOS/ARCH\tSTATUS\tIMAGES\tPOOL\tTOKEN\tLEASES\tLAST SEEN")
			for _, item := range resp.Workers {
				fmt.Fprintf(w, "%s\t%s\t%s/%s\t%s\t%s\t%s\t%s\t%d\t%s\n", item.WorkerId, textOrDash(item.Hostname), item.Os, item.Arch, item.Status, workerImageLabel(item), item.PoolId, workerTokenLabel(item), item.ActiveLeaseCount, textOrDash(item.LastSeenAt))
			}
		})
	}}
}

// workerImageLabel is the image readiness of a worker (healthy, updating
// images, or image update failed with its reason), or "-" for a worker that
// does not use image sets.
func workerImageLabel(item csilapi.WorkerSummary) string {
	if item.ImageStatus == nil {
		return "-"
	}
	if item.ImageStatusDetail != nil && *item.ImageStatusDetail != "" {
		return *item.ImageStatus + ": " + *item.ImageStatusDetail
	}
	return *item.ImageStatus
}

// workerTokenLabel is the enrollment token of a worker as "ID (name)", with
// a DEACTIVATED mark for a worker that runs on a deactivated token.
func workerTokenLabel(item csilapi.WorkerSummary) string {
	if item.EnrollmentTokenId == nil {
		return "-"
	}
	out := *item.EnrollmentTokenId
	if item.EnrollmentTokenName != nil && *item.EnrollmentTokenName != "" {
		out += " (" + *item.EnrollmentTokenName + ")"
	}
	if item.OnDeactivatedToken != nil && *item.OnDeactivatedToken {
		out += " DEACTIVATED"
	}
	return out
}

func workersStatusCommand() *cli.Command {
	return &cli.Command{Name: "set-status", Usage: "Set a worker status", ArgsUsage: "<worker-id> <active|quarantined|disabled>", Flags: append(apiFlags(), formatFlag()), Action: func(ctx *cli.Context) error {
		if ctx.NArg() != 2 {
			return fmt.Errorf("usage: reactorcide workers set-status <worker-id> <active|quarantined|disabled>")
		}
		client, err := managementClient(ctx)
		if err != nil {
			return err
		}
		resp, err := client.SetWorkerStatus(ctx.Context, csilapi.SetWorkerStatusRequest{WorkerId: ctx.Args().Get(0), Status: ctx.Args().Get(1)})
		if err != nil {
			return err
		}
		return render(ctx.String("format"), resp.Worker, func(w *tabwriter.Writer) {
			fmt.Fprintln(w, "ID\tSTATUS")
			fmt.Fprintf(w, "%s\t%s\n", resp.Worker.WorkerId, resp.Worker.Status)
		})
	}}
}

func workersDrainCommand() *cli.Command {
	return &cli.Command{Name: "drain", Usage: "Stop a worker from accepting new jobs", ArgsUsage: "<worker-id>", Flags: append(apiFlags(), formatFlag()), Action: func(ctx *cli.Context) error {
		if ctx.NArg() != 1 {
			return fmt.Errorf("usage: reactorcide workers drain <worker-id>")
		}
		client, err := managementClient(ctx)
		if err != nil {
			return err
		}
		resp, err := client.DrainWorker(ctx.Context, csilapi.DrainWorkerRequest{WorkerId: ctx.Args().First()})
		if err != nil {
			return err
		}
		return render(ctx.String("format"), resp.Worker, func(w *tabwriter.Writer) {
			fmt.Fprintln(w, "ID\tSTATUS\tACTIVE LEASES")
			fmt.Fprintf(w, "%s\t%s\t%d\n", resp.Worker.WorkerId, resp.Worker.Status, resp.Worker.ActiveLeaseCount)
		})
	}}
}

func workerPoolsCommand() *cli.Command {
	return &cli.Command{Name: "pools", Usage: "Manage worker pools", Subcommands: []*cli.Command{
		{Name: "list", Usage: "List worker pools (needs workers:read or workers:manage)", Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Usage: "Show only the pools of this organization"}, formatFlag()), Action: func(ctx *cli.Context) error {
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.ListPools(ctx.Context, csilapi.ListPoolsRequest{OrgId: optionalText(ctx.String("org"))})
			if err != nil {
				return err
			}
			return render(ctx.String("format"), resp.Pools, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "ID\tNAME\tORG\tDESCRIPTION")
				for _, item := range resp.Pools {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", item.PoolId, item.Name, textOrDash(item.OrgId), textOrDash(item.Description))
				}
			})
		}},
		{Name: "create", Usage: "Create a worker pool (needs workers:manage)", ArgsUsage: "<name>", Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Usage: "Organization that owns the pool (default: a global pool, which only a global admin can create)"}, &cli.StringFlag{Name: "description", Usage: "Description of the pool"}, formatFlag()), Action: func(ctx *cli.Context) error {
			if ctx.NArg() != 1 {
				return fmt.Errorf("usage: reactorcide workers pools create <name>")
			}
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.CreatePool(ctx.Context, csilapi.CreatePoolRequest{OrgId: optionalText(ctx.String("org")), Name: ctx.Args().First(), Description: optionalText(ctx.String("description"))})
			if err != nil {
				return err
			}
			return render(ctx.String("format"), resp.Pool, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "ID\tNAME")
				fmt.Fprintf(w, "%s\t%s\n", resp.Pool.PoolId, resp.Pool.Name)
			})
		}},
		{Name: "update", Usage: "Change the name or description of a worker pool (needs workers:manage)", ArgsUsage: "<pool-id>", Flags: append(apiFlags(), &cli.StringFlag{Name: "name", Usage: "New name (default: keep the current value)"}, &cli.StringFlag{Name: "description", Usage: "New description (default: keep the current value)"}, formatFlag()), Action: func(ctx *cli.Context) error {
			if ctx.NArg() != 1 || (ctx.String("name") == "" && ctx.String("description") == "") {
				return fmt.Errorf("provide a pool ID and at least one of --name or --description")
			}
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.UpdatePool(ctx.Context, csilapi.UpdatePoolRequest{PoolId: ctx.Args().First(), Name: optionalText(ctx.String("name")), Description: optionalText(ctx.String("description"))})
			if err != nil {
				return err
			}
			return render(ctx.String("format"), resp.Pool, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "ID\tNAME\tDESCRIPTION")
				fmt.Fprintf(w, "%s\t%s\t%s\n", resp.Pool.PoolId, resp.Pool.Name, textOrDash(resp.Pool.Description))
			})
		}},
		{Name: "delete", Usage: "Delete a worker pool (needs workers:manage)", ArgsUsage: "<pool-id>", Flags: apiFlags(), Action: func(ctx *cli.Context) error {
			if ctx.NArg() != 1 {
				return fmt.Errorf("usage: reactorcide workers pools delete <pool-id>")
			}
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.DeletePool(ctx.Context, csilapi.DeletePoolRequest{PoolId: ctx.Args().First()})
			if err != nil {
				return err
			}
			fmt.Printf("deleted=%t\n", resp.Deleted)
			return nil
		}},
	}}
}

func workerTokensCommand() *cli.Command {
	return &cli.Command{Name: "tokens", Usage: "Manage worker enrollment tokens", Subcommands: []*cli.Command{
		{Name: "create", Usage: "Create an enrollment token for a worker pool and write it to a new file", ArgsUsage: "<pool-id>", Flags: append(apiFlags(), &cli.StringFlag{Name: "name", Usage: "Name of the token"}, &cli.StringFlag{Name: "output-file", Required: true, Usage: "New file that receives the raw token"}), Action: createEnrollmentToken},
		{Name: "list", Usage: "List the enrollment tokens of a worker pool", ArgsUsage: "<pool-id>", Flags: append(apiFlags(), formatFlag()), Action: func(ctx *cli.Context) error {
			if ctx.NArg() != 1 {
				return fmt.Errorf("usage: reactorcide workers tokens list <pool-id>")
			}
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.ListEnrollmentTokens(ctx.Context, csilapi.ListEnrollmentTokensRequest{PoolId: ctx.Args().First()})
			if err != nil {
				return err
			}
			return render(ctx.String("format"), resp.Tokens, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "ID\tNAME\tACTIVE\tACTIVE WORKERS\tLAST USED\tCREATED")
				for _, item := range resp.Tokens {
					fmt.Fprintf(w, "%s\t%s\t%t\t%d\t%s\t%s\n", item.TokenId, textOrDash(item.Name), item.IsActive, derefInt(item.ActiveWorkerCount), textOrDash(item.LastUsedAt), item.CreatedAt)
				}
			})
		}},
		{Name: "deactivate", Usage: "Deactivate an enrollment token permanently", ArgsUsage: "<token-id>", Flags: append(apiFlags(),
			&cli.BoolFlag{Name: "confirm-permanent", Usage: "Required: deactivation is permanent and cannot be undone"}, formatFlag()), Action: func(ctx *cli.Context) error {
			if ctx.NArg() != 1 {
				return fmt.Errorf("usage: reactorcide workers tokens deactivate <token-id> --confirm-permanent")
			}
			if !ctx.Bool("confirm-permanent") {
				return fmt.Errorf("deactivation is permanent and cannot be undone; run \"reactorcide workers list --enrollment-token %s\" first, then add --confirm-permanent", ctx.Args().First())
			}
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			confirm := true
			resp, err := client.DeactivateEnrollmentToken(ctx.Context, csilapi.DeactivateEnrollmentTokenRequest{TokenId: ctx.Args().First(), ConfirmPermanent: &confirm})
			if err != nil {
				return err
			}
			if n := derefInt(resp.Summary.ActiveWorkerCount); n > 0 {
				fmt.Fprintf(os.Stderr, "warning: %d worker(s) still run on this token. They cannot register again after they stop. List them with: reactorcide workers list --on-deactivated-token\n", n)
			}
			return render(ctx.String("format"), resp.Summary, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "ID\tNAME\tACTIVE\tACTIVE WORKERS")
				fmt.Fprintf(w, "%s\t%s\t%t\t%d\n", resp.Summary.TokenId, textOrDash(resp.Summary.Name), resp.Summary.IsActive, derefInt(resp.Summary.ActiveWorkerCount))
			})
		}},
	}}
}

func createEnrollmentToken(ctx *cli.Context) (retErr error) {
	if ctx.NArg() != 1 {
		return fmt.Errorf("usage: reactorcide workers tokens create <pool-id> --output-file FILE")
	}
	path := ctx.String("output-file")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create token file: %w", err)
	}
	written := false
	defer func() {
		_ = file.Close()
		if !written {
			_ = os.Remove(path)
		}
	}()
	client, err := managementClient(ctx)
	if err != nil {
		return err
	}
	resp, err := client.CreateEnrollmentToken(ctx.Context, csilapi.CreateEnrollmentTokenRequest{PoolId: ctx.Args().First(), Name: optionalText(ctx.String("name"))})
	if err != nil {
		return err
	}
	if _, err := file.WriteString(resp.Token + "\n"); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync token file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close token file: %w", err)
	}
	written = true
	fmt.Printf("Created enrollment token %s. The raw token is in %s.\n", resp.Summary.TokenId, path)
	return nil
}

func workerQueuesCommand() *cli.Command {
	return &cli.Command{Name: "queues", Usage: "Manage worker queues", Subcommands: []*cli.Command{
		{Name: "list", Usage: "List worker queues with their characteristics and backlog (needs workers:read or workers:manage)", Flags: append(apiFlags(), formatFlag()), Action: func(ctx *cli.Context) error {
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.ListQueues(ctx.Context, csilapi.ListQueuesRequest{})
			if err != nil {
				return err
			}
			return render(ctx.String("format"), resp.Queues, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "ID\tNAME\tCHARACTERISTICS\tBACKLOG\tDEFAULT")
				for _, item := range resp.Queues {
					fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%t\n", item.QueueId, item.DisplayName, formatCharacteristics(item.Characteristics), item.BacklogCount, item.IsDefault)
				}
			})
		}},
		{Name: "create", Usage: "Create a worker queue for a set of characteristics (needs workers:manage)", Flags: append(apiFlags(), &cli.StringSliceFlag{Name: "characteristic", Aliases: []string{"c"}, Required: true, Usage: "Required worker value as key=value; repeat for each value"}, &cli.StringFlag{Name: "display-name", Usage: "Display name of the queue"}, formatFlag()), Action: func(ctx *cli.Context) error {
			entries, err := parseQueueCharacteristics(ctx.StringSlice("characteristic"))
			if err != nil {
				return err
			}
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.CreateQueue(ctx.Context, csilapi.CreateQueueRequest{Characteristics: entries, DisplayName: optionalText(ctx.String("display-name"))})
			if err != nil {
				return err
			}
			return render(ctx.String("format"), resp.Queue, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "ID\tNAME\tCHARACTERISTICS")
				fmt.Fprintf(w, "%s\t%s\t%s\n", resp.Queue.QueueId, resp.Queue.DisplayName, formatCharacteristics(resp.Queue.Characteristics))
			})
		}},
		{Name: "rename", Usage: "Change the display name of a worker queue (needs workers:manage)", ArgsUsage: "<queue-id> <display-name>", Flags: append(apiFlags(), formatFlag()), Action: func(ctx *cli.Context) error {
			if ctx.NArg() != 2 {
				return fmt.Errorf("usage: reactorcide workers queues rename <queue-id> <display-name>")
			}
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.RenameQueue(ctx.Context, csilapi.RenameQueueRequest{QueueId: ctx.Args().Get(0), DisplayName: ctx.Args().Get(1)})
			if err != nil {
				return err
			}
			return render(ctx.String("format"), resp.Queue, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "ID\tNAME")
				fmt.Fprintf(w, "%s\t%s\n", resp.Queue.QueueId, resp.Queue.DisplayName)
			})
		}},
		{Name: "delete", Usage: "Delete a worker queue (needs workers:manage)", ArgsUsage: "<queue-id>", Flags: append(apiFlags(), formatFlag()), Action: func(ctx *cli.Context) error {
			if ctx.NArg() != 1 {
				return fmt.Errorf("usage: reactorcide workers queues delete <queue-id>")
			}
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.DeleteQueue(ctx.Context, csilapi.DeleteQueueRequest{QueueId: ctx.Args().First()})
			if err != nil {
				return err
			}
			return render(ctx.String("format"), resp, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "DELETED\tCANCELLED JOBS")
				fmt.Fprintf(w, "%t\t%d\n", resp.Deleted, len(resp.CancelledJobIds))
			})
		}},
	}}
}

func workerClassesCommand() *cli.Command {
	return &cli.Command{Name: "classes", Usage: "Manage worker classes", Subcommands: []*cli.Command{
		{Name: "list", Usage: "List the worker classes of an organization and their pools", Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true, Usage: "Organization"}, formatFlag()), Action: func(ctx *cli.Context) error {
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.ListWorkerClasses(ctx.Context, csilapi.ListWorkerClassesRequest{Organization: ctx.String("org")})
			if err != nil {
				return err
			}
			return render(ctx.String("format"), resp.WorkerClasses, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "NAME\tPROTECTED\tPOOLS")
				for _, item := range resp.WorkerClasses {
					fmt.Fprintf(w, "%s\t%t\t%s\n", item.Name, item.Protected, strings.Join(item.PoolIds, ","))
				}
			})
		}},
		{Name: "put", Usage: "Create or replace a worker class (needs workers:manage)", ArgsUsage: "<name>", Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true, Usage: "Organization"}, &cli.BoolFlag{Name: "protected", Usage: "A profile that denies secrets cannot select the class, unless the profile names it in allowed_worker_classes"}, &cli.StringSliceFlag{Name: "pool", Usage: "Pool ID that serves the class; repeat for each pool"}, formatFlag()), Action: func(ctx *cli.Context) error {
			if ctx.NArg() != 1 {
				return fmt.Errorf("usage: reactorcide workers classes put <name> --org ORG")
			}
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.PutWorkerClass(ctx.Context, csilapi.PutWorkerClassRequest{Organization: ctx.String("org"), WorkerClass: csilapi.WorkerClassSummary{Name: ctx.Args().First(), Protected: ctx.Bool("protected"), PoolIds: ctx.StringSlice("pool")}})
			if err != nil {
				return err
			}
			return render(ctx.String("format"), resp.WorkerClass, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "NAME\tPROTECTED\tPOOLS")
				fmt.Fprintf(w, "%s\t%t\t%s\n", resp.WorkerClass.Name, resp.WorkerClass.Protected, strings.Join(resp.WorkerClass.PoolIds, ","))
			})
		}},
		{Name: "delete", Usage: "Delete a worker class (needs workers:manage)", ArgsUsage: "<name>", Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true, Usage: "Organization"}), Action: func(ctx *cli.Context) error {
			if ctx.NArg() != 1 {
				return fmt.Errorf("usage: reactorcide workers classes delete <name> --org ORG")
			}
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.DeleteWorkerClass(ctx.Context, csilapi.DeleteWorkerClassRequest{Organization: ctx.String("org"), Name: ctx.Args().First()})
			if err != nil {
				return err
			}
			fmt.Printf("deleted=%t\n", resp.Deleted)
			return nil
		}},
		{Name: "set-pool", Usage: "Grant a pool to a worker class, or remove it with --revoke (needs workers:manage)", ArgsUsage: "<class-name> <pool-id>", Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true, Usage: "Organization"}, &cli.BoolFlag{Name: "revoke", Usage: "Remove the pool from the class"}), Action: func(ctx *cli.Context) error {
			if ctx.NArg() != 2 {
				return fmt.Errorf("usage: reactorcide workers classes set-pool <class-name> <pool-id> --org ORG")
			}
			client, err := managementClient(ctx)
			if err != nil {
				return err
			}
			resp, err := client.SetWorkerClassPool(ctx.Context, csilapi.SetWorkerClassPoolRequest{Organization: ctx.String("org"), WorkerClass: ctx.Args().Get(0), PoolId: ctx.Args().Get(1), Granted: !ctx.Bool("revoke")})
			if err != nil {
				return err
			}
			fmt.Printf("ok=%t\n", resp.Ok)
			return nil
		}},
	}}
}

func optionalText(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func textOrDash(value *string) string {
	if value == nil || *value == "" {
		return "-"
	}
	return *value
}

func parseQueueCharacteristics(values []string) ([]csilapi.CharacteristicEntry, error) {
	entries := make([]csilapi.CharacteristicEntry, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		key, value, ok := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("characteristic %q must use key=value", raw)
		}
		if seen[key] {
			return nil, fmt.Errorf("characteristic %q is repeated", key)
		}
		parsed, err := characteristics.ParseCustomValue(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("characteristic %q: %w", key, err)
		}
		if parsed.IsList() {
			return nil, fmt.Errorf("queue characteristic %q must be one scalar value", key)
		}
		seen[key] = true
		entries = append(entries, csilapi.CharacteristicEntry{Key: key, Value: characteristicScalar(parsed)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries, nil
}

func characteristicScalar(value characteristics.Value) interface{} {
	switch item := value.(type) {
	case characteristics.StringValue:
		return string(item)
	case characteristics.IntValue:
		return int64(item)
	case characteristics.BoolValue:
		return bool(item)
	default:
		return nil
	}
}

func formatCharacteristics(entries []csilapi.CharacteristicEntry) string {
	parts := make([]string, len(entries))
	for i, entry := range entries {
		parts[i] = fmt.Sprintf("%s=%v", entry.Key, entry.Value)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func derefInt(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
