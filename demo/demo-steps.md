# Demo Steps

| # | Command | Expected |
|---|---------|----------|
| 1 | `./demo.sh validate` | All green, backend detected, config values shown |
| 2 | `./demo.sh setup` | Secrets storage ready, API token + test secret stored |
| 3 | `./demo.sh setup-registry` | Registry password stored |
| 4 | `./demo.sh setup-remote` | Test secret + registry password synced to coordinator |
| 5 | `./demo.sh hello-local` | Greeting printed, secret value shows `***` |
| 6 | `./demo.sh hello-remote` | Job completes, logs show greeting in plain text, secret as `[REDACTED]` |
| 7 | `./demo.sh check-image` | "Image does not exist yet" |
| 8 | `./demo.sh build-local` | Buildkit runs, image pushed, "Image built and pushed!" |
| 9 | `./demo.sh check-image` | "Image already exists" |
| 10 | `sudo nerdctl pull <image> && sudo nerdctl run -p 8080:80 <image>` | Page at http://localhost:8080 titled "Hello from Reactorcide" |
| 11 | `./demo.sh build-remote` | *(optional)* Job submitted, polls to completed, logs show build output |
| 12 | `./demo.sh reset` | Image deleted from registry, test + registry secrets removed, API token kept |
