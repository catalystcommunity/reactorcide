package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
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
				&cli.StringFlag{Name: "from", Required: true, Usage: "Bootstrap bundle path, OCI reference, or s3:// reference"},
				&cli.BoolFlag{Name: "from-local", Usage: "Treat --from as a local bundle path instead of an OCI reference"},
				&cli.StringFlag{Name: "output", Required: true, Usage: "New bundle directory"},
				&cli.StringFlag{Name: "cache-dir", Value: vmrunner.DefaultImageCacheDir(), Usage: "Local content-addressed VM image cache"},
				&cli.StringFlag{Name: "guest-user", Value: "reactorcide", Usage: "Guest account that runs the provisioning scripts"},
				&cli.StringFlag{Name: "guest-daemon", Usage: "darwin/arm64 vmguest binary to install in the new image (updates the guest daemon)"},
				&cli.StringSliceFlag{Name: "provision", Usage: "Shell script file to run in the guest (repeatable)"},
				&cli.IntFlag{Name: "cpus", Value: 4, Usage: "Virtual CPUs used while provisioning"},
				&cli.StringFlag{Name: "memory", Value: "8Gi", Usage: "Memory used while provisioning"},
				&cli.StringFlag{Name: "publish", Usage: "Publish the completed bundle to this OCI or s3:// reference"},
				&cli.BoolFlag{Name: "allow-unclean-stop", Usage: "Force-stop after sync if the guest does not shut down"},
			),
			Action: runVMImageBuildMacOS,
		},
		{
			Name:  "windows",
			Usage: "Provision a Windows base image in a Hyper-V VM and write a new base image",
			Flags: append(vmImageRegistryFlags(),
				&cli.StringFlag{Name: "from", Required: true, Usage: "Base bundle directory (disk.vhdx), OCI reference, or s3:// reference"},
				&cli.BoolFlag{Name: "from-local", Usage: "Treat --from as a local bundle path (the default when --from is an existing directory)"},
				&cli.StringFlag{Name: "output", Required: true, Usage: "New bundle directory (must not exist)"},
				&cli.StringFlag{Name: "cache-dir", Value: vmrunner.DefaultImageCacheDir(), Usage: "Local content-addressed VM image cache"},
				&cli.StringFlag{Name: "guest-user", Value: "reactorcide", Usage: "Guest account that runs the provisioning scripts"},
				&cli.StringFlag{Name: "guest-daemon", Usage: "windows/amd64 vmguest.exe to install in the new image (updates the guest daemon)"},
				&cli.StringSliceFlag{Name: "provision", Usage: "PowerShell script file to run in the guest (repeatable, in order)"},
				&cli.IntFlag{Name: "cpus", Value: 4, Usage: "Virtual CPUs used while provisioning"},
				&cli.StringFlag{Name: "memory", Value: "8Gi", Usage: "Memory used while provisioning"},
				&cli.DurationFlag{Name: "shutdown-timeout", Value: 30 * time.Minute, Usage: "Time limit for the BitLocker decryption and the guest shutdown"},
				&cli.StringFlag{Name: "publish", Usage: "Publish the completed bundle to this OCI or s3:// reference"},
			),
			Action: runVMImageBuildWindows,
		},
	},
}

func runVMImageBuildWindows(c *cli.Context) error {
	store, err := openVMImageCredentialStore(c.String("auth-file"), false)
	if err != nil {
		return err
	}
	sourcePath := c.String("from")
	local := c.Bool("from-local")
	if info, statErr := os.Stat(sourcePath); statErr == nil && info.IsDir() {
		local = true
	}
	// Ctrl-C cancels the build, and the build removes its VM and disks.
	ctx, stop := signal.NotifyContext(c.Context, os.Interrupt)
	defer stop()
	if !local {
		source, err := newVMImageSource(c, store)
		if err != nil {
			return err
		}
		sourcePath, err = source.Resolve(ctx, sourcePath)
		if err != nil {
			return err
		}
	}
	var scripts []string
	for _, path := range c.StringSlice("provision") {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("vm-image build windows: read provision script %q: %w", path, err)
		}
		scripts = append(scripts, string(data))
	}
	memoryBytes, err := resources.ParseMemory(c.String("memory"))
	if err != nil {
		return fmt.Errorf("vm-image build windows: invalid --memory: %w", err)
	}
	output := c.String("output")
	if err := vmrunner.BuildWindowsImage(ctx, vmrunner.WindowsImageBuildOptions{
		SourceBundle: sourcePath,
		OutputBundle: output,
		CPUs:         c.Int("cpus"), MemoryBytes: memoryBytes,
		User:            strings.TrimSpace(c.String("guest-user")),
		GuestDaemon:     strings.TrimSpace(c.String("guest-daemon")),
		Scripts:         scripts,
		LogWriter:       c.App.Writer,
		ShutdownTimeout: c.Duration("shutdown-timeout"),
	}); err != nil {
		return err
	}
	if publish := strings.TrimSpace(c.String("publish")); publish != "" {
		pinned, err := vmrunner.PublishWindowsBundle(ctx, output, publish, vmImagePublishOptions(c, store))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(c.App.Writer, pinned)
		return err
	}
	return nil
}

func runVMImageBuildMacOS(c *cli.Context) error {
	store, err := openVMImageCredentialStore(c.String("auth-file"), false)
	if err != nil {
		return err
	}
	sourcePath := c.String("from")
	if !c.Bool("from-local") {
		source, err := newVMImageSource(c, store)
		if err != nil {
			return err
		}
		sourcePath, err = source.Resolve(c.Context, sourcePath)
		if err != nil {
			return err
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
		User:        strings.TrimSpace(c.String("guest-user")),
		GuestDaemon: strings.TrimSpace(c.String("guest-daemon")),
		Scripts:     scripts, LogWriter: c.App.Writer,
		AllowUncleanStop: c.Bool("allow-unclean-stop"),
	}); err != nil {
		return err
	}
	if publish := strings.TrimSpace(c.String("publish")); publish != "" {
		pinned, err := vmrunner.PublishMacBundle(c.Context, output, publish, vmImagePublishOptions(c, store))
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
	Usage:     "Package a macOS or Windows VM bundle and publish it to an OCI registry or S3; prints the pinned reference",
	ArgsUsage: "<bundle-directory> <registry/repository:tag | s3://bucket/prefix/name:tag>",
	Flags:     vmImageRegistryFlags(),
	Action: func(c *cli.Context) error {
		if c.NArg() != 2 {
			return errors.New("vm-image publish: expected a bundle directory and an OCI or s3:// reference")
		}
		store, err := openVMImageCredentialStore(c.String("auth-file"), false)
		if err != nil {
			return err
		}
		pinned, err := vmrunner.PublishVMBundle(c.Context, c.Args().Get(0), c.Args().Get(1), vmImagePublishOptions(c, store))
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
	ArgsUsage: "<registry/repository:tag-or-digest | s3://bucket/prefix/name:tag[@sha256:<hex>]>",
	Flags: append(vmImageRegistryFlags(),
		&cli.StringFlag{Name: "cache-dir", Value: vmrunner.DefaultImageCacheDir(), Usage: "Local content-addressed VM image cache"},
		&cli.StringFlag{Name: "output", Usage: "Also copy the materialized bundle to this new directory"},
	),
	Action: func(c *cli.Context) error {
		if c.NArg() != 1 {
			return errors.New("vm-image pull: expected one OCI or s3:// reference")
		}
		store, err := openVMImageCredentialStore(c.String("auth-file"), false)
		if err != nil {
			return err
		}
		source, err := newVMImageSource(c, store)
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
		&cli.StringFlag{Name: "s3-endpoint", Usage: "S3-compatible endpoint URL for s3:// references (default: REACTORCIDE_VM_S3_ENDPOINT, then REACTORCIDE_S3_ENDPOINT, then AWS S3)"},
		&cli.StringFlag{Name: "s3-region", Usage: "S3 region for s3:// references (default: AWS_REGION, then us-east-1)"},
	}
}

// vmImageS3Config returns the S3 client settings of s3:// references.
// Credentials come from the standard AWS chain and are never printed.
func vmImageS3Config(c *cli.Context) vmrunner.S3ImageStoreConfig {
	cfg := vmrunner.S3ImageStoreConfigFromEnv()
	if v := strings.TrimSpace(c.String("s3-endpoint")); v != "" {
		cfg.Endpoint = v
	}
	if v := strings.TrimSpace(c.String("s3-region")); v != "" {
		cfg.Region = v
	}
	return cfg
}

// vmImagePublishOptions collects the registry and S3 settings of a publish.
func vmImagePublishOptions(c *cli.Context, store credentials.Store) vmrunner.PublishOptions {
	return vmrunner.PublishOptions{
		CredentialStore: store,
		PlainHTTPHosts:  c.StringSlice("plain-http"),
		S3:              vmImageS3Config(c),
	}
}

// newVMImageSource opens the local image cache for registry and s3://
// references.
func newVMImageSource(c *cli.Context, store credentials.Store) (*vmrunner.OCIImageSource, error) {
	return vmrunner.NewOCIImageSource(c.String("cache-dir"),
		vmrunner.WithCredentialStore(store),
		vmrunner.WithPlainHTTPRegistries(c.StringSlice("plain-http")...),
		vmrunner.WithS3Config(vmImageS3Config(c)),
	)
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
