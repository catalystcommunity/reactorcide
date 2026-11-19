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

Select these events: pushes, pull requests, issue comments, and pull request
reviews. Issue comments carry the `/reactorcide` commands. Pull request
reviews count toward required reviews. Use `application/json`. Enable SSL
verification.

A GitHub App is the recommended connection. See [GitHub App](./github-app.md).
