# GitHub Webhook Setup

Use [Connect a VCS Repository](./vcs-setup.md) for the complete GitHub setup.
That guide includes:

- Deployment settings
- Secure webhook and API-token storage
- Project creation
- Repository workflow files
- GitHub webhook fields
- Verification and troubleshooting

The GitHub webhook endpoint is:

```text
https://YOUR_REACTORCIDE_URL/api/v1/webhooks/github
```

Select push and pull-request events. Use `application/json`. Enable SSL
verification.
