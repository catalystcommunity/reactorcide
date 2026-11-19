package cmd

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/urfave/cli/v2"
)

// ImagesCommand manages image aliases and shows image rollouts
// (docs/image-aliases.md). It uses the CSIL management operations with the
// API token, like the workers command.
var ImagesCommand = &cli.Command{
	Name:  "images",
	Usage: "Manage image aliases and see image rollouts to workers",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		imageAliasCommand(),
		imageRolloutCommand(),
	},
}

// imagesAPI is the part of the management client that the images command
// uses. Tests replace newImagesClient.
type imagesAPI interface {
	ListImageAliases(ctx context.Context, req csilapi.ListImageAliasesRequest) (csilapi.ListImageAliasesResponse, error)
	GetImageAlias(ctx context.Context, req csilapi.GetImageAliasRequest) (csilapi.GetImageAliasResponse, error)
	SetImageAlias(ctx context.Context, req csilapi.SetImageAliasRequest) (csilapi.SetImageAliasResponse, error)
	DeleteImageAlias(ctx context.Context, req csilapi.DeleteImageAliasRequest) (csilapi.DeleteImageAliasResponse, error)
	GetImageRolloutStatus(ctx context.Context, req csilapi.GetImageRolloutStatusRequest) (csilapi.GetImageRolloutStatusResponse, error)
}

var newImagesClient = func(ctx *cli.Context) (imagesAPI, error) {
	return managementClient(ctx)
}

func imageScopeFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "org", Required: true, Usage: "Organization name"},
		&cli.StringFlag{Name: "project", Usage: "Project ID (project scope). Without it, the organization scope"},
	}
}

func imageAliasCommand() *cli.Command {
	return &cli.Command{
		Name:  "alias",
		Usage: "Manage image aliases (organization or project scope)",
		Flags: apiFlags(),
		Subcommands: []*cli.Command{
			{
				Name: "list", Usage: "List the image aliases of a scope",
				Flags: append(append(apiFlags(), formatFlag()), imageScopeFlags()...),
				Action: func(ctx *cli.Context) error {
					client, err := newImagesClient(ctx)
					if err != nil {
						return err
					}
					resp, err := client.ListImageAliases(ctx.Context, csilapi.ListImageAliasesRequest{
						Organization: ctx.String("org"), ProjectId: optionalText(ctx.String("project")),
					})
					if err != nil {
						return err
					}
					return render(ctx.String("format"), resp.Aliases, func(w *tabwriter.Writer) {
						fmt.Fprintln(w, "NAME\tSCOPE\tKIND\tPLATFORM\tACTIVE\tPENDING")
						for _, alias := range resp.Aliases {
							fmt.Fprintf(w, "%s\t%s\t%s\t%s/%s\t%s\t%s\n", alias.Name, alias.Scope, alias.Kind, alias.Os, alias.Arch, alias.ActiveRef, textOrDash(alias.PendingRef))
						}
					})
				},
			},
			{
				Name: "get", Usage: "Show one image alias", ArgsUsage: "NAME",
				Flags: append(append(apiFlags(), formatFlag()), imageScopeFlags()...),
				Action: func(ctx *cli.Context) error {
					if ctx.NArg() != 1 {
						return fmt.Errorf("give one NAME argument")
					}
					client, err := newImagesClient(ctx)
					if err != nil {
						return err
					}
					resp, err := client.GetImageAlias(ctx.Context, csilapi.GetImageAliasRequest{
						Organization: ctx.String("org"), ProjectId: optionalText(ctx.String("project")), Name: ctx.Args().First(),
					})
					if err != nil {
						return err
					}
					return renderImageAlias(ctx, resp.Alias, "")
				},
			},
			{
				Name:      "set",
				Usage:     "Create an image alias or give it a new pinned reference",
				ArgsUsage: "NAME REF",
				Description: "REF must be pinned (@sha256:<digest>). A new alias uses REF at once. For an alias that exists, " +
					"REF becomes the pending pin: new jobs use the old pin until every online worker that serves the alias has " +
					"the new image. --force uses REF at once. --kind, --os, --arch, and --characteristic are for a new alias " +
					"only: they cannot change after the create. For a different platform or constraint, create an alias with a new name.",
				Flags: append(append(apiFlags(), formatFlag()), append(imageScopeFlags(),
					&cli.StringFlag{Name: "kind", Usage: "New alias only: vm or container (default: vm for --os macos or windows, else container)"},
					&cli.StringFlag{Name: "os", Usage: "New alias only (required): platform os, for example windows, macos, linux"},
					&cli.StringFlag{Name: "arch", Usage: "New alias only (required): platform arch, for example amd64, arm64"},
					&cli.StringSliceFlag{Name: "characteristic", Usage: "New alias only: more worker characteristic key=value that a worker must satisfy (repeat). worker_class=NAME limits to pools that serve that class"},
					&cli.BoolFlag{Name: "force", Usage: "Use the new reference at once; do not wait for the workers"},
				)...),
				Action: func(ctx *cli.Context) error {
					req, err := imageAliasSetRequest(ctx)
					if err != nil {
						return err
					}
					client, err := newImagesClient(ctx)
					if err != nil {
						return err
					}
					resp, err := client.SetImageAlias(ctx.Context, req)
					if err != nil {
						return err
					}
					return renderImageAlias(ctx, resp.Alias, resp.Outcome)
				},
			},
			{
				Name: "delete", Usage: "Delete an image alias (refused while a job that is not terminal uses it)", ArgsUsage: "NAME",
				Flags: append(apiFlags(), imageScopeFlags()...),
				Action: func(ctx *cli.Context) error {
					if ctx.NArg() != 1 {
						return fmt.Errorf("give one NAME argument")
					}
					client, err := newImagesClient(ctx)
					if err != nil {
						return err
					}
					if _, err := client.DeleteImageAlias(ctx.Context, csilapi.DeleteImageAliasRequest{
						Organization: ctx.String("org"), ProjectId: optionalText(ctx.String("project")), Name: ctx.Args().First(),
					}); err != nil {
						return err
					}
					fmt.Fprintf(ctx.App.Writer, "Deleted image alias %s\n", ctx.Args().First())
					return nil
				},
			},
		},
	}
}

// imageAliasSetRequest builds the set-image-alias request from the flags.
func imageAliasSetRequest(ctx *cli.Context) (csilapi.SetImageAliasRequest, error) {
	if ctx.NArg() != 2 {
		return csilapi.SetImageAliasRequest{}, fmt.Errorf("give NAME and REF arguments")
	}
	req := csilapi.SetImageAliasRequest{
		Organization: ctx.String("org"),
		ProjectId:    optionalText(ctx.String("project")),
		Name:         ctx.Args().Get(0),
		Ref:          strings.TrimSpace(ctx.Args().Get(1)),
		Kind:         optionalText(ctx.String("kind")),
		Os:           optionalText(ctx.String("os")),
		Arch:         optionalText(ctx.String("arch")),
	}
	values := ctx.StringSlice("characteristic")
	switch {
	case len(values) > 0:
		entries, err := parseQueueCharacteristics(values)
		if err != nil {
			return req, err
		}
		req.Characteristics = entries
	}
	if ctx.Bool("force") {
		force := true
		req.Force = &force
	}
	return req, nil
}

func renderImageAlias(ctx *cli.Context, alias csilapi.ImageAliasSummary, outcome string) error {
	view := struct {
		Outcome string                    `json:"outcome,omitempty" yaml:"outcome,omitempty"`
		Alias   csilapi.ImageAliasSummary `json:"alias" yaml:"alias"`
	}{Outcome: outcome, Alias: alias}
	return render(ctx.String("format"), view, func(w *tabwriter.Writer) {
		if outcome != "" {
			fmt.Fprintf(w, "OUTCOME\t%s\n", outcome)
		}
		fmt.Fprintf(w, "NAME\t%s\n", alias.Name)
		fmt.Fprintf(w, "SCOPE\t%s\n", alias.Scope)
		fmt.Fprintf(w, "KIND\t%s\n", alias.Kind)
		fmt.Fprintf(w, "PLATFORM\t%s/%s\n", alias.Os, alias.Arch)
		fmt.Fprintf(w, "ACTIVE\t%s\n", alias.ActiveRef)
		fmt.Fprintf(w, "PENDING\t%s\n", textOrDash(alias.PendingRef))
		chars := make([]string, 0, len(alias.Characteristics))
		for _, entry := range alias.Characteristics {
			chars = append(chars, fmt.Sprintf("%s=%v", entry.Key, entry.Value))
		}
		fmt.Fprintf(w, "CHARACTERISTICS\t%s\n", strings.Join(chars, ","))
		fmt.Fprintf(w, "UPDATED\t%s\n", alias.UpdatedAt)
	})
}

func imageRolloutCommand() *cli.Command {
	return &cli.Command{
		Name:  "rollout",
		Usage: "See image alias rollouts to workers",
		Flags: apiFlags(),
		Subcommands: []*cli.Command{
			{
				Name: "status", Usage: "Show the serving workers of each alias and their image state", ArgsUsage: "[NAME]",
				Flags: append(append(apiFlags(), formatFlag()), imageScopeFlags()...),
				Action: func(ctx *cli.Context) error {
					client, err := newImagesClient(ctx)
					if err != nil {
						return err
					}
					resp, err := client.GetImageRolloutStatus(ctx.Context, csilapi.GetImageRolloutStatusRequest{
						Organization: ctx.String("org"), ProjectId: optionalText(ctx.String("project")), Name: optionalText(ctx.Args().First()),
					})
					if err != nil {
						return err
					}
					return render(ctx.String("format"), resp.Rollouts, func(w *tabwriter.Writer) {
						fmt.Fprintln(w, "ALIAS\tWORKER\tHOST\tONLINE\tIMAGE STATUS\tVERSION\tACTIVE\tPENDING")
						for _, rollout := range resp.Rollouts {
							state := "no rollout"
							if rollout.Alias.PendingRef != nil {
								state = fmt.Sprintf("pending %s: %d/%d online workers ready", *rollout.Alias.PendingRef, rollout.ReadyWorkers, rollout.OnlineWorkers)
								if rollout.Blocked != nil {
									state += " (" + *rollout.Blocked + ")"
								}
							}
							fmt.Fprintf(w, "%s\t-\t-\t-\t%s\t-\t%s\t%s\n", rollout.Alias.Name, state, rollout.Alias.ActiveRef, textOrDash(rollout.Alias.PendingRef))
							for _, problem := range rollout.Problems {
								fmt.Fprintf(w, "%s\t-\t-\t-\tPROBLEM: %s\t-\t-\t-\n", rollout.Alias.Name, problem)
							}
							for _, pool := range rollout.OtherPools {
								fmt.Fprintf(w, "%s\tpool %s\t-\t%d/%d\t%d ready, %d failed\t-\t-\t-\n", rollout.Alias.Name, pool.PoolName,
									pool.OnlineWorkers, pool.Workers, pool.ReadyWorkers, pool.FailedWorkers)
							}
							for _, worker := range rollout.Workers {
								version := fmt.Sprintf("%d", worker.ImageSetVersion)
								if worker.ImageReportedVersion != nil {
									version = fmt.Sprintf("%d/%d", *worker.ImageReportedVersion, worker.ImageSetVersion)
								}
								status := worker.ImageStatus
								if worker.ImageStatusDetail != nil {
									status += ": " + *worker.ImageStatusDetail
								}
								for _, f := range worker.Failed {
									class := "will retry"
									if f.Permanent {
										class = "permanent"
									}
									status += fmt.Sprintf("; %s failed (%s): %s", f.Digest, class, f.Detail)
								}
								fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\t%s\t%t\t%t\n", rollout.Alias.Name, worker.WorkerId, textOrDash(worker.Hostname),
									worker.Online, status, version, worker.HasActive, worker.HasPending)
							}
						}
					})
				},
			},
		},
	}
}
