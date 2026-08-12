package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker/vmrunner"
	"github.com/urfave/cli/v2"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

var VMImageCommand = &cli.Command{
	Name:  "vm-image",
	Usage: "Build, publish, pull, and manage VM images",
	Subcommands: []*cli.Command{
		vmImageBuildCommand,
		vmImagePublishCommand,
		vmImagePullCommand,
		vmImageCacheCommand,
		vmImageRegistryCommand,
	},
}

var vmImageBuildCommand = &cli.Command{
	Name:  "build",
	Usage: "Create a derived VM image",
	Subcommands: []*cli.Command{
		{
			Name:  "macos",
			Usage: "Clone and provision a macOS bootstrap image",
			Flags: append(vmImageRegistryFlags(),
				&cli.StringFlag{Name: "from", Required: true, Usage: "Bootstrap bundle path or OCI reference"},
				&cli.BoolFlag{Name: "from-local", Usage: "Treat --from as a local bundle path instead of an OCI reference"},
				&cli.StringFlag{Name: "output", Required: true, Usage: "New bundle directory"},
				&cli.StringFlag{Name: "cache-dir", Value: vmrunner.DefaultImageCacheDir(), Usage: "Local content-addressed VM image cache"},
				&cli.StringFlag{Name: "ssh-user", Value: "reactorcide", Usage: "Bootstrap guest account"},
				&cli.StringFlag{Name: "ssh-private-key-file", Usage: "Private key for the bootstrap guest"},
				&cli.StringFlag{Name: "ssh-password-file", Usage: "Password file for the bootstrap guest"},
				&cli.StringSliceFlag{Name: "provision", Usage: "Shell script file to run in the guest (repeatable)"},
				&cli.IntFlag{Name: "cpus", Value: 4, Usage: "Virtual CPUs used while provisioning"},
				&cli.StringFlag{Name: "memory", Value: "8Gi", Usage: "Memory used while provisioning"},
				&cli.StringFlag{Name: "publish", Usage: "Publish the completed bundle to this OCI reference"},
				&cli.BoolFlag{Name: "allow-unclean-stop", Usage: "Force-stop after sync if the bootstrap account cannot request shutdown"},
			),
			Action: runVMImageBuildMacOS,
		},
	},
}

func runVMImageBuildMacOS(c *cli.Context) error {
	store, err := openVMImageCredentialStore(c.String("auth-file"), false)
	if err != nil {
		return err
	}
	sourcePath := c.String("from")
	if !c.Bool("from-local") {
		source, err := vmrunner.NewOCIImageSource(c.String("cache-dir"),
			vmrunner.WithCredentialStore(store),
			vmrunner.WithPlainHTTPRegistries(c.StringSlice("plain-http")...),
		)
		if err != nil {
			return err
		}
		sourcePath, err = source.Resolve(c.Context, sourcePath)
		if err != nil {
			return err
		}
	}
	creds := vmrunner.GuestCreds{User: c.String("ssh-user")}
	if path := strings.TrimSpace(c.String("ssh-private-key-file")); path != "" {
		creds.PrivateKeyPEM, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("vm-image build macos: read SSH private key: %w", err)
		}
	}
	if path := strings.TrimSpace(c.String("ssh-password-file")); path != "" {
		password, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("vm-image build macos: read SSH password file: %w", err)
		}
		creds.Password = strings.TrimSpace(string(password))
		for i := range password {
			password[i] = 0
		}
	}
	var scripts []string
	for _, path := range c.StringSlice("provision") {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("vm-image build macos: read provision script %q: %w", path, err)
		}
		scripts = append(scripts, string(data))
	}
	memoryBytes, err := resources.ParseMemory(c.String("memory"))
	if err != nil {
		return fmt.Errorf("vm-image build macos: invalid --memory: %w", err)
	}
	output := c.String("output")
	if err := vmrunner.BuildMacImage(c.Context, vmrunner.MacImageBuildOptions{
		SourceBundle: sourcePath,
		OutputBundle: output,
		CPUs:         c.Int("cpus"), MemoryBytes: memoryBytes,
		Creds: creds, Scripts: scripts, LogWriter: c.App.Writer,
		AllowUncleanStop: c.Bool("allow-unclean-stop"),
	}); err != nil {
		return err
	}
	if publish := strings.TrimSpace(c.String("publish")); publish != "" {
		pinned, err := vmrunner.PushMacBundle(c.Context, output, publish, store, c.StringSlice("plain-http"))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(c.App.Writer, pinned)
		return err
	}
	return nil
}

var vmImagePublishCommand = &cli.Command{
	Name:      "publish",
	Usage:     "Package a macOS or Windows VM bundle and publish it to an OCI registry",
	ArgsUsage: "<bundle-directory> <registry/repository:tag>",
	Flags:     vmImageRegistryFlags(),
	Action: func(c *cli.Context) error {
		if c.NArg() != 2 {
			return errors.New("vm-image publish: expected a bundle directory and OCI reference")
		}
		store, err := openVMImageCredentialStore(c.String("auth-file"), false)
		if err != nil {
			return err
		}
		pinned, err := vmrunner.PushVMBundle(c.Context, c.Args().Get(0), c.Args().Get(1), store, c.StringSlice("plain-http"))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(c.App.Writer, pinned)
		return err
	},
}

var vmImagePullCommand = &cli.Command{
	Name:      "pull",
	Usage:     "Pull and materialize a VM image in the local cache",
	ArgsUsage: "<registry/repository:tag-or-digest>",
	Flags: append(vmImageRegistryFlags(),
		&cli.StringFlag{Name: "cache-dir", Value: vmrunner.DefaultImageCacheDir(), Usage: "Local content-addressed VM image cache"},
		&cli.StringFlag{Name: "output", Usage: "Also copy the materialized bundle to this new directory"},
	),
	Action: func(c *cli.Context) error {
		if c.NArg() != 1 {
			return errors.New("vm-image pull: expected one OCI reference")
		}
		store, err := openVMImageCredentialStore(c.String("auth-file"), false)
		if err != nil {
			return err
		}
		source, err := vmrunner.NewOCIImageSource(c.String("cache-dir"),
			vmrunner.WithCredentialStore(store),
			vmrunner.WithPlainHTTPRegistries(c.StringSlice("plain-http")...),
		)
		if err != nil {
			return err
		}
		path, err := source.Resolve(c.Context, c.Args().First())
		if err != nil {
			return err
		}
		if output := strings.TrimSpace(c.String("output")); output != "" {
			if err := vmrunner.CopyVMBundle(c.Context, path, output); err != nil {
				return err
			}
			path = output
		}
		_, err = fmt.Fprintln(c.App.Writer, path)
		return err
	},
}

var vmImageCacheCommand = &cli.Command{
	Name:  "cache",
	Usage: "Manage the local VM image cache",
	Subcommands: []*cli.Command{
		{
			Name:  "prune",
			Usage: "Remove VM images that have not been used within the retention period",
			Flags: append(vmImageRegistryFlags(),
				&cli.StringFlag{Name: "cache-dir", Value: vmrunner.DefaultImageCacheDir(), Usage: "Local content-addressed VM image cache"},
				&cli.DurationFlag{Name: "max-unused", Value: vmrunner.DefaultImageMaxUnused, Usage: "Remove images unused for longer than this duration"},
			),
			Action: func(c *cli.Context) error {
				store, err := openVMImageCredentialStore(c.String("auth-file"), false)
				if err != nil {
					return err
				}
				source, err := vmrunner.NewOCIImageSource(c.String("cache-dir"), vmrunner.WithCredentialStore(store))
				if err != nil {
					return err
				}
				removed, err := source.Prune(c.Context, c.Duration("max-unused"), time.Now())
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(c.App.Writer, "removed %d image(s)\n", removed)
				return err
			},
		},
	},
}

var vmImageRegistryCommand = &cli.Command{
	Name:  "registry",
	Usage: "Manage credentials for VM image registries",
	Subcommands: []*cli.Command{
		{
			Name:      "login",
			Usage:     "Store credentials read from standard input",
			ArgsUsage: "<registry-host>",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "auth-file", Value: defaultVMImageAuthFile(), Usage: "Docker-compatible registry credential file"},
				&cli.StringFlag{Name: "username", Required: true, Usage: "Registry username"},
				&cli.BoolFlag{Name: "password-stdin", Required: true, Usage: "Read the registry password or token from standard input"},
			},
			Action: vmImageRegistryLogin,
		},
		{
			Name:      "logout",
			Usage:     "Remove credentials for one registry",
			ArgsUsage: "<registry-host>",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "auth-file", Value: defaultVMImageAuthFile(), Usage: "Docker-compatible registry credential file"},
			},
			Action: vmImageRegistryLogout,
		},
	},
}

func vmImageRegistryFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "auth-file", Value: defaultVMImageAuthFile(), Usage: "Docker-compatible registry credential file"},
		&cli.StringSliceFlag{Name: "plain-http", Usage: "Registry host that uses plain HTTP (repeatable; development only)"},
	}
}

func vmImageRegistryLogin(c *cli.Context) error {
	if c.NArg() != 1 {
		return errors.New("vm-image registry login: expected one registry host")
	}
	password, err := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
	if err != nil {
		return fmt.Errorf("vm-image registry login: read password: %w", err)
	}
	secret := strings.TrimSpace(string(password))
	for i := range password {
		password[i] = 0
	}
	if secret == "" {
		return errors.New("vm-image registry login: password from standard input is empty")
	}
	store, err := openVMImageCredentialStore(c.String("auth-file"), true)
	if err != nil {
		return err
	}
	err = store.Put(context.Background(), c.Args().First(), auth.Credential{Username: c.String("username"), Password: secret})
	secret = ""
	if err != nil {
		return fmt.Errorf("vm-image registry login: store credential: %w", err)
	}
	if err := os.Chmod(c.String("auth-file"), 0o600); err != nil {
		return fmt.Errorf("vm-image registry login: protect auth file: %w", err)
	}
	_, err = fmt.Fprintf(c.App.Writer, "stored credentials for %s\n", c.Args().First())
	return err
}

func vmImageRegistryLogout(c *cli.Context) error {
	if c.NArg() != 1 {
		return errors.New("vm-image registry logout: expected one registry host")
	}
	store, err := openVMImageCredentialStore(c.String("auth-file"), true)
	if err != nil {
		return err
	}
	if err := store.Delete(context.Background(), c.Args().First()); err != nil {
		return fmt.Errorf("vm-image registry logout: remove credential: %w", err)
	}
	_, err = fmt.Fprintf(c.App.Writer, "removed credentials for %s\n", c.Args().First())
	return err
}

func openVMImageCredentialStore(path string, create bool) (credentials.Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("vm-image: auth file path must not be empty")
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("vm-image: create auth directory: %w", err)
		}
	}
	store, err := credentials.NewStore(path, credentials.StoreOptions{AllowPlaintextPut: true})
	if err != nil {
		return nil, fmt.Errorf("vm-image: open registry auth file %q: %w", path, err)
	}
	return store, nil
}

func defaultVMImageAuthFile() string {
	return vmrunner.DefaultRegistryAuthFile()
}
